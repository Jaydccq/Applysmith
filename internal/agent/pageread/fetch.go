// Clean-room: docs/provenance.md#CR-AGENT-TAINT

// Package pageread fetches one public web page for the Ask Agent and reduces
// it to text. It refuses every destination that is not a public internet host,
// and it makes the connection to the address it checked, so a name cannot be
// answered differently between the check and the connection.
package pageread

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/identity"
)

// Limits of one read.
const (
	// MaxRedirects is the most redirects one read follows.
	MaxRedirects = 5
	// MaxBodyBytes bounds the decoded body that is read.
	MaxBodyBytes = 1 << 20
	// MaxTextRunes bounds the extracted text.
	MaxTextRunes = 20000
	// DefaultTimeout bounds a whole read, redirects included.
	DefaultTimeout = 20 * time.Second
)

// Rules a refusal can name, besides the host rules of identity.
const (
	RuleScheme      = "scheme"
	RuleUserInfo    = "user-info"
	RuleRedirectTo  = "redirect-scheme"
	RuleRedirects   = "too-many-redirects"
	RuleResolves    = "resolves-non-public"
	RuleContentType = "content-type"
)

// Refusal is a read that was not made, or not completed, because a rule fired.
// Host is the host of the URL that was refused, never the full URL.
type Refusal struct {
	Rule string
	Host string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("the page was refused by rule %s (host %s)", r.Rule, r.Host)
}

// Resolver looks a host name up. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configure a Fetcher; the zero value is the production reader.
type Options struct {
	Resolver Resolver
	// Dial opens the connection to an address that was already checked. Tests
	// use it to reach a local server.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS overrides the client TLS configuration, for tests.
	TLS     *tls.Config
	Timeout time.Duration
}

// Fetcher reads pages.
type Fetcher struct {
	resolver Resolver
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	tls      *tls.Config
	timeout  time.Duration
}

// New returns a Fetcher.
func New(o Options) *Fetcher {
	f := &Fetcher{resolver: o.Resolver, dial: o.Dial, tls: o.TLS, timeout: o.Timeout}
	if f.resolver == nil {
		f.resolver = net.DefaultResolver
	}
	if f.dial == nil {
		f.dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	if f.timeout <= 0 {
		f.timeout = DefaultTimeout
	}
	return f
}

// Page is a page that was read.
type Page struct {
	// Host is the host of the page that finally answered.
	Host        string
	Status      int
	ContentType string
	Title       string
	Text        string
	// Truncated says the body or the text was cut at a limit.
	Truncated bool
}

var allowedTypes = map[string]bool{
	"text/html": true, "application/xhtml+xml": true, "text/plain": true, "application/json": true,
}

// Read fetches rawURL and returns its text. A destination that is not a public
// host is a *Refusal, also when a redirect leads to it.
func (f *Fetcher) Read(ctx context.Context, rawURL string) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	cur, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Page{}, &Refusal{Rule: RuleScheme}
	}
	for hop := 0; ; hop++ {
		addrs, err := f.check(ctx, cur)
		if err != nil {
			return Page{}, err
		}
		resp, err := f.get(ctx, cur, addrs)
		if err != nil {
			return Page{}, err
		}
		if loc := redirectTarget(resp); loc != "" {
			_ = resp.Body.Close()
			if hop == MaxRedirects {
				return Page{}, &Refusal{Rule: RuleRedirects, Host: cur.Hostname()}
			}
			next, err := cur.Parse(loc)
			if err != nil || (next.Scheme != "http" && next.Scheme != "https") {
				return Page{}, &Refusal{Rule: RuleRedirectTo, Host: cur.Hostname()}
			}
			cur = next
			continue
		}
		defer resp.Body.Close()
		return readBody(resp, cur)
	}
}

func redirectTarget(r *http.Response) string {
	switch r.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return r.Header.Get("Location")
	}
	return ""
}

// check applies the rules to one URL and returns the addresses it may be
// reached at, every one of them checked.
func (f *Fetcher) check(ctx context.Context, u *url.URL) ([]netip.Addr, error) {
	host := u.Hostname()
	refuse := func(rule string) error { return &Refusal{Rule: rule, Host: host} }
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, refuse(RuleScheme)
	case u.User != nil:
		return nil, refuse(RuleUserInfo)
	}
	if rule := identity.NonPublicHost(host); rule != "" {
		return nil, refuse(rule)
	}
	if ip, ok := identity.ParseIPLiteral(host); ok {
		return []netip.Addr{ip}, nil
	}
	addrs, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("the name %s did not resolve: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("the name %s has no address", host)
	}
	for _, a := range addrs {
		if rule := identity.NonPublicAddr(a); rule != "" {
			return nil, &Refusal{Rule: RuleResolves + ":" + rule, Host: host}
		}
	}
	return addrs, nil
}

// get makes one request without following a redirect. The connection goes to
// one of the checked addresses whatever the URL's name would resolve to now.
func (f *Fetcher) get(ctx context.Context, u *url.URL, addrs []netip.Addr) (*http.Response, error) {
	tr := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		TLSClientConfig:       f.tls,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var last error
			for _, a := range addrs {
				c, err := f.dial(ctx, network, net.JoinHostPort(a.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Applysmith)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("the page could not be fetched: %w", err)
	}
	return resp, nil
}

// Text is the visible text of an HTML or plain body that did not come from a
// page read (a posting an ATS's API gave), cut as a page's text is.
func Text(body string) (text string, truncated bool) {
	_, text = extractHTML(strings.ToValidUTF8(body, ""))
	if r := []rune(text); len(r) > MaxTextRunes {
		text, truncated = string(r[:MaxTextRunes]), true
	}
	return strings.TrimSpace(text), truncated
}

func readBody(resp *http.Response, u *url.URL) (Page, error) {
	p := Page{Host: u.Host, Status: resp.StatusCode}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !allowedTypes[media] {
		return Page{}, &Refusal{Rule: RuleContentType + ":" + media, Host: u.Hostname()}
	}
	p.ContentType = media
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("the page could not be read: %w", err)
	}
	if len(body) > MaxBodyBytes {
		body, p.Truncated = body[:MaxBodyBytes], true
	}
	text := strings.ToValidUTF8(string(body), "")
	if media == "text/html" || media == "application/xhtml+xml" {
		p.Title, text = extractHTML(text)
	}
	if r := []rune(text); len(r) > MaxTextRunes {
		text, p.Truncated = string(r[:MaxTextRunes]), true
	}
	p.Text = strings.TrimSpace(text)
	return p, nil
}
