package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/remote/qr"
	"applysmith/internal/workspace"
)

func TestRemote_DefaultOffNoRemoteListener(t *testing.T) {
	r := newRemoteRig(t)
	if r.srv.listener == nil {
		t.Error("a fresh workspace has no loopback listener")
	}
	st := r.get("/api/remote/status").data()
	if st["mode"] != "off" {
		t.Errorf("status %v", st)
	}
	if r.tunnelProc() != nil {
		t.Error("a tunnel was created while remote access is off")
	}
	if got := r.get("/api/health").data()["components"]; !strings.Contains(string(jsonBody(got)), `"name":"remote","status":"unconfigured"`) {
		t.Errorf("the remote component of a fresh workspace: %v", got)
	}
}

func TestRemote_ModeChangeAppliesWithoutARestartAndOffClosesEverything(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	if started, _, _ := r.tunnelProc().counts(); started != 1 {
		t.Fatalf("tunnel started %d times", started)
	}
	st := r.get("/api/remote/status").data()
	if st["mode"] != "tunnel" || len(st["listeners"].([]any)) != 2 {
		t.Fatalf("status %v", st)
	}
	dev := r.pair("Phone")

	// An open remote stream ends with the mode.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := r.request(ctx, tcall{path: "/api/board/events", cookie: dev.token})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	ended := make(chan struct{})
	go func() { _, _ = bufio.NewReader(resp.Body).WriteTo(discard{}); close(ended) }()

	r.disable()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote stream is still open after the mode went off")
	}
	if _, stopped, _ := r.tunnelProc().counts(); stopped != 1 {
		t.Errorf("tunnel stopped %d times", stopped)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(r.port), time.Second); err == nil {
		_ = conn.Close()
		t.Error("the tunnel listener still accepts connections")
	}
	if got := r.get("/api/remote/status").data(); got["mode"] != "off" || len(got["listeners"].([]any)) != 1 {
		t.Errorf("status after off: %v", got)
	}

	// Turning it on again restores the paired devices: their tokens stay valid.
	r.enable()
	r.tunnelProc().up("https://" + tunnelHost)
	if res := r.device(dev, "GET", "/api/remote/me", nil); res.status != 200 {
		t.Errorf("the device after off and on: %d %s", res.status, res.body)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Switching remote access off ends the pairing offers that were waiting: a
// link made before the switch pairs nothing when the mode comes back on.
func TestRemote_ModeOffVoidsOutstandingOffers(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	code := codeOf(r.offerLink())
	r.disable()
	r.online()
	if res := r.claim(code, "Phone", "198.51.100.7"); res.status != 401 || res.errCode() != "unauthenticated" {
		t.Fatalf("an offer from before the mode went off: %d %s", res.status, res.body)
	}
	if res := r.claim(codeOf(r.offerLink()), "Phone", "198.51.100.8"); res.status != 201 {
		t.Fatalf("a new offer: %d %s", res.status, res.body)
	}
}

func TestRemote_RestartRestoresTheConfiguredMode(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	r.restart()
	// The listener opens with the server; the tunnel is a new child.
	if r.tunnelProc() == nil {
		t.Fatal("no tunnel after the restart")
	}
	r.tunnelProc().up("https://" + tunnelHost)
	if res := r.device(dev, "GET", "/api/remote/me", nil); res.status != 200 {
		t.Errorf("a paired device after a restart: %d %s", res.status, res.body)
	}
}

func TestRemote_InvalidOrLanPolicyReadsAsOff(t *testing.T) {
	r := newRemoteRig(t)
	on := "version: 1\nremote_access:\n  mode: tunnel\n  tunnel_port: " + itoa(r.port) + "\n"
	for _, body := range []string{"version: 1\nremote_access:\n  mode: lan\n", "version: [\n", "version: 1\nremote_access:\n  mode: wan\n",
		"version: 1\nremote_access:\n  mode: tunnel\n  tunnel_port: 80\n"} {
		writeRaw(t, r.ws.Path(workspace.FilePolicy), on)
		r.srv.remote.reconcile()
		if r.get("/api/remote/status").data()["mode"] != "tunnel" {
			t.Fatal("tunnel mode did not start")
		}
		// The write path refuses an invalid policy, so the file is replaced directly.
		writeRaw(t, r.ws.Path(workspace.FilePolicy), body)
		r.srv.remote.reconcile()
		if got := r.get("/api/remote/status").data(); got["mode"] != "off" {
			t.Errorf("%q: mode %v", body, got["mode"])
		}
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(r.port), time.Second); err == nil {
			_ = conn.Close()
			t.Errorf("%q: the tunnel listener is open", body)
		}
	}
}

func TestRemote_PrimaryListenerNeverBindsAllInterfaces(t *testing.T) {
	r := newRemoteRig(t)
	check := func(when string) {
		t.Helper()
		for _, ln := range []net.Listener{r.srv.listener, mustListener(t, r.srv.remote)} {
			if ip := ln.Addr().(*net.TCPAddr).IP; !ip.IsLoopback() {
				t.Errorf("%s: a listener is on %v, which is not loopback", when, ip)
			}
		}
	}
	r.srv.remote.mu.Lock()
	r.srv.remote.addr = r.srv.listener.Addr().String()
	r.srv.remote.mu.Unlock()
	check("remote access off")
	r.online()
	check("tunnel mode")
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.20", "10.0.0.5", "example.com"} {
		if _, err := New(Config{Workspace: r.ws, Vault: r.vault, Host: host}); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("host %q: %v", host, err)
		}
	}
}

