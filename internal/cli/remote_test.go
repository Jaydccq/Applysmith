package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/remote/qr"
	"applysmith/internal/remote/tunnel"
	"applysmith/internal/workspace"
)

func remoteRun(te *testEnv, terminal bool, stdin string, args ...string) output {
	te.stdin = stdin
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	e.terminal = func() bool { return terminal }
	code := e.run(context.Background(), append([]string{"remote"}, append(args, "--workspace", te.ws)...))
	return output{code, out.String(), errw.String()}
}

func policyText(t *testing.T, te *testEnv) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(te.ws, "policy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func remoteMode(t *testing.T, te *testEnv) string {
	t.Helper()
	p, _ := workspace.ParsePolicy([]byte(policyText(t, te)))
	return p.RemoteAccess.Mode
}

func TestRemote_OnWithoutYesNonInteractiveRefused(t *testing.T) {
	te := newInitTestEnv(t)
	before := policyText(t, te)
	o := remoteRun(te, false, "", "on", "tunnel")
	if o.code != 3 || !strings.Contains(o.stderr, "reachable from the internet") {
		t.Fatalf("%+v", o)
	}
	if policyText(t, te) != before || remoteMode(t, te) != "off" {
		t.Error("the mode changed without a confirmation")
	}
}

func TestRemoteOnAsksOnATerminalAndYesSkipsTheQuestion(t *testing.T) {
	te := newInitTestEnv(t)
	if o := remoteRun(te, true, "n\n", "on", "tunnel"); o.code != 3 || !strings.Contains(o.stdout, "reachable from the internet") || remoteMode(t, te) != "off" {
		t.Fatalf("declined: %+v", o)
	}
	if o := remoteRun(te, true, "y\n", "on", "tunnel"); o.code != 0 || remoteMode(t, te) != "tunnel" {
		t.Fatalf("confirmed: %+v mode %s", o, remoteMode(t, te))
	}
	te2 := newInitTestEnv(t)
	if o := remoteRun(te2, false, "", "on", "tunnel", "--yes"); o.code != 0 || remoteMode(t, te2) != "tunnel" {
		t.Fatalf("--yes: %+v", o)
	}
}

func TestRemoteOnLanIsNotSupported(t *testing.T) {
	te := newInitTestEnv(t)
	before := policyText(t, te)
	o := remoteRun(te, true, "y\n", "on", "lan", "--yes")
	if o.code == 0 || !strings.Contains(o.stderr, "LAN mode is not supported") || !strings.Contains(o.stderr, "tunnel") {
		t.Fatalf("%+v", o)
	}
	if policyText(t, te) != before {
		t.Error("policy.yml changed")
	}
	if o := remoteRun(te, true, "", "on"); o.code != 2 {
		t.Errorf("on with no mode: %d", o.code)
	}
}

