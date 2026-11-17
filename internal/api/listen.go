package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"applysmith/internal/api/wire"
)

// loopbackHost validates the configured host. The primary listener serves
// loopback addresses only; localhost means 127.0.0.1.
func loopbackHost(host string) (string, error) {
	switch host {
	case "":
		return "127.0.0.1", nil
	case "localhost", "127.0.0.1", "::1":
		return host, nil
	}
	return "", &StartupError{Code: CodeNonLoopbackBind,
		Message: "the primary listener is loopback only; use 127.0.0.1, ::1 or localhost, not " + host}
}

// primaryHost is the address of the first listener: localhost means IPv4.
func (s *Server) primaryHost() string {
	if s.cfg.Host == "::1" {
		return "::1"
	}
	return "127.0.0.1"
}

// bind opens the one listener. Binding the other loopback family as well
// would let `localhost` reach whatever program holds that address when the
// second bind fails, so a server listens on exactly one address.
func (s *Server) bind() (net.Listener, int, error) {
	if s.cfg.Port == 0 && s.cfg.PreferredPort > 0 && s.cfg.PreferredPort != s.cfg.AvoidPort {
		if ln, err := net.Listen("tcp", net.JoinHostPort(s.primaryHost(), strconv.Itoa(s.cfg.PreferredPort))); err == nil {
			return ln, s.cfg.PreferredPort, nil
		}
	}
	var held []net.Listener
	defer func() {
		for _, h := range held {
			_ = h.Close()
		}
	}()
	for {
		ln, err := net.Listen("tcp", net.JoinHostPort(s.primaryHost(), strconv.Itoa(s.cfg.Port)))
		if err != nil {
			return nil, 0, s.listenError(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if s.cfg.Port != 0 || port != s.cfg.AvoidPort {
			return ln, port, nil
		}
		// The system handed out the avoided port. Keep it held while asking
		// again, so it cannot be handed out a second time.
		held = append(held, ln)
	}
}

// listenError tells apart a port held by another Applysmith server from one held
// by some other program. It never picks another port.
func (s *Server) listenError(err error) error {
	addr := net.JoinHostPort(s.primaryHost(), strconv.Itoa(s.cfg.Port))
	if h, ok := ProbeServer(addr); ok {
		rel := "a different workspace"
		if h.PathKey == s.pathKey {
			rel = "this workspace"
		}
		return &StartupError{Code: CodeServerAlreadyRunning, Port: s.cfg.Port, OtherWorkspace: h.PathKey != s.pathKey,
			Message: "port " + strconv.Itoa(s.cfg.Port) + " is held by an Applysmith server of " + rel}
	}
	if c, derr := net.DialTimeout("tcp", addr, probeTimeout); derr == nil {
		_ = c.Close()
		return &StartupError{Code: CodePortInUse, Port: s.cfg.Port,
			Message: "port " + strconv.Itoa(s.cfg.Port) + " is held by another program; stop it or choose another port with --port"}
	}
	return err
}

// ProbeServer asks whatever listens on addr whether it is an Applysmith server
// and returns its public health document.
func ProbeServer(addr string) (wire.Health, bool) {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return wire.Health{}, false
	}
	defer resp.Body.Close()
	var env wire.Success[wire.Health]
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<16)).Decode(&env); err != nil {
		return wire.Health{}, false
	}
	h := env.Data
	return h, env.OK && h.APIVersion > 0 && h.PathKey != ""
}

// LoopbackOrigin reports whether origin is a plain http origin on 127.0.0.1,
// [::1] or localhost. A lock file names the server's origin, but anyone who
// can write the workspace can edit it, so callers check before they connect.
func LoopbackOrigin(origin string) bool {
	_, ok := loopbackAddr(origin)
	return ok
}

// loopbackAddr is the host:port of a loopback origin.
func loopbackAddr(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Path != "" || u.User != nil || u.Port() == "" {
		return "", false
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return u.Host, true
	}
	return "", false
}

// bindTunnel opens the listener the tunnel child connects to: loopback only,
// on the configured port.
func bindTunnel(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}