func TestRemote_ListenerPortCollisionIsErrorState(t *testing.T) {
	r := newRemoteRig(t)
	r.setPolicy(set("remote_access.mode", "tunnel"), set("remote_access.tunnel_port", itoa(r.srv.port)))
	r.srv.remote.reconcile()
	st := r.get("/api/remote/status").data()
	if msg, _ := st["error"].(string); !strings.Contains(msg, "primary") || len(st["listeners"].([]any)) != 1 {
		t.Errorf("status %v", st)
	}
	if r.tunnelProc() != nil {
		t.Error("a tunnel started on a colliding port")
	}
	if comp := r.get("/api/health").data()["components"]; !strings.Contains(string(jsonBody(comp)), `"name":"remote","status":"down"`) {
		t.Errorf("components %v", comp)
	}
	// A port held by another program is an error state too, and the next
	// reconcile retries it once the port is free.
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := other.Addr().(*net.TCPAddr).Port
	r.setPolicy(set("remote_access.tunnel_port", itoa(busy)))
	r.srv.remote.reconcile()
	if msg, _ := r.get("/api/remote/status").data()["error"].(string); !strings.Contains(msg, "could not open port") {
		t.Errorf("a busy port: %q", msg)
	}
	_ = other.Close()
	r.srv.remote.reconcile()
	if st := r.get("/api/remote/status").data(); st["error"] != nil || len(st["listeners"].([]any)) != 2 {
		t.Errorf("after the port was freed: %v", st)
	}
}

func mustListener(t *testing.T, rt *remoteRT) net.Listener {
	t.Helper()
	rt.mu.Lock()
	addr := rt.addr
	rt.mu.Unlock()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("tunnel address %q: %v", addr, err)
	}
	ip := net.ParseIP(host)
	p, _ := net.LookupPort("tcp", port)
	return fixedListener{&net.TCPAddr{IP: ip, Port: p}}
}

type fixedListener struct{ addr net.Addr }

func (f fixedListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (f fixedListener) Close() error              { return nil }
func (f fixedListener) Addr() net.Addr            { return f.addr }

func tunnelTrafficIsRemote(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	// The peer of a tunnel request is loopback, yet it is remote: an admin
	// route is refused for a device and for no credential alike.
	for _, c := range []tcall{asDevice(dev, "GET", "/api/remote/devices"), {path: "/api/remote/devices"}, {path: "/api/config/profile", cookie: dev.token}} {
		got := r.tdo(c)
		if got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("%s: %d %s", c.path, got.status, got.body)
		}
	}
	if got := r.device(dev, "GET", "/api/remote/me", nil); got.status != 200 || got.data()["kind"] != "remote-device" {
		t.Errorf("me: %d %s", got.status, got.body)
	}
}

func forwardedHeaderOnTheLoopbackListenerIsIgnored(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	res := r.do(call{path: "/api/remote/status", useToken: true,
		headers: map[string]string{"X-Forwarded-For": "203.0.113.9", "Forwarded": "for=203.0.113.9", "X-Real-Ip": "203.0.113.9"}})
	if res.StatusCode != 200 {
		t.Errorf("a loopback request with proxy headers: %d %s", res.StatusCode, res.body)
	}
}

// A named tunnel whose ingress points at the dashboard port, with a Host
// override of the loopback address, would make internet traffic look local.
// The primary listener refuses what Cloudflare forwarded and names the port the
// ingress must target.
func TestRemote_PrimaryListenerRefusesTunnelTraffic(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for _, h := range []map[string]string{{"Cf-Ray": "8a1b2c3d4e5f-AMS"}, {"Cf-Connecting-Ip": "203.0.113.9"}, {"Cdn-Loop": "cloudflare; loops=1"},
		{"CF-Visitor": `{"scheme":"https"}`}, {"Cdn-Loop": "foo, Cloudflare"}} {
		for _, c := range []call{{path: "/api/tracker/summary", useToken: true, headers: h}, {path: "/api/health", headers: h}, {path: "/", headers: h}} {
			got := r.do(c)
			if got.StatusCode != 403 || got.errReason() != wire.ReasonTunnelIngress {
				t.Errorf("%v %s: %d %s", h, c.path, got.StatusCode, got.body)
			}
			if !strings.Contains(string(got.body), "http://127.0.0.1:"+itoa(r.port)) {
				t.Errorf("the refusal does not name the ingress target: %s", got.body)
			}
		}
	}
	// A dashboard session from this computer is unaffected.
	if got := r.do(call{path: "/api/remote/status", useToken: true}); got.StatusCode != 200 {
		t.Errorf("a local request: %d", got.StatusCode)
	}
	// The tunnel listener itself serves what Cloudflare forwards.
	if got := r.tdo(tcall{path: "/pair", headers: map[string]string{"Cf-Ray": "abc", "Cdn-Loop": "cloudflare"}}); got.status != 200 {
		t.Errorf("the tunnel listener refused tunnel traffic: %d", got.status)
	}
}

// The listener that accepted the connection decides what a request is; no
// header moves it either way.
func TestRemote_ClassifiedByListenerNotHeader(t *testing.T) {
	t.Run("tunnel traffic is remote", tunnelTrafficIsRemote)
	t.Run("a forwarded header on the loopback listener changes nothing", forwardedHeaderOnTheLoopbackListenerIsIgnored)
	t.Run("a forwarded header on the tunnel cannot make a request local", func(t *testing.T) {
		r := newRemoteRig(t)
		r.online()
		for _, h := range []map[string]string{{"X-Forwarded-For": "127.0.0.1"}, {"Forwarded": "for=127.0.0.1"}, {"X-Real-Ip": "127.0.0.1"}, {"Cf-Connecting-Ip": "127.0.0.1"}} {
			got := r.tdo(tcall{path: "/api/remote/status", headers: h, bearer: r.token()})
			if got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
				t.Errorf("%v: %d %s", h, got.status, got.body)
			}
		}
	})
}

