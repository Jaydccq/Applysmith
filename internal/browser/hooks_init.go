package browser

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// AddInitScript runs source in the main world at the start of every document
// of the tab and of its out-of-process frames, and now in the documents that
// are already there. It is for code that must be in place before page scripts
// run, such as an observer of the page's network calls.
func (hk *Hooks) AddInitScript(ctx context.Context, source string) error {
	if hk.Released() {
		return hk.closedError()
	}
	hk.mu.Lock()
	hk.initScripts = append(hk.initScripts, source)
	sessions := []*hookSession{hk.top}
	for _, s := range hk.sessions {
		sessions = append(sessions, s)
	}
	hk.mu.Unlock()
	for _, s := range sessions {
		if err := s.addInit(ctx, source); err != nil {
			return hk.mapErr(ctx, err)
		}
	}
	return nil
}

func (s *hookSession) addInit(ctx context.Context, source string) error {
	c, cancel := opCtx(ctx, s.ctx, inputDeadline)
	defer cancel()
	_, err := chromedp.Call(c, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: source, RunImmediately: ptr(true)})
	return err
}
