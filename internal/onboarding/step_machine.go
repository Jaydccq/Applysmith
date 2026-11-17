package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/browser"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const (
	gib           = 1 << 30
	diskFailBelow = 2 * gib
	diskWarnBelow = 5 * gib
)

// chromeProbe finds an acceptable Google Chrome.
func chromeProbe(ctx context.Context, env *Env) Outcome {
	find := env.Chrome
	if find == nil {
		find = func(ctx context.Context) (browser.Chrome, error) { return browser.DiscoverChrome(ctx, env.getenv) }
	}
	c, err := find(ctx)
	switch {
	case err == nil:
		return Outcome{Status: Pass, Summary: "Google Chrome " + c.Version}
	case errors.Is(err, &browser.Error{Code: browser.CodeChromeTooOld}):
		return Outcome{Status: Fail, Summary: err.Error(),
			Fix: "update Google Chrome from https://www.google.com/chrome/, then run `applysmith doctor --only prerequisites`"}
	case errors.Is(err, &browser.Error{Code: browser.CodeChromeMissing}):
		return Outcome{Status: Fail, Summary: err.Error(), Fix: "install Google Chrome from https://www.google.com/chrome/, then run `applysmith doctor --only prerequisites`"}
	}
	return Outcome{Status: Fail, Summary: err.Error(), Fix: "install Google Chrome, or point APPLYSMITH_CHROME_BIN at its executable"}
}

