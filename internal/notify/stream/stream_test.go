package stream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

const testToken = "test-token"

// fakeServer plays the notification stream. Each connection serves the next
// script entry and then closes; a connection past the script is refused.
type fakeServer struct {
	mu     sync.Mutex
	script []string // the raw body of each connection
	cursor []string // Last-Event-ID sent by each connection
	acks   []string
	hold   chan struct{} // when set, the last connection stays open until closed
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/notifications/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.cursor = append(f.cursor, r.Header.Get("Last-Event-ID"))
		if len(f.script) == 0 {
			f.mu.Unlock()
			http.Error(w, "gone", http.StatusServiceUnavailable)
			return
		}
		body := f.script[0]
		f.script = f.script[1:]
		last := len(f.script) == 0 && f.hold != nil
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
		w.(http.Flusher).Flush()
		if last {
			select {
			case <-f.hold:
			case <-r.Context().Done():
			}
		}
	})
	mux.HandleFunc("POST /api/notifications/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.acks = append(f.acks, r.PathValue("id"))
		f.mu.Unlock()
		fmt.Fprint(w, `{"data":{}}`)
	})
	return mux
}

func event(id, name, data string) string {
	s := ""
	if id != "" {
		s = "id: " + id + "\n"
	}
	return s + "event: " + name + "\ndata: " + data + "\n\n"
}

func announce(id, entry, body string, urgent bool) string {
	return event(id, "announce", fmt.Sprintf(`{"id":%q,"title":"Run finished","body":%q,"link":"/runs/%s","urgent":%t,"entry":true}`, entry, body, entry, urgent))
}

// rig runs a subscriber against the fake until stop is called.
type rig struct {
	sub    *Subscriber
	mu     sync.Mutex
	shown  []wire.NotificationAnnounce
	closed []string
	done   chan struct{}
	stop   context.CancelFunc
	waits  []time.Duration
	events chan struct{}
}

func newRig(t *testing.T, f *fakeServer) *rig {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	r := &rig{done: make(chan struct{}), events: make(chan struct{}, 64)}
	r.sub = New(Config{
		Origin: srv.URL,
		Token:  func() (string, error) { return testToken, nil },
		Show: func(a wire.NotificationAnnounce) error {
			r.mu.Lock()
			r.shown = append(r.shown, a)
			r.mu.Unlock()
			r.events <- struct{}{}
			return nil
		},
		Close: func(id string) {
			r.mu.Lock()
			r.closed = append(r.closed, id)
			r.mu.Unlock()
			r.events <- struct{}{}
		},
	})
	r.sub.wait = func(ctx context.Context, d time.Duration) bool {
		r.mu.Lock()
		r.waits = append(r.waits, d)
		r.mu.Unlock()
		return ctx.Err() == nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	t.Cleanup(cancel)
	go func() { defer close(r.done); r.sub.Run(ctx) }()
	return r
}

// await blocks until n events (shows and closes) were handled.
func (r *rig) await(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-r.events:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the subscriber")
		}
	}
}

func (r *rig) finish(t *testing.T) {
	t.Helper()
	r.stop()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}

func (r *rig) shownIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, a := range r.shown {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestAnnouncementIsShownAndWithdrawalClosesIt(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		announce("e1-1", "run-a", "3 postings", false) + event("e1-2", "withdraw", `{"id":"run-a"}`),
	}}
	r := newRig(t, f)
	r.await(t, 2)
	if got := r.shown[0]; got.Title != "Run finished" || got.Body != "3 postings" || got.Link != "/runs/run-a" {
		t.Errorf("shown %+v", got)
	}
	if !slices.Equal(r.closed, []string{"run-a"}) {
		t.Errorf("closed %v", r.closed)
	}
	close(f.hold)
	r.finish(t)
}

func TestNoticeAndKeepAliveShowNothingNative(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		": keep-alive\n\n" + event("e1-1", "notice", `{"title":"Telegram paired","body":"x"}`) + announce("e1-2", "run-a", "b", false),
	}}
	r := newRig(t, f)
	r.await(t, 1)
	if ids := r.shownIDs(); !slices.Equal(ids, []string{"run-a"}) {
		t.Errorf("shown %v, want only the announcement", ids)
	}
	close(f.hold)
	r.finish(t)
}

