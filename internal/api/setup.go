package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/documentsapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const setupApplyBudget = 100 * time.Second

// SetupHost is what the setup service works on: this server's workspace,
// database, vault and model router.
type SetupHost struct {
	Workspace *workspace.Workspace
	Store     *store.Store
	Vault     *vault.Vault
	Router    *llm.Router
	Port      int
	// Bank is the server's answer bank, which keeps its parse of the file
	// until the file changes.
	Bank *answerbank.Service
}

// SetupService is the onboarding catalog behind the setup routes. The
// onboarding package implements it (it imports this one for the workspace
// lock), and the launcher injects it; the routes carry no step logic.
type SetupService interface {
	State(ctx context.Context, h SetupHost) (wire.SetupState, error)
	Apply(ctx context.Context, h SetupHost, id string, input map[string]any, dry bool) (wire.SetupApplied, error)
	// Decide records an "ack", "skip" or "unskip" decision and returns the
	// step with its new status.
	Decide(ctx context.Context, h SetupHost, id, kind string) (wire.SetupStep, error)
	Doctor(ctx context.Context, h SetupHost) (wire.DoctorReport, error)
	// IntakePreview compares the selected documents with the profile and writes
	// nothing; IntakeAccept applies the items the person selected, against the
	// profile revision the preview was built from.
	IntakePreview(ctx context.Context, h SetupHost, req wire.IntakeRequest) (wire.IntakePreview, error)
	IntakeAccept(ctx context.Context, h SetupHost, req wire.IntakeAcceptRequest) (wire.IntakeResult, error)
	// Demo creates or completes the demo workspace in its own folder; it never
	// writes into this server's workspace unless that is the demo itself.
	Demo(ctx context.Context, h SetupHost) (wire.DemoCreated, error)
}

func (s *Server) setupHost() (SetupHost, error) {
	d, err := s.dom()
	if err != nil {
		return SetupHost{}, err
	}
	if s.cfg.Setup == nil {
		return SetupHost{}, apikit.Unavailable("setup is not available in this build")
	}
	return SetupHost{Workspace: s.cfg.Workspace, Store: s.store.Load(), Vault: s.cfg.Vault, Router: d.router, Port: s.port, Bank: d.bank}, nil
}

func (s *Server) setupState(c *apikit.Call) (apikit.Reply, error) {
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.cfg.Setup.State(c.R.Context(), h)
	return apikit.Reply{Data: st}, err
}

func (s *Server) setupApply(c *apikit.Call) (apikit.Reply, error) {
	var req wire.SetupApplyRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	// The llm step makes a live test call that may take up to 90 seconds, which
	// is longer than a synchronous handler; the caller waits for the answer.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.R.Context()), setupApplyBudget)
	defer cancel()
	res, err := s.cfg.Setup.Apply(ctx, h, c.R.PathValue("id"), req.Input, req.DryRun)
	if err != nil {
		return apikit.Reply{}, err
	}
	if res.Wrote {
		s.notify("setup")
	}
	return apikit.Reply{Data: res}, nil
}

func (s *Server) setupDecide(kind string) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		h, err := s.setupHost()
		if err != nil {
			return apikit.Reply{}, err
		}
		st, err := s.cfg.Setup.Decide(c.R.Context(), h, c.R.PathValue("id"), kind)
		if err != nil {
			return apikit.Reply{}, err
		}
		s.notify("setup")
		return apikit.Reply{Data: st}, nil
	}
}

func (s *Server) setupDoctor(c *apikit.Call) (apikit.Reply, error) {
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	rep, err := s.cfg.Setup.Doctor(c.R.Context(), h)
	return apikit.Reply{Data: rep}, err
}

func (s *Server) setupIntakePreview(c *apikit.Call) (apikit.Reply, error) {
	var req wire.IntakeRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := s.cfg.Setup.IntakePreview(c.R.Context(), h, req)
	return apikit.Reply{Data: res}, err
}

func (s *Server) setupIntakeAccept(c *apikit.Call) (apikit.Reply, error) {
	var req wire.IntakeAcceptRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	h, err := s.setupHost()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := s.cfg.Setup.IntakeAccept(c.R.Context(), h, req)
	if err != nil {
		return apikit.Reply{}, err
	}
	if res.Wrote {
		s.notify("setup")
	}
	return apikit.Reply{Data: res}, nil
}

// toolchainConfig is the per-user document toolchain, or the tests' own; no
// request supplies a path.
func (s *Server) toolchainConfig() toolchain.Config {
	if s.cfg.Domain.DocumentToolchainConfig != nil {
		return *s.cfg.Domain.DocumentToolchainConfig
	}
	return toolchain.Config{Root: toolchain.DefaultRoot(""), SearchDirs: toolchain.SearchDirs(os.Getenv)}
}

// documentToolchain is the read-only rendering diagnosis: it spawns the tools'
// version probes and changes nothing.
func (s *Server) documentToolchain(c *apikit.Call) (apikit.Reply, error) {
	rep, _ := toolchain.Probe(c.R.Context(), s.toolchainConfig())
	return apikit.Reply{Data: toolchainView(rep)}, nil
}