func TestRemote_SourceKeyIgnoresForwardedForHeaders(t *testing.T) {
	r := newRemoteRig(t)
	req := func(remote bool, headers map[string]string) string {
		rq := mustRequest(t, "http://127.0.0.1/", headers)
		if remote {
			rq = rq.WithContext(context.WithValue(rq.Context(), listenerKey{}, listenTunnel))
		}
		return r.srv.source(rq)
	}
	if got := req(true, map[string]string{"Cf-Connecting-Ip": "203.0.113.9", "X-Forwarded-For": "10.0.0.1", "X-Real-Ip": "10.0.0.2"}); got != "203.0.113.9" {
		t.Errorf("tunnel source %q", got)
	}
	if got := req(true, map[string]string{"X-Forwarded-For": "10.0.0.1", "X-Real-Ip": "10.0.0.2"}); got != "unknown" {
		t.Errorf("a tunnel request without the tunnel's header shares one bucket, got %q", got)
	}
	if got := req(false, map[string]string{"Cf-Connecting-Ip": "203.0.113.9", "X-Forwarded-For": "10.0.0.1"}); got != "127.0.0.1" {
		t.Errorf("loopback source %q", got)
	}
}

func mustRequest(t *testing.T, url string, headers map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "127.0.0.1:5555"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestRemote_HostHeaderCheckedOnRemoteListeners(t *testing.T) {
	r := newRemoteRig(t)
	r.enable() // no public address yet
	if got := r.tdo(tcall{path: "/pair"}); got.status != 403 || got.errReason() != wire.ReasonHostNotAllowed {
		t.Errorf("before the tunnel is up: %d %s", got.status, got.body)
	}
	r.tunnelProc().up("https://" + tunnelHost)
	for _, host := range []string{"evil.example.org", "127.0.0.1:" + itoa(r.port), "localhost", "applysmith.example.com.evil.example.org"} {
		got := r.tdo(tcall{path: "/pair", host: host})
		if got.status != 403 || got.errReason() != wire.ReasonHostNotAllowed {
			t.Errorf("host %q: %d %s", host, got.status, got.body)
		}
	}
	for _, host := range []string{tunnelHost, "Applysmith.Example.COM", tunnelHost + ":443"} {
		if got := r.tdo(tcall{path: "/pair", host: host}); got.status != 200 {
			t.Errorf("host %q: %d", host, got.status)
		}
	}
	// A tunnel that drops loses its host: nothing is served until it is back.
	r.tunnelProc().set(tunnelStatus("error"))
	if got := r.tdo(tcall{path: "/pair"}); got.status != 403 {
		t.Errorf("while the tunnel is down: %d", got.status)
	}
}

func TestRemote_PlaintextTunnelAddressMakesNoOffer(t *testing.T) {
	r := newRemoteRig(t)
	r.enable()
	if got := r.send("POST", "/api/remote/offers", nil); got.StatusCode != 503 || got.errReason() != reasonTunnelDown {
		t.Errorf("tunnel starting: %d %s", got.StatusCode, got.body)
	}
	r.tunnelProc().up("http://applysmith.example.com")
	if got := r.send("POST", "/api/remote/offers", nil); got.StatusCode != 503 {
		t.Errorf("an http tunnel address produced an offer: %d %s", got.StatusCode, got.body)
	}
	r.disable()
	if got := r.send("POST", "/api/remote/offers", nil); got.StatusCode != 503 {
		t.Errorf("remote access off: %d %s", got.StatusCode, got.body)
	}
}

func TestRemote_QRLocalPayloadAtMost512Bytes(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	d := r.send("POST", "/api/remote/offers", nil).data()
	link := d["link"].(string)
	if !strings.HasPrefix(link, "https://"+tunnelHost+"/pair#") || len(link) > qr.MaxPayload {
		t.Fatalf("link %q", link)
	}
	want, err := qr.Encode(link)
	if err != nil {
		t.Fatal(err)
	}
	code := d["qr"].(map[string]any)
	rows := code["rows"].([]any)
	if int(code["size"].(float64)) != len(want) || len(rows) != len(want) {
		t.Fatalf("qr size %v rows %d, want %d", code["size"], len(rows), len(want))
	}
	for y, row := range want {
		for x, dark := range row {
			if (rows[y].(string)[x] == '1') != dark {
				t.Fatalf("module %d,%d differs from the local encoder", x, y)
			}
		}
	}
	exp, _ := time.Parse(time.RFC3339Nano, d["expiresAt"].(string))
	if got := exp.Sub(r.clock.now()); got != 300*time.Second {
		t.Errorf("offer lives %v", got)
	}
}

func TestRemote_OfferCreationLocalAdminOnly(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	for _, c := range []tcall{asDevice(dev, "POST", "/api/remote/offers"), {method: "POST", path: "/api/remote/offers"}} {
		if got := r.tdo(c); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("remote offer: %d %s", got.status, got.body)
		}
	}
	if got := r.do(call{method: "POST", path: "/api/remote/offers", bearer: dev.token}); got.StatusCode != 401 {
		t.Errorf("device token on the loopback listener: %d", got.StatusCode)
	}
}

