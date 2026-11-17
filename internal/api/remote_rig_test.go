package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/remote/tunnel"
	"applysmith/internal/workspace"
)

const tunnelHost = "applysmith.example.com"

// fakeTunnel is a scripted supervisor: the test says when the tunnel is up.
type fakeTunnel struct {
	cfg tunnel.Config

	mu                        sync.Mutex
	st                        tunnel.Status
	started, stopped, retried int
}

func (f *fakeTunnel) Start() {
	f.mu.Lock()
	f.started++
	f.mu.Unlock()
	f.set(tunnel.Status{Mode: tunnel.StateStarting})
}

func (f *fakeTunnel) Stop() {
	f.mu.Lock()
	f.stopped++
	f.mu.Unlock()
	f.set(tunnel.Status{Mode: tunnel.StateStopped})
}

func (f *fakeTunnel) Retry() {
	f.mu.Lock()
	f.retried++
	f.mu.Unlock()
}

func (f *fakeTunnel) Status() tunnel.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeTunnel) set(st tunnel.Status) {
	f.mu.Lock()
	f.st = st
	f.mu.Unlock()
	if f.cfg.OnChange != nil {
		f.cfg.OnChange(st)
	}
}

func (f *fakeTunnel) up(url string) {
	f.set(tunnel.Status{Mode: tunnel.StateRunning, PublicURL: url, Version: "2026.1.0"})
}

func (f *fakeTunnel) counts() (started, stopped, retried int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.stopped, f.retried
}

// remoteRig is a harness whose tunnel is scripted and whose tunnel listener
// sits on a free loopback port.
type remoteRig struct {
	*harness
	port int

	mu    sync.Mutex
	held  net.Listener // the reserved port, open until the runtime takes it
	tun   *fakeTunnel
	tuns  int
	pairs int
}

// reserve opens a listener on a free port and keeps it open until the
// runtime's first tunnel bind of that port takes it, so no other process can
// take the port in between.
func (r *remoteRig) reserve() {
	r.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	r.held, r.port = ln, ln.Addr().(*net.TCPAddr).Port
	r.mu.Unlock()
	r.t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.held != nil {
			_ = r.held.Close()
		}
	})
}

// mod gives a harness the scripted tunnel and stops the periodic policy read,
// which tests replace by calling reconcile.
func (r *remoteRig) mod() func(*Config) {
	return func(c *Config) {
		c.RemoteEvery = time.Hour
		c.tunnelListen = func(port int) (net.Listener, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if ln := r.held; ln != nil && port == r.port {
				r.held = nil
				return ln, nil
			}
			return bindTunnel(port)
		}
		c.RemoteTunnel = func(cfg tunnel.Config) TunnelProcess {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.tun = &fakeTunnel{cfg: cfg}
			r.tuns++
			return r.tun
		}
	}
}

func newRemoteRig(t *testing.T, mods ...func(*Config)) *remoteRig {
	t.Helper()
	r := &remoteRig{}
	r.harness = newDomainHarness(t, append([]func(*Config){r.mod()}, mods...)...)
	r.reserve()
	return r
}

func (r *remoteRig) tunnelProc() *fakeTunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tun
}

func (r *remoteRig) setPolicy(ops ...workspace.Op) {
	r.t.Helper()
	cur, err := r.ws.Read(workspace.FilePolicy)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.ws.Edit(workspace.FilePolicy, ops, cur.Revision); err != nil {
		r.t.Fatal(err)
	}
}

func set(path, value string) workspace.Op {
	return workspace.Op{Kind: workspace.OpSet, Path: path, Value: value}
}

// enable switches the policy to tunnel mode on the rig's port and applies it.
func (r *remoteRig) enable() {
	r.t.Helper()
	r.setPolicy(set("remote_access.mode", "tunnel"), set("remote_access.tunnel_port", strconv.Itoa(r.port)))
	r.srv.remote.reconcile()
}

// online enables tunnel mode and reports the tunnel up on the public host.
func (r *remoteRig) online() {
	r.t.Helper()
	r.enable()
	r.tunnelProc().up("https://" + tunnelHost)
}

func (r *remoteRig) disable() {
	r.t.Helper()
	r.setPolicy(set("remote_access.mode", "off"))
	r.srv.remote.reconcile()
}

