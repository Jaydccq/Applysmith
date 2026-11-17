package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

type sseMsg struct {
	id, name, data, comment string
}

type streamClient struct {
	t      *testing.T
	resp   *http.Response
	msgs   chan sseMsg
	cancel context.CancelFunc
	epoch  string
}

// openStream connects to a stream route and starts parsing events.
func (h *harness) openStream(path, bearer, lastID string) *streamClient {
	h.t.Helper()
	s, status := h.tryStream(path, bearer, lastID)
	if s == nil {
		h.t.Fatalf("stream %s refused with %d", path, status)
	}
	return s
}

func (h *harness) tryStream(path, bearer, lastID string) (*streamClient, int) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", h.srv.Origin()+path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if _, err := strconv.Atoi(lastID); err == nil {
		lastID = h.srv.epoch + "-" + lastID // a bare sequence number means "of this run"
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		cancel()
		h.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	s := &streamClient{t: h.t, resp: resp, msgs: make(chan sseMsg, 1024), cancel: cancel, epoch: h.srv.epoch}
	go s.pump()
	h.t.Cleanup(s.close)
	return s, 200
}

func (s *streamClient) pump() {
	defer close(s.msgs)
	sc := bufio.NewScanner(s.resp.Body)
	var cur sseMsg
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur != (sseMsg{}) {
				s.msgs <- cur
			}
			cur = sseMsg{}
		case strings.HasPrefix(line, ":"):
			comment := strings.TrimSpace(line[1:])
			// Connection comments are framing, not application events.
			if comment != "connected" {
				s.msgs <- sseMsg{comment: comment}
			}
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line[4:], s.epoch+"-")
		case strings.HasPrefix(line, "event: "):
			cur.name = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
}

func (s *streamClient) next() sseMsg {
	s.t.Helper()
	select {
	case m, ok := <-s.msgs:
		if !ok {
			s.t.Fatal("stream closed while waiting for an event")
		}
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for an event")
	}
	return sseMsg{}
}

func (s *streamClient) expectClosed() {
	s.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.msgs:
			if !ok {
				return
			}
		case <-deadline:
			s.t.Fatal("stream was not closed")
		}
	}
}

func (s *streamClient) close() {
	s.cancel()
	s.resp.Body.Close()
}

// A quiet stream still sends body bytes immediately: an HTTPS tunnel may
// otherwise hold its flushed headers until the first event or heartbeat.
func TestIdleNotificationStreamStartsWithFlushedComment(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.Origin()+"/api/notifications/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status: %d", resp.StatusCode)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("idle stream did not flush its connection comment: %q, %v", line, err)
	}
}

func TestBoardStreamSendsSnapshotOfAllTopicsFirst(t *testing.T) {
	h := newHarness(t)
	// A server run that has published nothing still names its epoch, so a page
	// that reconnects after a restart reloads what it shows.
	if m := h.openStream("/api/board/events", h.token(), "").next(); m.name != "snapshot" || m.id != "0" {
		t.Errorf("first snapshot of a run %+v", m)
	}
	h.srv.Notify("runs", "")
	s := h.openStream("/api/board/events", h.token(), "")
	m := s.next()
	if m.name != "snapshot" || m.id != "1" {
		t.Fatalf("first event %+v", m)
	}
	if h := s.resp.Header; h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("stream headers %v", h)
	}
	var snap wire.BoardSnapshot
	if err := json.Unmarshal([]byte(m.data), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Revisions) != 12 || snap.Revisions["runs"] != 1 {
		t.Errorf("snapshot %v", snap.Revisions)
	}
	for _, topic := range wire.Topics {
		if _, ok := snap.Revisions[topic]; !ok {
			t.Errorf("topic %s missing", topic)
		}
	}
}