func TestRemote_ClaimIssuesATokenOnceAndACookie(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	res := r.claim(codeOf(r.offerLink()), "Alex's phone", "")
	if res.status != 201 {
		t.Fatalf("claim: %d %s", res.status, res.body)
	}
	tok := res.data()["token"].(string)
	ck := res.setCookie(DeviceCookie)
	if ck == nil || ck.Value != tok || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Fatalf("cookie %+v", ck)
	}
	if got := r.tdo(tcall{path: "/api/remote/me", bearer: tok}); got.status != 200 {
		t.Errorf("bearer: %d", got.status)
	}
	if got := r.tdo(tcall{path: "/api/remote/me", cookie: tok}); got.status != 200 || got.data()["label"] != "Alex's phone" {
		t.Errorf("cookie: %d %s", got.status, got.body)
	}
	list := r.get("/api/remote/devices").json()["data"].(map[string]any)["devices"].([]any)
	if len(list) != 1 || strings.Contains(string(jsonBody(list)), tok) {
		t.Errorf("device list %v", list)
	}
}

func TestRemote_ExpiredOfferFailsUnauthenticatedNoCookie(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	code := codeOf(r.offerLink())
	r.clock.advance(301 * time.Second)
	res := r.claim(code, "Phone", "")
	if res.status != 401 || res.errCode() != "unauthenticated" || res.setCookie(DeviceCookie) != nil {
		t.Errorf("expired: %d %s", res.status, res.body)
	}
	fresh := codeOf(r.offerLink())
	r.clock.advance(200 * time.Second)
	if res := r.claim(fresh, "Phone", ""); res.status != 201 {
		t.Errorf("an offer 200 seconds old: %d %s", res.status, res.body)
	}
}

func TestRemote_OfferIsSingleUseAndFourthInvalidatesTheOldest(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	code := codeOf(r.offerLink())
	if res := r.claim(code, "Phone", ""); res.status != 201 {
		t.Fatal(res.status, string(res.body))
	}
	if res := r.claim(code, "Tablet", ""); res.status != 401 {
		t.Errorf("second claim: %d", res.status)
	}
	var codes []string
	for range 4 {
		codes = append(codes, codeOf(r.offerLink()))
	}
	if res := r.claim(codes[0], "Phone", ""); res.status != 401 {
		t.Errorf("the oldest of four offers still works: %d", res.status)
	}
	if res := r.claim(codes[3], "Phone", ""); res.status != 201 {
		t.Errorf("the newest offer: %d", res.status)
	}
}

func TestRemote_CodesAreNotInterchangeable(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	// The extension code from the local pairing route is refused by the
	// remote claim, and a remote offer by the local route.
	local := r.post("/api/pairing/codes", nil).data()["code"].(string)
	if res := r.claim(local, "Phone", ""); res.status != 401 {
		t.Errorf("extension code at the remote claim: %d", res.status)
	}
	remoteCode := codeOf(r.offerLink())
	if res := r.do(call{method: "POST", path: "/api/pairing/claim", contentType: "application/json", body: jsonBody(map[string]string{"code": remoteCode})}); res.StatusCode != 401 {
		t.Errorf("remote offer at the extension claim: %d", res.StatusCode)
	}
	// The remote claim exists only on the tunnel.
	if res := r.do(call{method: "POST", path: "/api/remote/pair/claim", contentType: "application/json", body: jsonBody(map[string]string{"code": remoteCode, "label": "x"})}); res.StatusCode != 403 {
		t.Errorf("remote claim on the loopback listener: %d", res.StatusCode)
	}
}

func TestRemote_PostPairRedirectUsesConfiguredOriginNotBindAddress(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	res := r.claim(codeOf(r.offerLink()), "Phone", "")
	if got := res.data()["redirect"]; got != "https://"+tunnelHost+"/" {
		t.Errorf("redirect %v", got)
	}
	if strings.Contains(string(res.body), "127.0.0.1") {
		t.Errorf("the answer names the bind address: %s", res.body)
	}
}

func TestRemote_PairingCodeInFragmentNotInRequestLine(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	link := r.offerLink()
	code := codeOf(link)
	if !strings.Contains(link, "/pair#") || strings.Contains(strings.Split(link, "#")[0], code) {
		t.Fatalf("link %q", link)
	}
	// A phone opens the link: the browser sends the path and never the fragment.
	page := r.tdo(tcall{path: "/pair"})
	if page.status != 200 || strings.Contains(string(page.body), code) {
		t.Fatalf("pair page: %d", page.status)
	}
	if res := r.claim(code, "Phone", ""); res.status != 201 {
		t.Fatal(res.status)
	}
	log := r.logText()
	if strings.Contains(log, code) {
		t.Error("the server log holds the pairing code")
	}
	if !strings.Contains(log, "/api/remote/pair/claim") {
		t.Error("the claim is not in the request log")
	}
}

func TestRemote_PairPageIsStaticWithItsOwnScriptHash(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for _, p := range []string{"/pair", "/", "/tracker"} {
		res := r.tdo(tcall{path: p})
		if res.status != 200 || !strings.Contains(string(res.body), "Pair this device") {
			t.Errorf("%s without a credential: %d", p, res.status)
		}
		// The page's scripts are its own script, by hash, and nothing else.
		csp := res.header.Get("Content-Security-Policy")
		var scriptSrc string
		for _, d := range strings.Split(csp, ";") {
			if src, ok := strings.CutPrefix(strings.TrimSpace(d), "script-src "); ok {
				scriptSrc = src
			}
		}
		if sum := sha256.Sum256([]byte(pairPageScript)); scriptSrc != "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'" {
			t.Errorf("%s CSP %q", p, csp)
		}
	}
	if res := r.tdo(tcall{path: "/assets/app.js"}); res.status != 401 {
		t.Errorf("an asset without a credential: %d", res.status)
	}
	dev := r.pair("Phone")
	if res := r.tdo(tcall{path: "/", cookie: dev.token}); res.status != 200 || strings.Contains(string(res.body), "Pair this device") {
		t.Errorf("root for a device: %d %s", res.status, res.body)
	}
}

