package browser

import (
	"net/url"
	"strings"
)

// schemeAllowed reports whether a managed page may be on raw: http, https,
// about:blank, data, and blob of an http or https origin. An empty URL is a
// page that has not started loading yet.
func schemeAllowed(raw string) bool {
	if raw == "" || raw == "about:blank" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "data":
		return true
	case "blob":
		inner, err := url.Parse(u.Opaque)
		if err != nil || inner.Host == "" {
			return false
		}
		s := strings.ToLower(inner.Scheme)
		return s == "http" || s == "https"
	case "about":
		return strings.HasPrefix(raw, "about:blank")
	}
	return false
}

// checkNavigationURL refuses a system-requested navigation before it reaches
// the browser.
func checkNavigationURL(raw string) error {
	if schemeAllowed(raw) {
		return nil
	}
	scheme := "unknown"
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		scheme = strings.ToLower(u.Scheme)
	}
	return newError(CodeSchemeNotAllowed, "navigation to a %s: URL is not allowed", scheme)
}
