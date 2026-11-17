package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"applysmith/internal/extension"
)

// fakeDesk records what the bindings asked of the operating system.
type fakeDesk struct {
	revealed   []string
	opened     []string
	openErr    error
	picked     []mygo.OpenDialogOptions
	login      bool
	loginErr   error
	atLogin    bool
	copied     []string
	notices    []string // title and detail of each notice
	noticePick int
}

func (f *fakeDesk) desktop() desktop {
	return desktop{
		showItem:     func(p string) { f.revealed = append(f.revealed, p) },
		openPath:     func(p string) error { f.opened = append(f.opened, p); return nil },
		openExternal: func(u string) error { f.opened = append(f.opened, u); return f.openErr },
		choose: func(o mygo.OpenDialogOptions) (string, error) {
			f.picked = append(f.picked, o)
			return "/chosen", nil
		},
		loginEnabled:  func() bool { return f.login },
		setLogin:      func(on bool) error { f.login = on && f.loginErr == nil; return f.loginErr },
		openedAtLogin: func() bool { return f.atLogin },
		copyText:      func(t string) { f.copied = append(f.copied, t) },
		notice: func(_ *mygo.Window, title, detail string, _ []string) int {
			f.notices = append(f.notices, title+"|"+detail)
			return f.noticePick
		},
	}
}

func testShell(t *testing.T) (*Shell, *fakeDesk, string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	f := &fakeDesk{}
	var logs bytes.Buffer
	a := &shell{desk: f.desktop(), workspaceRoot: root, log: slog.New(slog.NewTextHandler(&logs, nil))}
	return &Shell{a}, f, root, &logs
}

func TestReveal_InsideTheWorkspace(t *testing.T) {
	s, f, root, _ := testShell(t)
	pdf := filepath.Join(root, "documents", "example-co", "1", "resume.pdf")
	if err := os.MkdirAll(filepath.Dir(pdf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(pdf)
	for name, p := range map[string]string{"relative": "documents/example-co/1/resume.pdf", "absolute": pdf} {
		f.revealed = nil
		if err := s.RevealInFinder(p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.revealed) != 1 || f.revealed[0] != want {
			t.Errorf("%s: revealed %v, want %s", name, f.revealed, want)
		}
	}
	f.revealed = nil
	if err := s.RevealInFinder(root); err != nil || len(f.revealed) != 1 {
		t.Errorf("the workspace folder itself: %v %v", err, f.revealed)
	}
}

func TestReveal_OutsideTheWorkspaceOpensNothing(t *testing.T) {
	s, f, root, _ := testShell(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "secret.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"absolute elsewhere": filepath.Join(other, "secret.txt"),
		"dot dot":            "../" + filepath.Base(other) + "/secret.txt",
		"dot dot inside":     "documents/../../etc/passwd",
		"symbolic link":      "escape/secret.txt",
		"empty":              "",
		"sibling prefix":     root + "-other/x",
	} {
		err := s.RevealInFinder(p)
		if err == nil || err.Error() != "path_outside_workspace" {
			t.Errorf("%s: error %v, want path_outside_workspace", name, err)
		}
	}
	if len(f.revealed) != 0 {
		t.Errorf("revealed %v", f.revealed)
	}
}

func TestReveal_MissingFileIsNotOutside(t *testing.T) {
	s, f, _, _ := testShell(t)
	if err := s.RevealInFinder("documents/none.pdf"); err == nil || err.Error() != "path_not_found" {
		t.Errorf("error %v, want path_not_found", err)
	}
	if len(f.revealed) != 0 {
		t.Error("revealed a missing file")
	}
}

func TestStartAtLogin_ToggleAndLiveState(t *testing.T) {
	s, f, _, _ := testShell(t)
	if f.login {
		t.Fatal("start at login must default to off")
	}
	if err := s.SetStartAtLogin(true); err != nil || !f.login {
		t.Fatalf("enable: %v, state %v", err, f.login)
	}
	// The live read is GetShellInfo's (TestShellInfo_ReadsStartAtLoginLive...).
	f.loginErr = errors.New("denied")
	if err := s.SetStartAtLogin(false); err == nil {
		t.Error("a refusal must reach the caller")
	}
}

func TestPickers(t *testing.T) {
	s, f, _, _ := testShell(t)
	got, err := s.PickFile(context.Background(), []string{".pdf", "DOCX"})
	if err != nil || got != "/chosen" {
		t.Fatalf("%q %v", got, err)
	}
	o := f.picked[0]
	if o.Directory || len(o.Filters) != 1 || strings.Join(o.Filters[0].Extensions, ",") != "pdf,DOCX" {
		t.Errorf("file dialog %+v", o)
	}
	for _, bad := range [][]string{nil, {"*"}, {"pdf;rm"}, {""}} {
		if _, err := s.PickFile(context.Background(), bad); err == nil {
			t.Errorf("extensions %q were accepted", bad)
		}
	}
	if _, err := s.PickFolder(context.Background()); err != nil || !f.picked[len(f.picked)-1].Directory {
		t.Errorf("folder dialog %+v %v", f.picked, err)
	}
}

func TestExternalLinkRules(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", maxLinkLength)
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"https://example.com/report", true},
		{"mailto:alex@example.com", true},
		{"javascript:alert(1)", false},
		{"https://user:pass@example.com/", false},
		{"https://user@example.com/", false},
		{"https:///nohost", false},
		{long, false},
	} {
		if _, err := checkLink(tc.raw); (err == nil) != tc.ok {
			t.Errorf("checkLink(%q) error = %v, want ok=%v", tc.raw[:min(len(tc.raw), 40)], err, tc.ok)
		}
	}
}

