package main

import (
	"errors"
	"net/url"
	"strings"
)

// maxLinkLength is the longest address the shell will hand to another program.
const maxLinkLength = 4096

// errLinkRefused is why a link was not opened; it never carries the address,
// whose query string may hold a token.
var errLinkRefused = errors.New("link_refused")

// checkLink applies the external-link rules: a web address of at most 4,096
// characters without credentials, or a mail address. Every other scheme
// (file, javascript, data, custom schemes) is refused. The returned label
// names the scheme and host only, for the log.
func checkLink(raw string) (label string, err error) {
	if len(raw) > maxLinkLength {
		return "too long", errLinkRefused
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "unparsable", errLinkRefused
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
		if u.User != nil || u.Hostname() == "" {
			return scheme + " without a plain host", errLinkRefused
		}
		return scheme + "://" + u.Host, nil
	case "mailto":
		return "mailto", nil
	}
	return "scheme " + scheme, errLinkRefused
}

// webOnly accepts the addresses an apply window can load.
func webOnly(raw string) error {
	label, err := checkLink(raw)
	if err != nil || strings.HasPrefix(label, "mailto") {
		return errLinkRefused
	}
	return nil
}

// sameOrigin reports whether raw is a page of the server at origin.
func sameOrigin(raw, origin string) bool {
	u, err := url.Parse(raw)
	o, err2 := url.Parse(origin)
	return err == nil && err2 == nil && o.Host != "" && u.Scheme == o.Scheme && u.Host == o.Host
}
