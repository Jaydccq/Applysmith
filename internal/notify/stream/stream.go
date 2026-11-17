// Package stream is the client side of the desktop sink: it follows
// GET /api/notifications/events, hands each announcement to a shower, closes
// withdrawn ones and reconnects when the server goes away. It knows nothing of
// the windowing framework, so the desktop shell supplies the native calls.
package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/wire"
)

// Reconnect delays: the first retry waits MinDelay, each further failure
// doubles it up to MaxDelay, and a connection that was accepted starts over.
const (
	MinDelay = time.Second
	MaxDelay = 30 * time.Second
)

// maxKnown bounds the announcements remembered for clicks.
const maxKnown = 512

// Config describes one subscription.
type Config struct {
	// Origin is the server's loopback origin.
	Origin string
	// Token returns the current bearer token. It is read for every connection
	// and every acknowledgement, so a rotated token is picked up.
	Token func() (string, error)
	// Show displays or replaces the native notification with the
	// announcement's identifier.
	Show func(wire.NotificationAnnounce) error
	// Close removes the native notification with this identifier.
	Close func(id string)
	Log   *slog.Logger
	// Client is the HTTP client; the default has no overall timeout because
	// the stream stays open.
	Client *http.Client
}

// Subscriber follows the notification stream of one server.
type Subscriber struct {
	cfg  Config
	wait func(context.Context, time.Duration) bool // false when ctx ended first

	// The cursor of the connection last served: events of that run up to seq
	// were handled.
	epoch string
	seq   int64

	mu    sync.Mutex
	known map[string]wire.NotificationAnnounce
}

// New returns a subscriber; Run starts it.
func New(cfg Config) *Subscriber {
	if cfg.Client == nil {
		cfg.Client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Subscriber{cfg: cfg, wait: sleep, known: map[string]wire.NotificationAnnounce{}}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Run follows the stream until ctx ends. It never returns an error: a failed
// connection is logged and retried.
func (s *Subscriber) Run(ctx context.Context) {
	delay := MinDelay
	for ctx.Err() == nil {
		connected, err := s.once(ctx)
		if connected {
			delay = MinDelay
		}
		retry := delay
		if !connected {
			delay = min(delay*2, MaxDelay)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.cfg.Log.Warn("notification stream lost", "error", err.Error(), "retry_in", retry.String())
		} else {
			s.cfg.Log.Info("notification stream ended", "retry_in", retry.String())
		}
		if !s.wait(ctx, retry) {
			return
		}
	}
}

// once serves one connection. connected reports that the server accepted it.
func (s *Subscriber) once(ctx context.Context) (connected bool, err error) {
	token, err := s.cfg.Token()
	if err != nil {
		return false, fmt.Errorf("read the API token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.Origin+"/api/notifications/events", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	if s.epoch != "" {
		req.Header.Set("Last-Event-ID", s.epoch+"-"+strconv.FormatInt(s.seq, 10))
	}
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("the server answered status %d", resp.StatusCode)
	}
	s.cfg.Log.Info("notification stream connected")
	return true, s.read(resp.Body)
}

// frame is one server-sent event.
type frame struct{ id, name, data string }

// read dispatches events until the body ends.
func (s *Subscriber) read(body io.Reader) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var f frame
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if f.name != "" {
				if stop := s.handle(f); stop {
					return nil
				}
			}
			f = frame{}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			f.id = value
		case "event":
			f.name = value
		case "data":
			if f.data != "" {
				f.data += "\n"
			}
			f.data += value
		}
	}
	return sc.Err()
}

// handle processes one event and reports whether the connection must end.
func (s *Subscriber) handle(f frame) (stop bool) {
	switch f.name {
	case "reset":
		// The server cannot continue from the cursor, so the next connection
		// starts without one.
		s.epoch, s.seq = "", 0
		return true
	case "shutdown":
		return true
	}
	if f.id != "" && s.seen(f.id) {
		return false
	}
	switch f.name {
	case wire.NotifyAnnounce:
		var a wire.NotificationAnnounce
		if err := decode(f.data, &a); err != nil || a.ID == "" {
			s.cfg.Log.Warn("notification announcement ignored", "reason", "malformed")
			return false
		}
		s.remember(a)
		if err := s.cfg.Show(a); err != nil {
			s.cfg.Log.Warn("notification not shown", "error", err.Error())
		}
	case wire.NotifyWithdraw:
		var w wire.NotificationWithdraw
		if err := decode(f.data, &w); err != nil || w.ID == "" {
			s.cfg.Log.Warn("notification withdrawal ignored", "reason", "malformed")
			return false
		}
		s.forget(w.ID)
		s.cfg.Close(w.ID)
	}
	return false
}

// seen reports whether the event was already handled and advances the cursor
// otherwise. A different epoch is a different server run.
func (s *Subscriber) seen(id string) bool {
	epoch, n, ok := strings.Cut(id, "-")
	seq, err := strconv.ParseInt(n, 10, 64)
	if !ok || err != nil {
		return false
	}
	if epoch == s.epoch && seq <= s.seq {
		return true
	}
	s.epoch, s.seq = epoch, seq
	return false
}

func (s *Subscriber) remember(a wire.NotificationAnnounce) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.known) >= maxKnown {
		clear(s.known)
	}
	s.known[a.ID] = a
}

func (s *Subscriber) forget(id string) {
	s.mu.Lock()
	delete(s.known, id)
	s.mu.Unlock()
}

// Clicked is called when the user clicks the notification with this
// identifier. It returns the announcement's link, empty when the identifier is
// not one of this run. A non-urgent announcement of an entry (a finished run)
// is also acknowledged: the user has seen the result. An urgent one stays in
// the inbox until the user acts on it, and one of no entry (a Gmail alert, a
// cost cap, a summary) has nothing to acknowledge.
func (s *Subscriber) Clicked(ctx context.Context, id string) (link string) {
	s.mu.Lock()
	a, ok := s.known[id]
	delete(s.known, id)
	s.mu.Unlock()
	if !ok {
		return ""
	}
	if !a.Urgent && a.Entry {
		if err := s.ack(ctx, id); err != nil {
			s.cfg.Log.Debug("notification not acknowledged", "error", err.Error())
		}
	}
	return a.Link
}

func (s *Subscriber) ack(ctx context.Context, id string) error {
	token, err := s.cfg.Token()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Origin+"/api/notifications/"+url.PathEscape(id)+"/ack", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered status %d", resp.StatusCode)
	}
	return nil
}

// Target resolves an announcement link against the server origin. Only a path
// of the dashboard is accepted; anything else returns false.
func Target(origin, link string) (string, bool) {
	if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") || strings.ContainsAny(link, "\\\r\n") {
		return "", false
	}
	return strings.TrimSuffix(origin, "/") + link, true
}

func decode(data string, v any) error {
	if data == "" {
		return errors.New("empty data")
	}
	return json.Unmarshal([]byte(data), v)
}
