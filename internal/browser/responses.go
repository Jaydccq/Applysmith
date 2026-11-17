package browser

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"applysmith/internal/lazyregexp"
)

// Response observation limits.
const (
	// MaxResponseBody is how many characters of a response body a watch keeps.
	MaxResponseBody = 200_000
	// maxWatchedResponses is how many records a watch keeps: the newest.
	maxWatchedResponses = 200
)

// Response is one answer to a request the page made. The address is reduced to
// origin and path. Body is read only for a request that matches a filter or for
// a failing XHR or fetch, and is cut at MaxResponseBody characters.
type Response struct {
	Method        string
	URL           string
	Status        int
	Type          string
	At            time.Time
	Body          string
	BodyTruncated bool
}

// ResponseFilter selects responses by address (substring or pattern), method
// (case-insensitive) and resource type. The zero filter matches everything.
type ResponseFilter struct {
	URL     string
	Pattern *lazyregexp.Regexp
	Method  string
	Type    string
}

// WriteFilter matches the answers to writes, which is what a submission
// confirmation reads.
var WriteFilter = ResponseFilter{Method: "WRITE"}

func (f ResponseFilter) matches(r Response) bool {
	if f.Method == "WRITE" {
		if !isWrite(r.Method) {
			return false
		}
	} else if f.Method != "" && !strings.EqualFold(f.Method, r.Method) {
		return false
	}
	if f.URL != "" && !strings.Contains(r.URL, f.URL) {
		return false
	}
	if f.Pattern != nil && !f.Pattern.MatchString(r.URL) {
		return false
	}
	return f.Type == "" || strings.EqualFold(f.Type, r.Type)
}

