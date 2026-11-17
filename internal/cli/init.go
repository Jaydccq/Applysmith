package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/answerbank"
	"applysmith/internal/onboarding"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// initWorkspace creates the workspace directories, seeds the user-owned files
// that do not exist yet and creates the database, which completes the
// workspace step. Existing files are reported, never touched.
func (e *env) initWorkspace(ctx context.Context, args []string) int {
	fs := e.flags("init")
	wsFlag := fs.String("workspace", "", "workspace directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "applysmith init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := e.newWorkspacePath(*wsFlag)
	if err != nil {
		return e.fail("init", err)
	}
	ws := workspace.Open(root)
	results, err := ws.Init()
	if err != nil {
		return e.fail("init", err)
	}
	created, err := answerbank.New(ws, nil).Seed()
	if err != nil {
		return e.fail("init", err)
	}
	results = append(results, workspace.InitResult{Name: workspace.FileAnswerBank.Name(), Created: created})
	// A server that holds the workspace has its database already.
	if _, err := os.Stat(filepath.Join(ws.StateDir(), store.DBFile)); errors.Is(err, os.ErrNotExist) {
		if code := e.withWrite(ctx, "init", root, func(*onboarding.Env) int { return 0 }); code != 0 {
			return code
		}
	}
	for _, r := range results {
		verb := "present"
		if r.Created {
			verb = "created"
		}
		fmt.Fprintf(e.stdout, "%s %s\n", verb, r.Name)
	}
	st, err := ws.Status()
	if err != nil {
		return e.fail("init", err)
	}
	var seeded, templates int
	for _, f := range st.Files {
		if !f.File.Seeded() {
			continue
		}
		seeded++
		if f.Status == workspace.StatusTemplate {
			templates++
		}
	}
	fmt.Fprintf(e.stdout, "workspace %s: %d of %d files are still templates; fill them in step by step with `applysmith setup`, or on the Setup page of `applysmith serve`\n", ws.Root, templates, seeded)
	return 0
}