func TestExternalLink_RefusalIsLoggedWithoutTheQuery(t *testing.T) {
	s, f, _, logs := testShell(t)
	if err := s.OpenExternal("javascript:fetch('https://example.com/?token=abc')"); err == nil {
		t.Fatal("a script link opened")
	}
	if err := s.OpenExternal("https://u:p@example.com/x?token=secretvalue"); err == nil {
		t.Fatal("a link with credentials opened")
	}
	if len(f.opened) != 0 {
		t.Errorf("opened %v", f.opened)
	}
	if !strings.Contains(logs.String(), "external link refused") {
		t.Error("the refusal was not logged")
	}
	if strings.Contains(logs.String(), "secretvalue") || strings.Contains(logs.String(), "token") {
		t.Errorf("the log holds the query: %s", logs.String())
	}
}

func TestSameOrigin(t *testing.T) {
	const o = "http://127.0.0.1:47319"
	for raw, want := range map[string]bool{
		o + "/runs?a=1":                  true,
		o + "/#k=abc":                    true,
		"http://127.0.0.1:47320/":        false,
		"https://127.0.0.1:47319/":       false,
		"http://localhost:47319/":        false,
		"https://example.com/":           false,
		"http://127.0.0.1:47319.evil.io": false,
	} {
		if got := sameOrigin(raw, o); got != want {
			t.Errorf("sameOrigin(%q) = %v", raw, got)
		}
	}
}

// serverStub answers the two apply-window routes and records the calls.
func serverStub(t *testing.T, status int, body string) (*shell, *fakeDesk, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(raw))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	f := &fakeDesk{noticePick: 1}
	a := &shell{desk: f.desktop(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), origin: srv.URL, token: func() (string, error) { return "tok", nil }}
	return a, f, &calls
}

// A call that carries the API token goes to the address the shell holds and
// no further: a redirect is a failure, not a place to send the token.
func TestCallServer_DoesNotFollowARedirect(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/api/first" {
			http.Redirect(w, r, "/api/second", http.StatusTemporaryRedirect)
		}
	}))
	t.Cleanup(srv.Close)
	a := &shell{origin: srv.URL, token: func() (string, error) { return "tok", nil }}
	if err := a.callServer(t.Context(), "/api/first", nil); err == nil {
		t.Error("a redirected call succeeded")
	}
	if strings.Join(calls, " ") != "/api/first" {
		t.Errorf("calls %v", calls)
	}
}

func TestApplyWindow_ShowAndOpenCallTheServer(t *testing.T) {
	a, _, calls := serverStub(t, 200, `{"ok":true,"data":{}}`)
	s := &Shell{a}
	if err := s.ShowApplyWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenInApplyWindow(context.Background(), "https://example.com/jobs/1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/browser/apply-window/show Bearer tok {}",
		"POST /api/browser/apply-window/open Bearer tok " + mustJSON(map[string]string{"url": "https://example.com/jobs/1"}),
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls %q, want %q", *calls, want)
	}
	if err := s.OpenInApplyWindow(context.Background(), "file:///etc/passwd"); err == nil || len(*calls) != 2 {
		t.Errorf("a file address reached the server: %v %v", err, *calls)
	}
}

func TestApplyWindow_ChromeMissingShowsTheSetupNotice(t *testing.T) {
	a, f, _ := serverStub(t, 503, `{"ok":false,"error":{"code":"unavailable","message":"Chrome 120 or later was not found","details":{"reason":"chrome-missing"},"retryable":false}}`)
	err := (&Shell{a}).ShowApplyWindow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Chrome 120") {
		t.Errorf("error %v", err)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "Google Chrome was not found") {
		t.Errorf("notices %v", f.notices)
	}
}