func isWrite(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func isScripted(typ string) bool { return typ == "XHR" || typ == "Fetch" }

// ResponseWatch observes the answers a tab receives. The Network domain is on
// only while navigation or a response watch needs it. Request headers, cookies,
// credentials and request bodies are never read.
type ResponseWatch struct {
	ctx            context.Context // the observation's: a body read ends with it
	cancel         context.CancelFunc
	releaseNetwork func()
	stop           sync.Once

	mu      sync.Mutex
	reqs    map[network.RequestID]*inflight
	done    []Response
	filters []ResponseFilter
}

// inflight is a request between its first event and its record. Each of the
// four network events arrives on its own subscription, handled on its own
// goroutine, so the events of one request are handled in any order (a loaded
// machine made the end of loading come first and the answer was lost). Each
// event adds only what it carries; the record is made once the request, its
// answer and the end of its loading are all in. An entry whose request was
// never seen is parked: it was sent before the watch began, or its request
// event is still on its way.
//
// A redirect is a second request event of the same request, carrying the
// answer to the hop before it; only the last hop gets an answer event. asked
// is the address the latest hop seen was sent to, which tells an answer of
// that hop from the answer of a later one whose request event is not in yet.
type inflight struct {
	Response
	asked                                       string
	sent, answered, finished, failed, recording bool
}

func newResponseWatch(filters ...ResponseFilter) *ResponseWatch {
	return &ResponseWatch{ctx: context.Background(), reqs: map[network.RequestID]*inflight{}, filters: filters}
}

// WatchResponses turns the tab's network observation on, reading the bodies of
// the answers to writes. Observation is for the submit window: the caller
// stops it afterwards.
func (h *LaneHandle) WatchResponses(ctx context.Context, tabID string) (*ResponseWatch, error) {
	return h.WatchResponsesFor(ctx, tabID, WriteFilter)
}

// WatchResponsesFor is WatchResponses reading the bodies of the answers that
// match any of the filters (and of failing XHR and fetch responses).
func (h *LaneHandle) WatchResponsesFor(ctx context.Context, tabID string, filters ...ResponseFilter) (*ResponseWatch, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	sess, err := h.session(t)
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithCancel(sess)
	w := newResponseWatch(filters...)
	w.ctx, w.cancel = wctx, cancel
	octx, ocancel := opCtx(ctx, sess, clickDeadline)
	defer ocancel()
	release, err := t.observeNetwork(octx, sess)
	if err != nil {
		cancel()
		return nil, h.wrapOp(t, err, "starting response observation", clickDeadline)
	}
	w.releaseNetwork = release
	watch(wctx, network.RequestWillBeSent, w.onRequest)
	watch(wctx, network.ResponseReceived, w.onResponse)
	watch(wctx, network.LoadingFinished, w.onFinished)
	watch(wctx, network.LoadingFailed, w.onFailed)
	return w, nil
}

// entry returns the request's entry, adding it. The caller holds w.mu.
func (w *ResponseWatch) entry(id network.RequestID) *inflight {
	e := w.reqs[id]
	if e == nil {
		e = &inflight{}
		w.reqs[id] = e
	}
	return e
}

func (w *ResponseWatch) onRequest(ev network.EventRequestWillBeSent) {
	w.mu.Lock()
	if w.reqs[ev.RequestID] == nil && ev.RedirectResponse != nil {
		// A redirect hop of a request that already settled or began before
		// the watch: it must not start an entry that never ends.
		w.mu.Unlock()
		return
	}
	e := w.entry(ev.RequestID)
	if e.sent && ev.RedirectResponse != nil {
		// The hop before this one is answered here and nowhere else: it keeps
		// its own record, so a write answered with a redirect stays a write.
		w.record(Response{Method: e.Method, URL: reduceURL(ev.RedirectResponse.URL), Status: int(ev.RedirectResponse.Status), Type: e.Type})
	}
	e.sent, e.Method, e.asked = true, ev.Request.Method, reduceURL(ev.Request.URL)
	if !e.answered {
		e.URL, e.Type = e.asked, string(ev.Type)
	}
	w.settle(ev.RequestID, e)
}

func (w *ResponseWatch) onResponse(ev network.EventResponseReceived) {
	w.mu.Lock()
	e := w.entry(ev.RequestID)
	e.answered, e.Status = true, int(ev.Response.Status)
	e.URL = reduceURL(ev.Response.URL)
	if e.Type == "" {
		e.Type = string(ev.Type)
	}
	w.settle(ev.RequestID, e)
}

func (w *ResponseWatch) onFinished(ev network.EventLoadingFinished) {
	w.mu.Lock()
	w.entry(ev.RequestID).finished = true
	w.settle(ev.RequestID, w.reqs[ev.RequestID])
}

// onFailed forgets a request that never produced an answer.
func (w *ResponseWatch) onFailed(ev network.EventLoadingFailed) {
	w.mu.Lock()
	w.entry(ev.RequestID).failed = true
	w.settle(ev.RequestID, w.reqs[ev.RequestID])
}

// settle ends the request once all its events are in: a failed one is
// forgotten, an answered one is recorded with its body when one is wanted.
// The caller holds w.mu; settle releases it.
func (w *ResponseWatch) settle(id network.RequestID, e *inflight) {
	switch {
	case !e.sent || e.recording:
	case e.failed:
		delete(w.reqs, id)
	case e.answered && isWrite(e.Method) && e.asked != e.URL:
		// The answer came from another address than the write went to: it
		// answers a later hop, whose request event is still on its way.
		// Recording now would credit the write with that page's status.
	case e.answered && e.finished:
		e.recording = true
		done := e.Response
		w.mu.Unlock()
		if w.wantsBody(done) {
			bctx, cancel := context.WithTimeout(w.ctx, bodyDeadline)
			res, err := chromedp.Call(bctx, network.GetResponseBody, network.GetResponseBodyParams{RequestID: id})
			cancel()
			if err == nil {
				done.Body, done.BodyTruncated = cut(string(res.Body), MaxResponseBody)
			}
		}
		w.mu.Lock()
		delete(w.reqs, id)
		w.record(done)
	}
	w.mu.Unlock()
}

// wantsBody reports whether the body of an answer is read: a failing XHR or
// fetch, or an answer that matches a filter. Each read is a round trip.
func (w *ResponseWatch) wantsBody(r Response) bool {
	if r.Status >= 400 && isScripted(r.Type) {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.filters {
		if f.matches(r) {
			return true
		}
	}
	return false
}

// record keeps the newest maxWatchedResponses records. The caller holds w.mu.
func (w *ResponseWatch) record(r Response) {
	r.At = time.Now()
	w.done = append(w.done, r)
	if len(w.done) > maxWatchedResponses {
		w.done = append(w.done[:0], w.done[len(w.done)-maxWatchedResponses:]...)
	}
}

func cut(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	return s[:n], true
}

func truncate(s string, n int) string {
	out, _ := cut(s, n)
	return out
}

// Responses returns the finished answers seen so far, oldest first.
func (w *ResponseWatch) Responses() []Response {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Response(nil), w.done...)
}

// Pending is the number of requests sent whose answer is not recorded yet: a
// caller that reads Responses right after an event waits for it to reach zero
// so a late answer is not missed.
func (w *ResponseWatch) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, e := range w.reqs {
		if e.sent {
			n++
		}
	}
	return n
}

// Stop turns the observation off. It is safe to call more than once and after
// the tab closed. The recorded bodies are dropped.
func (w *ResponseWatch) Stop() {
	w.stop.Do(func() {
		w.cancel()
		w.releaseNetwork()
		w.mu.Lock()
		w.done, w.reqs = nil, map[network.RequestID]*inflight{}
		w.mu.Unlock()
	})
}

// observeNetwork shares the session's event domain among navigations and
// response watches. Releasing one consumer must not stop another consumer.
// A detached session cannot release observation acquired on its replacement.
func (t *tab) observeNetwork(ctx, sess context.Context) (func(), error) {
	t.networkMu.Lock()
	defer t.networkMu.Unlock()
	if t.networkUsers[sess] == 0 {
		if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{}); err != nil {
			return nil, err
		}
	}
	if t.networkUsers == nil {
		t.networkUsers = make(map[context.Context]int)
	}
	t.networkUsers[sess]++
	var once sync.Once
	return func() {
		once.Do(func() {
			t.networkMu.Lock()
			defer t.networkMu.Unlock()
			t.networkUsers[sess]--
			if t.networkUsers[sess] == 0 {
				delete(t.networkUsers, sess)
				dctx, cancel := context.WithTimeout(sess, 2*time.Second)
				defer cancel()
				_, _ = chromedp.Call(dctx, network.Disable, cdp.Empty{})
			}
		})
	}, nil
}