func TestReconnectAfterServerDropsResumesFromCursor(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		announce("e1-1", "run-a", "b", false),
		announce("e1-2", "run-b", "b", false),
	}}
	r := newRig(t, f)
	r.await(t, 2)
	f.mu.Lock()
	cursor := slices.Clone(f.cursor)
	f.mu.Unlock()
	if want := []string{"", "e1-1"}; !slices.Equal(cursor, want) {
		t.Errorf("Last-Event-ID per connection %q, want %q", cursor, want)
	}
	close(f.hold)
	r.finish(t)
}

func TestNoDuplicateShowOfTheSameEventAfterReconnect(t *testing.T) {
	// The second connection replays event 1, as a server whose buffer reaches
	// back further than the cursor would.
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		announce("e1-1", "run-a", "b", false),
		announce("e1-1", "run-a", "b", false) + announce("e1-2", "run-b", "b", false),
	}}
	r := newRig(t, f)
	r.await(t, 2)
	if ids := r.shownIDs(); !slices.Equal(ids, []string{"run-a", "run-b"}) {
		t.Errorf("shown %v", ids)
	}
	close(f.hold)
	r.finish(t)
}

func TestServerRestartResetsTheCursor(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		announce("e1-5", "run-a", "b", false),
		event("", "reset", `{"reason":"server-restart"}`),
		announce("e2-1", "run-b", "b", false),
	}}
	r := newRig(t, f)
	r.await(t, 2)
	f.mu.Lock()
	cursor := slices.Clone(f.cursor)
	f.mu.Unlock()
	if want := []string{"", "e1-5", ""}; !slices.Equal(cursor, want) {
		t.Errorf("Last-Event-ID per connection %q, want %q", cursor, want)
	}
	if ids := r.shownIDs(); !slices.Equal(ids, []string{"run-a", "run-b"}) {
		t.Errorf("shown %v: an event of the new run must show although its number is lower", ids)
	}
	close(f.hold)
	r.finish(t)
}

func TestStopsCleanlyWhenTheContextEndsDuringAConnection(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{announce("e1-1", "run-a", "b", false)}}
	r := newRig(t, f)
	r.await(t, 1)
	r.finish(t) // the connection is still open
	close(f.hold)
}

func TestClickAcknowledgesAFinishedRunButNotAnUrgentOne(t *testing.T) {
	f := &fakeServer{hold: make(chan struct{}), script: []string{
		announce("e1-1", "run-done", "b", false) + announce("e1-2", "run-wait", "b", true) +
			event("e1-3", "announce", `{"id":"n-7","title":"Gmail alert","body":"b","link":"/gmail"}`),
	}}
	r := newRig(t, f)
	r.await(t, 3)
	if link := r.sub.Clicked(context.Background(), "run-done"); link != "/runs/run-done" {
		t.Errorf("link %q", link)
	}
	if link := r.sub.Clicked(context.Background(), "run-wait"); link != "/runs/run-wait" {
		t.Errorf("link %q", link)
	}
	if link := r.sub.Clicked(context.Background(), "n-7"); link != "/gmail" {
		t.Errorf("link %q", link)
	}
	if link := r.sub.Clicked(context.Background(), "from-an-earlier-run"); link != "" {
		t.Errorf("link %q for an unknown notification", link)
	}
	f.mu.Lock()
	acks := slices.Clone(f.acks)
	f.mu.Unlock()
	if !slices.Equal(acks, []string{"run-done"}) {
		t.Errorf("acknowledged %v, want only the finished run (an alert of no entry has nothing to acknowledge)", acks)
	}
	close(f.hold)
	r.finish(t)
}

func TestTargetAcceptsOnlyDashboardPaths(t *testing.T) {
	for _, tc := range []struct {
		link string
		want string
		ok   bool
	}{
		{"/runs/a", "http://127.0.0.1:1/runs/a", true},
		{"/usage", "http://127.0.0.1:1/usage", true},
		{"//example.org/x", "", false},
		{"https://example.org/x", "", false},
		{"", "", false},
		{"/\\example.org", "", false},
	} {
		got, ok := Target("http://127.0.0.1:1", tc.link)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Target(%q) = %q, %v", tc.link, got, ok)
		}
	}
}