func TestBoardNoticeCarriesTopicRevisionAndConfigurationFile(t *testing.T) {
	h := newHarness(t)
	s := h.openStream("/api/board/events", h.token(), "")
	s.next()
	h.srv.Notify("configuration", "profile.yml")
	m := s.next()
	var n wire.BoardNotice
	if err := json.Unmarshal([]byte(m.data), &n); err != nil {
		t.Fatal(err)
	}
	if m.name != "notice" || n.Topic != "configuration" || n.Revision != 1 || n.File != "profile.yml" || m.id != "1" {
		t.Errorf("%+v %+v", m, n)
	}
	if err := h.srv.Notify("nonsense", ""); err == nil {
		t.Error("an unknown topic was accepted")
	}
}

func TestResumeAfterADrop(t *testing.T) {
	h := newHarness(t)
	for range 45 {
		h.srv.Notify("runs", "")
	}
	s := h.openStream("/api/board/events", h.token(), "41")
	if m := s.next(); m.name != "snapshot" {
		t.Fatalf("first event %q", m.name)
	}
	for want := 42; want <= 45; want++ {
		m := s.next()
		if m.name != "notice" || m.id != strconv.Itoa(want) {
			t.Fatalf("want event %d, got %+v", want, m)
		}
	}
	h.srv.Notify("runs", "")
	if m := s.next(); m.id != "46" {
		t.Errorf("live event %+v", m)
	}
}

func TestExpiredCursorGetsReset(t *testing.T) {
	h := newHarness(t)
	for range 600 {
		h.srv.Notify("tracker", "")
	}
	s := h.openStream("/api/board/events", h.token(), "10")
	m := s.next()
	if m.name != "reset" || !strings.Contains(m.data, "cursor_expired") {
		t.Fatalf("first event %+v", m)
	}
	s.expectClosed()
	// The newest 500 events are kept: an identifier just inside the buffer resumes.
	s2 := h.openStream("/api/board/events", h.token(), "101")
	s2.next()
	if m := s2.next(); m.name != "notice" || m.id != "102" {
		t.Fatalf("resume at the buffer edge: %+v", m)
	}
}

func TestCursorBeyondHeadGetsReset(t *testing.T) {
	h := newHarness(t)
	h.srv.Notify("runs", "")
	s := h.openStream("/api/board/events", h.token(), "9999")
	m := s.next()
	if m.name != "reset" || !strings.Contains(m.data, "server_restarted") {
		t.Fatalf("first event %+v", m)
	}
}

func TestHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.srv.keepAlive = 40 * time.Millisecond
	s := h.openStream("/api/board/events", h.token(), "")
	s.next()
	if m := s.next(); m.comment != "keep-alive" {
		t.Fatalf("idle stream wrote %+v", m)
	}
}

func TestShutdownEventBeforeClose(t *testing.T) {
	h := newHarness(t)
	s := h.openStream("/api/board/events", h.token(), "")
	s.next()
	go h.srv.Shutdown(context.Background())
	if m := s.next(); m.name != "shutdown" {
		t.Fatalf("got %+v", m)
	}
	s.expectClosed()
}

func TestSlowConsumerOverflowDropsTheSubscriber(t *testing.T) {
	es := newEventStream("test-epoch")
	_, sub, reset := es.subscribe("", 0, false)
	if reset != "" {
		t.Fatal(reset)
	}
	for i := 0; i < subscriberQ+1; i++ {
		es.publish("notice", wire.BoardNotice{Topic: "runs", Revision: wire.SafeInt(i)})
	}
	if !es.wasDropped(sub) {
		t.Fatal("subscriber with a full queue was kept")
	}
	n := 0
	for range sub.ch {
		n++
	}
	if n != subscriberQ {
		t.Errorf("queued %d events, want %d", n, subscriberQ)
	}
	if es.head() != subscriberQ+1 {
		t.Errorf("head %d", es.head())
	}
}