func TestRemote_ClaimRateLimitSixthInMinuteRejected(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	var last tres
	for range 6 {
		last = r.claim("not-a-code", "Phone", "203.0.113.5")
	}
	if last.status != 429 || last.errCode() != "rate_limited" {
		t.Errorf("sixth claim: %d %s", last.status, last.body)
	}
	if res := r.claim(codeOf(r.offerLink()), "Phone", "203.0.113.6"); res.status != 201 {
		t.Errorf("another source: %d", res.status)
	}
}

func TestRemote_DeviceLimit(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for i := range 10 {
		r.claim(codeOf(r.offerLink()), "Device "+itoa(i), itoa(i)+".0.113.1")
	}
	res := r.claim(codeOf(r.offerLink()), "Eleventh", "")
	if res.status != 409 || res.errCode() != "conflict" || res.errReason() != wire.ReasonDeviceLimit {
		t.Errorf("eleventh: %d %s", res.status, res.body)
	}
	if !strings.Contains(string(jsonBody(r.get("/api/remote/events").data())), "device_limit") {
		t.Error("no device_limit event")
	}
}

func TestRemote_RevokeClosesStreamsWithinTwoSecondsAndTokenIsRejected(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	other := r.pair("Tablet")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := r.request(ctx, tcall{path: "/api/notifications/events", cookie: dev.token})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { _, _ = br.WriteTo(discard{}); close(ended) }()

	if res := r.send("DELETE", "/api/remote/devices/"+dev.id, nil); res.StatusCode != 200 {
		t.Fatalf("revoke: %d %s", res.StatusCode, res.body)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream outlived its device's revocation by more than 2 seconds")
	}
	if res := r.device(dev, "GET", "/api/remote/me", nil); res.status != 401 || res.errCode() != "unauthenticated" {
		t.Errorf("revoked token: %d %s", res.status, res.body)
	}
	if res := r.device(other, "GET", "/api/remote/me", nil); res.status != 200 {
		t.Errorf("another device after a revoke: %d", res.status)
	}
	if res := r.send("DELETE", "/api/remote/devices/"+dev.id, nil); res.StatusCode != 404 {
		t.Errorf("revoking a revoked device: %d", res.StatusCode)
	}
	if res := r.send("POST", "/api/remote/devices/revoke-all", nil); res.StatusCode != 200 || res.data()["revoked"].(float64) != 1 {
		t.Errorf("revoke all: %d %s", res.StatusCode, res.body)
	}
	if res := r.device(other, "GET", "/api/remote/me", nil); res.status != 401 {
		t.Errorf("after revoke-all: %d", res.status)
	}
}

