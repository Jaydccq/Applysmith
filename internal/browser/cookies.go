package browser

import (
	"context"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

// cookieDeadline bounds one cookie command.
const cookieDeadline = 15 * time.Second

// Cookie is one unpartitioned cookie of the managed profile. Value is a secret:
// it is written only to the profile and to the undo snapshot, and a Cookie
// formatted by accident prints no name or value.
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Domain has a leading dot for a domain cookie and none for a host-only one.
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
	// SameSite is "", "Lax", "Strict" or "None".
	SameSite string `json:"sameSite"`
	// Expires is Unix seconds; zero is a session cookie.
	Expires float64 `json:"expires"`
}

func (c Cookie) String() string   { return "cookie of " + c.host() }
func (c Cookie) GoString() string { return c.String() }

// host is the domain without its leading dot.
func (c Cookie) host() string { return strings.TrimPrefix(strings.ToLower(c.Domain), ".") }

// key identifies a cookie in the profile.
func (c Cookie) key() string { return c.Name + "\x00" + strings.ToLower(c.Domain) + "\x00" + c.Path }

// CookieJar is the managed profile's cookie store.
type CookieJar interface {
	// Cookies lists every unpartitioned cookie.
	Cookies(ctx context.Context) ([]Cookie, error)
	// SetCookie stores a cookie. The browser may accept a cookie and still drop
	// it, so callers read the store back.
	SetCookie(ctx context.Context, c Cookie) error
	// DeleteCookie removes the cookie with the same name, domain and path.
	DeleteCookie(ctx context.Context, c Cookie) error
}

var _ CookieJar = (*Instance)(nil)

// Cookies lists the unpartitioned cookies of the profile.
func (i *Instance) Cookies(ctx context.Context) ([]Cookie, error) {
	c, cancel := opCtx(ctx, i.root, cookieDeadline)
	defer cancel()
	res, err := chromedp.CallBrowser(c, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return nil, wrapError(CodeBrowserDisconnected, err, "cannot read the cookies")
	}
	out := make([]Cookie, 0, len(res.Cookies))
	for _, ck := range res.Cookies {
		if ck.PartitionKey != nil {
			continue
		}
		out = append(out, fromNetwork(ck))
	}
	return out, nil
}

// SetCookie stores the cookie.
func (i *Instance) SetCookie(ctx context.Context, c Cookie) error {
	return i.writeCookie(ctx, c, c.Expires)
}

// DeleteCookie expires the cookie. The browser's store has no deletion
// command, and a cookie set with a past expiry replaces and removes it.
func (i *Instance) DeleteCookie(ctx context.Context, c Cookie) error {
	c.Value = ""
	return i.writeCookie(ctx, c, 1)
}

func (i *Instance) writeCookie(ctx context.Context, c Cookie, expires float64) error {
	cctx, cancel := opCtx(ctx, i.root, cookieDeadline)
	defer cancel()
	p := &network.CookieParam{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
	switch c.SameSite {
	case "Lax":
		p.SameSite = network.CookieSameSiteLax
	case "Strict":
		p.SameSite = network.CookieSameSiteStrict
	case "None":
		p.SameSite = network.CookieSameSiteNone
	}
	if expires > 0 {
		p.Expires = cdp.TimeSinceEpoch(expires)
	}
	if _, err := chromedp.CallBrowser(cctx, storage.SetCookies, storage.SetCookiesParams{Cookies: []*network.CookieParam{p}}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot write a cookie of %s", c.host())
	}
	return nil
}

func fromNetwork(ck *network.Cookie) Cookie {
	c := Cookie{Name: ck.Name, Value: ck.Value, Domain: ck.Domain, Path: ck.Path, Secure: ck.Secure, HTTPOnly: ck.HTTPOnly, SameSite: string(ck.SameSite)}
	if !ck.Session {
		c.Expires = ck.Expires
	}
	return c
}
