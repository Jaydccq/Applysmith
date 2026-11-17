package api

import (
	"testing"

	"applysmith/internal/api/wire"
)

func TestRemote_ScreenshotsFollowThePolicyAndAreOnePerTwoSeconds(t *testing.T) {
	ctrl := &fakeControl{}
	r := newRemoteRig(t, func(c *Config) { c.BrowserControl = ctrl })
	r.online()
	dev := r.pair("Phone")
	shot := "/api/browser/lanes/apply/tabs/tab-1/screenshot"
	if got := r.tdo(asDevice(dev, "GET", shot)); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden || ctrl.shots != 0 {
		t.Fatalf("the switch is off: %d %s", got.status, got.body)
	}
	r.setPolicy(set("remote_access.allow_screenshots", "true"))
	r.srv.remote.reconcile()
	got := r.tdo(asDevice(dev, "GET", shot))
	if got.status != 200 || got.header.Get("Content-Type") != "image/jpeg" || string(got.body) != string(stillBytes) {
		t.Fatalf("the switch is on: %d %s", got.status, got.body)
	}
	if again := r.tdo(asDevice(dev, "GET", shot)); again.status != 429 || again.errCode() != "rate_limited" || ctrl.shots != 1 {
		t.Fatalf("a second still within two seconds: %d %s shots=%d", again.status, again.body, ctrl.shots)
	}
	for _, p := range [][2]string{{"GET", "/api/browser/status"}, {"POST", "/api/browser/apply-window/show"}, {"POST", "/api/browser/stop"}} {
		c := asDevice(dev, p[0], p[1])
		c.contentType, c.body = "application/json", []byte("{}")
		if got := r.tdo(c); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
			t.Errorf("%s %s stays closed with screenshots on: %d %s", p[0], p[1], got.status, got.body)
		}
	}
	r.setPolicy(set("remote_access.allow_screenshots", "false"))
	r.srv.remote.reconcile()
	if got := r.tdo(asDevice(dev, "GET", shot)); got.status != 403 || got.errReason() != wire.ReasonRemoteForbidden {
		t.Fatalf("the switch is off again: %d %s", got.status, got.body)
	}
}
