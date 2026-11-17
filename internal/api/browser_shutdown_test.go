package api

import (
	"context"
	"testing"
)

// The managed browser belongs to the server that opened it: a graceful stop
// closes it, so quitting the app leaves no Chrome behind (browser-lanes,
// "Graceful server stop closes the browser").
func TestShutdown_StopsTheManagedBrowserForced(t *testing.T) {
	f := &fakeControl{leased: true}
	h := controlHarness(t, f, nil)
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stops) != 1 || !f.stops[0] {
		t.Fatalf("stops %v, want one forced stop even while a lane is leased", f.stops)
	}
}
