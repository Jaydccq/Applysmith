package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/remote"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// DeviceCookie carries a paired device's token on the tunnel listener.
const DeviceCookie = "applysmith_device"

const (
	remoteEvery      = 2 * time.Second
	remoteSweepEvery = 24 * time.Hour
	tunnelTokenName  = "cloudflared-tunnel-token"
	tunnelLogName    = "tunnel.log"
	tunnelStatusName = "tunnel.json"
	remoteEventsList = 100
	// Failed credentials from one source: this many within the window block it.
	blockFailures = 20
	blockWindow   = time.Minute
	blockFor      = 10 * time.Minute
	// Streams a remote device may hold, and all remote devices together.
	maxStreamsPerDevice = 4
	maxStreamsRemote    = 16
	deviceIDPrefix      = "device:"
)

// TunnelProcess is the tunnel supervisor the server drives. Production runs
// cloudflared; tests script it.
type TunnelProcess interface {
	Start()
	Stop()
	Retry()
	Status() tunnel.Status
}

// listenerKind says which listener accepted a request. The classification is
// made when the connection is accepted and never from a header.
type listenerKind int8

const (
	listenLoopback listenerKind = iota
	listenTunnel
)

type listenerKey struct{}

func requestIsRemote(r *http.Request) bool {
	k, _ := r.Context().Value(listenerKey{}).(listenerKind)
	return k == listenTunnel
}

// listenerHandler serves one kind of listener with the shared pipeline.
func (s *Server) listenerHandler(kind listenerKind) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serve(w, r.WithContext(context.WithValue(r.Context(), listenerKey{}, kind)))
	})
}

// source names where a request came from, for the rate limits and the block.
// Only the tunnel listener trusts a header: the tunnel child writes it, and no
// one else can reach that listener.
func (s *Server) source(r *http.Request) string {
	if requestIsRemote(r) {
		if v := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); v != "" && len(v) <= 64 {
			return v
		}
		return "unknown"
	}
	return remoteHost(r)
}

// authBlocks counts failed credentials per source and blocks a source that
// fails too often.
type authBlocks struct {
	mu  sync.Mutex
	now func() time.Time
	src map[string]*blockState
}

type blockState struct {
	fails []time.Time
	until time.Time
	seen  map[[sha256.Size]byte]time.Time // token digests already counted in the window
}

func newAuthBlocks(now func() time.Time) *authBlocks {
	return &authBlocks{now: now, src: map[string]*blockState{}}
}

// blocked reports whether the source is blocked and for how many more seconds.
func (b *authBlocks) blocked(src string) (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.src[src]
	if st == nil || !st.until.After(b.now()) {
		return false, 0
	}
	return true, max(1, int(st.until.Sub(b.now()).Seconds()+0.999))
}

// fail records a failed credential and reports whether it started a block. A
// token digest counts once per window: a phone that keeps sending one stale
// cookie is one failure, so it cannot lock out the other devices behind the
// same address, while guessing many tokens still adds up.
func (b *authBlocks) fail(src string, token [sha256.Size]byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if len(b.src) > pruneAbove {
		for k, v := range b.src {
			if !v.until.After(now) && (len(v.fails) == 0 || now.Sub(v.fails[len(v.fails)-1]) > blockWindow) {
				delete(b.src, k)
			}
		}
	}
	st := b.src[src]
	if st == nil {
		st = &blockState{}
		b.src[src] = st
	}
	if st.seen == nil {
		st.seen = map[[sha256.Size]byte]time.Time{}
	}
	for d, t := range st.seen {
		if now.Sub(t) >= blockWindow {
			delete(st.seen, d)
		}
	}
	if _, again := st.seen[token]; again {
		return false
	}
	st.seen[token] = now
	kept := st.fails[:0]
	for _, t := range st.fails {
		if now.Sub(t) < blockWindow {
			kept = append(kept, t)
		}
	}
	st.fails = append(kept, now)
	if len(st.fails) >= blockFailures && !st.until.After(now) {
		st.until = now.Add(blockFor)
		st.fails = nil
		return true
	}
	return false
}

