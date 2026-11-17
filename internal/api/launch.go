package api

import (
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

const (
	launchParam = "launch"
	launchTTL   = 120 * time.Second
	// launchRefresh is how often the lock document is checked for a code that
	// has expired.
	launchRefresh  = 5 * time.Second
	launchPageText = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="color-scheme" content="light dark"><title>Applysmith</title>` +
		`<main style="max-width:36rem;margin:2rem auto;padding:0 1rem;font:16px/1.5 system-ui,sans-serif"><h1>Applysmith</h1>` +
		`<p>This address does not open the dashboard by itself.</p>` +
		`<p>Open it from the Applysmith app (menu bar item, Open Dashboard), or run ` +
		`<code>applysmith serve</code> and use the link it prints; <code>applysmith open</code> prints a new link at any time. ` +
		`A link carries a one-time code that expires after two minutes.</p></main>`
)

// launchCodes holds the one live launch code. Redeeming it is the only way to
// obtain a dashboard session, so a process that merely reaches the port cannot
// mint one. The code lives in memory and in the lock document of the private
// state directory. A code nobody has used is kept until it expires, so a link
// that was printed stays good for its whole lifetime; it is replaced when it is
// used, when it has expired, or when an authenticated caller asks for another.
type launchCodes struct {
	mu      sync.Mutex
	now     func() time.Time
	code    string
	sum     [sha256.Size]byte
	expires time.Time
}

// current returns the live code, creating a fresh one when there is none or
// the last one has expired. changed reports a new code.
func (l *launchCodes) current() (code string, changed bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.code != "" && l.now().Before(l.expires) {
		return l.code, false, nil
	}
	return l.replaceLocked()
}

// mint replaces whatever code is live with a new one.
func (l *launchCodes) mint() (code string, expires time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	code, _, err = l.replaceLocked()
	return code, l.expires, err
}

func (l *launchCodes) replaceLocked() (string, bool, error) {
	code, err := randomSecret()
	if err != nil {
		return "", false, err
	}
	l.code, l.sum, l.expires = code, digest(code), l.now().Add(launchTTL)
	return code, true, nil
}

// redeem consumes the code if it is the live one.
func (l *launchCodes) redeem(code string) bool {
	if code == "" {
		return false
	}
	sum := digest(code)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.code == "" || !l.now().Before(l.expires) || !matches(sum, l.sum) {
		return false
	}
	l.code, l.sum = "", [sha256.Size]byte{}
	return true
}

// LaunchURL is the address that opens the dashboard once: the API origin with
// the current launch code.
func (s *Server) LaunchURL() (string, error) {
	code, err := s.launchCode()
	if err != nil {
		return "", err
	}
	return DashboardURL(s.Origin(), code), nil
}

// DashboardURL is the address that opens the dashboard of the server at origin
// once, with the launch code a lock document or LaunchURL supplied. Without a
// code it is the plain address, which explains how to get one.
func DashboardURL(origin, code string) string {
	if code == "" {
		return origin + "/"
	}
	return origin + "/?" + launchParam + "=" + url.QueryEscape(code)
}

// launchCode returns the live code and republishes the lock document when it
// changed.
func (s *Server) launchCode() (string, error) {
	code, changed, err := s.launch.current()
	if err != nil {
		return "", err
	}
	if changed {
		s.publishLaunch(code)
	}
	return code, nil
}

// mintLaunchCode is the route behind `applysmith open`: an authenticated local
// caller gets a fresh code, and the previous one stops working.
func (s *Server) mintLaunchCode(c *apikit.Call) (apikit.Reply, error) {
	code, exp, err := s.launch.mint()
	if err != nil {
		return apikit.Reply{}, err
	}
	s.publishLaunch(code)
	return apikit.Reply{Data: wire.LaunchCode{Code: code, ExpiresAt: wire.Time{Time: exp.UTC()}}}, nil
}

// publishLaunch writes the code into the lock document. A failed write only
// means that a process attaching later cannot read the code; it is logged.
func (s *Server) publishLaunch(code string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if s.lock == nil {
		return
	}
	s.info.LaunchCode = code
	if err := s.lock.WriteInfo(s.info); err != nil && s.log != nil {
		s.log.Warn("lock document not updated", "error", err.Error())
	}
}

// openDocument decides what `GET /` does. It reports true when the dashboard
// document should be served to this request. Otherwise the response has been
// written: a redirect that carries the session key (and the new session's
// cookie, unless the browser already holds a live one), or the page that
// explains how to open the dashboard. Only a redeemed launch code issues a
// session or a key.
func (s *Server) openDocument(c *apikit.Call) (serve bool, err error) {
	var cookie string
	if ck, e := c.R.Cookie(SessionCookie); e == nil && s.sessions.live(ck.Value) {
		cookie = ck.Value
	}
	if c.R.Method == http.MethodGet && sameOriginDocument(c.R) {
		if codes := c.R.URL.Query()[launchParam]; len(codes) == 1 && s.launch.redeem(codes[0]) {
			// A live cookie alone opens the document, but its key lives in the
			// page's storage. A browser that lost the storage presents a fresh
			// link: the session it holds gets a new key and no second session
			// is issued.
			var key string
			if cookie != "" {
				var ok bool
				if key, ok, err = s.sessions.rekey(cookie); err != nil {
					return false, err
				}
				if !ok {
					cookie = ""
				}
			}
			if cookie == "" {
				var value string
				if value, key, err = s.sessions.issue(); err != nil {
					return false, err
				}
				http.SetCookie(c.W, &http.Cookie{
					Name: SessionCookie, Value: value, Path: "/", HttpOnly: true,
					SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL / time.Second),
				})
			}
			if _, err := s.launchCode(); err != nil {
				return false, err
			}
			// The fragment is read by the page and never sent to a server.
			c.W.Header().Set("Cache-Control", "no-store")
			c.W.Header().Set("Location", "/#"+SessionKeyFragment+"="+key)
			c.W.WriteHeader(http.StatusSeeOther)
			c.Handled = true
			return false, nil
		}
	}
	if cookie != "" {
		return true, nil
	}
	h := c.W.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusUnauthorized)
	_, _ = strings.NewReader(launchPageText).WriteTo(c.W)
	c.Handled = true
	return false, nil
}

// sameOriginDocument is true for a navigation the user made or one from the
// page's own origin. A page embedded or opened from another origin must not
// redeem a code, whatever it knows.
func sameOriginDocument(r *http.Request) bool {
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "", "none", "same-origin":
		return true
	}
	return false
}
