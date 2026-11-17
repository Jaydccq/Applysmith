//go:build browser

package browser

import (
	"context"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// WithHostRules maps host names to loopback in the launched Chrome, so an
// end-to-end test can serve pages from sites with different registrable
// domains. It exists only under the browser build tag, which production
// builds never set.
func (o Options) WithHostRules(rules string) Options {
	o.hostRules = rules
	return o
}

// OfflineRequest is one request of a tab that ServeOffline answers.
type OfflineRequest struct {
	URL, Method string
	// Type is the resource type Chrome gives the request (Document, Image,
	// Stylesheet, ...).
	Type string
}

// OfflineReply is the response to a request that ServeOffline serves.
type OfflineReply struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// offlineCall bounds one answer to a paused request.
const offlineCall = 5 * time.Second

// ServeOffline answers every request the tab can have intercepted from serve,
// and fails each one serve declines as blocked by the client, so no request
// of the tab reaches the network. It is installed before the tab's first
// navigation and lasts as long as the tab. It exists only under the browser
// build tag, for tools that load a recorded page at its own address.
func (h *LaneHandle) ServeOffline(ctx context.Context, tabID string, serve func(OfflineRequest) (OfflineReply, bool)) error {
	t, err := h.tab(tabID)
	if err != nil {
		return err
	}
	sess, err := h.session(t)
	if err != nil {
		return err
	}
	watch(sess, fetch.RequestPaused, func(ev fetch.EventRequestPaused) {
		go func() {
			c, cancel := context.WithTimeout(sess, offlineCall)
			defer cancel()
			req := OfflineRequest{Type: string(ev.ResourceType)}
			if ev.Request != nil {
				req.URL, req.Method = ev.Request.URL, ev.Request.Method
			}
			reply, ok := serve(req)
			if !ok {
				_, _ = chromedp.Call(c, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
				return
			}
			var headers []*fetch.HeaderEntry
			for k, v := range reply.Headers {
				headers = append(headers, &fetch.HeaderEntry{Name: k, Value: v})
			}
			_, _ = chromedp.Call(c, fetch.FulfillRequest, fetch.FulfillRequestParams{RequestID: ev.RequestID, ResponseCode: int64(reply.Status), ResponseHeaders: headers, Body: reply.Body})
		}()
	})
	octx, cancel := opCtx(ctx, sess, setupDeadline)
	defer cancel()
	if _, err := chromedp.Call(octx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}}); err != nil {
		return h.wrapOp(t, err, "installing the offline server", setupDeadline)
	}
	return nil
}