// remoteRT is the remote-access state of one server: the devices, the tunnel
// listener and the tunnel child, kept in step with policy.yml.
type remoteRT struct {
	s      *Server
	blocks *authBlocks

	opMu sync.Mutex // serialises reconcile and close

	mu      sync.Mutex
	devices *remote.Devices
	cur     remote.Settings
	active  bool
	srv     *http.Server
	addr    string
	tun     TunnelProcess
	tlog    *rotatingFile
	err     string
	closed  bool
	tstat   tunnel.Status
	pubURL  string
	host    string // lowercase host the tunnel listener accepts; empty while no tunnel runs
}

func newRemoteRT(s *Server) *remoteRT {
	return &remoteRT{s: s, blocks: newAuthBlocks(s.now), cur: remote.Settings{Mode: remote.ModeOff}}
}

func (r *remoteRT) statusPath() string {
	return filepath.Join(r.s.cfg.Workspace.StateDir(), tunnelStatusName)
}

func (r *remoteRT) service() (*remote.Devices, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.devices == nil {
		return nil, apikit.Unavailable("remote access is not ready")
	}
	return r.devices, nil
}

// start opens the device store, ends a tunnel child an earlier server left
// behind and applies policy.yml; a loop then keeps them in step.
func (r *remoteRT) start(ctx context.Context, st *store.Store) error {
	devs, err := remote.Open(ctx, st, r.s.now)
	if err != nil {
		return fmt.Errorf("open the paired devices: %w", err)
	}
	r.mu.Lock()
	r.devices = devs
	r.mu.Unlock()
	if tunnel.CleanOrphan(r.statusPath(), nil, nil) {
		r.s.log.Warn("a tunnel process of an earlier server was ended")
	}
	_ = tunnel.WriteFile(r.statusPath(), tunnel.Status{Mode: tunnel.StateOff})
	r.reconcile()
	_ = devs.Sweep(ctx)
	every := cmpOrDuration(r.s.cfg.RemoteEvery, remoteEvery)
	r.s.bgWG.Go(func() {
		tick, sweep := time.NewTicker(every), time.NewTicker(remoteSweepEvery)
		defer tick.Stop()
		defer sweep.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				r.reconcile()
			case <-sweep.C:
				_ = devs.Sweep(ctx)
			}
		}
	})
	return nil
}

func sameTunnel(a, b remote.Settings) bool {
	return a.Mode == b.Mode && a.TunnelPort == b.TunnelPort && a.Binary == b.Binary && a.PublicURL == b.PublicURL
}

// reconcile brings the listener and the tunnel in line with policy.yml. A mode
// that is not exactly tunnel is off, so an unreadable policy closes everything.
func (r *remoteRT) reconcile() {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	closed, active, prev := r.closed, r.active, r.cur
	r.mu.Unlock()
	if closed {
		return
	}
	set := remote.ReadSettings(r.s.cfg.Workspace)
	switch {
	case set.Mode != remote.ModeTunnel:
		if active {
			r.stopTunnel()
			r.voidOffers()
			r.record(remote.EventModeChange, "policy", "tunnel -> off")
			r.s.notify("notifications")
		}
		r.mu.Lock()
		r.cur, r.err = set, ""
		r.mu.Unlock()
	case active && sameTunnel(prev, set):
		r.mu.Lock()
		r.cur = set
		r.mu.Unlock()
	default:
		if active {
			r.stopTunnel()
		}
		started := r.startTunnel(set)
		if started && !active {
			r.record(remote.EventModeChange, "policy", "off -> tunnel")
		}
		if started || active { // a tunnel that keeps failing to start does not move the board every tick
			r.s.notify("notifications")
		}
	}
}

// voidOffers ends the pairing offers made while the mode was on, so none of
// them pairs a device when the mode is switched on again.
func (r *remoteRT) voidOffers() {
	r.mu.Lock()
	devs := r.devices
	r.mu.Unlock()
	if devs != nil {
		devs.VoidOffers()
	}
}

