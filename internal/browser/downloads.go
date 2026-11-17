package browser

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// download tracks one apply-lane download from announcement to its move.
type download struct {
	suggested string
	keep      bool // the lane decision; false means cancelled
}

func (i *Instance) stagingDir() string { return filepath.Join(i.opts.StateDir, "downloads-staging") }

// setupDownloads stages every download under the state directory and turns on
// download events. Whether a download is kept is decided per lane when it
// starts; a partial file never reaches the Downloads directory.
func (i *Instance) setupDownloads(ctx context.Context) error {
	if err := i.prepareStaging(); err != nil {
		return err
	}
	_, err := chromedp.CallBrowser(ctx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{
		Behavior: browser.SetDownloadBehaviorBehaviorAllowAndName, DownloadPath: i.stagingDir(), EventsEnabled: ptr(true),
	})
	if err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot set download behavior")
	}
	return nil
}

// prepareStaging makes the staging directory. A browser this server launched
// starts with it empty: what an earlier browser left there is a download that
// will never finish. An adopted browser's downloads go on, so its files stay.
func (i *Instance) prepareStaging() error {
	if launched := i.proc.unwatch == nil; launched {
		if err := os.RemoveAll(i.stagingDir()); err != nil {
			return wrapError(CodeProfileUnsafe, err, "cannot empty the download staging directory")
		}
	}
	if err := os.MkdirAll(i.stagingDir(), 0o700); err != nil {
		return wrapError(CodeProfileUnsafe, err, "cannot create download staging directory")
	}
	return nil
}

func (i *Instance) watchDownloads() {
	watchBrowser(i.root, browser.DownloadWillBegin, func(ev browser.EventDownloadWillBegin) {
		go i.onDownloadBegin(ev)
	})
	watchBrowser(i.root, browser.DownloadProgress, func(ev browser.EventDownloadProgress) {
		go i.onDownloadProgress(ev)
	})
}

// laneOfFrame returns the lane of the tab that holds frameID, or "" when no
// managed tab does.
func (i *Instance) laneOfFrame(ctx context.Context, frameID cdp.FrameID) Lane {
	if t := i.lookup(target.ID(frameID)); t != nil {
		return t.lane
	}
	if infos, err := chromedp.Targets(ctx); err == nil {
		for _, in := range infos {
			if in.TargetID == target.ID(frameID) && in.ParentID != "" {
				if t := i.lookup(in.ParentID); t != nil {
					return t.lane
				}
			}
		}
	}
	i.mu.Lock()
	var tabs []*tab
	for _, t := range i.tabs {
		tabs = append(tabs, t)
	}
	i.mu.Unlock()
	for _, t := range tabs {
		t.mu.Lock()
		sess := t.ctx
		t.mu.Unlock()
		if sess == nil {
			continue
		}
		fctx, cancel := context.WithTimeout(sess, 2*time.Second)
		tree, err := chromedp.Call(fctx, page.GetFrameTree, cdp.Empty{})
		cancel()
		if err == nil && frameInTree(tree.FrameTree, frameID) {
			return t.lane
		}
	}
	return ""
}

func frameInTree(t *page.FrameTree, id cdp.FrameID) bool {
	if t.Frame.ID == id {
		return true
	}
	for _, c := range t.ChildFrames {
		if frameInTree(c, id) {
			return true
		}
	}
	return false
}

func (i *Instance) onDownloadBegin(ev browser.EventDownloadWillBegin) {
	ctx, cancel := context.WithTimeout(i.root, 5*time.Second)
	defer cancel()
	lane := i.laneOfFrame(ctx, ev.FrameID)
	d := &download{suggested: ev.SuggestedFilename, keep: lane == LaneApply}
	i.mu.Lock()
	i.downloads[ev.GUID] = d
	i.mu.Unlock()
	if !d.keep {
		_, _ = chromedp.CallBrowser(ctx, browser.CancelDownload, browser.CancelDownloadParams{GUID: ev.GUID})
		reason := "scan-lane"
		if lane != LaneScan {
			reason = "no-lane"
		}
		i.logEvent(EventDownloadCancelled, lane, reason)
		i.discardStaged(ev.GUID)
		go i.discardStagedLater(ev.GUID)
	}
}

// decision waits for the lane decision of a download; progress events can be
// handled before the begin event's lane lookup returns.
func (i *Instance) decision(guid string) *download {
	for range 250 {
		i.mu.Lock()
		d := i.downloads[guid]
		i.mu.Unlock()
		if d != nil {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (i *Instance) onDownloadProgress(ev browser.EventDownloadProgress) {
	switch ev.State {
	case browser.DownloadProgressStateInProgress:
		return
	case browser.DownloadProgressStateCompleted:
		d := i.decision(ev.GUID)
		i.mu.Lock()
		delete(i.downloads, ev.GUID)
		i.mu.Unlock()
		staged := filepath.Join(i.stagingDir(), ev.GUID)
		if d == nil || !d.keep {
			i.discardStaged(ev.GUID)
			return
		}
		if err := os.MkdirAll(i.opts.DownloadsDir, 0o755); err != nil {
			i.logEvent("download_failed", LaneApply, "downloads directory unavailable")
			i.discardStaged(ev.GUID)
			return
		}
		if _, err := moveStaged(staged, i.opts.DownloadsDir, d.suggested, time.Now()); err != nil {
			i.logEvent("download_failed", LaneApply, "cannot move the finished file")
			i.discardStaged(ev.GUID)
			return
		}
		i.logEvent(EventDownloadSaved, LaneApply, SafeDownloadName(d.suggested))
	default:
		i.mu.Lock()
		delete(i.downloads, ev.GUID)
		i.mu.Unlock()
		i.discardStaged(ev.GUID)
	}
}

// discardStagedLater removes what Chrome wrote for a cancelled download after
// the cancel, which can create its staging file a moment late.
func (i *Instance) discardStagedLater(guid string) {
	for _, wait := range []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 2 * time.Second} {
		select {
		case <-i.root.Done():
			i.discardStaged(guid)
			return
		case <-time.After(wait):
		}
		i.discardStaged(guid)
	}
}

func (i *Instance) discardStaged(guid string) {
	_ = os.Remove(filepath.Join(i.stagingDir(), guid))
	_ = os.Remove(filepath.Join(i.stagingDir(), guid+".crdownload"))
}
