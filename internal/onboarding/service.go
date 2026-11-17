package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// Service is the catalog behind the wizard's routes. It builds an Env over the
// open store, vault and router of the running server for each call, so the
// wizard, the command line and the doctor all read the same catalog and call
// the same detectors and writers.
type Service struct {
	// Backend is the keychain the doctor's keychain probe uses.
	Backend vault.Backend
	Getenv  func(string) string
	// BrowserCheck probes the managed browser's windows; nil leaves the check
	// to the command line.
	BrowserCheck func(ctx context.Context) (BrowserReport, error)
	// DemoDir is where "Try the demo" creates the demo workspace; empty is
	// ~/Applysmith-Demo. HomeDir stands in for the user's home (tests).
	DemoDir, HomeDir string
}

var _ api.SetupService = (*Service)(nil)

func (s *Service) env(h api.SetupHost) *Env {
	return &Env{Workspace: h.Workspace, Backend: s.Backend, Port: h.Port, Store: h.Store, Vault: h.Vault, Router: h.Router, Bank: h.Bank,
		Getenv: s.Getenv, BrowserCheck: s.BrowserCheck, InServer: true}
}

// wireStep is a step as its reader sees it: inServer is the dashboard
// (Env.InServer), which gets the instruction worded for it.
func wireStep(s Step, d Detection, inServer bool) wire.SetupStep {
	out := wire.SetupStep{N: s.N, ID: s.ID, Title: s.Title, Phase: s.Phase, Required: s.Required, Status: string(d.Status),
		Reasons: []wire.SetupReason{}, Warnings: append([]string{}, d.Warnings...), Purpose: s.Purpose, SkipCost: s.SkipCost, Storage: s.Storage,
		Guide: s.Guide, Commands: append([]string{}, s.Commands...), Fields: []wire.SetupField{}, Page: s.Page}
	for _, r := range d.Reasons {
		out.Reasons = append(out.Reasons, wire.SetupReason{Field: r.Field, Text: r.Text, Ackable: r.Kind == ReasonPersona || r.Kind == ReasonReview, Blank: r.Kind == ReasonBlank})
	}
	for _, f := range s.Schema {
		out.Fields = append(out.Fields, wire.SetupField{Name: f.Name, Type: f.Type, Required: f.Required, Doc: f.Doc, Enum: f.Enum})
	}
	if s.Human != "" && d.Status != StepComplete && d.Status != StepSkipped {
		out.NeedsHuman, out.Instruction = true, s.Human
		if inServer && s.DashboardHuman != "" {
			out.Instruction = s.DashboardHuman
		}
	}
	return out
}

// State is the whole catalog with every status recomputed from the stored data.
func (s *Service) State(ctx context.Context, h api.SetupHost) (wire.SetupState, error) {
	return State(ctx, s.env(h)), nil
}

// Apply runs a step's writer. The workspace step cannot move the workspace of
// a running server.
func (s *Service) Apply(ctx context.Context, h api.SetupHost, id string, input map[string]any, dry bool) (wire.SetupApplied, error) {
	if id == "workspace" && input["path"] != nil {
		return wire.SetupApplied{}, toAPIError(FieldErrors{"path": "this server already runs in a workspace; start the app with another workspace to use one"})
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return wire.SetupApplied{}, api.ErrSetupRejected("the input is not valid JSON")
	}
	env := s.env(h)
	res, err := Apply(ctx, env, id, raw, dry)
	if err != nil {
		return wire.SetupApplied{}, toAPIError(err)
	}
	return WireApplied(res, env.InServer), nil
}

// WireApplied is the answer of a step writer as the wizard (inServer) and the
// command line print it.
func WireApplied(res Applied, inServer bool) wire.SetupApplied {
	st, _ := StepByID(res.Step)
	out := wire.SetupApplied{Step: wireStep(st, res.Detection, inServer), DryRun: res.DryRun, Wrote: res.Wrote,
		Notes: append([]string{}, res.Notes...), Untouched: append([]string{}, res.Untouched...)}
	if res.Draft != nil {
		d := &wire.SetupDraft{Employment: []wire.CVJob{}, Education: []wire.CVStudy{}, Skills: append([]string{}, res.Draft.Skills...)}
		for _, j := range res.Draft.Employment {
			d.Employment = append(d.Employment, wire.CVJob{Employer: j.Employer, Title: j.Title, Start: j.Start, End: j.End})
		}
		for _, e := range res.Draft.Education {
			d.Education = append(d.Education, wire.CVStudy{School: e.School, Degree: e.Degree, Start: e.Start, End: e.End})
		}
		out.Draft = d
	}
	return out
}

// Decide records an acknowledgement or a skip decision.
func (s *Service) Decide(ctx context.Context, h api.SetupHost, id, kind string) (wire.SetupStep, error) {
	env := s.env(h)
	var err error
	switch kind {
	case "ack":
		err = Ack(ctx, env, id)
	case "skip":
		err = Skip(ctx, env, id)
	case "unskip":
		err = Unskip(ctx, env, id)
	default:
		err = &RejectedError{Message: "unknown decision " + kind}
	}
	if err != nil {
		return wire.SetupStep{}, toAPIError(err)
	}
	if id == voiceSkipID {
		id = "story"
	}
	st, _ := StepByID(id)
	return wireStep(st, Evaluate(ctx, env, st), env.InServer), nil
}

