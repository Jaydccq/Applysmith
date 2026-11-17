package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

const (
	streamBuffer   = 500
	subscriberQ    = 256
	keepAlive      = 15 * time.Second
	streamWriteTTL = 10 * time.Second
)

var (
	errStreamShutdown = errors.New("server shutting down")
	errStreamRevoked  = errors.New("credential revoked")
	errStreamMakeRoom = errors.New("closed to make room for a device with no stream")
)

type sseEvent struct {
	epoch string
	id    int64
	name  string
	data  []byte
}

// bytes frames the event. One without an epoch carries no id line. The
// board's snapshot carries one even before anything was published (id 0):
// the epoch is how a page tells a new server run from the one it knew.
func (e sseEvent) bytes() []byte {
	var b []byte
	if e.epoch != "" {
		b = fmt.Appendf(b, "id: %s-%d\n", e.epoch, e.id)
	}
	b = fmt.Appendf(b, "event: %s\ndata: %s\n\n", e.name, e.data)
	return b
}

func newEvent(name string, payload any) sseEvent {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte("{}")
	}
	return sseEvent{name: name, data: data}
}

// eventStream is the replay buffer and the live subscribers of one stream.
// Identifiers start at 1 and only grow while the server runs.
type eventStream struct {
	epoch  string // identifies the server run; ids from another run are stale
	mu     sync.Mutex
	nextID int64
	buf    []sseEvent
	subs   map[*subscriber]struct{}
}

type subscriber struct {
	ch      chan sseEvent
	dropped bool // guarded by the stream mutex; set when the queue overflowed
}

// newEpoch names one server run. Event ids restart from 1 in every run, so the
// epoch is what lets a reconnecting client be told apart from a stale one.
func newEpoch() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newEventStream(epoch string) *eventStream {
	return &eventStream{epoch: epoch, nextID: 1, subs: map[*subscriber]struct{}{}}
}

func (s *eventStream) publish(name string, payload any) {
	ev := newEvent(name, payload)
	s.mu.Lock()
	defer s.mu.Unlock()
	ev.epoch, ev.id = s.epoch, s.nextID
	s.nextID++
	s.buf = append(s.buf, ev)
	if len(s.buf) > streamBuffer {
		s.buf = append([]sseEvent(nil), s.buf[len(s.buf)-streamBuffer:]...)
	}
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			sub.dropped = true
			delete(s.subs, sub)
			close(sub.ch)
		}
	}
}

// listening reports whether a client is subscribed.
func (s *eventStream) listening() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// head is the identifier of the newest event, zero when none was published.
func (s *eventStream) head() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextID - 1
}

// subscribe registers a live subscriber and returns the events a reconnecting
// client missed. A non-empty reset reason means the client cannot be caught
// up and must refetch.
func (s *eventStream) subscribe(lastEpoch string, last int64, resumed bool) (replay []sseEvent, sub *subscriber, reset string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.nextID - 1
	if resumed {
		switch {
		case lastEpoch != s.epoch || last > head:
			return nil, nil, wire.ResetServerRestart
		case last < head && (len(s.buf) == 0 || last < s.buf[0].id-1):
			return nil, nil, wire.ResetCursorExpired
		}
		for _, ev := range s.buf {
			if ev.id > last {
				replay = append(replay, ev)
			}
		}
	}
	sub = &subscriber{ch: make(chan sseEvent, subscriberQ)}
	s.subs[sub] = struct{}{}
	return replay, sub, ""
}

func (s *eventStream) unsubscribe(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		close(sub.ch)
	}
}

func (s *eventStream) wasDropped(sub *subscriber) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sub.dropped
}

// streamConn is one open stream connection.
type streamConn struct {
	key    string
	kind   apikit.PrincipalKind
	cancel context.CancelCauseFunc
}

// streamSet enforces the stream caps and lets the server close streams by
// credential or all at once.
type streamSet struct {
	mu     sync.Mutex
	conns  map[*streamConn]struct{}
	perID  map[string]int
	remote int  // open streams of remote devices
	closed bool // set by closeAll: a stream arriving during shutdown is refused
}

func newStreamSet() *streamSet {
	return &streamSet{conns: map[*streamConn]struct{}{}, perID: map[string]int{}}
}

// deviceStreamCap is how many streams one remote device may hold when devices
// are paired: the 16 of the listener shared evenly, between 1 and 4. The caps
// of all devices add up to at most 16, so no device can take the streams of
// another, and every device can always open one.
func deviceStreamCap(devices int) int {
	return min(maxStreamsPerDevice, max(1, maxStreamsRemote/max(devices, 1)))
}

// open registers a stream. devices is the number of paired remote devices, used
// for a remote credential's share.
func (s *streamSet) open(p apikit.Principal, cancel context.CancelCauseFunc, devices int) (*streamConn, *apikit.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, apikit.Unavailable("the server is shutting down")
	}
	perKey, remote := maxStreamsPerKey, p.Kind == apikit.PrincipalRemoteDevice
	if remote {
		perKey = deviceStreamCap(devices)
	}
	if len(s.conns) >= maxStreamsTotal || s.perID[p.ID] >= perKey {
		return nil, errRateLimited(1, wire.ReasonTooManyStreams)
	}
	if remote && s.remote >= maxStreamsRemote && !s.makeRoomLocked(p.ID, perKey) {
		return nil, errRateLimited(1, wire.ReasonTooManyStreams)
	}
	c := &streamConn{key: p.ID, kind: p.Kind, cancel: cancel}
	s.conns[c] = struct{}{}
	s.perID[p.ID]++
	if remote {
		s.remote++
	}
	return c, nil
}