func TestStreamWriterSendsResetForADroppedSubscriber(t *testing.T) {
	h := newHarness(t)
	rec := &flushRecorder{header: http.Header{}}
	req, _ := http.NewRequest("GET", "/api/board/events", nil)
	c := &apikit.Call{W: rec, R: req, P: apikit.Principal{Kind: apikit.PrincipalLocalToken, ID: "local-token"}}
	done := make(chan error, 1)
	block, entered := make(chan struct{}), make(chan struct{})
	go func() {
		done <- h.srv.serveStream(c, h.srv.board.es, func() ([]sseEvent, bool) {
			close(entered) // the preamble runs after the subscription
			<-block        // hold the writer so the queue fills
			return nil, false
		})
	}()
	<-entered
	for range subscriberQ + 5 {
		h.srv.Notify("runs", "")
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.String(), "event: reset") || !strings.Contains(rec.String(), "slow_consumer") {
		t.Errorf("writer output:\n%s", rec.String())
	}
}

func TestStreamCaps(t *testing.T) {
	h := newHarness(t)
	tok := h.token()
	for range maxStreamsPerKey {
		h.openStream("/api/board/events", tok, "").next()
	}
	r := h.do(call{path: "/api/board/events", bearer: tok})
	if r.StatusCode != 429 || r.errCode() != "rate_limited" || r.errReason() != "too_many_streams" {
		t.Fatalf("ninth stream: %d %s", r.StatusCode, r.body)
	}
	s, status := h.tryStream("/api/board/events", tok, "")
	if s != nil || status != 429 {
		t.Fatalf("tenth stream: %d", status)
	}
}

func TestTotalStreamCap(t *testing.T) {
	set := newStreamSet()
	var opened int
	for i := 0; i < maxStreamsTotal+1; i++ {
		p := apikit.Principal{Kind: apikit.PrincipalLocalToken, ID: "client-" + strconv.Itoa(i/maxStreamsPerKey)}
		if _, e := set.open(p, func(error) {}, 0); e == nil {
			opened++
		}
	}
	if opened != maxStreamsTotal {
		t.Errorf("opened %d streams, want %d", opened, maxStreamsTotal)
	}
}

// A remote device holds fewer streams than a local credential, and all remote
// devices together hold 16, shared so that every paired device can open one.
func TestRemote_StreamShareFollowsThePairedDevices(t *testing.T) {
	for devices, want := range map[int]int{0: 4, 1: 4, 2: 4, 4: 4, 5: 3, 6: 2, 8: 2, 9: 1, 10: 1} {
		if got := deviceStreamCap(devices); got != want {
			t.Errorf("%d devices: share %d, want %d", devices, got, want)
		}
		if deviceStreamCap(devices)*max(devices, 1) > maxStreamsRemote && devices > 4 {
			t.Errorf("%d devices: the shares add up to more than %d", devices, maxStreamsRemote)
		}
	}
}

func TestRemote_NoDeviceCanTakeTheStreamsOfAnother(t *testing.T) {
	remoteOpen := func(set *streamSet, device, devices int) *apikit.Error {
		_, e := set.open(apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: deviceIDPrefix + strconv.Itoa(device)}, func(error) {}, devices)
		return e
	}
	for _, devices := range []int{1, 3, 4, 5, 7, 10} {
		set := newStreamSet()
		// Every device but the last takes all it may, greedily.
		for d := 0; d < devices-1; d++ {
			for remoteOpen(set, d, devices) == nil {
			}
		}
		if e := remoteOpen(set, devices-1, devices); e != nil {
			t.Errorf("%d devices: the last device could not open a stream: %v", devices, e)
		}
		if set.remote > maxStreamsRemote {
			t.Errorf("%d devices: %d remote streams, more than %d", devices, set.remote, maxStreamsRemote)
		}
	}
	// One device never holds more than 4, and one alone never more than 16 in all.
	one := newStreamSet()
	var opened int
	for remoteOpen(one, 0, 1) == nil {
		opened++
	}
	if opened != maxStreamsPerDevice {
		t.Errorf("one device opened %d streams, want %d", opened, maxStreamsPerDevice)
	}
}

