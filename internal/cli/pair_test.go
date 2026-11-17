package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// statusWith calls an authenticated route of the running server.
func statusWith(t *testing.T, origin, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", origin+"/api/tracker/summary", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPairPrintsTheTokenTheRunningServerAccepts(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	o := te.run(context.Background(), "pair", "--workspace", te.ws)
	token := strings.TrimSpace(o.stdout)
	if o.code != 0 || len(token) != 43 || strings.Contains(token, "\n") {
		t.Fatalf("%+v", o)
	}
	if got := statusWith(t, rs.origin, token); got != 200 {
		t.Errorf("the printed token is refused: %d", got)
	}
	if strings.Contains(o.stderr, token) {
		t.Error("the token is repeated on stderr")
	}
}

func TestPairBeforeAnyServerStartedExplainsAndCreatesNothing(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "pair", "--workspace", te.ws)
	if o.code != 1 || o.stdout != "" || !strings.Contains(o.stderr, "applysmith serve") {
		t.Fatalf("%+v", o)
	}
}

func TestTokenRotateWhileAServerRunsInvalidatesTheOldToken(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	old := strings.TrimSpace(te.run(context.Background(), "pair", "--workspace", te.ws).stdout)
	o := te.run(context.Background(), "token", "rotate", "--workspace", te.ws)
	fresh := strings.TrimSpace(o.stdout)
	if o.code != 0 || len(fresh) != 43 || fresh == old {
		t.Fatalf("%+v", o)
	}
	if got := statusWith(t, rs.origin, old); got != 401 {
		t.Errorf("old token: %d", got)
	}
	if got := statusWith(t, rs.origin, fresh); got != 200 {
		t.Errorf("new token: %d", got)
	}
	if again := strings.TrimSpace(te.run(context.Background(), "pair", "--workspace", te.ws).stdout); again != fresh {
		t.Error("pair does not print the rotated token")
	}
}

func TestTokenRotateWithoutAServerReplacesTheStoredToken(t *testing.T) {
	te := newTestEnv(t)
	te.startServe().stop() // the first start creates the token
	before := strings.TrimSpace(te.run(context.Background(), "pair", "--workspace", te.ws).stdout)
	o := te.run(context.Background(), "token", "rotate", "--workspace", te.ws)
	after := strings.TrimSpace(o.stdout)
	if o.code != 0 || len(after) != 43 || after == before {
		t.Fatalf("%+v", o)
	}
	if got := strings.TrimSpace(te.run(context.Background(), "pair", "--workspace", te.ws).stdout); got != after {
		t.Errorf("the vault holds a different token than the one printed")
	}
}

func TestTokenRotateRefusesAnEnvironmentManagedToken(t *testing.T) {
	te := newTestEnv(t)
	te.vars["APPLYSMITH_SECRET_API_TOKEN"] = strings.Repeat("e", 43)
	te.startServe()
	o := te.run(context.Background(), "token", "rotate", "--workspace", te.ws)
	if o.code != 1 || !strings.Contains(o.stderr, "APPLYSMITH_SECRET_API_TOKEN") || strings.Contains(o.stdout, "eeee") {
		t.Fatalf("%+v", o)
	}
}

func TestTokenUsage(t *testing.T) {
	te := newTestEnv(t)
	for _, args := range [][]string{{"token"}, {"token", "show"}, {"pair", "extra"}} {
		if o := te.run(context.Background(), args...); o.code != 2 {
			t.Errorf("%v: %+v", args, o)
		}
	}
}

func TestOpenPrintsAFreshLinkAndTheOldOneStopsWorking(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	printed := rs.link()
	o := te.run(context.Background(), "open", "--workspace", te.ws)
	link := strings.TrimSpace(o.stdout)
	if o.code != 0 || !strings.HasPrefix(link, rs.origin+"/?launch=") || link == printed {
		t.Fatalf("%+v (serve printed %s)", o, printed)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for name, want := range map[string]bool{printed: false, link: true} {
		resp, err := client.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := len(resp.Cookies()) > 0; got != want {
			t.Errorf("%s: session issued = %v, want %v", name, got, want)
		}
	}
}

func TestOpenHandsTheLinkToTheBrowser(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	var opened string
	var out, errw bytes.Buffer
	e := te.env(&out, &errw)
	e.openURL = func(u string) error { opened = u; return nil }
	if code := e.run(context.Background(), []string{"open", "--browser", "--workspace", te.ws}); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	if !strings.HasPrefix(opened, rs.origin+"/?launch=") || !strings.Contains(out.String(), opened) {
		t.Errorf("opened %q, printed %q", opened, out.String())
	}
}

func TestOpenWithoutAServerSaysSo(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "open", "--workspace", te.ws)
	if o.code != 1 || !strings.Contains(o.stderr, "applysmith serve") {
		t.Fatalf("%+v", o)
	}
}