func (r *remoteRT) record(kind, source, detail string) { r.recordDevice(kind, "", source, detail) }

func (r *remoteRT) recordDevice(kind, deviceID, source, detail string) {
	r.mu.Lock()
	devs := r.devices
	r.mu.Unlock()
	if devs == nil {
		return
	}
	if err := devs.Record(context.WithoutCancel(r.s.bg), kind, deviceID, source, detail); err != nil && r.s.log != nil {
		r.s.log.Warn("remote event not recorded", "kind", kind, "error", err.Error())
	}
}

func (r *remoteRT) fail(set remote.Settings, msg string) {
	r.mu.Lock()
	r.cur, r.err = set, msg
	r.mu.Unlock()
}

// startTunnel opens the tunnel listener and starts the supervisor. It reports
// whether remote access is now on; on a failure the error is in the status and
// no tunnel listener is open.
func (r *remoteRT) startTunnel(set remote.Settings) bool {
	if set.TunnelPort == r.s.port {
		r.fail(set, fmt.Sprintf("tunnel_port %d is the port of the primary listener; choose another in policy.yml", set.TunnelPort))
		return false
	}
	bind := bindTunnel
	if r.s.cfg.tunnelListen != nil {
		bind = r.s.cfg.tunnelListen
	}
	ln, err := bind(set.TunnelPort)
	if err != nil {
		r.fail(set, fmt.Sprintf("the tunnel listener could not open port %d: %v", set.TunnelPort, err))
		return false
	}
	logf, err := openRotating(filepath.Join(r.s.cfg.Workspace.StateDir(), "logs", tunnelLogName), logMaxBytes, logKeepFiles)
	if err != nil {
		_ = ln.Close()
		r.fail(set, "the tunnel log could not be opened: "+err.Error())
		return false
	}
	srv := &http.Server{
		Handler:           r.s.listenerHandler(listenTunnel),
		ReadHeaderTimeout: headerTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(r.s.log.Handler(), slog.LevelWarn),
	}
	go func() { _ = srv.Serve(ln) }()
	cfg := tunnel.Config{
		Binary: set.Binary, PublicURL: set.PublicURL, StatusPath: r.statusPath(),
		Log:      vault.RedactWriter{W: logf},
		Token:    r.tunnelToken,
		OnChange: r.onTunnel,
	}
	var tun TunnelProcess
	if f := r.s.cfg.RemoteTunnel; f != nil {
		tun = f(cfg)
	} else {
		tun = tunnel.New(cfg)
	}
	r.mu.Lock()
	r.cur, r.err, r.active = set, "", true
	r.srv, r.addr, r.tun, r.tlog = srv, ln.Addr().String(), tun, logf
	r.tstat = tunnel.Status{Mode: tunnel.StateStarting}
	r.mu.Unlock()
	tun.Start()
	return true
}

// tunnelToken returns the named-tunnel token, or "" when none is stored.
func (r *remoteRT) tunnelToken() (string, error) {
	tok, err := r.s.cfg.Vault.Get(tunnelTokenName)
	if vault.IsCode(err, vault.CodeNotFound) {
		return "", nil
	}
	return tok, err
}

// stopTunnel stops the child, closes the tunnel listener and every remote
// stream. The device credentials stay valid for the next time.
func (r *remoteRT) stopTunnel() {
	r.mu.Lock()
	srv, tun, tlog := r.srv, r.tun, r.tlog
	r.active, r.srv, r.tun, r.tlog, r.addr = false, nil, nil, nil, ""
	r.host, r.pubURL = "", ""
	r.tstat = tunnel.Status{Mode: tunnel.StateStopped}
	r.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	r.s.streams.revoke(apikit.PrincipalRemoteDevice, errStreamRevoked)
	if tun != nil {
		tun.Stop()
	}
	if tlog != nil {
		_ = tlog.Close()
	}
}

