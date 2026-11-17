package telegram

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Outbound limits per chat.
const (
	SendInterval = time.Second
	SendPerMin   = 20
	MaxText      = 3700
)

// Clock is the sender's source of time. Sleep returns early with the context's
// error when it ends.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// out is one outbound message or edit waiting for its turn.
type out struct {
	chat    int64
	text    string
	html    string // text in Telegram's HTML, sent first; text replaces it when refused
	replyTo int64  // the message this one replies to; zero for none
	markup  *Markup
	edit    int64 // message to edit; zero sends a new message
	urgent  bool
	ctx     context.Context
	done    chan outResult
}

type outResult struct {
	id  int64
	err error
}

// Sender is the outbound queue of one gateway. Messages go out in order, urgent
// ones first, one per second and at most 20 per minute per chat; a 429 is waited
// out for the retry_after it states.
type Sender struct {
	c     *Client
	clk   Clock
	mu    sync.Mutex
	urg   []*out
	norm  []*out
	wake  chan struct{}
	sent  map[int64][]time.Time
	until time.Time // no request before this instant (a 429)
}

// NewSender returns a sender over the client.
func NewSender(c *Client, clk Clock) *Sender {
	if clk == nil {
		clk = realClock{}
	}
	return &Sender{c: c, clk: clk, wake: make(chan struct{}, 1), sent: map[int64][]time.Time{}}
}

// Run sends queued messages until ctx ends.
func (s *Sender) Run(ctx context.Context) {
	for {
		o := s.next()
		if o == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if err := s.wait(ctx, o.chat); err != nil {
			s.finish(o, outResult{err: err})
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if o.ctx.Err() != nil {
			s.finish(o, outResult{err: o.ctx.Err()})
			continue
		}
		s.mark(o.chat)
		var res outResult
		switch {
		case o.edit != 0:
			res.err = s.c.EditMessageText(o.ctx, o.chat, o.edit, o.text, o.markup)
		case o.html != "":
			res.id, res.err = s.c.SendMessage(o.ctx, o.chat, o.html, "HTML", o.replyTo, o.markup)
		default:
			res.id, res.err = s.c.SendMessage(o.ctx, o.chat, o.text, "", o.replyTo, o.markup)
		}
		var api *APIError
		if errors.As(res.err, &api) && api.Code == 429 {
			// Wait it out and try the same message again.
			s.mu.Lock()
			s.until = s.clk.Now().Add(time.Duration(max(api.RetryAfter, 1)) * time.Second)
			s.requeue(o)
			s.mu.Unlock()
			continue
		}
		if o.html != "" && errors.As(res.err, &api) && api.Code == http.StatusBadRequest {
			// Telegram refused the formatting: the plain text goes in its place.
			o.html = ""
			s.mu.Lock()
			s.requeue(o)
			s.mu.Unlock()
			continue
		}
		s.finish(o, res)
	}
}

func (s *Sender) requeue(o *out) {
	if o.urgent {
		s.urg = append([]*out{o}, s.urg...)
	} else {
		s.norm = append([]*out{o}, s.norm...)
	}
}

func (s *Sender) next() *out {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range []*[]*out{&s.urg, &s.norm} {
		for len(*q) > 0 {
			o := (*q)[0]
			*q = (*q)[1:]
			if o.ctx.Err() != nil {
				o.done <- outResult{err: o.ctx.Err()}
				continue
			}
			return o
		}
	}
	return nil
}

func (s *Sender) finish(o *out, r outResult) { o.done <- r }

// wait sleeps until a request to the chat is allowed.
func (s *Sender) wait(ctx context.Context, chat int64) error {
	for {
		s.mu.Lock()
		now := s.clk.Now()
		at := now
		if s.until.After(at) {
			at = s.until
		}
		hist := s.sent[chat]
		if n := len(hist); n > 0 {
			if next := hist[n-1].Add(SendInterval); next.After(at) {
				at = next
			}
		}
		// Twenty in the last minute: wait for the oldest of them to age out.
		var recent []time.Time
		for _, t := range hist {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		s.sent[chat] = recent
		if len(recent) >= SendPerMin {
			if free := recent[len(recent)-SendPerMin].Add(time.Minute); free.After(at) {
				at = free
			}
		}
		s.mu.Unlock()
		d := at.Sub(now)
		if d <= 0 {
			return nil
		}
		if err := s.clk.Sleep(ctx, d); err != nil {
			return err
		}
	}
}

func (s *Sender) mark(chat int64) {
	s.mu.Lock()
	s.sent[chat] = append(s.sent[chat], s.clk.Now())
	s.mu.Unlock()
}

func (s *Sender) enqueue(o *out) (int64, error) {
	o.done = make(chan outResult, 1)
	s.mu.Lock()
	if o.urgent {
		s.urg = append(s.urg, o)
	} else {
		s.norm = append(s.norm, o)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	select {
	case r := <-o.done:
		return r.id, r.err
	case <-o.ctx.Done():
		return 0, o.ctx.Err()
	}
}

// Send queues a new message and waits until it is sent. Text over the limit is
// cut; callers split long answers themselves.
func (s *Sender) Send(ctx context.Context, chat int64, text string, markup *Markup, urgent bool) (int64, error) {
	return s.enqueue(&out{chat: chat, text: clipText(text), markup: markup, urgent: urgent, ctx: ctx})
}

// SendFormatted queues a message in Telegram's HTML with the plain text that
// says the same, as a reply to replyTo when it is not zero, and waits until one
// of them is sent: the plain text goes when Telegram refuses the HTML.
func (s *Sender) SendFormatted(ctx context.Context, chat int64, htmlText, plain string, replyTo int64) (int64, error) {
	return s.enqueue(&out{chat: chat, text: clipText(plain), html: htmlText, replyTo: replyTo, ctx: ctx})
}

// Edit queues an edit of a message and waits until it is made. A nil markup
// removes the buttons.
func (s *Sender) Edit(ctx context.Context, chat, message int64, text string, markup *Markup) error {
	_, err := s.enqueue(&out{chat: chat, edit: message, text: clipText(text), markup: markup, ctx: ctx})
	return err
}

func clipText(s string) string {
	r := []rune(s)
	if len(r) <= MaxText {
		return s
	}
	return string(r[:MaxText-1]) + "…"
}
