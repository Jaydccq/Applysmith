package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/cliinstall"
	"applysmith/internal/gates"
)

// cliInstallRoutes serve the command-line link in Settings. Reading it is
// standard; making or removing the link changes a file in the user's home
// directory, so those two accept the dashboard session on the loopback
// listener alone.
var cliInstallRoutes = []Route{
	route("GET", "/api/system/cli", reads("CLIInstall")),
	route("PUT", "/api/system/cli", func(r *Route) { reads("CLIInstall")(r); r.SessionOnly = true }),
	route("DELETE", "/api/system/cli", func(r *Route) { reads("CLIInstall")(r); r.SessionOnly = true }),
}

// cliTarget resolves the home directory and the running program. A program
// that cannot be resolved still gives a Target whose Available is false.
func (s *Server) cliTarget() cliinstall.Target {
	home, exe := s.cfg.Home, s.cfg.Executable
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if exe == "" {
		exe, _ = os.Executable()
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return cliinstall.Target{Home: home, Exe: exe}
}

func (s *Server) cliView(ctx context.Context, t cliinstall.Target) (wire.CLIInstall, error) {
	state, err := t.Inspect()
	if err != nil {
		return wire.CLIInstall{}, err
	}
	getenv := os.Getenv
	file, line := cliinstall.ProfileHint(getenv("SHELL"))
	return wire.CLIInstall{Available: t.Available(), State: string(state), Link: t.Link(), Target: t.Exe,
		OnPath: t.OnPath(ctx, getenv), ProfileFile: file, ProfileLine: line}, nil
}

func (s *Server) cliStatus(c *apikit.Call) (apikit.Reply, error) {
	v, err := s.cliView(c.R.Context(), s.cliTarget())
	return apikit.Reply{Data: v}, err
}

func (s *Server) cliInstall(c *apikit.Call) (apikit.Reply, error) {
	return s.cliChange(c, true)
}

func (s *Server) cliRemove(c *apikit.Call) (apikit.Reply, error) {
	return s.cliChange(c, false)
}

// cliChange makes or removes the link and audits what happened.
func (s *Server) cliChange(c *apikit.Call, install bool) (apikit.Reply, error) {
	t := s.cliTarget()
	var changed bool
	var err error
	if install {
		changed, err = t.Install()
	} else {
		changed, err = t.Remove()
	}
	switch {
	case errors.Is(err, cliinstall.ErrUnavailable):
		return apikit.Reply{}, apikit.NewErr(wire.CodeUnavailable, "the command link can be made only from the installed app")
	case errors.Is(err, cliinstall.ErrForeign):
		s.auditCLI(c, gates.CLIInstallRefused())
		return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s already exists and is not the applysmith link; it was left as it is", t.Link()), reasonCLIForeign)
	case err != nil:
		return apikit.Reply{}, err
	}
	if changed {
		s.auditCLI(c, gates.CLIInstallChange(install))
	}
	v, err := s.cliView(c.R.Context(), t)
	return apikit.Reply{Data: v}, err
}

// reasonCLIForeign tells the page that a different file holds the link's place.
const reasonCLIForeign = "cli_link_foreign"

func (s *Server) auditCLI(c *apikit.Call, d gates.Decision) {
	if err := s.recordGate(c.R.Context(), c.P, d); err != nil {
		s.log.Error("the command link change could not be audited", "error", err.Error())
	}
}

// recordGate appends a decision to the gate audit with the caller the
// principal implies.
func (s *Server) recordGate(ctx context.Context, p apikit.Principal, d gates.Decision) error {
	caller := gates.CallerCLI
	if p.Kind == apikit.PrincipalDashboard {
		caller = gates.CallerDashboard
	}
	return s.recordGateAs(ctx, caller, d)
}

// recordGateAs appends a decision to the gate audit for the caller given.
func (s *Server) recordGateAs(ctx context.Context, caller gates.Caller, d gates.Decision) error {
	st, err := s.db()
	if err != nil {
		return err
	}
	salt, err := agent.AuditSalt(s.cfg.Workspace.StateDir())
	if err != nil {
		return err
	}
	return apply.NewAudit(st, salt, s.now).Record(ctx, d, gates.AuditContext{Caller: caller})
}