func TestRemote_OffEditsPolicyKeepsCommentsNoServerNeeded(t *testing.T) {
	te := newInitTestEnv(t)
	if o := remoteRun(te, false, "", "on", "tunnel", "--yes"); o.code != 0 {
		t.Fatalf("%+v", o)
	}
	withMode := policyText(t, te)
	if o := remoteRun(te, false, "", "off"); o.code != 0 {
		t.Fatalf("off: %+v", o)
	}
	after := policyText(t, te)
	if remoteMode(t, te) != "off" {
		t.Fatalf("mode after off: %s", remoteMode(t, te))
	}
	// Only the mode line differs: every comment and key survives.
	if strings.Count(withMode, "\n") != strings.Count(after, "\n") || strings.Count(withMode, "#") != strings.Count(after, "#") || !strings.Contains(after, "#") {
		t.Errorf("the edit rewrote the file:\n--- before\n%s\n--- after\n%s", withMode, after)
	}
	bl, al := strings.Split(withMode, "\n"), strings.Split(after, "\n")
	changed := 0
	for i := range bl {
		if bl[i] != al[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want only the mode line", changed)
	}
}

func TestRemoteLiveCommandsNeedAServer(t *testing.T) {
	te := newInitTestEnv(t)
	for _, args := range [][]string{{"status"}, {"pair"}, {"devices"}, {"revoke", "all"}, {"retry"}} {
		o := remoteRun(te, false, "", args...)
		if o.code == 0 || !strings.Contains(o.stderr, "applysmith serve") || o.stdout != "" {
			t.Errorf("%v: %+v", args, o)
		}
	}
	if o := remoteRun(te, false, "", "revoke"); o.code != 2 {
		t.Errorf("revoke without an id: %d", o.code)
	}
}

type cliTunnel struct {
	cfg tunnel.Config
	mu  sync.Mutex
	st  tunnel.Status
}

func (c *cliTunnel) Start()                { c.set(tunnel.Status{Mode: tunnel.StateStarting}) }
func (c *cliTunnel) Stop()                 { c.set(tunnel.Status{Mode: tunnel.StateStopped}) }
func (c *cliTunnel) Retry()                {}
func (c *cliTunnel) Status() tunnel.Status { c.mu.Lock(); defer c.mu.Unlock(); return c.st }
func (c *cliTunnel) set(st tunnel.Status) {
	c.mu.Lock()
	c.st = st
	c.mu.Unlock()
	c.cfg.OnChange(st)
}

func TestRemoteCLIAgainstARunningServer(t *testing.T) {
	te := newInitTestEnv(t)
	var tun *cliTunnel
	var mu sync.Mutex
	te.remoteTunnel = func(cfg tunnel.Config) api.TunnelProcess {
		mu.Lock()
		defer mu.Unlock()
		tun = &cliTunnel{cfg: cfg}
		return tun
	}
	// The server re-reads policy.yml every few milliseconds, so the waits
	// below end long before their deadlines on a loaded machine too.
	te.remoteEvery = 20 * time.Millisecond
	// A port for the tunnel listener that is not the server's.
	port := freeTCPPort(t)
	cur, _ := te.env(&syncBuffer{}, &syncBuffer{}).openWorkspace(te.ws)
	rev, _ := cur.Read(workspace.FilePolicy)
	if _, err := cur.Edit(workspace.FilePolicy, []workspace.Op{{Kind: workspace.OpSet, Path: "remote_access.tunnel_port", Value: port}}, rev.Revision); err != nil {
		t.Fatal(err)
	}
	rs := te.startServe()
	_ = rs

	if o := remoteRun(te, false, "", "status"); o.code != 0 || !strings.Contains(o.stdout, "mode: off") || !strings.Contains(o.stdout, "devices: 0 paired") {
		t.Fatalf("status: %+v", o)
	}
	if o := remoteRun(te, false, "", "pair"); o.code == 0 || !strings.Contains(o.stderr, "not running") {
		t.Fatalf("pair while off: %+v", o)
	}

	// Turning it on is picked up by the running server within 5 seconds.
	if o := remoteRun(te, false, "", "on", "tunnel", "--yes"); o.code != 0 || !strings.Contains(o.stdout, "within 5 seconds") {
		t.Fatalf("on: %+v", o)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		ready := tun != nil
		mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the running server did not open the tunnel within 5 seconds")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o := remoteRun(te, false, "", "status"); o.code != 0 || !strings.Contains(o.stdout, "mode: tunnel") || !strings.Contains(o.stdout, "listener tunnel: 127.0.0.1:") {
		t.Fatalf("status after on: %+v", o)
	}

	mu.Lock()
	tun.set(tunnel.Status{Mode: tunnel.StateRunning, PublicURL: "https://applysmith.example.com"})
	mu.Unlock()
	o := remoteRun(te, false, "", "pair")
	if o.code != 0 {
		t.Fatalf("pair: %+v", o)
	}
	var link string
	for _, line := range strings.Split(o.stdout, "\n") {
		if strings.HasPrefix(line, "https://applysmith.example.com/pair#") {
			link = line
		}
	}
	if link == "" || !strings.Contains(o.stdout, "█") || !strings.Contains(o.stdout, "expires in 5 minutes") {
		t.Fatalf("pair output:\n%s", o.stdout)
	}
	want, _ := qr.Encode(link)
	if got := strings.Count(o.stdout, "\n"); got < len(want)/2 {
		t.Errorf("the text QR code has %d lines for %d modules", got, len(want))
	}

	if o := remoteRun(te, false, "", "devices"); o.code != 0 || !strings.Contains(o.stdout, "no devices are paired") {
		t.Fatalf("devices: %+v", o)
	}
	if o := remoteRun(te, false, "", "revoke", "dev_missing"); o.code == 0 || !strings.Contains(o.stderr, "no such device") {
		t.Fatalf("revoke missing: %+v", o)
	}
	if o := remoteRun(te, false, "", "revoke", "all"); o.code != 0 || !strings.Contains(o.stdout, "(0)") {
		t.Fatalf("revoke all: %+v", o)
	}

	// The kill switch works while the server runs: the listener closes.
	if o := remoteRun(te, false, "", "off"); o.code != 0 {
		t.Fatalf("off: %+v", o)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if o := remoteRun(te, false, "", "status"); strings.Contains(o.stdout, "mode: off") && !strings.Contains(o.stdout, "listener tunnel") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the running server did not close the tunnel within 5 seconds")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}