func TestApplyWindow_OtherRefusalsAreNotChromeNotices(t *testing.T) {
	a, f, _ := serverStub(t, 409, `{"ok":false,"error":{"code":"conflict","message":"an apply run holds the window","retryable":false}}`)
	if err := (&Shell{a}).ShowApplyWindow(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if len(f.notices) != 0 {
		t.Errorf("notices %v", f.notices)
	}
}

func TestReopenDashboard_MintsACodeAndReturnsOnlyTheAddress(t *testing.T) {
	a, _, calls := serverStub(t, 200, `{"ok":true,"data":{"code":"c0de","expiresAt":"2026-01-01T00:00:00Z"}}`)
	u, err := a.freshDashboardURL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := a.origin + "/?launch=c0de"; u != want {
		t.Errorf("url %q, want %q", u, want)
	}
	if want := "POST /api/launch-codes Bearer tok "; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls %q, want %q", *calls, want)
	}
	empty, _, _ := serverStub(t, 200, `{"ok":true,"data":{}}`)
	if _, err := empty.freshDashboardURL(context.Background()); err == nil {
		t.Error("an empty code was accepted")
	}
	if _, err := (&shell{desk: (&fakeDesk{}).desktop()}).freshDashboardURL(context.Background()); !errors.Is(err, errNoServer) {
		t.Errorf("error %v", err)
	}
}

func TestApplyWindow_NoServerYet(t *testing.T) {
	a := &shell{desk: (&fakeDesk{}).desktop()}
	if err := (&Shell{a}).ShowApplyWindow(context.Background()); !errors.Is(err, errNoServer) {
		t.Errorf("error %v", err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestShellInfo_ReadsStartAtLoginLiveAndNamesTheLogDirectory(t *testing.T) {
	s, f, root, _ := testShell(t)
	if got := s.GetShellInfo(); got.StartAtLogin || got.LogDir != filepath.Join(root, ".applysmith", "logs") {
		t.Errorf("info %+v", got)
	}
	f.login = true
	if !s.GetShellInfo().StartAtLogin {
		t.Error("a login item added outside the app is not reported")
	}
}

// Settings tells the user to load the folder the shell names, so a startup
// writes this build there and the shell names the folder only once it holds it.
func TestShellInfo_NamesTheExtensionFolderOnlyOnceItHoldsThisBuild(t *testing.T) {
	if _, err := extension.Files(); err != nil {
		t.Skip("run make generate first")
	}
	occupied := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		base string
		err  error
		want bool
	}{
		{"the user data folder", t.TempDir(), nil, true},
		{"no user data folder", t.TempDir(), errors.New("unavailable"), false},
		{"a folder that cannot be written", occupied, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := testShell(t)
			s.a.desk.userDataDir = func() (string, error) { return tc.base, tc.err }
			started := make(chan struct{})
			s.a.attemptFn = func() { close(started) }
			if !s.a.begin() {
				t.Fatal("no startup attempt began")
			}
			<-started
			got := s.GetShellInfo().ExtensionDir
			if !tc.want {
				if got != "" {
					t.Fatalf("the shell names %q", got)
				}
				return
			}
			if got != filepath.Join(tc.base, "extension") {
				t.Fatalf("the shell names %q", got)
			}
			if _, err := os.Stat(filepath.Join(got, "manifest.json")); err != nil {
				t.Fatalf("the named folder holds no extension: %v", err)
			}
		})
	}
}

// The dashboard decodes ShellInfo strictly, so a field added on either side
// must be added on both.
func TestShellInfoFieldsAreTheOnesTheDashboardDecodes(t *testing.T) {
	raw, err := json.Marshal(ShellInfo{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	ts, err := os.ReadFile("../web/src/desktop.ts")
	if err != nil {
		t.Fatal(err)
	}
	decoder := string(ts)[strings.Index(string(ts), "shellInfoDecoder"):]
	for name := range fields {
		if !strings.Contains(decoder, "  "+name+": ") {
			t.Errorf("web/src/desktop.ts does not decode %q", name)
		}
	}
	if got := len(regexp.MustCompile(`(?m)^  \w+: `).FindAllString(decoder[:strings.Index(decoder, "});")], -1)); got != len(fields) {
		t.Errorf("the dashboard decodes %d fields, the shell sends %d", got, len(fields))
	}
}
