//go:build browser

package browser

import (
	"encoding/json"
	"strings"
	"testing"
)

// Task 1.2 proof: a launched, attached tab sees navigator.webdriver === false.
func TestE2E_Browser_WebdriverFalseOnExplicitPort(t *testing.T) {
	inst, fx := headedInstance(t)
	h := inst.Lane(LaneScan)
	id, err := h.OpenTab(ctx30(t), "run-webdriver")
	ok(t, err)
	t.Cleanup(func() { h.CloseOwned(ctx30(t), "run-webdriver") })
	res, err := h.Navigate(ctx30(t), id, fx.Loopback("/probe"))
	ok(t, err)
	if res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	var probe struct {
		Webdriver any    `json:"webdriver"`
		UA        string `json:"ua"`
	}
	raw, err := h.Evaluate(ctx30(t), id, `document.getElementById('o').textContent`)
	ok(t, err)
	var text string
	_ = json.Unmarshal(raw, &text)
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Webdriver != false {
		t.Fatalf("navigator.webdriver = %v, want false", probe.Webdriver)
	}
	if !strings.Contains(probe.UA, "Chrome/") || strings.Contains(probe.UA, "HeadlessChrome") {
		t.Fatalf("user agent %q", probe.UA)
	}
	t.Logf("ua=%s chrome=%s", probe.UA, inst.Version())
}

// The headers a page request carries are exactly what Chrome sends on its own:
// no override of the user agent or the client hints.
func TestE2E_Browser_IdentityIsNotRewritten(t *testing.T) {
	inst, fx := headedInstance(t)
	h := inst.Lane(LaneScan)
	id, err := h.OpenTab(ctx30(t), "run-identity")
	ok(t, err)
	t.Cleanup(func() { h.CloseOwned(ctx30(t), "run-identity") })
	_, err = h.Navigate(ctx30(t), id, fx.Loopback("/echo-headers"))
	ok(t, err)
	var headers map[string]string
	var body string
	raw, err := h.Evaluate(ctx30(t), id, `document.body.innerText`)
	ok(t, err)
	_ = json.Unmarshal(raw, &body)
	if err := json.Unmarshal([]byte(body), &headers); err != nil {
		t.Fatalf("echo body %q: %v", body, err)
	}
	ua := eval[string](t, h, id, `navigator.userAgent`)
	if headers["User-Agent"] != ua {
		t.Fatalf("header user agent %q differs from navigator.userAgent %q", headers["User-Agent"], ua)
	}
	if strings.Contains(headers["User-Agent"], "Headless") || !strings.Contains(headers["Sec-Ch-Ua"], "Google Chrome") {
		t.Fatalf("headers %v", headers)
	}
	for _, k := range []string{"Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform"} {
		if headers[k] == "" {
			t.Errorf("client hint %s missing", k)
		}
	}
	t.Logf("User-Agent=%s Sec-CH-UA=%s", headers["User-Agent"], headers["Sec-Ch-Ua"])
}
