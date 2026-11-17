package api

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

// TestCanaryResolvedPasswordLeavesOnlyThroughTheResolveResponse is the sign-in
// resolve part of the release canary (credentials-vault, "Secrets never leave
// the vault's channels"): a canary password is stored, the extension resolves
// it, and a scan of the workspace (database, write-ahead log, logs, every file)
// and of the responses of the other routes finds it nowhere else.
func TestCanaryResolvedPasswordLeavesOnlyThroughTheResolveResponse(t *testing.T) {
	const canary = "Canary-Pw-7c1e!Qz"
	h := newDomainHarness(t)
	h.seedCredential("signin-default", "signin_default", "", "alex@example.com", canary)
	h.seedCredential("signin-site-careers.example.test", "signin_site", "careers.example.test", "alex@example.com", canary+"-site")

	var other [][]byte
	got, r := h.resolve("https://jobs.example.test/login")
	if r.StatusCode != 200 || got.Password != canary || bytes.Count(r.body, []byte(canary)) != 1 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got, r := h.resolve("https://careers.example.test/login"); got.Password != canary+"-site" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// Refusals and failures that are written to the log and returned to the caller.
	h.hold("held.example.test", false)
	for _, u := range []string{"https://held.example.test/", "file:///etc/hosts", "https://jobs.example.test/" + strings.Repeat("a", 2100)} {
		other = append(other, h.post(resolvePath, wire.SigninResolveRequest{URL: u}).body)
	}
	// Even a careless log line or error text built from the value is scrubbed.
	h.srv.log.Warn("careless", "password", canary, "error", "keychain said "+canary)

	for _, route := range routeDefs {
		if route.Method != "GET" || strings.ContainsAny(route.Path, "{}") || !strings.HasPrefix(route.Path, "/api/") ||
			strings.Contains(route.Path, "events") || strings.Contains(route.Path, "stream") || strings.Contains(route.Path, "otp") {
			continue
		}
		other = append(other, h.get(route.Path).body)
	}
	for i, b := range other {
		if bytes.Contains(b, []byte(canary)) {
			t.Errorf("response %d carries the canary: %.200s", i, b)
		}
	}
	if logs := h.logText(); strings.Contains(logs, canary) || !strings.Contains(logs, "[redacted]") {
		t.Errorf("log: canary present or redaction not applied")
	}
	scanned := 0
	err := filepath.WalkDir(h.ws.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if bytes.Contains(b, []byte(canary)) {
			t.Errorf("%s carries the canary", path)
		}
		return nil
	})
	if err != nil || scanned == 0 {
		t.Fatalf("scanned %d files: %v", scanned, err)
	}
}