// onTunnel follows the supervisor: the listener accepts only the tunnel's
// public host, and only while the tunnel runs.
func (r *remoteRT) onTunnel(st tunnel.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	r.tstat = st
	r.host, r.pubURL = "", ""
	if st.Mode == tunnel.StateRunning {
		if u, err := url.Parse(st.PublicURL); err == nil && u.Scheme == "https" && u.Hostname() != "" {
			r.host, r.pubURL = strings.ToLower(u.Hostname()), "https://"+strings.ToLower(u.Host)
		}
	}
}

// close ends remote access for good when the server stops.
func (r *remoteRT) close() {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	r.closed = true
	active := r.active
	r.mu.Unlock()
	if active {
		r.stopTunnel()
	}
}

// pairedCount is the number of paired devices, for the share of a remote
// device's streams; zero for any other credential.
func (r *remoteRT) pairedCount(p apikit.Principal) int {
	if p.Kind != apikit.PrincipalRemoteDevice {
		return 0
	}
	if devs, err := r.service(); err == nil {
		return devs.Count()
	}
	return 0
}

// stillPaired is false for a remote credential whose device was revoked.
func (r *remoteRT) stillPaired(p apikit.Principal) bool {
	if p.Kind != apikit.PrincipalRemoteDevice {
		return true
	}
	devs, err := r.service()
	return err == nil && devs.Active(deviceID(p))
}

// publicBase is the tunnel's https origin while it runs.
func (r *remoteRT) publicBase() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pubURL, r.pubURL != ""
}

// admit applies the checks that come before routing on the tunnel listener: a
// blocked source, and a Host other than the tunnel's public host.
func (r *remoteRT) admit(req *http.Request) *apikit.Error {
	if blocked, wait := r.blocks.blocked(r.s.source(req)); blocked {
		return errRateLimited(wait, wire.ReasonBlocked)
	}
	r.mu.Lock()
	host := r.host
	r.mu.Unlock()
	got := strings.ToLower(req.Host)
	if h, p, err := net.SplitHostPort(got); err == nil && p == "443" {
		got = h
	}
	if host == "" || got != host {
		return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "this host is not allowed"), wire.ReasonHostNotAllowed)
	}
	return nil
}

// credential identifies a device from its token, in the Authorization header
// when one is present and otherwise in the device cookie. presented says that
// the request carried a credential at all; a local credential is none here.
func (r *remoteRT) credential(req *http.Request) (p apikit.Principal, presented, byCookie bool, token [sha256.Size]byte) {
	var raw string
	if tok, ok := bearerFrom(req); ok {
		presented, raw = true, tok
	} else if ck, err := req.Cookie(DeviceCookie); err == nil && ck.Value != "" {
		presented, byCookie, raw = true, true, ck.Value
	}
	token = digest(raw)
	devs, err := r.service()
	if !presented || err != nil {
		return apikit.Principal{}, presented, byCookie, token
	}
	dev, ok := devs.Authenticate(req.Context(), raw)
	if !ok {
		return apikit.Principal{}, true, byCookie, token
	}
	return apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: deviceIDPrefix + dev.ID}, true, byCookie, token
}

// deviceCookie builds the device cookie with the attributes it is always set
// with; a negative age removes it.
func deviceCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: DeviceCookie, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
}

func remoteForbidden() *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "this route is not available to remote devices"), wire.ReasonRemoteForbidden)
}

// authorizeRemote is the credential rule of the tunnel listener: only routes the
// allowlist contains, only a device credential, and a cookie only from the
// tunnel's own origin.
func (s *Server) authorizeRemote(w http.ResponseWriter, r *http.Request, route *boundRoute) (apikit.Principal, *apikit.Error) {
	if route.Remote != RemoteAllowed {
		return apikit.Principal{}, remoteForbidden()
	}
	p, presented, byCookie, token := s.remote.credential(r)
	if presented && p.Kind == apikit.PrincipalNone {
		// Public routes tell a valid token from an invalid one too (the pages
		// load either way, but a device sees its own), so a refused credential
		// counts there as well, once per token digest in the window. An invalid
		// cookie is removed, so a revoked phone stops sending it.
		if s.remote.blocks.fail(s.source(r), token) {
			s.remote.record(remote.EventAuthBlock, s.source(r), "20 failed credentials in a minute")
		}
		if byCookie {
			http.SetCookie(w, deviceCookie("", -1))
		}
	}
	if route.Access == AccessPublic {
		return p, nil
	}
	if p.Kind == apikit.PrincipalNone {
		return p, errUnauthenticated()
	}
	if byCookie {
		if e := checkBrowserOrigin(r, "https"); e != nil {
			return p, e
		}
	}
	return p, nil
}

