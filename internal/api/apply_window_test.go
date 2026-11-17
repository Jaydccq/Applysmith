package api

import (
	"context"
	"sync/atomic"
	"testing"

	"applysmith/internal/browser"
)

const openPath = "/api/browser/apply-window/open"

func openHarness(t *testing.T, live *atomic.Bool, opened *[]string, openErr error) *harness {
	t.Helper()
	b := &fakeBrowser{sites: []string{"jobs.example.test"}}
	return newDomainHarness(t, func(c *Config) {
		c.SessionBrowser = b
		c.ApplyRunLive = live.Load
		c.OpenInApplyWindow = func(_ context.Context, u string) error {
			*opened = append(*opened, u)
			return openErr
		}
	})
}

func TestApplyWindowOpen_OpensAUserTabAndRefusesAnUnsafeAddress(t *testing.T) {
	var live atomic.Bool
	var opened []string
	h := openHarness(t, &live, &opened, nil)
	if r := h.post(openPath, map[string]string{"url": "https://jobs.example.test/posting/1"}); r.StatusCode != 200 || r.data()["opened"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, bad := range []string{"javascript:alert(1)", "https://user:pw@jobs.example.test/"} {
		if r := h.post(openPath, map[string]string{"url": bad}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%q: %d %s", bad, r.StatusCode, r.body)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("only the good address was opened: %v", opened)
	}
}

func TestApplyWindowOpen_ASignInSiteIsRefusedWhileARunDrivesTheWindow(t *testing.T) {
	var live atomic.Bool
	var opened []string
	h := openHarness(t, &live, &opened, nil)
	live.Store(true)
	r := h.post(openPath, map[string]string{"url": "https://jobs.example.test/login"})
	if r.StatusCode != 409 || r.errCode() != "conflict" || r.errReason() != "apply-run-live" || len(opened) != 0 {
		t.Fatalf("%d %s opened=%v", r.StatusCode, r.body, opened)
	}
	if r := h.post(openPath, map[string]string{"url": "https://elsewhere.example.org/posting"}); r.StatusCode != 200 {
		t.Fatalf("a page of no listed site opens during a run: %d %s", r.StatusCode, r.body)
	}
	live.Store(false)
	if r := h.post(openPath, map[string]string{"url": "https://jobs.example.test/login"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestApplyWindowOpen_NoBrowserIsUnavailableAndALaunchFaultKeepsItsReason(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.post(openPath, map[string]string{"url": "https://jobs.example.test/"}); r.StatusCode != 503 || r.errReason() != "browser-unavailable" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var live atomic.Bool
	var opened []string
	h = openHarness(t, &live, &opened, &browser.Error{Code: browser.CodeLaunchTimeout, Message: "the browser did not start"})
	if r := h.post(openPath, map[string]string{"url": "https://jobs.example.test/"}); r.StatusCode != 503 || r.errReason() != string(browser.CodeLaunchTimeout) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}
