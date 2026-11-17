package browser

import (
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// trackerHosts are the analytics and advertising hosts the scan lane does not
// load. The list lives here only. A tracker vendor's own site still loads its
// own scripts: nothing of a vendor is blocked while the page's main frame is on
// that vendor's domain.
var trackerHosts = []string{
	"google-analytics.com", "googletagmanager.com", "googletagservices.com", "doubleclick.net", "googlesyndication.com",
	"googleadservices.com", "facebook.net", "hotjar.com", "segment.io", "segment.com", "mixpanel.com", "amplitude.com",
	"fullstory.com", "clarity.ms", "adsrvr.org", "scorecardresearch.com", "quantserve.com", "taboola.com", "outbrain.com",
	"criteo.com", "adnxs.com",
}

// blockedTypes are the resource types the scan lane fails outright.
var blockedTypes = []network.ResourceType{network.ResourceTypeImage, network.ResourceTypeMedia, network.ResourceTypeFont}

// setupDeadline bounds installing a tab's policies. A variable so a test can
// shorten it.
var setupDeadline = 15 * time.Second

// policyHook, when set, runs first when a scan tab's policy is installed. A
// test uses it to make the installation fail or hang.
var policyHook func(ctx context.Context) error

// shouldBlock decides one request of a scan tab. Documents and frames are never
// failed; nothing is blocked when the page's main frame, the requested host or
// the referring host is guarded.
func shouldBlock(topHost, reqHost, refHost string, typ network.ResourceType) bool {
	switch typ {
	case network.ResourceTypeDocument:
		return false
	}
	if Guarded(topHost) || Guarded(reqHost) || Guarded(refHost) {
		return false
	}
	for _, b := range blockedTypes {
		if typ == b {
			return true
		}
	}
	for _, h := range trackerHosts {
		if scoped(reqHost, h) {
			return topHost == "" || !scoped(topHost, h)
		}
	}
	return false
}

func hostOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// resourcePatterns are the request-stage patterns that pause only the blocked
// types and hosts; every other request proceeds without a round trip.
func resourcePatterns() []*fetch.RequestPattern {
	var ps []*fetch.RequestPattern
	for _, typ := range blockedTypes {
		ps = append(ps, &fetch.RequestPattern{ResourceType: typ, RequestStage: fetch.RequestStageRequest})
	}
	for _, h := range trackerHosts {
		ps = append(ps, &fetch.RequestPattern{URLPattern: "*" + h + "*", RequestStage: fetch.RequestStageRequest})
	}
	return ps
}

// installResourcePolicy installs (events live on ctx, the tab session; the commands are bounded by sctx) the scan-lane resource policy on a new tab,
// before its first navigation. The caller closes the tab when it fails.
func (i *Instance) installResourcePolicy(ctx, sctx context.Context, t *tab) error {
	if policyHook != nil {
		if err := policyHook(sctx); err != nil {
			return wrapError(CodeBrowserDisconnected, err, "cannot install the scan resource policy")
		}
	}
	if t.loadEverything {
		return nil
	}
	var top atomic.Value // the host of the tab's main frame
	watch(ctx, page.FrameNavigated, func(ev page.EventFrameNavigated) {
		if ev.Frame != nil && ev.Frame.ParentID == "" {
			top.Store(hostOfURL(ev.Frame.URL))
		}
	})
	watch(ctx, fetch.RequestPaused, func(ev fetch.EventRequestPaused) {
		go func() {
			topHost, _ := top.Load().(string)
			ref := ""
			if ev.Request != nil {
				r, _ := ev.Request.Headers["Referer"].(string)
				ref = hostOfURL(r)
			}
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if ev.Request != nil && shouldBlock(topHost, hostOfURL(ev.Request.URL), ref, ev.ResourceType) {
				_, _ = chromedp.Call(c, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
				return
			}
			_, _ = chromedp.Call(c, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID})
		}()
	})
	if _, err := chromedp.Call(sctx, fetch.Enable, fetch.EnableParams{Patterns: resourcePatterns()}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot install the scan resource policy")
	}
	return nil
}