// status is the document of GET /api/remote/status.
func (r *remoteRT) status(ctx context.Context) wire.RemoteStatus {
	r.mu.Lock()
	devs, active, cur, addr, tstat, msg := r.devices, r.active, r.cur, r.addr, r.tstat, r.err
	r.mu.Unlock()
	st := wire.RemoteStatus{Mode: string(cur.Mode), AllowScreenshots: cur.AllowScreenshots, Error: msg,
		Listeners: []wire.RemoteListener{{Name: "primary", Address: net.JoinHostPort(r.s.primaryHost(), strconv.Itoa(r.s.port))}},
		Tunnel:    wire.RemoteTunnel{State: wire.TunnelOff}}
	if active {
		st.Listeners = append(st.Listeners, wire.RemoteListener{Name: "tunnel", Address: addr})
		st.Tunnel = wire.RemoteTunnel{State: string(tstat.Mode), PublicURL: tstat.PublicURL, Version: tstat.Version, Reason: tstat.Reason,
			Error: tstat.Error, ConsecutiveFailures: wire.SafeInt(tstat.ConsecutiveFailures)}
	}
	if devs != nil {
		list := devs.List()
		st.Devices = wire.SafeInt(len(list))
		for _, d := range list {
			if r.connected(devs, d) {
				st.Connected++
			}
		}
	}
	return st
}

// connected is true for a device with an open stream or a request in the last
// 60 seconds.
func (r *remoteRT) connected(devs *remote.Devices, d remote.Device) bool {
	return devs.Connected(d) || r.s.streams.has(deviceIDPrefix+d.ID)
}

// remoteComponent is the health entry of remote access: unconfigured while it
// is off, degraded while the tunnel starts, down when it cannot run.
func (s *Server) remoteComponent() wire.Component {
	c := wire.Component{Name: "remote", Status: wire.ComponentUnconfigured, Detail: "off"}
	if s.remote == nil {
		return c
	}
	s.remote.mu.Lock()
	defer s.remote.mu.Unlock()
	switch {
	case s.remote.err != "":
		c.Status, c.Detail = wire.ComponentDown, s.remote.err
	case s.remote.active:
		switch s.remote.tstat.Mode {
		case tunnel.StateRunning:
			c.Status, c.Detail = wire.ComponentOK, "tunnel running"
		case tunnel.StateError, tunnel.StateFailed:
			c.Status, c.Detail = wire.ComponentDown, s.remote.tstat.Error
		default:
			c.Status, c.Detail = wire.ComponentDegraded, "tunnel starting"
		}
	}
	return c
}

// viaTunnel reports a request that carries the marks Cloudflare puts on what it
// forwards. The primary listener serves this computer only: a tunnel whose
// ingress points at it (with a Host override of the loopback address) would
// make internet traffic look local, so such a request is refused.
func viaTunnel(r *http.Request) bool {
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "cf-") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Cdn-Loop"), ",")), "cloudflare")
}

// tunnelIngressError names the address a tunnel's ingress must target.
func (r *remoteRT) tunnelIngressError() *apikit.Error {
	r.mu.Lock()
	port := r.cur.TunnelPort
	r.mu.Unlock()
	if port == 0 {
		port = workspace.DefaultPolicy().RemoteAccess.TunnelPort
	}
	return apikit.WithReason(apikit.NewErr(wire.CodeForbidden,
		"this request came through a tunnel to the dashboard port, which only this computer may use; point the tunnel's ingress at http://127.0.0.1:%d", port),
		wire.ReasonTunnelIngress)
}
