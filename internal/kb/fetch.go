package kb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Limits of the company-site fetch.
const (
	fetchTimeout   = 8 * time.Second
	fetchMaxBody   = 2 << 20
	fetchRedirects = 10
)

// ErrRefused marks a fetch the address guard refused.
var ErrRefused = errors.New("kb: fetch refused")

// Page is one fetched document.
type Page struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
}

// Fetcher reads a web page. The product implementation is GuardedFetcher; tests
// supply a stub.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (*Page, error)
}

// GuardedFetcher fetches over http and https only. It resolves the host itself
// and refuses loopback, private, link-local and cloud-metadata addresses before
// every connection, which covers each redirect hop. It follows at most 10
// redirects, gives up after 8 seconds and reads at most 2 MiB.
type GuardedFetcher struct {
	// lookup and allow are replaced by tests.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	allow  func(netip.Addr) bool
}

// NewGuardedFetcher returns the product fetcher.
func NewGuardedFetcher() *GuardedFetcher { return &GuardedFetcher{} }

var reservedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", // includes the broadcast address
		"fd00:ec2::/32", "fec0::/10",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

var (
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
	v4Compat    = netip.MustParsePrefix("::/96")
)

// publicAddr reports whether a connection to the address is allowed. An IPv6
// address that embeds an IPv4 one (IPv4-mapped, IPv4-compatible, NAT64, 6to4)
// is judged by the embedded address.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range reservedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64Prefix.Contains(a):
			return publicAddr(netip.AddrFrom4([4]byte(b[12:16])))
		case sixToFour.Contains(a):
			return publicAddr(netip.AddrFrom4([4]byte(b[2:6])))
		case v4Compat.Contains(a):
			return publicAddr(netip.AddrFrom4([4]byte(b[12:16])))
		}
	}
	return true
}

func (f *GuardedFetcher) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if f.lookup != nil {
		return f.lookup(ctx, host)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	return ips, err
}

func (f *GuardedFetcher) permitted(a netip.Addr) bool {
	if f.allow != nil {
		return f.allow(a)
	}
	return publicAddr(a)
}

func (f *GuardedFetcher) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := f.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %s did not resolve", ErrRefused, host)
	}
	// Every address must be public: a mixed answer is a rebinding attempt.
	for _, ip := range ips {
		if !f.permitted(ip) {
			return nil, fmt.Errorf("%w: %s resolves to a non-public address", ErrRefused, host)
		}
	}
	var d net.Dialer
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// Fetch implements Fetcher.
func (f *GuardedFetcher) Fetch(ctx context.Context, rawURL string) (*Page, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	c := http.Client{Transport: &http.Transport{DialContext: f.dial, Proxy: nil, ResponseHeaderTimeout: fetchTimeout, DisableKeepAlives: true}}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= fetchRedirects {
			return fmt.Errorf("%w: more than %d redirects", ErrRefused, fetchRedirects)
		}
		return checkScheme(req.URL.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if err := checkScheme(req.URL.Scheme); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,text/plain;q=0.9")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody))
	if err != nil {
		return nil, err
	}
	return &Page{URL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

func checkScheme(s string) error {
	if s != "http" && s != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrRefused, s)
	}
	return nil
}

func textual(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "application/xhtml")
}