// documentToolchainSmoke runs the render test of `applysmith documents toolchain
// smoke`: both synthetic templates compiled and verified with the installed
// tools, and the record written for this combination, so new PDFs can be made
// again after a tool or template changed. A missing component refuses it as a
// documents run is refused; it answers with the diagnosis as it now stands.
func (s *Server) documentToolchainSmoke(c *apikit.Call) (apikit.Reply, error) {
	ctx, cfg := c.R.Context(), s.toolchainConfig()
	tc, err := toolchain.OpenForSmoke(ctx, cfg)
	if err != nil {
		return apikit.Reply{}, documentsapi.DocumentError(err)
	}
	if res, err := tc.Smoke(ctx); errors.Is(err, toolchain.ErrSmokeFailed) {
		var problems []string
		for _, d := range res.Record.Documents {
			for _, p := range d.Problems {
				problems = append(problems, d.Name+": "+p)
			}
			if len(d.Problems) == 0 && (d.Verify != toolchain.Pass || d.Layout != toolchain.Pass) {
				problems = append(problems, d.Name+": verify "+d.Verify+", layout "+d.Layout)
			}
		}
		return apikit.Reply{}, apikit.Unavailable("the render test did not pass with the installed tools: " + truncate(strings.Join(problems, "; "), 600))
	} else if err != nil {
		return apikit.Reply{}, err
	}
	s.notify("setup")
	rep, _ := toolchain.Probe(ctx, cfg)
	return apikit.Reply{Data: toolchainView(rep)}, nil
}

func toolchainView(rep toolchain.Report) wire.DocumentToolchain {
	out := wire.DocumentToolchain{State: string(rep.State), Code: rep.Code, Missing: apikit.NonNil(rep.Missing), Smoke: rep.Smoke, Fix: rep.Fix,
		Components: make([]wire.ToolchainComponent, 0, len(rep.Components))}
	for _, comp := range rep.Components {
		out.Components = append(out.Components, wire.ToolchainComponent{Name: comp.Name, Status: comp.Status, Version: comp.Version, Detail: comp.Detail, Fix: comp.Fix})
	}
	b := toolchain.SetupBudget()
	out.Budget = wire.ToolchainBudget{Downloads: apikit.NonNil(b.Downloads), DiskBytes: wire.SafeInt(b.DiskBytes), External: make([]wire.ToolchainExternal, 0, len(b.External))}
	for _, x := range b.External {
		out.Budget.External = append(out.Budget.External, wire.ToolchainExternal{Name: x.Name, Install: x.Install, Size: x.Size})
	}
	return out
}

// RejectFields is the rejection of several input fields at once.
func RejectFields(fields map[string]string) *apikit.Error {
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, k+": "+v)
	}
	slices.Sort(parts)
	e := apikit.NewErr(wire.CodeValidationFailed, "input rejected: %s", strings.Join(parts, "; "))
	e.Details = &wire.ErrorDetails{Fields: fields}
	return e
}

// ErrNeedsPerson reports a step that only a person can finish.
func ErrNeedsPerson(instruction string) *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", instruction), "needs_person")
}

// ErrSetupUnavailable reports a failure of the machine, not of the input.
func ErrSetupUnavailable(msg string) *apikit.Error { return apikit.Unavailable(msg) }

// ErrSetupStale reports an edit built from an older revision of a file than the
// one on disk.
func ErrSetupStale(msg string) *apikit.Error {
	return apikit.NewErr(wire.CodeRevisionConflict, "%s", msg)
}

// ErrSetupRejected reports input a setup call refuses as a whole.
func ErrSetupRejected(msg string) *apikit.Error {
	return apikit.NewErr(wire.CodeValidationFailed, "%s", msg)
}

// ExtensionCheckinKey is the workspace_meta key that holds the time of the
// paired extension's latest check-in; the extension step is complete once it
// exists.
const ExtensionCheckinKey = "setup.extension-checkin"

// recordExtensionCheckin notes an authenticated check-in of the extension (the
// local token, not the dashboard session). The row is refreshed at most once
// an hour, and the first one tells the wizard so it updates by itself.
func (s *Server) recordExtensionCheckin(c *apikit.Call) {
	st := s.store.Load()
	if c.P.Kind != apikit.PrincipalLocalToken || st == nil {
		return
	}
	ctx := c.R.Context()
	var prev string
	err := st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, ExtensionCheckinKey).Scan(&prev)
	if t, perr := time.Parse(time.RFC3339, prev); err == nil && perr == nil && s.now().Sub(t) < time.Hour {
		return
	}
	werr := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, ExtensionCheckinKey, s.now().UTC().Format(time.RFC3339))
		return err
	})
	if werr != nil {
		s.log.Warn("extension check-in not recorded", "error", werr.Error())
		return
	}
	if err != nil {
		s.notify("setup")
	}
}

// setupCVExtract returns a reviewable editor value; no workspace or profile
// writer runs. The existing save/confirmation paths remain the only writers.
func (s *Server) setupCVExtract(c *apikit.Call) (apikit.Reply, error) {
	data, err := io.ReadAll(c.R.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "CV files are limited to 10 MiB")
		}
		return apikit.Reply{}, apikit.NewErr(wire.CodeBadRequest, "the selected CV could not be read")
	}
	text, err := toolchain.ExtractCVText(c.R.Context(), apikit.QueryOf(c).Str("name"), data)
	if err != nil {
		return apikit.Reply{}, apikit.InvalidField("file", "%s", err.Error())
	}
	return apikit.Reply{Data: wire.CVExtracted{Markdown: text}}, nil
}
