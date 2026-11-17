package browser

import (
	"context"
	"os"
	"sync"
)

// InstanceSessions serves the sign-in sync from the managed browser o
// describes. The browser starts, or is adopted, on the first import or undo;
// listing sites never starts it. An empty o.Chrome is discovered then.
func InstanceSessions(o Options, dir string, accounts func(context.Context) ([]StoredAccount, error), states SignInStore) *Sessions {
	var mu sync.Mutex
	open := func(ctx context.Context) (*Instance, error) {
		mu.Lock()
		if o.Chrome.Path == "" {
			c, err := DiscoverChrome(ctx, os.Getenv)
			if err != nil {
				mu.Unlock()
				return nil, err
			}
			o.Chrome = c
		}
		opts := o
		mu.Unlock()
		return Open(ctx, opts)
	}
	return NewSessions(SessionsConfig{
		Dir: dir,
		Open: func(ctx context.Context) (CookieJar, error) {
			inst, err := open(ctx)
			if err != nil {
				return nil, err
			}
			return inst, nil
		},
		Probe: func(ctx context.Context, site SignInSite) (PageReading, error) {
			inst, err := open(ctx)
			if err != nil {
				return PageReading{}, err
			}
			return inst.readSignInPage(ctx, site)
		},
		States: states,
		Running: func() CookieJar {
			if i := running(o.ProfileDir); i != nil {
				return i
			}
			return nil
		},
		Accounts: accounts,
	})
}

// running returns the started instance for the profile, or nil.
func running(profileDir string) *Instance {
	registry.mu.Lock()
	c := registry.calls[profileKey(profileDir)]
	registry.mu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
	default:
		return nil
	}
	if c.err != nil || c.inst == nil || c.inst.isStopped() {
		return nil
	}
	return c.inst
}

// openPending reports a launch in progress for the profile.
func openPending(profileDir string) bool {
	registry.mu.Lock()
	c := registry.calls[profileKey(profileDir)]
	registry.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
