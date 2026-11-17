package launch

import (
	"context"
	"os"
	"sync"

	"applysmith/internal/browser"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
)

// BrowserCheck is the setup step's probe of the managed browser: it starts or
// adopts the browser and asks the apply window and the scan window what they
// advertise. Concurrent checks share the one discovered Chrome.
func BrowserCheck(ws *workspace.Workspace) func(context.Context) (onboarding.BrowserReport, error) {
	o, _ := browserOptions(ws)
	var mu sync.Mutex
	return func(ctx context.Context) (onboarding.BrowserReport, error) {
		mu.Lock()
		if o.Chrome.Path == "" {
			c, err := browser.DiscoverChrome(ctx, os.Getenv)
			if err != nil {
				mu.Unlock()
				return onboarding.BrowserReport{}, err
			}
			o.Chrome = c
		}
		opts := o
		mu.Unlock()
		inst, err := browser.Open(ctx, opts)
		if err != nil {
			return onboarding.BrowserReport{}, err
		}
		var r onboarding.BrowserReport
		for _, w := range browser.CheckFingerprint(ctx, inst) {
			r.Windows = append(r.Windows, onboarding.WindowReport{Lane: string(w.Lane), Attached: w.Attached, Webdriver: w.Webdriver, UserAgent: w.UserAgent, Error: w.Err})
		}
		return r, nil
	}
}
