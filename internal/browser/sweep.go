package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

// Marker cookie: it lets an extension loaded into the managed profile tell
// where it runs. It holds no data.
const (
	markerName = "applysmith_managed"
	markerHost = "managed.applysmith.invalid"
)

// SweepSessionCookies gives every session cookie of the profile an expiry 30
// days ahead, so a sign-in survives a restart of Chrome. Scope (host-only or
// domain), path, Secure, HttpOnly, SameSite and the value are carried; a cookie
// that already has an expiry is left alone, which is also what keeps the sweep
// from rewriting the same cookie forever. It returns how many cookies it
// rewrote. Chrome commits its cookie store to disk on its own schedule; there is
// no command to force that, so a sweep is durable within about 30 seconds.
//
// A sweep is the instance's own upkeep and does not count as use: marking it
// in flight would restart the idle clock every minute, and the idle stop would
// never come.
func (i *Instance) SweepSessionCookies(ctx context.Context) (int, error) {
	cookies, err := i.Cookies(ctx)
	if err != nil {
		return 0, err
	}
	expires := float64(i.clock().Add(sessionCookieLife).Unix())
	var n int
	hosts := map[string]bool{}
	for _, c := range cookies {
		if c.Expires != 0 || c.host() == markerHost {
			continue
		}
		c.Expires = expires
		if err := i.SetCookie(ctx, c); err != nil {
			return n, err
		}
		n++
		hosts[c.host()] = true
	}
	if n > 0 {
		i.flushCookies(ctx)
		var names []string
		for h := range hosts {
			names = append(names, h)
		}
		sort.Strings(names)
		// A count and the domains; never a name or a value.
		i.logEvent(EventCookieSweep, "", strconv.Itoa(n)+" cookies: "+strings.Join(names, ", "))
	}
	return n, nil
}

// flushBatch is how many times the marker cookie is rewritten to push the
// cookie store over Chrome's batch threshold (512 pending changes), which makes
// it commit at once. Chrome has no command to flush the store, and left alone
// it commits 30 seconds after the first change, or not at all when the process
// is killed or exits quickly.
const flushBatch = 600

// flushCookies commits the cookie store to disk now, best effort: a sweep that
// changed cookies is durable when it returns, within a short bound.
func (i *Instance) flushCookies(ctx context.Context) {
	exp := cdp.TimeSinceEpoch(float64(i.clock().Add(sessionCookieLife).Unix()))
	batch := make([]*network.CookieParam, flushBatch)
	for n := range batch {
		batch[n] = &network.CookieParam{Name: markerName, Value: "1", Domain: markerHost, Path: "/", Expires: exp, SameSite: network.CookieSameSiteLax}
	}
	before := cookieStoreStamp(i.proc.Profile)
	c, cancel := opCtx(ctx, i.root, cookieDeadline)
	defer cancel()
	if _, err := chromedp.CallBrowser(c, storage.SetCookies, storage.SetCookiesParams{Cookies: batch}); err != nil {
		return
	}
	// The commit runs on Chrome's own thread; wait for the file to change.
	for end := time.Now().Add(flushWait); time.Now().Before(end); {
		if cookieStoreStamp(i.proc.Profile) != before {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// flushWait bounds the wait for the cookie store file to change after a flush.
const flushWait = 1500 * time.Millisecond

// cookieStoreStamp identifies the state of Chrome's cookie database files by
// modification time and size.
func cookieStoreStamp(profile string) string {
	var b strings.Builder
	for _, rel := range []string{"Default/Network/Cookies", "Default/Network/Cookies-journal", "Default/Cookies", "Default/Cookies-journal"} {
		if st, err := os.Stat(filepath.Join(profile, rel)); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", rel, st.ModTime().UnixNano(), st.Size())
		}
	}
	return b.String()
}

// sweepBeforeStop runs the sweep a graceful stop owes. A browser that cannot be
// asked loses nothing it still held; the stop goes on.
func (i *Instance) sweepBeforeStop(ctx context.Context) {
	if !i.ready.Load() || i.deadInfo() != nil {
		return
	}
	c, cancel := context.WithTimeout(ctx, cookieDeadline)
	defer cancel()
	_, _ = i.SweepSessionCookies(c)
}

// writeMarker writes the marker cookie, valid for 30 days, so its expiry
// dates the last write.
func (i *Instance) writeMarker(ctx context.Context) error {
	return i.SetCookie(ctx, Cookie{
		Name: markerName, Value: "1", Domain: markerHost, Path: "/", SameSite: "Lax",
		Expires: float64(i.clock().Add(sessionCookieLife).Unix()),
	})
}

// watchCookies sweeps session cookies every minute and writes the marker again
// every five, until the instance ends.
func (i *Instance) watchCookies() {
	sweep := time.NewTicker(i.sweepEvery)
	defer sweep.Stop()
	mark := time.NewTicker(i.markEvery)
	defer mark.Stop()
	for {
		select {
		case <-i.root.Done():
			return
		case <-sweep.C:
			c, cancel := context.WithTimeout(i.root, cookieDeadline)
			_, _ = i.SweepSessionCookies(c)
			cancel()
		case <-mark.C:
			c, cancel := context.WithTimeout(i.root, cookieDeadline)
			_ = i.writeMarker(c)
			cancel()
		}
	}
}