// diskProbe measures free space on the volume that holds the workspace, or the
// home volume before one exists.
func diskProbe(_ context.Context, env *Env) Outcome {
	path := env.HomeDir
	if env.Workspace != nil {
		path = env.Workspace.Root
	}
	for path != "" {
		if _, err := os.Stat(path); err == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	free := env.DiskFree
	if free == nil {
		free = freeBytes
	}
	n, err := free(path)
	if err != nil {
		return Outcome{Status: Warn, Summary: "free disk space could not be measured: " + err.Error()}
	}
	text := fmt.Sprintf("%.1f GiB free", float64(n)/gib)
	switch {
	case n < diskFailBelow:
		return Outcome{Status: Fail, Summary: text + ", at least 2 GiB are needed", Fix: "free disk space or choose a workspace on another volume"}
	case n < diskWarnBelow:
		return Outcome{Status: Warn, Summary: text + ", under 5 GiB leaves little room for browser profiles and reports", Fix: "free some disk space"}
	}
	return Outcome{Status: Pass, Summary: text}
}

func detectPrerequisites(ctx context.Context, env *Env) Detection {
	var reasons []Reason
	var warnings []string
	for _, p := range []struct {
		field string
		out   Outcome
	}{{"platform", checkPlatform(ctx, env)}, {"chrome", chromeProbe(ctx, env)}, {"disk", diskProbe(ctx, env)}} {
		switch p.out.Status {
		case Fail:
			reasons = append(reasons, reasonf(ReasonInvalid, p.field, p.out.Summary+" ("+p.out.Fix+")"))
		case Warn:
			warnings = append(warnings, p.field+": "+p.out.Summary)
		}
	}
	d := verdict(reasons...)
	d.Warnings = warnings
	return d
}

func applyPrerequisites(ctx context.Context, env *Env, input json.RawMessage, dry bool) (Applied, error) {
	if err := decodeInput(input, nil, &struct{}{}); err != nil {
		return Applied{}, err
	}
	return Applied{Step: "prerequisites", DryRun: dry, Detection: detectPrerequisites(ctx, env)}, nil
}

var workspaceFields = []Field{{Name: "path", Type: "string", Doc: "Workspace directory; default ~/Applysmith. Omit to keep the resolved one."}}

// WorkspaceDirs are the directories a complete workspace holds.
var workspaceDirs = []string{"resumes", "knowledge", "reports", "documents"}

func detectWorkspace(ctx context.Context, env *Env) Detection {
	if env.Workspace == nil {
		msg := "no workspace could be resolved"
		if env.ResolveErr != nil {
			msg = env.ResolveErr.Error()
		}
		return missing(msg)
	}
	ws := env.Workspace
	if info, err := os.Stat(ws.Root); err != nil || !info.IsDir() {
		return missing("workspace directory " + ws.Root + " does not exist")
	}
	var reasons []Reason
	if info, err := os.Stat(ws.StateDir()); err != nil || !info.IsDir() {
		reasons = append(reasons, reasonf(ReasonBlank, "state", "the state directory does not exist"))
	}
	for _, d := range workspaceDirs {
		if info, err := os.Stat(filepath.Join(ws.Root, d)); err != nil || !info.IsDir() {
			reasons = append(reasons, reasonf(ReasonBlank, d, "the directory "+d+"/ does not exist"))
		}
	}
	st, err := ws.Status()
	if err != nil {
		return verdict(append(reasons, reasonf(ReasonInvalid, "files", err.Error()))...)
	}
	for _, f := range st.Files {
		switch {
		case f.Status == workspace.StatusMissing && !f.Optional:
			reasons = append(reasons, reasonf(ReasonBlank, f.Name, "the file "+f.Name+" is missing"))
		case f.Status == workspace.StatusInvalid:
			msg := f.Name + " is invalid"
			if first, ok := f.Report.First(); ok {
				msg = first.Error()
			}
			reasons = append(reasons, reasonf(ReasonInvalid, f.Name, msg))
		}
	}
	ms, err := migrations(ctx, env)
	switch {
	case errors.Is(err, store.ErrSchemaTooNew):
		reasons = append(reasons, reasonf(ReasonInvalid, "database", "the database is newer than this binary"))
	case err != nil:
		reasons = append(reasons, reasonf(ReasonInvalid, "database", err.Error()))
	case !ms.Exists:
		reasons = append(reasons, reasonf(ReasonBlank, "database", "the database does not exist yet"))
	case ms.Pending():
		reasons = append(reasons, reasonf(ReasonBlank, "database", fmt.Sprintf("schema %d of %d applied", ms.Applied, ms.Supported)))
	}
	return verdict(reasons...)
}

func applyWorkspace(ctx context.Context, env *Env, input json.RawMessage, dry bool) (Applied, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeInput(input, workspaceFields, &in); err != nil {
		return Applied{}, err
	}
	ws := env.Workspace
	var notes []string
	restart := false
	if in.Path != "" {
		home := env.HomeDir
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		root, err := workspace.Resolve(workspace.ResolveOptions{Flag: in.Path, Getenv: func(string) string { return "" }, HomeDir: home, ConfigDir: configDir(env)})
		if err != nil {
			return Applied{}, FieldErrors{"path": err.Error()}
		}
		if ws == nil || filepath.Clean(root) != ws.Root {
			ws = workspace.Open(root)
			restart = true
			notes = append(notes, "the workspace is "+root+"; run `applysmith setup --workspace "+root+"` to continue there")
		}
	}
	if ws == nil {
		msg := "no workspace location could be resolved"
		if env.ResolveErr != nil {
			msg = env.ResolveErr.Error()
		}
		return Applied{}, FieldErrors{"path": msg}
	}
	if dry {
		return Applied{Step: "workspace", DryRun: true, Detection: detectWorkspace(ctx, env), Notes: append(notes, "would create "+ws.Root)}, nil
	}
	results, err := ws.Init()
	if err == nil {
		var created bool
		if created, err = answerbank.New(ws, nil).Seed(); err == nil {
			results = append(results, workspace.InitResult{Name: workspace.FileAnswerBank.Name(), Created: created})
		}
	}
	if err != nil {
		return Applied{}, &EnvError{Message: "cannot create the workspace", Err: err}
	}
	var untouched []string
	for _, r := range results {
		if !r.Created {
			untouched = append(untouched, r.Name)
		}
	}
	// A demo workspace is never recorded as the user's workspace: nothing of
	// the demo is written outside its own folder.
	// Another workspace the app already opens stays the default unless this
	// one was chosen by path: creating files in a workspace (the wizard of a
	// server started with --workspace, a test copy) is no choice of the default.
	if !ws.IsDemo() {
		recorded, _ := workspace.RecordedChoice(configDir(env))
		switch {
		case in.Path == "" && recorded != "" && filepath.Clean(recorded) != ws.Root && workspace.Open(recorded).Holds():
			notes = append(notes, "the app still opens "+recorded+" at launch; this workspace did not become the default")
		default:
			if err := workspace.RecordChoice(configDir(env), ws.Root); err != nil {
				return Applied{}, &EnvError{Message: "cannot record the workspace location", Err: err}
			}
		}
	}
	env.Workspace = ws
	if !env.InServer {
		notes = append(notes, serverNote(ws))
	}
	return Applied{Step: "workspace", Wrote: true, Detection: detectWorkspace(ctx, env), Notes: notes, Untouched: untouched, Restart: restart}, nil
}

// WizardAddress is where the setup wizard of the server that runs for ws can
// be reached, or "" when no healthy server holds the workspace. It carries no
// credential; `applysmith open` prints a sign-in link.
func WizardAddress(ws *workspace.Workspace) string {
	if info, err := api.ReadLockInfo(ws.StateDir()); err == nil && api.LoopbackOrigin(info.Origin) && serverAnswers(info) {
		return info.Origin + "/setup"
	}
	return ""
}

// serverNote says whether a server runs for the workspace and where its setup
// wizard is.
func serverNote(ws *workspace.Workspace) string {
	if addr := WizardAddress(ws); addr != "" {
		return "a server is running for this workspace; the setup wizard is at " + addr + " (`applysmith open --workspace " + ws.Root + "` prints a sign-in link)"
	}
	return "no server is running for this workspace; start one with `applysmith serve --workspace " + ws.Root + "`, which prints the dashboard address; the setup wizard is its Setup page"
}

func configDir(env *Env) string {
	if env.ConfigDir != "" {
		return env.ConfigDir
	}
	d, _ := os.UserConfigDir()
	return d
}
