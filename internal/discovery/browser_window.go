package discovery

import (
	"context"
	"errors"
)

// Window opens tabs for a browser scan. By default they are scan-lane tabs in
// the hidden scan window. When the user asked to watch scans the window may
// open them in the apply window instead, but never for a guarded board.
// Whichever lane it picks, a Tab has no way to type or submit.
type Window interface {
	// OpenTab opens a tab for owner. host is the main host of the source being
	// read, which decides whether the user's "watch scans" setting applies.
	OpenTab(ctx context.Context, owner, host string) (Tab, error)
}

// Tab is one scan tab. It can navigate, run a read-only script and press the
// checkbox of a Cloudflare challenge; it has no way to type, press a key or
// upload a file, so a scan cannot do so either.
type Tab interface {
	// Goto loads a page and returns once its document is parsed.
	Goto(ctx context.Context, rawURL string) (Nav, error)
	// Eval runs a script in the page and decodes its JSON result into out
	// (nil to drop it).
	Eval(ctx context.Context, expr string, out any) error
	// ClearChallenge presses the "Verify you are human" checkbox of a
	// Cloudflare challenge on the page, and nothing else.
	ClearChallenge(ctx context.Context) (ChallengeClear, error)
	// PopupURLs lists the URLs the page tried to open in a new window.
	PopupURLs() []string
	Close()
}

// Nav is how a navigation ended.
type Nav struct {
	URL, Title string
	Status     int
	// Failed is the load error text when the page did not load.
	Failed string
}

// ChallengeClear is what an attempt at a Cloudflare checkbox did. Reason is
// no-challenge, solved, target-not-resolved or clicked-no-pass.
type ChallengeClear struct {
	Present, Solved bool
	Clicks          int
	Reason          string
}

// ErrOffline is a host with no network. With a sign-in wall it is the only
// failure that ends a whole browser scan.
var ErrOffline = errors.New("the host has no network")