// Devices that pair after the listener filled up are not shut out: a device with
// no stream closes one of an over-share device's.
func TestRemote_ADeviceThatPairsLateGetsAStream(t *testing.T) {
	set := newStreamSet()
	var closed []error
	for d := range 4 {
		for range 4 {
			if _, e := set.open(apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: deviceIDPrefix + strconv.Itoa(d)}, func(err error) { closed = append(closed, err) }, 4); e != nil {
				t.Fatalf("device %d: %v", d, e)
			}
		}
	}
	if set.remote != maxStreamsRemote {
		t.Fatalf("%d remote streams", set.remote)
	}
	late := apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: deviceIDPrefix + "late"}
	if _, e := set.open(late, func(error) {}, 5); e != nil {
		t.Fatalf("the fifth device: %v", e)
	}
	if set.remote != maxStreamsRemote || len(closed) != 1 || closed[0] != errStreamMakeRoom {
		t.Errorf("remote streams %d, closed %v", set.remote, closed)
	}
	// Its second stream waits: it is within its share only after the others shrink.
	if _, e := set.open(late, func(error) {}, 5); e == nil {
		t.Error("a device with a stream took another from the full listener")
	}
}

func TestEventIdsCarryTheServerEpoch(t *testing.T) {
	h := newHarness(t)
	h.srv.Notify("runs", "")
	h.srv.Notify("runs", "")
	s := h.openStream("/api/board/events", h.token(), "")
	s.next() // snapshot
	h.srv.Notify("runs", "")
	// The client strips the epoch; check the raw form separately.
	raw := newEvent("notice", wire.BoardNotice{Topic: "runs", Revision: 1})
	raw.epoch, raw.id = "abcd1234", 7
	if !strings.Contains(string(raw.bytes()), "id: abcd1234-7\n") {
		t.Errorf("event bytes %q", raw.bytes())
	}
	if len(h.srv.epoch) != 8 {
		t.Errorf("epoch %q", h.srv.epoch)
	}
	if m := s.next(); m.id != "3" {
		t.Errorf("sequence %q", m.id)
	}
}

func TestCursorFromAnotherServerRunGetsReset(t *testing.T) {
	h := newHarness(t)
	for range 5 {
		h.srv.Notify("runs", "")
	}
	// A valid-looking sequence number from a different run must not resume.
	for _, id := range []string{"deadbeef-3", "garbage"} {
		s := h.openStream("/api/board/events", h.token(), id)
		m := s.next()
		if m.name != "reset" || !strings.Contains(m.data, "server_restarted") {
			t.Errorf("Last-Event-ID %q: first event %+v", id, m)
		}
	}
	// After a real restart the old identifier is stale even when the number fits.
	old := h.srv.epoch + "-3"
	h.restart()
	for range 5 {
		h.srv.Notify("runs", "")
	}
	s := h.openStream("/api/board/events", h.token(), old)
	if m := s.next(); m.name != "reset" {
		t.Errorf("identifier from before the restart: %+v", m)
	}
}

// failingWriter accepts headers but fails every body write, like a client that
// went away right after connecting.
type failingWriter struct{ header http.Header }

func (f *failingWriter) Header() http.Header       { return f.header }
func (f *failingWriter) WriteHeader(int)           {}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (f *failingWriter) Flush()                    {}

func TestStreamSubscriberIsRemovedWhenTheFirstWriteFails(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("GET", "/api/board/events", nil)
	c := &apikit.Call{W: &failingWriter{header: http.Header{}}, R: req, P: apikit.Principal{Kind: apikit.PrincipalLocalToken, ID: "local-token"}}
	if err := h.srv.serveStream(c, h.srv.board.es, func() ([]sseEvent, bool) { return nil, false }); err != nil {
		t.Fatal(err)
	}
	es := h.srv.board.es
	es.mu.Lock()
	n := len(es.subs)
	es.mu.Unlock()
	if n != 0 {
		t.Errorf("%d subscribers left behind", n)
	}
}
