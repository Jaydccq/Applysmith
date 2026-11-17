//go:build browser

package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

// TestSessionSyncThroughTheRoutesAgainstARealProfile runs the whole request
// side over HTTP with the real managed browser on a throwaway profile: a
// dashboard request, the extension's check-in and answer, the read-back counts,
// the undo, and a run's own-host request.
func TestSessionSyncThroughTheRoutesAgainstARealProfile(t *testing.T) {
	chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	dir := t.TempDir()
	snapDir := filepath.Join(dir, "browser")
	opts := browser.Options{
		Chrome:       chrome,
		ProfileDir:   filepath.Join(dir, "applysmith-browser-test-profile"),
		StateDir:     filepath.Join(dir, "state"),
		DownloadsDir: filepath.Join(dir, "downloads"),
		Environ:      os.Environ(),
	}.WithHeadless()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if inst, err := browser.Open(ctx, opts); err == nil {
			inst.Stop(ctx)
		}
	})
	sessions := browser.InstanceSessions(opts, snapDir, func(context.Context) ([]browser.StoredAccount, error) {
		return []browser.StoredAccount{{Host: "jobs.example.test"}, {Host: "careers.example.org"}}, nil
	}, nil)
	h := newDomainHarness(t, func(c *Config) { c.SessionBrowser = sessions })

	if r := h.get(sessionsPath); r.StatusCode != 200 || r.data()["undoAvailable"] != false {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	req := h.syncRequest("jobs.example.test")
	if req.StatusCode != 202 {
		t.Fatalf("%d %s", req.StatusCode, req.body)
	}
	pending := h.checkin(wire.SessionSiteCount{Domain: "jobs.example.test", Cookies: 3}).data()["pending"].(map[string]any)
	cookies := []wire.SessionCookie{cookie("jobs.example.test", "sid"), cookie(".jobs.example.test", "csrf"), cookie("jobs.example.test", "pref")}
	imported := h.post(importsPath, wire.SessionImport{RequestID: pending["requestId"].(string), Cookies: cookies, Skipped: []string{}})
	if imported.StatusCode != 200 || imported.data()["imported"] != float64(3) || imported.data()["failed"] != float64(0) || imported.data()["replaced"] != float64(0) {
		t.Fatalf("%d %s", imported.StatusCode, imported.body)
	}
	st, err := os.Stat(filepath.Join(snapDir, "session-snapshot.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the snapshot: %v %v", st, err)
	}

	view := h.get(sessionsPath)
	v := view.data()
	if v["undoAvailable"] != true || v["lastOutcome"].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("%s", view.body)
	}
	var managed float64 = -1
	for _, s := range v["sites"].([]any) {
		if row := s.(map[string]any); row["domain"] == "jobs.example.test" {
			managed = row["cookies"].(float64)
			if row["sourceCookies"] != float64(3) {
				t.Errorf("%v", row)
			}
		}
	}
	if managed != 3 {
		t.Fatalf("the profile holds the three cookies: %s", view.body)
	}

	// A second import replaces the three cookies and the snapshot with them.
	push := h.post(pushesPath, wire.SessionPush{Site: "jobs.example.test", Cookies: []wire.SessionCookie{cookie("jobs.example.test", "sid")}})
	if push.StatusCode != 200 || push.data()["imported"] != float64(1) || push.data()["replaced"] != float64(3) {
		t.Fatalf("%d %s", push.StatusCode, push.body)
	}
	undo := h.post(undoPath, nil)
	if undo.StatusCode != 200 || undo.data()["restored"] != float64(3) || undo.data()["failed"] != float64(0) {
		t.Fatalf("%d %s", undo.StatusCode, undo.body)
	}
	if r := h.post(undoPath, nil); r.StatusCode != 404 || r.errReason() != "no-snapshot" {
		t.Fatalf("a second undo: %d %s", r.StatusCode, r.body)
	}

	// A run's own-host request, answered by the extension's next check-in.
	h.checkin(wire.SessionSiteCount{Domain: "careers.example.org", Cookies: 1})
	answered := h.answerWhenAsked("careers.example.org", cookie("careers.example.org", "sid"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	own, err := h.srv.SyncOwnHost(ctx, "careers.example.org")
	if err != nil || own.Status != OwnHostImported || own.Result.Imported != 1 {
		t.Fatalf("%+v %v", own, err)
	}
	<-answered

	for _, text := range []string{string(view.body), string(imported.body), string(push.body), string(undo.body), h.logText()} {
		if strings.Contains(text, cookieSecret) {
			t.Fatal("a cookie value was repeated")
		}
	}
	if h.dbHolds(cookieSecret) {
		t.Fatal("a cookie value reached the database")
	}
}