func TestRemote_ConnectedFlagWithinSixtySeconds(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	connected := func() bool {
		list := r.get("/api/remote/devices").data()["devices"].([]any)
		return list[0].(map[string]any)["connected"].(bool)
	}
	r.clock.advance(30 * time.Second)
	r.device(dev, "GET", "/api/remote/me", nil)
	r.clock.advance(30 * time.Second)
	if !connected() {
		t.Error("a device that made a request 30 seconds ago is not connected")
	}
	r.clock.advance(31 * time.Second)
	if connected() {
		t.Error("a device quiet for 61 seconds is connected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := r.request(ctx, tcall{path: "/api/notifications/events", cookie: dev.token})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r.clock.advance(5 * time.Minute)
	if !connected() {
		t.Error("a device with an open stream is not connected")
	}
	if got := r.get("/api/remote/status").data(); got["devices"].(float64) != 1 || got["connected"].(float64) != 1 {
		t.Errorf("status %v", got)
	}
}

func TestRemote_BlockAfterTwentyFailuresOtherSourcesUnaffected(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	for i := range 20 {
		if got := r.tdo(tcall{path: "/api/remote/me", bearer: "not-a-token-" + itoa(i), src: "203.0.113.7"}); got.status != 401 {
			t.Fatalf("bad token: %d", got.status)
		}
	}
	got := r.tdo(tcall{path: "/api/remote/me", cookie: dev.token, src: "203.0.113.7"})
	if got.status != 429 || got.errCode() != "rate_limited" || got.errReason() != wire.ReasonBlocked || got.header.Get("Retry-After") == "" {
		t.Errorf("blocked source: %d %s", got.status, got.body)
	}
	if got := r.tdo(tcall{path: "/pair", src: "203.0.113.7"}); got.status != 429 {
		t.Errorf("a blocked source reaches a public page: %d", got.status)
	}
	if got := r.tdo(tcall{path: "/api/remote/me", cookie: dev.token, src: "203.0.113.8"}); got.status != 200 {
		t.Errorf("another source: %d", got.status)
	}
	if !strings.Contains(string(jsonBody(r.get("/api/remote/events").data())), "auth_block") {
		t.Error("no auth_block event")
	}
	r.clock.advance(10*time.Minute + time.Second)
	if got := r.tdo(tcall{path: "/api/remote/me", cookie: dev.token, src: "203.0.113.7"}); got.status != 200 {
		t.Errorf("after ten minutes: %d", got.status)
	}
}

func TestRemote_FailuresOutsideTheWindowDoNotBlock(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for range 3 {
		for i := range 10 {
			r.tdo(tcall{path: "/api/remote/me", bearer: "bad-" + itoa(i), src: "203.0.113.7"})
		}
		r.clock.advance(61 * time.Second)
	}
	if got := r.tdo(tcall{path: "/pair", src: "203.0.113.7"}); got.status != 200 {
		t.Errorf("slow failures blocked the source: %d", got.status)
	}
}

func streamCaps(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func(d paired) int {
		resp, err := r.request(ctx, tcall{path: "/api/notifications/events", cookie: d.token})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp.StatusCode
	}
	one := r.pair("One")
	for i := range 4 {
		if got := open(one); got != 200 {
			t.Fatalf("stream %d: %d", i, got)
		}
	}
	if got := open(one); got != 429 {
		t.Errorf("a fifth stream for one device: %d", got)
	}
	// Seven more devices pair: eight in all, two streams each. Every one of
	// them can open a stream although the first device filled its share before.
	for i := range 7 {
		d := r.pair("Dev " + itoa(i))
		if got := open(d); got != 200 {
			t.Fatalf("device %d: %d", i, got)
		}
	}
}

func TestRemote_BodyAndStreamLimits(t *testing.T) {
	t.Run("json bodies stop at 256 KiB", bodyLimit256KiB)
	t.Run("4 streams per device, 16 in total", streamCaps)
	t.Run("no remote route takes an upload", func(t *testing.T) {
		for _, rt := range Routes() {
			if rt.Remote == RemoteAllowed && (rt.Body == BodyUpload || rt.Body == BodyJSONLarge) {
				t.Errorf("%s is remote-allowed and takes %s", rt.Pattern(), rt.Body)
			}
		}
	})
}

func bodyLimit256KiB(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	big := make([]byte, 300<<10)
	for i := range big {
		big[i] = 'a'
	}
	c := asDevice(dev, "POST", "/api/chat/sessions/new/messages")
	c.contentType, c.body = "application/json", append(append([]byte(`{"text":"`), big...), []byte(`"}`)...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := r.request(ctx, c)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 413 {
			t.Errorf("a 300 KiB json body: %d", resp.StatusCode)
		}
	} // else the server cut the connection before the body was sent, which also refuses it
}

func TestRemote_CookieMutationNeedsTheTunnelOrigin(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	post := func(origin string, extra map[string]string) tres {
		return r.tdo(tcall{method: "POST", path: "/api/notifications/x/ack", cookie: dev.token, origin: origin, headers: extra})
	}
	if got := post("", nil); got.status != 403 || got.errReason() != wire.ReasonOriginMismatch {
		t.Errorf("no Origin: %d %s", got.status, got.body)
	}
	if got := post("http://"+tunnelHost, nil); got.status != 403 {
		t.Errorf("an http Origin: %d", got.status)
	}
	if got := post("https://evil.example.org", nil); got.status != 403 {
		t.Errorf("another Origin: %d", got.status)
	}
	if got := post("https://"+tunnelHost, map[string]string{"Sec-Fetch-Site": "cross-site"}); got.status != 403 || got.errReason() != wire.ReasonCrossSite {
		t.Errorf("cross-site: %d %s", got.status, got.body)
	}
	if got := post("https://"+tunnelHost, nil); got.status == 403 || got.status == 401 {
		t.Errorf("the tunnel's own origin: %d %s", got.status, got.body)
	}
	// A bearer token is not a browser credential and needs no Origin.
	if got := r.tdo(tcall{method: "POST", path: "/api/notifications/x/ack", bearer: dev.token}); got.status == 403 || got.status == 401 {
		t.Errorf("bearer without Origin: %d %s", got.status, got.body)
	}
}

func TestRemote_MinimalHealthOnTheTunnel(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	for _, c := range []tcall{{path: "/api/health"}, {path: "/api/health", cookie: dev.token}} {
		res := r.tdo(c)
		d := res.data()
		if res.status != 200 || d["status"] != "ok" {
			t.Fatalf("health: %d %s", res.status, res.body)
		}
		for _, k := range []string{"components", "uptimeSeconds", "schemaVersion"} {
			if _, ok := d[k]; ok {
				t.Errorf("remote health carries %s", k)
			}
		}
		if d["version"] != "" || d["workspaceId"] != "" || d["buildId"] != "" {
			t.Errorf("remote health names the build or workspace: %v", d)
		}
	}
}

func TestRemote_EventsRecordedWithoutSecrets_RetainedNinetyDays(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	link := r.offerLink()
	code := codeOf(link)
	dev := r.pair("Phone")
	r.claim("wrong-code", "Phone", "203.0.113.4")
	r.send("DELETE", "/api/remote/devices/"+dev.id, nil)
	body := string(jsonBody(r.get("/api/remote/events").data()))
	for _, kind := range []string{"mode_change", "pair_offer", "pair_claim", "pair_failed", "revoke"} {
		if !strings.Contains(body, `"`+kind+`"`) {
			t.Errorf("no %s event in %s", kind, body)
		}
	}
	if strings.Contains(body, dev.token) || strings.Contains(body, code) || strings.Contains(body, "wrong-code") {
		t.Errorf("an event holds a secret: %s", body)
	}
	if !strings.Contains(body, "203.0.113.4") {
		t.Error("the failed claim does not name its source")
	}
	// The event at the head of the list carries the newest first.
	events := r.get("/api/remote/events").data()["events"].([]any)
	if events[0].(map[string]any)["kind"] != "revoke" {
		t.Errorf("newest first: %v", events[0])
	}
	// Ninety days on, the daily sweep removes them.
	r.clock.advance(90*24*time.Hour + time.Hour)
	devs, _ := r.srv.remote.service()
	if err := devs.Record(context.Background(), "mode_change", "", "test", "recent"); err != nil {
		t.Fatal(err)
	}
	if err := devs.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	left := r.get("/api/remote/events").data()["events"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["detail"] != "recent" {
		t.Errorf("after the sweep: %v", left)
	}
}

func TestRemote_NewDevicePairedNotice(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	r.pair("Alex's phone")
	var got wire.Success[wire.NoticeList]
	strictInto(t, r.get("/api/notices").body, &got)
	if n := got.Data.Notices; got.Data.Unread != 1 || len(n) != 1 || n[0].Title != "New device paired" || n[0].Body != "Alex's phone" {
		t.Errorf("notices = %+v", got.Data)
	}
}

func TestRemote_RetryOnlyAFailedTunnel(t *testing.T) {
	r := newRemoteRig(t)
	if got := r.send("POST", "/api/remote/retry", nil); got.StatusCode != 409 {
		t.Errorf("retry while off: %d", got.StatusCode)
	}
	r.online()
	if got := r.send("POST", "/api/remote/retry", nil); got.StatusCode != 409 {
		t.Errorf("retry of a running tunnel: %d", got.StatusCode)
	}
	r.tunnelProc().set(tunnelStatus("failed"))
	if got := r.send("POST", "/api/remote/retry", nil); got.StatusCode != 202 {
		t.Errorf("retry of a failed tunnel: %d %s", got.StatusCode, got.body)
	}
	if _, _, retried := r.tunnelProc().counts(); retried != 1 {
		t.Errorf("retried %d times", retried)
	}
	if got := r.tdo(tcall{method: "POST", path: "/api/remote/retry"}); got.status != 403 {
		t.Errorf("retry from the tunnel: %d", got.status)
	}
}

func TestRemote_TunnelLogRedactsTheTokenAndHasTheSupervisorsConfig(t *testing.T) {
	r := newRemoteRig(t)
	const token = "tunnel-token-example-value-0123456789"
	if _, err := r.vault.Set("cloudflared-tunnel-token", token); err != nil {
		t.Fatal(err)
	}
	r.online()
	cfg := r.tunnelProc().cfg
	if got, err := cfg.Token(); err != nil || got != token {
		t.Fatalf("token source: %q %v", got, err)
	}
	if _, err := cfg.Log.Write([]byte("connecting with " + token + "\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.ws.StateDir(), "logs", "tunnel.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) || !strings.Contains(string(data), "[redacted]") {
		t.Errorf("tunnel.log: %q", data)
	}
	if !strings.HasSuffix(cfg.StatusPath, filepath.Join(".applysmith", "tunnel.json")) || cfg.Binary != "auto" {
		t.Errorf("config %+v", cfg)
	}
}

func TestRemote_DeviceCookieOnTheLoopbackListenerIsNoCredential(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	req, _ := http.NewRequest("GET", r.srv.Origin()+"/api/tracker/summary", nil)
	req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: dev.token})
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("a device cookie on the primary listener: %d", resp.StatusCode)
	}
}

func TestRemote_DeviceTokenStoredHashedRevokedImmediately(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	var sum string
	if err := r.srv.store.Load().Read().QueryRow(`SELECT token_sha256 FROM device_sessions WHERE id = ?`, dev.id).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum == dev.token || len(sum) != 64 || strings.Contains(sum, dev.token) {
		t.Errorf("stored digest %q", sum)
	}
	var n int
	_ = r.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM device_sessions WHERE token_sha256 = ?`, dev.token).Scan(&n)
	if n != 0 {
		t.Error("the token itself is in the database")
	}
	if got := r.device(dev, "GET", "/api/remote/me", nil); got.status != 200 {
		t.Fatalf("before the revoke: %d", got.status)
	}
	r.send("DELETE", "/api/remote/devices/"+dev.id, nil)
	if got := r.device(dev, "GET", "/api/remote/me", nil); got.status != 401 {
		t.Errorf("the next request after the revoke: %d", got.status)
	}
	r.restart()
	r.online()
	if got := r.device(dev, "GET", "/api/remote/me", nil); got.status != 401 {
		t.Errorf("a revoked token after a restart: %d", got.status)
	}
}

func TestRemote_StreamsFlushUnbuffered(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := r.request(ctx, tcall{path: "/api/notifications/events", cookie: dev.token})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	// An event published now reaches the phone's connection at once, without
	// waiting for a buffer to fill.
	r.srv.notifs.publish(wire.NotifyAnnounce, wire.NotificationAnnounce{ID: "n-1", Title: "Flushed", Body: "now"})
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, "Flushed") {
				return
			}
		case <-deadline:
			t.Fatal("the event did not reach the remote stream within 3 seconds")
		}
	}
}

func TestRemote_NoBrowserInputRouteAndScreenshotsOffByDefault(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	var browserRoutes []string
	for _, rt := range Routes() {
		if rt.Remote == RemoteAllowed && strings.HasPrefix(rt.Path, "/api/browser/") {
			browserRoutes = append(browserRoutes, rt.Pattern())
			if rt.Method != "GET" || !strings.HasSuffix(rt.Path, "/screenshot") {
				t.Errorf("%s sends something to a browser window", rt.Pattern())
			}
		}
	}
	if len(browserRoutes) != 1 {
		t.Errorf("exactly the screenshot route is remote-allowed under /api/browser/: %v", browserRoutes)
	}
	if st := r.get("/api/remote/status").data(); st["allowScreenshots"] != false {
		t.Errorf("screenshots default: %v", st["allowScreenshots"])
	}
	for _, p := range [][2]string{{"GET", "/api/browser/status"}, {"POST", "/api/browser/apply-window/show"}, {"POST", "/api/browser/apply-window/open"},
		{"GET", "/api/browser/lanes/apply/tabs/1/screenshot"}} {
		if got := r.tdo(asDevice(dev, p[0], p[1])); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("%s %s: %d %s", p[0], p[1], got.status, got.body)
		}
	}
}

func TestRemote_BrowserSessionAdminNeverRemote(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	for _, p := range [][2]string{{"GET", "/api/browser/sessions"}, {"POST", "/api/browser/sessions/sync-requests"}, {"POST", "/api/browser/sessions/undo"},
		{"POST", "/api/browser/sessions/check"}, {"POST", "/api/browser/sessions/checkin"}, {"POST", "/api/browser/sessions/imports"}, {"POST", "/api/browser/sessions/pushes"}} {
		c := asDevice(dev, p[0], p[1])
		c.contentType, c.body = "application/json", []byte("{}")
		if got := r.tdo(c); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("%s %s: %d %s", p[0], p[1], got.status, got.body)
		}
	}
}

func TestRemote_UnlistedRouteForbiddenUnauthenticatedRootIsPairingPage(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for _, c := range []tcall{{path: "/api/config/profile"}, {path: "/api/candidacies/1/notes", method: "POST"}, {path: "/api/tracker/export"}} {
		if got := r.tdo(c); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("%s: %d %s", c.path, got.status, got.body)
		}
	}
	root := r.tdo(tcall{path: "/"})
	if root.status != 200 || !strings.Contains(string(root.body), "Pair this device") || strings.Contains(string(root.body), "assets/") {
		t.Errorf("root without a credential: %d %s", root.status, root.body)
	}
	if !strings.HasPrefix(root.header.Get("Content-Type"), "text/html") {
		t.Errorf("content type %q", root.header.Get("Content-Type"))
	}
}

// A revoked phone keeps sending its cookie. It is one failure in the window, the
// cookie is removed from the browser, and the user's other devices behind the
// same address keep working.
func TestRemote_AStaleCookieCountsOncePerWindowAndIsCleared(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	stale := r.pair("Old phone")
	good := r.pair("New phone")
	r.send("DELETE", "/api/remote/devices/"+stale.id, nil)
	const nat = "203.0.113.7"

	first := r.tdo(tcall{path: "/pair", cookie: stale.token, src: nat})
	ck := first.setCookie(DeviceCookie)
	if ck == nil || ck.MaxAge >= 0 || ck.Value != "" || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
		t.Errorf("an invalid cookie is not removed: %+v", ck)
	}
	if u := r.tdo(tcall{path: "/api/remote/me", cookie: stale.token, src: nat}); u.status != 401 || u.setCookie(DeviceCookie) == nil || u.setCookie(DeviceCookie).MaxAge >= 0 {
		t.Errorf("the 401 does not remove the cookie: %d %v", u.status, u.header.Values("Set-Cookie"))
	}
	for range 100 {
		r.tdo(tcall{path: "/api/remote/me", cookie: stale.token, src: nat})
		r.tdo(tcall{path: "/assets/app.js", cookie: stale.token, src: nat})
	}
	if got := r.tdo(tcall{path: "/api/remote/me", cookie: good.token, src: nat}); got.status != 200 {
		t.Errorf("another device behind the same address: %d %s", got.status, got.body)
	}
	if got := r.tdo(tcall{path: "/pair", src: nat}); got.status != 200 {
		t.Errorf("one stale cookie blocked the address: %d", got.status)
	}
	// A valid cookie is not touched.
	if got := r.tdo(tcall{path: "/api/remote/me", cookie: good.token, src: nat}); got.setCookie(DeviceCookie) != nil {
		t.Error("a valid cookie was removed")
	}
	fails := func() []time.Time {
		b := r.srv.remote.blocks
		b.mu.Lock()
		defer b.mu.Unlock()
		return slices.Clone(b.src[nat].fails)
	}
	if got := fails(); len(got) != 1 {
		t.Fatalf("the stale token counted %d times in its window", len(got))
	}
	// The next minute the same stale token counts again, once.
	r.clock.advance(61 * time.Second)
	r.tdo(tcall{path: "/api/remote/me", cookie: stale.token, src: nat})
	r.tdo(tcall{path: "/api/remote/me", cookie: stale.token, src: nat})
	if got := fails(); len(got) != 1 || !got[0].Equal(r.clock.now()) {
		t.Errorf("failures in the next window: %v, want one at %v", got, r.clock.now())
	}
}

// Guessing does count, on public routes as well: twenty different tokens in a
// minute block the source.
func TestRemote_DifferentBadTokensOnPublicRoutesBlock(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	for i := range 20 {
		r.tdo(tcall{path: "/pair", bearer: "guess-" + itoa(i), src: "203.0.113.8"})
	}
	if got := r.tdo(tcall{path: "/pair", src: "203.0.113.8"}); got.status != 429 || got.errReason() != wire.ReasonBlocked {
		t.Errorf("twenty guesses on a public page: %d %s", got.status, got.body)
	}
	if got := r.tdo(tcall{path: "/pair", src: "203.0.113.9"}); got.status != 200 {
		t.Errorf("another source: %d", got.status)
	}
}

// A revoke that lands between the credential check and the registration of the
// stream finds no stream to close. The stream checks again once registered.
func TestRemote_AStreamCannotOutliveItsDevicesRevocation(t *testing.T) {
	r := newRemoteRig(t)
	r.online()
	dev := r.pair("Phone")
	principal := apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: deviceIDPrefix + dev.id}
	// The request passed authentication; now the device is revoked.
	if res := r.send("DELETE", "/api/remote/devices/"+dev.id, nil); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(res.body))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x/api/notifications/events", nil)
	rec := httptest.NewRecorder()
	c := &apikit.Call{W: rec, R: req, P: principal}
	err := r.srv.serveStream(c, r.srv.notifs, func() ([]sseEvent, bool) { return nil, false })
	var ae *apikit.Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeUnauthenticated {
		t.Fatalf("a stream of a revoked device: %v (status %d)", err, rec.Code)
	}
	if c.Handled || r.srv.streams.has(principal.ID) {
		t.Error("the stream stayed registered")
	}
	if ctx.Err() != nil {
		t.Error("serveStream blocked until the request ended")
	}
}
