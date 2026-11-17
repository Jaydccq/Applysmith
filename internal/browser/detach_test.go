package browser

import (
	"context"
	"os"
	"testing"
	"time"
)

// Chrome is started in a session of its own, so an abrupt death of the server
// does not take it down and the next server can adopt it.
func TestBrowser_Launch_StartsDetachedFromTheServersProcessGroup(t *testing.T) {
	r := newAdoptRig(t) // its process was started by spawn, as the browser is
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	own, err := processGroup(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := processGroup(ctx, r.proc.PID)
	if err != nil {
		t.Fatal(err)
	}
	if theirs != r.proc.PID || theirs == own {
		t.Fatalf("the browser's group is %d (its pid %d); the server's is %d: it must lead a group of its own", theirs, r.proc.PID, own)
	}
}

func TestBrowser_PopupVerdict_NotWebURL(t *testing.T) {
	i, _ := bareInstance(t)
	tb := &tab{id: "p", lane: LaneApply, window: 7}
	for _, u := range []string{"file:///etc/hosts", "javascript:alert(1)"} {
		if got := i.popupVerdict(tb, u); got != "not-web-url" {
			t.Errorf("%s: verdict %q, want not-web-url", u, got)
		}
	}
	for n, u := range []string{"https://jobs.example.test/p", "about:blank"} {
		// One window each: the popup budget counts per window.
		if got := i.popupVerdict(&tab{id: "q", lane: LaneApply, window: int64(100 + n)}, u); got != "" {
			t.Errorf("%s: verdict %q, want allowed", u, got)
		}
	}
	// The cap and the budget keep their own reasons.
	old := applyTabCap
	applyTabCap = 0
	defer func() { applyTabCap = old }()
	if got := i.popupVerdict(&tab{id: "r", lane: LaneApply, window: 9}, "https://jobs.example.test/"); got != "lane-tab-limit" {
		t.Errorf("at the cap: %q", got)
	}
}
