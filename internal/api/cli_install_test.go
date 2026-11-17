package api

import (
	"os"
	"path/filepath"
	"testing"

	"applysmith/internal/api/wire"
)

// cliRig is a server whose home directory and "installed app" are temporary.
type cliRig struct {
	*harness
	home, exe, cookie string
}

func newCLIRig(t *testing.T, path string) *cliRig {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(root, "Applysmith.app", "Contents", "MacOS", "Applysmith")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	t.Setenv("SHELL", "/bin/zsh")
	h := newDomainHarness(t, func(c *Config) { c.Home, c.Executable = home, exe })
	return &cliRig{harness: h, home: home, exe: exe, cookie: h.session()}
}

func (r *cliRig) call(method string) result { return r.chat(r.cookie, method, "/api/system/cli", nil) }

func (r *cliRig) audit(code string) int {
	var n int
	row := r.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gate_decisions WHERE gate = 'cli_install' AND code = ?`, code)
	if err := row.Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestCLIInstallAPI_RoutesAreSessionOnlyForChangesAndNeverRemote(t *testing.T) {
	want := map[string]bool{"GET /api/system/cli": false, "PUT /api/system/cli": true, "DELETE /api/system/cli": true}
	seen := 0
	for _, r := range Routes() {
		session, ok := want[r.Pattern()]
		if !ok {
			continue
		}
		seen++
		if r.SessionOnly != session || r.Remote != RemoteDenied || r.Access != AccessStandard || r.Secret {
			t.Errorf("%s = %+v", r.Pattern(), r)
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d routes", seen, len(want))
	}
}

func TestCLIInstallAPI_TheTokenCannotChangeTheLink(t *testing.T) {
	r := newCLIRig(t, "/usr/bin")
	for _, method := range []string{"PUT", "DELETE"} {
		if res := r.send(method, "/api/system/cli", nil); res.StatusCode != 403 || res.errReason() != wire.ReasonSessionOnly {
			t.Errorf("%s with the token: %d %s", method, res.StatusCode, res.body)
		}
	}
	if _, err := os.Lstat(filepath.Join(r.home, ".local", "bin", "applysmith")); err == nil {
		t.Fatal("a refused request made the link")
	}
	if res := r.get("/api/system/cli"); res.StatusCode != 200 {
		t.Fatalf("the token may read the state: %d %s", res.StatusCode, res.body)
	}
}

func TestCLIInstallAPI_MakeReportAndRemoveTheLink(t *testing.T) {
	r := newCLIRig(t, "/usr/bin")
	st := decode[wire.CLIInstall](t, r.call("GET"))
	link := filepath.Join(r.home, ".local", "bin", "applysmith")
	if !st.Available || st.State != "not-installed" || st.Link != link || st.OnPath || st.ProfileFile != "~/.zshrc" || st.ProfileLine != `export PATH="$HOME/.local/bin:$PATH"` {
		t.Fatalf("before: %+v", st)
	}
	st = decode[wire.CLIInstall](t, r.call("PUT"))
	if st.State != "installed" || st.Target == "" {
		t.Fatalf("after install: %+v", st)
	}
	if dest, err := os.Readlink(link); err != nil || dest != st.Target {
		t.Fatalf("link -> %q (%v), want %q", dest, err, st.Target)
	}
	if n := r.audit("cli_link_created"); n != 1 {
		t.Fatalf("created audit rows: %d", n)
	}
	r.call("PUT")
	if n := r.audit("cli_link_created"); n != 1 {
		t.Fatalf("a repeat that changes nothing is not audited: %d", n)
	}
	st = decode[wire.CLIInstall](t, r.call("DELETE"))
	if st.State != "not-installed" {
		t.Fatalf("after remove: %+v", st)
	}
	if n := r.audit("cli_link_removed"); n != 1 {
		t.Fatalf("removed audit rows: %d", n)
	}
	if _, err := os.Stat(r.exe); err != nil {
		t.Fatalf("the app binary is gone: %v", err)
	}
}

func TestCLIInstallAPI_AForeignFileIsRefusedAndLeftAlone(t *testing.T) {
	r := newCLIRig(t, "/usr/bin")
	link := filepath.Join(r.home, ".local", "bin", "applysmith")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		res := r.call(method)
		if res.StatusCode != 409 || res.errReason() != "cli_link_foreign" {
			t.Fatalf("%s: %d %s", method, res.StatusCode, res.body)
		}
	}
	if b, err := os.ReadFile(link); err != nil || string(b) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("the user's file changed: %q %v", b, err)
	}
	if st := decode[wire.CLIInstall](t, r.call("GET")); st.State != "foreign" {
		t.Fatalf("state %q", st.State)
	}
	if n := r.audit("cli_link_foreign"); n != 2 {
		t.Fatalf("refusals audited: %d", n)
	}
}
