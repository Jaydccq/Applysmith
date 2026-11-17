package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/wire"
)

const (
	dialTimeout    = 2 * time.Second
	callTimeout    = 8 * time.Second
	maxConcurrent  = 4
	maxAPIResponse = 16 << 20
)

// allowedRoutes is the one place that names what this package may ask the
// local API. Every entry is read with GET; {name} matches one path segment.
var allowedRoutes = []string{
	"/api/health",
	"/api/candidacies",
	"/api/applications/lookup",
	"/api/apply/list",
	"/api/reports/{number}",
}

// routeAllowed reports whether path is one of allowedRoutes.
func routeAllowed(path string) bool {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for _, pattern := range allowedRoutes {
		want := strings.Split(strings.Trim(pattern, "/"), "/")
		if len(want) != len(segs) {
			continue
		}
		ok := true
		for i, w := range want {
			if strings.HasPrefix(w, "{") {
				ok = ok && segs[i] != "" && segs[i] != "." && segs[i] != ".."
			} else {
				ok = ok && w == segs[i]
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// getOnly refuses, before anything is sent, every request that is not a GET to
// an allowed route.
type getOnly struct{ next http.RoundTripper }

func (g getOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("mcp: %s requests are not allowed", r.Method)
	}
	if !routeAllowed(r.URL.Path) {
		return nil, fmt.Errorf("mcp: route %q is not allowed", r.URL.Path)
	}
	return g.next.RoundTrip(r)
}

// ErrNoToken means no API token is stored and the environment supplies none.
var ErrNoToken = errors.New("no API token is stored for this workspace")

// Options says where the API is and how to authenticate to it. Origin is read
// for every call, so a restarted server is found without restarting the MCP
// process. Token is read once and kept; it is read again when the API answers
// unauthenticated, so a rotated token is found the same way.
type Options struct {
	Origin func() (string, error)
	Token  func() (string, error)
	// Base replaces the network transport (tests); nil dials with a 2 second limit.
	Base http.RoundTripper
	// CallTimeout bounds one call end to end; zero means 8 seconds.
	CallTimeout time.Duration
	// WorkspaceKey is the workspaceId the server must report on its public
	// health route before each call that may carry the token. A lock file left
	// by a stopped server names a port that another program may hold now.
	// Empty skips the check (tests with a fake API).
	WorkspaceKey string
}

// Client reads the local API. It is the only way this package touches data.
type Client struct {
	http    *http.Client
	origin  func() (string, error)
	token   func() (string, error)
	timeout time.Duration
	slots   chan struct{}
	key     string

	mu     sync.Mutex
	cached string
}

// NewClient builds a client whose transport permits only the allowed GETs.
func NewClient(o Options) *Client {
	base := o.Base
	if base == nil {
		base = &http.Transport{
			DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout: dialTimeout,
			MaxIdleConns:        maxConcurrent,
		}
	}
	timeout := o.CallTimeout
	if timeout <= 0 {
		timeout = callTimeout
	}
	return &Client{
		http: &http.Client{Transport: getOnly{next: base},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		origin: o.Origin, token: o.Token, timeout: timeout,
		slots: make(chan struct{}, maxConcurrent), key: o.WorkspaceKey,
	}
}

// bearer returns the cached token, reading it first when reread is set or
// nothing is cached.
func (c *Client) bearer(reread bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != "" && !reread {
		return c.cached, nil
	}
	t, err := c.token()
	if err != nil || strings.TrimSpace(t) == "" {
		c.cached = ""
		return "", newToolError(CodeUnauthorized, remedyPair, "no API token is available for this workspace")
	}
	c.cached = t
	return t, nil
}

// get reads one allowed route and decodes the envelope's data into out. With
// optionalAuth a missing token sends the request without one (the health
// route is public). It retries once, after re-reading the token, when the API
// answers unauthenticated.
func (c *Client) get(ctx context.Context, path string, query url.Values, optionalAuth bool, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return classify(ctx.Err())
	}
	err := c.once(ctx, path, query, optionalAuth, false, out)
	var te *ToolError
	if errors.As(err, &te) && te.staleToken {
		err = c.once(ctx, path, query, optionalAuth, true, out)
	}
	if err == nil {
		return nil
	}
	return classify(err)
}

func (c *Client) once(ctx context.Context, path string, query url.Values, optionalAuth, reread bool, out any) error {
	origin, err := c.origin()
	if err != nil {
		return err
	}
	if err := c.verify(ctx, origin); err != nil {
		return err
	}
	u := origin + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	tok, err := c.bearer(reread)
	switch {
	case err == nil:
		req.Header.Set("Authorization", "Bearer "+tok)
	case !optionalAuth:
		return err
	}
	req.Header.Set("Accept", "application/json")
	return c.do(req, out)
}

// verify asks the public health route, without the token, whether the server
// at origin is this workspace's. It is asked before every call and its answer
// is not kept: the server may have stopped since the last call, and another
// program may hold its port.
func (c *Client) verify(ctx context.Context, origin string) error {
	if c.key == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	var h wire.Health
	if err := c.do(req, &h); err != nil {
		return err
	}
	if h.PathKey != c.key {
		return newToolError(CodeServerNotRunning, remedyStart, "the program at "+origin+" is not this workspace's Applysmith server")
	}
	return nil
}

// do sends req and decodes the envelope's data into out.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse+1))
	if err != nil {
		return err
	}
	if len(body) > maxAPIResponse {
		return foreignAnswer("the local server's answer is too large")
	}
	if resp.StatusCode/100 != 2 {
		var f wire.Failure
		if json.Unmarshal(body, &f) != nil || f.Error.Code == "" {
			return foreignAnswer(fmt.Sprintf("the local server answered %d", resp.StatusCode))
		}
		return fromEnvelope(resp.StatusCode, f, resp.Header.Get("Retry-After"))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Data == nil {
		return foreignAnswer("the local server's answer is not an API envelope")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return newToolError(CodeServerError, remedyServer, "the local server's answer has an unexpected shape")
	}
	return nil
}
