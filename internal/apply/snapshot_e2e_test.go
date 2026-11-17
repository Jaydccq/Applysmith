//go:build browser

package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A controlled password box mirrors the typed text into its value attribute, so
// a saved copy of the page would hold the password in clear text.
func TestE2E_Snapshot_BlanksEveryPasswordValueAttribute(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Create account</title><form onsubmit="return false">
<label for="em">Email</label><input id="em" type="email" name="email" value="alex@example.com">
<label for="pw">Password</label><input id="pw" type="password" name="password" value="attribute-canary-1">
<label for="pw2">Confirm password</label><input id="pw2" type="password" name="confirm">
<label for="otp">Security code</label><input id="otp" type="text" name="otp" value="code-canary-2">
</form>`)
	// What a controlled input does after typing: the property and the attribute both hold the text.
	if err := tab.Eval(ctx, `(()=>{const b=document.getElementById('pw2');b.value='typed-canary-3';b.setAttribute('value','typed-canary-3');return true})()`, nil); err != nil {
		t.Fatal(err)
	}
	var live string
	if err := tab.Eval(ctx, `document.documentElement.outerHTML`, &live); err != nil || !strings.Contains(live, "attribute-canary-1") || !strings.Contains(live, "typed-canary-3") {
		t.Fatalf("the canaries are not on the page, so the check below proves nothing: %v", err)
	}
	dir := t.TempDir()
	g := newRig(t, func(d *Deps) { d.Page.SnapshotDir = dir })
	run := &Run{ID: "run_snap"}
	s := &session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: tab}
	s.snapshot(ctx, "stop")
	raw, err := os.ReadFile(filepath.Join(dir, "run_snap-stop.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, canary := range []string{"attribute-canary-1", "typed-canary-3", "code-canary-2"} {
		if strings.Contains(html, canary) {
			t.Errorf("the snapshot holds %q", canary)
		}
	}
	if !strings.Contains(html, `type="password"`) || !strings.Contains(html, "alex@example.com") {
		t.Errorf("the snapshot lost the page's structure or its ordinary fields:\n%s", html)
	}
	if fi, err := os.Stat(filepath.Join(dir, "run_snap-stop.html")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v %v", fi, err)
	}
}
