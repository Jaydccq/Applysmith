//go:build browser

package apply

import (
	"context"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/engine"
)

// The product's apply window holds a lease on the lane from the moment a run
// opens its tab until it closes or keeps it: a second run is told the lane is
// busy, and gets it once the first has let go.
func TestE2E_LaneWindow_LeasedAdmitsOneRunAtATime(t *testing.T) {
	inst, _ := sharedBrowser(t)
	w := LaneWindow{Lane: inst.Lane(browser.LaneApply), Leased: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first, err := w.OpenTab(ctx, "run-lease-1", engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.OpenTab(ctx, "run-lease-2", engine.Host{}); err == nil {
		t.Fatal("a second run took the apply lane while the first held it")
	} else if code, _ := browser.CodeOf(err); code != browser.CodeLaneBusy {
		t.Fatalf("code %q err %v, want lane-busy", code, err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := w.OpenTab(ctx, "run-lease-2", engine.Host{})
	if err != nil {
		t.Fatalf("the lane is free after the first run closed its tab: %v", err)
	}
	// Keeping the tab hands it to the user and lets go of the lane.
	if err := second.Keep(); err != nil {
		t.Fatal(err)
	}
	third, err := w.OpenTab(ctx, "run-lease-3", engine.Host{})
	if err != nil {
		t.Fatalf("the lane is free after a tab was kept: %v", err)
	}
	_ = third.Close(ctx)
	_ = inst.Lane(browser.LaneApply).CloseTab(ctx, second.ID())
}