// tcall is one request to the tunnel listener.
type tcall struct {
	method, path string
	host         string // the Host header; the tunnel's public host by default
	src          string // the tunnel's client-address header
	cookie       string // a device token, as the cookie
	bearer       string // a token, as the Authorization header
	origin       string
	body         []byte
	contentType  string
	headers      map[string]string
	session      string // a dashboard session cookie value, sent to the tunnel
}

type tres struct {
	status int
	header http.Header
	body   []byte
}

func (r tres) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	return m
}

func (r tres) data() map[string]any {
	d, _ := r.json()["data"].(map[string]any)
	return d
}

func (r tres) errCode() string {
	e, _ := r.json()["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (r tres) errReason() string {
	e, _ := r.json()["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	s, _ := d["reason"].(string)
	return s
}

func (r tres) setCookie(name string) *http.Cookie {
	for _, line := range r.header.Values("Set-Cookie") {
		if ck, err := http.ParseSetCookie(line); err == nil && ck.Name == name {
			return ck
		}
	}
	return nil
}

func (r *remoteRig) request(ctx context.Context, c tcall) (*http.Response, error) {
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	var rd io.Reader
	if c.body != nil {
		rd = bytes.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(r.port)+c.path, rd)
	if err != nil {
		return nil, err
	}
	req.Host = tunnelHost
	if c.host != "" {
		req.Host = c.host
	}
	req.Header.Set("Cf-Connecting-Ip", cmpOrString(c.src, "198.51.100.1"))
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: c.cookie})
	}
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.session})
		r.keyMu.Lock()
		key := r.keys[c.session]
		r.keyMu.Unlock()
		req.Header.Set(SessionKeyHeader, key)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.origin != "" {
		req.Header.Set("Origin", c.origin)
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}

func cmpOrString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// tdo sends the request and reads the answer; an event stream is reported by
// its headers alone and closed.
func (r *remoteRig) tdo(c tcall) tres {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := r.request(ctx, c)
	if err != nil {
		r.t.Fatalf("%s %s: %v", c.method, c.path, err)
	}
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return tres{status: resp.StatusCode, header: resp.Header}
	}
	b, _ := io.ReadAll(resp.Body)
	return tres{status: resp.StatusCode, header: resp.Header, body: b}
}

// paired is a device paired through the real flow.
type paired struct {
	token, id, label string
}

// offerLink makes an offer through the admin route and returns its link.
func (r *remoteRig) offerLink() string {
	r.t.Helper()
	res := r.send("POST", "/api/remote/offers", nil)
	if res.StatusCode != http.StatusCreated {
		r.t.Fatalf("offer: %d %s", res.StatusCode, res.body)
	}
	return res.data()["link"].(string)
}

func codeOf(link string) string {
	_, code, _ := strings.Cut(link, "#")
	return code
}

// claim posts a code to the tunnel's claim route.
func (r *remoteRig) claim(code, label, src string) tres {
	r.t.Helper()
	return r.tdo(tcall{method: "POST", path: "/api/remote/pair/claim", src: src, contentType: "application/json",
		body: jsonBody(map[string]string{"code": code, "label": label})})
}

// pair runs the whole flow for one device.
func (r *remoteRig) pair(label string) paired {
	r.t.Helper()
	r.mu.Lock()
	r.pairs++
	src := "198.51.100." + strconv.Itoa(r.pairs%250+1) // a claim is limited per source
	r.mu.Unlock()
	res := r.claim(codeOf(r.offerLink()), label, src)
	if res.status != http.StatusCreated {
		r.t.Fatalf("claim: %d %s", res.status, res.body)
	}
	d := res.data()
	return paired{token: d["token"].(string), id: d["deviceId"].(string), label: label}
}

// asDevice is a request as a paired device makes it from the browser: the
// cookie, and the tunnel's own origin on a mutation.
func asDevice(p paired, method, path string) tcall {
	c := tcall{method: method, path: path, cookie: p.token}
	if method != http.MethodGet {
		c.origin = "https://" + tunnelHost
	}
	return c
}

func (r *remoteRig) device(p paired, method, path string, body any) tres {
	r.t.Helper()
	c := asDevice(p, method, path)
	if body != nil {
		c.contentType, c.body = "application/json", jsonBody(body)
	}
	return r.tdo(c)
}

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tunnelStatus(mode string) tunnel.Status {
	return tunnel.Status{Mode: tunnel.State(mode), Error: "scripted"}
}
