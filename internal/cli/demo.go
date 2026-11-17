package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"applysmith/internal/demo"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
)

const demoUsage = `usage:
  applysmith demo init [--workspace <dir>]
  applysmith demo email <address> [--workspace <dir>]

init creates the demo workspace (default ~/Applysmith-Demo): a separate folder
seeded with the fictional example persona Alex Example and a few sample
postings. It never touches your own workspace and refuses a folder that holds
one.

email sets the persona's address in the demo workspace's profile, so a site's
emailed verification code reaches your own connected Gmail account. The address
must be a plus-address alias (you+tag@domain) of that account; nothing else is
accepted, and no other mail is ever read in the demo.
`

// demoCmd runs `applysmith demo init` and `applysmith demo email`.
func (e *env) demoCmd(ctx context.Context, args []string) int {
	if len(args) == 0 || (args[0] != "init" && args[0] != "email") {
		fmt.Fprint(e.stderr, demoUsage)
		return 2
	}
	if args[0] == "email" {
		return e.demoEmail(ctx, args[1:])
	}
	fs := e.flags("demo init")
	wsFlag := fs.String("workspace", "", "demo workspace directory (default ~/"+demo.DefaultDir+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "applysmith demo init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := onboarding.ResolveDemo(onboarding.DemoOptions{Dir: *wsFlag, HomeDir: e.homeDir})
	if err != nil {
		return e.fail("demo init", err)
	}
	ws, res, err := onboarding.PrepareDemo(root)
	if errors.Is(err, onboarding.ErrNotDemoWorkspace) {
		fmt.Fprintf(e.stderr, "applysmith demo init: %v\n", err)
		return 2
	}
	if err != nil {
		return e.fail("demo init", err)
	}
	fmt.Fprintf(e.stderr, "demo workspace %s: the fictional persona Alex Example. %s\n", root, demo.Never)
	e.demoNoted = true
	for _, f := range res.Created {
		fmt.Fprintf(e.stdout, "created %s\n", f)
	}
	_, st, release, err := e.openStore(ctx, ws.Root, accessExclusive)
	if err != nil {
		return e.fail("demo init", err)
	}
	seeded, err := onboarding.SeedDemo(ctx, ws, st, time.Now)
	release()
	if err != nil {
		return e.fail("demo init", err)
	}
	fmt.Fprintf(e.stdout, "added %d sample posting(s) to the inbox\n", seeded.Postings)
	fmt.Fprintf(e.stdout, "demo workspace %s is ready. Start it with\n\n    applysmith serve --workspace %s\n\nthen choose a model provider in Setup and follow %s. Your own workspace is unchanged;\n`applysmith serve` without --workspace starts it again.\n",
		ws.Root, ws.Root, demo.TutorialPath)
	return 0
}

// demoEmail runs `applysmith demo email <address>`: it sets candidate.email of the
// demo workspace's profile after checking that the address is a plus alias, and
// of the connected Gmail account when one is connected.
func (e *env) demoEmail(ctx context.Context, args []string) int {
	fs := e.flags("demo email")
	wsFlag := fs.String("workspace", "", "demo workspace directory (default ~/"+demo.DefaultDir+")")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprint(e.stderr, demoUsage)
		return 2
	}
	addr := pos[0]
	root, err := onboarding.ResolveDemo(onboarding.DemoOptions{Dir: *wsFlag, HomeDir: e.homeDir})
	if err != nil {
		return e.fail("demo email", err)
	}
	e.noteDemo(root)
	ws := workspace.Open(root)
	if !ws.IsDemo() {
		fmt.Fprintf(e.stderr, "applysmith demo email: %s is not a demo workspace; run `applysmith demo init` first\n", root)
		return 2
	}
	if !gates.IsPlusAlias(addr) {
		fmt.Fprintln(e.stderr, "applysmith demo email: the address must be a plus-address alias such as you+applysmithdemo@<your Gmail domain> of your connected Gmail account")
		return 2
	}
	_, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail("demo email", err)
	}
	account := gmail.NewRepo(st, nil).Mailbox(ctx)
	release()
	if account != "" && !gates.AliasOf(addr, account) {
		fmt.Fprintln(e.stderr, "applysmith demo email: the address is not an alias of the Gmail account connected to this workspace; nothing was changed")
		return 2
	}
	cur, err := ws.Read(workspace.FileProfile)
	if err != nil {
		return e.fail("demo email", err)
	}
	if _, err := ws.Edit(workspace.FileProfile, []workspace.Op{{Kind: workspace.OpSet, Path: "candidate.email", Value: strconv.Quote(addr)}}, cur.Revision); err != nil {
		return e.fail("demo email", err)
	}
	// The CV and the résumé PDF show the address too: a site that reads the
	// résumé must find the one the profile gives.
	prev, _ := workspace.ParseProfile(cur.Data)
	if err := onboarding.SyncDemoEmail(ws, prev.Candidate.Email, addr); err != nil {
		return e.fail("demo email", err)
	}
	fmt.Fprintln(e.stdout, "the demo persona's address is set")
	if account == "" {
		fmt.Fprintln(e.stdout, "Gmail is not connected to this workspace yet: a code is read only once `applysmith gmail connect --address <address> --password-stdin --workspace "+root+"` has connected the account this address is an alias of.")
	}
	return 0
}