func (s *streamSet) close(c *streamConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[c]; ok {
		delete(s.conns, c)
		s.perID[c.key]--
		if c.kind == apikit.PrincipalRemoteDevice {
			s.remote--
		}
	}
}

// makeRoomLocked frees a remote stream for a device that has none. The listener
// can be full only because devices paired after the streams were opened, so some
// device holds more than its share now; one of its streams is closed (the
// client reconnects and finds itself within its share).
func (s *streamSet) makeRoomLocked(device string, share int) bool {
	if s.perID[device] > 0 {
		return false
	}
	var victim *streamConn
	for c := range s.conns {
		if c.kind == apikit.PrincipalRemoteDevice && s.perID[c.key] > share && (victim == nil || s.perID[c.key] > s.perID[victim.key]) {
			victim = c
		}
	}
	if victim == nil {
		return false
	}
	delete(s.conns, victim)
	s.perID[victim.key]--
	s.remote--
	victim.cancel(errStreamMakeRoom)
	return true
}

// revokeID closes the streams one credential opened.
func (s *streamSet) revokeID(id string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.key == id {
			c.cancel(cause)
		}
	}
}

// has reports whether the credential holds an open stream.
func (s *streamSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perID[id] > 0
}

// revoke closes every stream opened with the given kind of credential.
func (s *streamSet) revoke(kind apikit.PrincipalKind, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.kind == kind {
			c.cancel(cause)
		}
	}
}

func (s *streamSet) closeAll(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.conns {
		c.cancel(cause)
	}
}

// serveStream writes an event stream until the client leaves, the credential
// is revoked or the server stops. preamble events are written first and carry
// no replay semantics of their own; a preamble that reports finished ends the
// stream after them. The stream also ends after a "final" event.
func (s *Server) serveStream(c *apikit.Call, es *eventStream, preamble func() (events []sseEvent, finished bool)) error {
	ctx, cancel := context.WithCancelCause(c.R.Context())
	defer cancel(nil)
	conn, e := s.streams.open(c.P, cancel, s.remote.pairedCount(c.P))
	if e != nil {
		return e
	}
	defer s.streams.close(conn)
	// The credential was checked before the stream was registered; a revoke in
	// between would have found no stream to close. Registered first, checked
	// second: either the revoke sees the stream or this sees the revoke.
	if !s.remote.stillPaired(c.P) {
		return errUnauthenticated()
	}

	var last int64
	var lastEpoch string
	resumed := false
	if h := c.R.Header.Get("Last-Event-ID"); h != "" {
		// An identifier this server could not have issued gets a reset too.
		resumed = true
		if ep, seq, ok := strings.Cut(h, "-"); ok {
			if n, err := strconv.ParseInt(seq, 10, 64); err == nil && n >= 0 {
				lastEpoch, last = ep, n
			}
		}
	}
	replay, sub, reset := es.subscribe(lastEpoch, last, resumed)
	if sub != nil {
		// Registered before the first write: a failing write must not leave
		// the subscriber behind.
		defer es.unsubscribe(sub)
	}

	rc := http.NewResponseController(c.W)
	write := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTTL))
		if _, err := c.W.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	h := c.W.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	c.W.WriteHeader(http.StatusOK)
	c.Handled = true
	// Send body bytes as well as headers: tunnels may buffer a headers-only
	// response until an event arrives. SSE clients ignore this comment.
	if !write([]byte(": connected\n\n")) {
		return nil
	}

	if reset != "" {
		write(newEvent("reset", wire.StreamReset{Reason: reset}).bytes())
		return nil
	}
	first, finished := preamble()
	for _, ev := range first {
		if !write(ev.bytes()) {
			return nil
		}
	}
	if finished {
		return nil
	}
	for _, ev := range replay {
		if !write(ev.bytes()) {
			return nil
		}
	}
	tick := time.NewTicker(s.keepAlive)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				if es.wasDropped(sub) {
					write(newEvent("reset", wire.StreamReset{Reason: wire.ResetSlowConsumer}).bytes())
				}
				return nil
			}
			if !write(ev.bytes()) || ev.name == "final" {
				return nil
			}
		case <-tick.C:
			if !write([]byte(": keep-alive\n\n")) {
				return nil
			}
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), errStreamShutdown) {
				// What was published before the stop (the error event of an
				// interrupted turn) is written before the shutdown event.
				if !drain(sub.ch, write) {
					return nil
				}
				write(newEvent("shutdown", wire.StreamShutdown{}).bytes())
			}
			return nil
		}
	}
}

// drain writes the events already waiting for a subscriber, without waiting
// for more. It reports false when a write failed.
func drain(ch <-chan sseEvent, write func([]byte) bool) bool {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			if !write(ev.bytes()) {
				return false
			}
		default:
			return true
		}
	}
}
