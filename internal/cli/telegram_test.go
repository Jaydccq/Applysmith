package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeBotAPI(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 1, "is_bot": true, "username": "ExampleBot"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTelegramCLI_SetTokenPairUnpairClearAndStatusLeakNothing(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %d %s", o.code, o.stderr)
	}
	token := "123456789:" + strings.Repeat("Tk_x-", 7)
	srv := fakeBotAPI(t, token)
	run := func(stdin string, args ...string) output {
		te.stdin = stdin
		var out, errw syncBuffer
		e := te.env(&out, &errw)
		e.telegramAPI = srv.URL
		code := e.run(context.Background(), append([]string{"telegram"}, append(args, "--workspace", te.ws)...))
		return output{code, out.String(), errw.String()}
	}

	if o := run("", "set-token"); o.code != 2 {
		t.Errorf("set-token without --stdin = %d", o.code)
	}
	bad := "987654321:" + strings.Repeat("Zz_9-", 7)
	if o := run(bad, "set-token", "--stdin"); o.code != 1 || !strings.Contains(o.stderr, "auth") || strings.Contains(o.stdout+o.stderr, bad) {
		t.Fatalf("a refused token = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := run("hello", "set-token", "--stdin"); o.code != 2 || strings.Contains(o.stderr, "hello") {
		t.Errorf("a non-token = %d %q", o.code, o.stderr)
	}
	o := run(token+"\n", "set-token", "--stdin")
	if o.code != 0 || !strings.Contains(o.stdout, "@ExampleBot") || strings.Contains(o.stdout+o.stderr, token) {
		t.Fatalf("set-token = %d %q %q", o.code, o.stdout, o.stderr)
	}
	o = run("", "pair")
	if o.code != 0 || !strings.Contains(o.stdout, "@ExampleBot") || strings.Contains(o.stdout+o.stderr, token) {
		t.Fatalf("pair = %d %q %q", o.code, o.stdout, o.stderr)
	}
	// The code also comes as a link that pairs with one press of Start, and as
	// a QR code of that link for a phone.
	lines := strings.Split(strings.TrimSpace(o.stdout), "\n")
	if code := lines[len(lines)-1]; !strings.Contains(o.stdout, "https://t.me/ExampleBot?start="+code) || !strings.Contains(o.stdout, "█") {
		t.Errorf("pair output has no start link or QR code: %q", o.stdout)
	}
	o = run("", "status", "--json")
	var st map[string]any
	if err := json.Unmarshal([]byte(o.stdout), &st); err != nil || st["tokenSet"] != true || st["botUsername"] != "ExampleBot" || st["paired"] != false || strings.Contains(o.stdout, token) {
		t.Fatalf("status = %d %q", o.code, o.stdout)
	}
	if o := run("", "unpair"); o.code != 0 {
		t.Fatalf("unpair = %d %q", o.code, o.stderr)
	}
	if o := run("", "clear-token"); o.code != 0 {
		t.Fatalf("clear = %d %q", o.code, o.stderr)
	}
	for _, a := range te.backend.Accounts() {
		if strings.Contains(a, "telegram") {
			t.Errorf("the keychain still holds %s", a)
		}
	}
	o = run("", "status")
	if !strings.Contains(o.stdout, "token: false") || !strings.Contains(o.stdout, "health: stopped") {
		t.Errorf("status after clear = %q", o.stdout)
	}
	if o := te.run(context.Background(), "telegram"); o.code != 2 {
		t.Errorf("usage = %d", o.code)
	}
}

func TestTelegramCLI_DoctorWarnsNeverFailsWhenNotSetUp(t *testing.T) {
	te := newTestEnv(t)
	te.run(context.Background(), "init", "--workspace", te.ws)
	te.run(context.Background(), "telegram", "status", "--workspace", te.ws) // creates the database
	o := te.run(context.Background(), "doctor", "--only", "telegram", "--json", "--workspace", te.ws)
	var rep struct {
		Results []struct {
			ID, Status string
			Required   bool
		}
	}
	_ = json.Unmarshal([]byte(o.stdout), &rep)
	if len(rep.Results) != 2 {
		t.Fatalf("doctor = %d %q %q", o.code, o.stdout, o.stderr)
	}
	for _, c := range rep.Results {
		if c.Status != "warn" || c.Required {
			t.Errorf("%s = %s required %v", c.ID, c.Status, c.Required)
		}
	}
}