// IntakePreview is the change set of the selected documents against profile.yml.
func (s *Service) IntakePreview(ctx context.Context, h api.SetupHost, req wire.IntakeRequest) (wire.IntakePreview, error) {
	out, err := IntakePreview(ctx, s.env(h), req.Documents)
	return out, toAPIError(err)
}

// IntakeAccept applies the selected items against the preview's revision.
func (s *Service) IntakeAccept(ctx context.Context, h api.SetupHost, req wire.IntakeAcceptRequest) (wire.IntakeResult, error) {
	out, err := IntakeAccept(ctx, s.env(h), req)
	return out, toAPIError(err)
}

// Doctor runs every check offline, as the command line does without --live.
func (s *Service) Doctor(ctx context.Context, h api.SetupHost) (wire.DoctorReport, error) {
	rep, err := Run(ctx, Checks(), s.env(h), Options{})
	if err != nil {
		return wire.DoctorReport{}, toAPIError(err)
	}
	out := wire.DoctorReport{Platform: PlatformStatement, Results: make([]wire.DoctorResult, 0, len(rep.Results))}
	for _, r := range rep.Results {
		out.Results = append(out.Results, wire.DoctorResult{ID: r.ID, Status: string(r.Status), Required: r.Required, Summary: r.Summary, Fix: r.Fix, Step: r.Step})
	}
	return out, nil
}

// toAPIError maps the setup errors onto the closed set of API errors.
func toAPIError(err error) error {
	var fe FieldErrors
	var rej *RejectedError
	var stale *StaleError
	var human *HumanError
	var env *EnvError
	switch {
	case errors.As(err, &fe):
		return api.RejectFields(fe)
	case errors.As(err, &rej):
		return api.ErrSetupRejected(rej.Message)
	case errors.As(err, &stale):
		return api.ErrSetupStale(stale.Message)
	case errors.As(err, &human):
		return api.ErrNeedsPerson(human.Instruction)
	case errors.As(err, &env):
		return api.ErrSetupUnavailable(env.Message)
	case errors.Is(err, ErrUnknownCheck):
		return api.ErrSetupRejected(err.Error())
	}
	return err
}

// Demo creates or completes the demo workspace for the Setup page. The
// server's own workspace is never written unless it is the demo itself; the
// demo's database is opened under its own workspace lock, so a server that
// already runs the demo keeps it and the postings are left to that server's
// earlier init.
func (s *Service) Demo(ctx context.Context, h api.SetupHost) (wire.DemoCreated, error) {
	root, err := ResolveDemo(DemoOptions{Dir: s.DemoDir, HomeDir: s.HomeDir})
	if err != nil {
		return wire.DemoCreated{}, api.ErrSetupRejected(err.Error())
	}
	ws, res, err := PrepareDemo(root)
	if errors.Is(err, ErrNotDemoWorkspace) {
		return wire.DemoCreated{}, api.ErrSetupRejected(err.Error())
	}
	if err != nil {
		return wire.DemoCreated{}, toAPIError(&EnvError{Message: "the demo workspace could not be created", Err: err})
	}
	shown, arg := homeRelative(ws.Root, s.HomeDir)
	out := wire.DemoCreated{Workspace: shown, Created: append([]string{}, res.Created...), Notes: []string{},
		Start: "applysmith serve --workspace " + arg}
	st := h.Store
	if h.Workspace == nil || h.Workspace.Root != ws.Root {
		lock, err := api.AcquireLock(ws.StateDir())
		if err != nil {
			out.Notes = append(out.Notes, "a server already runs the demo workspace; open it there")
			return out, nil
		}
		defer lock.Release()
		id, err := ws.ID()
		if err != nil {
			return wire.DemoCreated{}, toAPIError(&EnvError{Message: "the demo workspace has no identifier", Err: err})
		}
		if st, err = store.OpenWorkspace(ctx, ws.Root, store.Options{WorkspaceID: id}); err != nil {
			return wire.DemoCreated{}, toAPIError(&EnvError{Message: "the demo database could not be opened", Err: err})
		}
		defer st.Close()
	}
	seeded, err := SeedDemo(ctx, ws, st, time.Now)
	if err != nil {
		return wire.DemoCreated{}, toAPIError(&EnvError{Message: "the demo workspace could not be seeded", Err: err})
	}
	out.Postings = seeded.Postings
	return out, nil
}

// shellQuote quotes a path for a POSIX shell when it needs it.
// homeRelative returns a folder as the person reads it and as a shell argument:
// under the home folder both start with ~/, which the shell expands, so the
// page matches the folder it names in its own text.
func homeRelative(p, home string) (shown, arg string) {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if rel, err := filepath.Rel(home, p); home != "" && err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + rel, "~/" + shellQuote(rel)
	}
	return p, shellQuote(p)
}

func shellQuote(p string) string {
	if strings.ContainsAny(p, " \t'\"\\$`!*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}
