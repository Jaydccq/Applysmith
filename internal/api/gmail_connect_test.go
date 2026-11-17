package api

import (
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail"
)

func TestAPI_Gmail_SyncAndAccountRoutesAreLocalOnly(t *testing.T) {
	want := map[string]struct {
		access Access
		secret bool
		body   Body
		resp   string
	}{
		"POST /api/gmail/sync":         {AccessStandard, false, BodyNone, "GmailSyncStarted"},
		"PUT /api/gmail/account":       {AccessAdmin, true, BodyJSON, "GmailConnected"},
		"POST /api/gmail/account/test": {AccessAdmin, false, BodyNone, "GmailTest"},
		"DELETE /api/gmail/account":    {AccessAdmin, true, BodyNone, "GmailHealth"},
	}
	seen := 0
	for _, r := range Routes() {
		w, ok := want[r.Pattern()]
		if !ok {
			continue
		}
		seen++
		if r.Access != w.access || r.Secret != w.secret || r.Response != w.resp || r.Remote != RemoteDenied || r.Body != w.body {
			t.Errorf("%s: %+v", r.Pattern(), r)
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of %d routes", seen, len(want))
	}
}

// An address or app password that is malformed, or that Gmail refuses, is
// refused on its field; nothing is stored and no answer repeats the password.
func TestAPI_Gmail_ConnectRefusesOnAFieldAndNeverEchoesThePassword(t *testing.T) {
	h, g := unconnectedGmailHarness(t)
	g.Refuse("Invalid credentials (Failure)")
	other := strings.Repeat("abcd", 4)
	for _, c := range []struct{ name, address, password, field, says string }{
		{"not an address", "alex", gmPassword, "address", "one email address"},
		{"an account password", gmAddr("alex", "example.org"), "my-Google-password-1", "password", "16 letters"},
		{"refused by Gmail", gmAddr("alex", "example.org"), other, "password", "Invalid credentials"},
	} {
		r := h.send("PUT", "/api/gmail/account", map[string]string{"address": c.address, "password": c.password})
		if r.StatusCode != 422 || r.errField() != c.field || !strings.Contains(string(r.body), c.says) || strings.Contains(string(r.body), c.password) {
			t.Errorf("%s: %d %s", c.name, r.StatusCode, r.body)
		}
	}
	if h.vault.Has("gmail-app-password") {
		t.Fatal("a refused password was stored")
	}
	if h.get("/api/gmail/health").data()["connected"] != false {
		t.Fatal("a refused connection reads as connected")
	}
}

func TestAPI_Gmail_SyncNowStartsAManualRunInTheBackground(t *testing.T) {
	h, _ := gmailHarness(t)
	r := h.post("/api/gmail/sync", nil)
	if r.StatusCode != 202 || r.data()["started"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	waitFor(t, "the manual run to be recorded and finished", func() bool {
		runs, _ := h.get("/api/gmail/runs").data()["runs"].([]any)
		if len(runs) == 0 {
			return false
		}
		run := runs[0].(map[string]any)
		return run["trigger"] == "manual" && run["status"] != "running"
	})
	if h.get("/api/gmail/health").data()["running"] != false {
		t.Fatal("health still reads running after the run ended")
	}
}

// Connecting Gmail from the dashboard signs in, stores the password in the
// keychain, and the server's watcher reads the mailbox at once, so job mail
// moves the tracker with no further step (F-2) and no schedule. Disconnecting
// removes the password and ends the watcher's connection.
func TestAPI_Gmail_ConnectingStartsTheWatcherAndDisconnectingStopsIt(t *testing.T) {
	h, g := unconnectedGmailHarness(t, func(c *Config) { c.Domain.Gmail.NoWatch = false })
	g.add(gmMessage{id: "a1", from: gmAddr("x", "example.com"), subject: "s", text: "t", ms: h.ms(-time.Hour)})
	r := h.send("PUT", "/api/gmail/account", map[string]string{"address": gmAddr("alex", "example.org"), "password": "wxyz wxyz wxyz wxyz"})
	if r.StatusCode != 200 || strings.Contains(string(r.body), gmPassword) {
		t.Fatalf("connect: %d %s", r.StatusCode, r.body)
	}
	health, _ := r.data()["health"].(map[string]any)
	if health["connected"] != true || health["login"] != "ok" || health["mailbox"] != gmAddr("alex", "example.org") {
		t.Fatalf("connected health: %v", health)
	}
	if got, _ := h.vault.Get("gmail-app-password"); got != gmPassword {
		t.Fatal("the app password did not reach the keychain without its spaces")
	}
	// The watcher waits a few seconds for a burst of mail to settle.
	waitLong(t, "the watcher to read the mailbox after connecting", func() bool {
		runs, _ := h.get("/api/gmail/runs").data()["runs"].([]any)
		return len(runs) > 0 && runs[0].(map[string]any)["trigger"] == "scheduled" && runs[0].(map[string]any)["status"] != "running"
	})
	waitFor(t, "the watcher to idle", func() bool { return len(g.Commands("IDLE")) > 0 })
	var n int
	_ = h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM schedules WHERE job_kind = 'gmail.sync'`).Scan(&n)
	if n != 0 {
		t.Fatalf("connecting made %d sync schedules", n)
	}
	if r := h.post("/api/gmail/account/test", nil); r.StatusCode != 200 || r.data()["messages"] != float64(1) {
		t.Fatalf("test: %d %s", r.StatusCode, r.body)
	}
	r = h.send("DELETE", "/api/gmail/account", nil)
	if r.StatusCode != 200 || r.data()["connected"] != false {
		t.Fatalf("disconnect: %d %s", r.StatusCode, r.body)
	}
	if h.vault.Has("gmail-app-password") {
		t.Fatal("the app password was kept")
	}
	waitFor(t, "the watcher's connection to close", func() bool { return g.Connections() == 0 })
	if r := h.post("/api/gmail/account/test", nil); r.StatusCode != 503 || r.errReason() != gmail.ReasonNotSignedIn {
		t.Fatalf("test after disconnect: %d %s", r.StatusCode, r.body)
	}
}
