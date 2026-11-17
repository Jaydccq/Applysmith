package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/onboarding"
)

const documentsUsage = `usage:
  applysmith documents toolchain check [--json]
  applysmith documents toolchain setup [--yes]
  applysmith documents toolchain smoke [--json]

check    read-only diagnosis; exit 0 when new PDFs can be generated, 1 otherwise
setup    shows the download and disk budget, then (with --yes) prepares the private
         Python venv from the locked requirements and runs the smoke; it never installs
         TeX or Poppler
smoke    compiles and verifies both synthetic templates and records the result

APPLYSMITH_DOCUMENT_TOOL_PATH replaces the executable search with a list of directories
`

func (e *env) documents(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return e.documentsList(ctx, args[1:])
		case "show":
			return e.documentsShow(ctx, args[1:])
		case "delete":
			return e.documentsDelete(ctx, args[1:])
		case "review":
			return e.documentsReview(ctx, args[1:])
		}
	}
	if len(args) < 2 || args[0] != "toolchain" {
		fmt.Fprint(e.stderr, documentsUsage+documentCommandsUsage)
		return 2
	}
	switch args[1] {
	case "check":
		return e.toolchainCheck(ctx, args[2:])
	case "setup":
		return e.toolchainSetup(ctx, args[2:])
	case "smoke":
		return e.toolchainSmoke(ctx, args[2:])
	}
	fmt.Fprint(e.stderr, documentsUsage)
	return 2
}

func (e *env) toolchainConfig() toolchain.Config {
	return onboarding.ToolchainConfig(&onboarding.Env{Getenv: e.getenv, ConfigDir: e.configDir})
}

func (e *env) toolchainCheck(ctx context.Context, args []string) int {
	fs := e.flags("documents toolchain check")
	ignoredWorkspace(fs)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rep, _ := toolchain.Probe(ctx, e.toolchainConfig())
	if *asJSON {
		e.printJSON(rep)
	} else {
		writeToolchainReport(e.stdout, rep)
	}
	if rep.State != toolchain.StateReady {
		return 1
	}
	return 0
}

func writeToolchainReport(w io.Writer, rep toolchain.Report) {
	fmt.Fprintf(w, "document toolchain: %s\n", rep.State)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, c := range rep.Components {
		detail := c.Version
		if c.Status != toolchain.StatusOK {
			detail = c.Status + ": " + c.Detail
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", c.Name, detail, c.Path)
	}
	tw.Flush()
	fmt.Fprintf(w, "smoke: %s\n", rep.Smoke)
	if rep.Fix != "" {
		fmt.Fprintf(w, "fix: %s\n", rep.Fix)
	}
}

func (e *env) toolchainSetup(ctx context.Context, args []string) int {
	fs := e.flags("documents toolchain setup")
	ignoredWorkspace(fs)
	yes := fs.Bool("yes", false, "accept the budget and run the setup")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	b := toolchain.SetupBudget()
	fmt.Fprintln(e.stdout, "setup downloads:")
	for _, d := range b.Downloads {
		fmt.Fprintf(e.stdout, "  %s\n", d)
	}
	fmt.Fprintf(e.stdout, "setup writes about %d MiB below %s (private venv, TeX cache, smoke record)\n", b.DiskBytes>>20, e.toolchainConfig().Root.Path())
	fmt.Fprintln(e.stdout, "you install these yourself; setup does not:")
	for _, x := range b.External {
		fmt.Fprintf(e.stdout, "  %s: %s (%s)\n", x.Name, x.Install, x.Size)
	}
	if !*yes {
		fmt.Fprintln(e.stderr, "applysmith documents toolchain setup: run again with --yes to accept the budget")
		return 3
	}
	cfg := e.toolchainConfig()
	if err := toolchain.Setup(ctx, cfg); err != nil {
		return e.fail("documents toolchain setup", err)
	}
	fmt.Fprintln(e.stdout, "the private venv is ready")
	return e.runSmoke(ctx, cfg, false)
}

func (e *env) toolchainSmoke(ctx context.Context, args []string) int {
	fs := e.flags("documents toolchain smoke")
	ignoredWorkspace(fs)
	asJSON := fs.Bool("json", false, "print the smoke record as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return e.runSmoke(ctx, e.toolchainConfig(), *asJSON)
}

func (e *env) runSmoke(ctx context.Context, cfg toolchain.Config, asJSON bool) int {
	tc, err := toolchain.OpenForSmoke(ctx, cfg)
	var unavailable *toolchain.UnavailableError
	if errors.As(err, &unavailable) {
		writeToolchainReport(e.stdout, unavailable.Report)
		fmt.Fprintf(e.stderr, "applysmith documents toolchain smoke: %v\n", err)
		return 3
	}
	if err != nil {
		return e.fail("documents toolchain smoke", err)
	}
	res, err := tc.Smoke(ctx)
	if asJSON {
		e.printJSON(res.Record)
	} else {
		for _, d := range res.Record.Documents {
			fmt.Fprintf(e.stdout, "  %-14s %-8s pages %d  verify %s  layout %s\n", d.Name, d.Engine, d.Pages, d.Verify, d.Layout)
		}
	}
	if err != nil {
		for _, d := range res.Record.Documents {
			for _, p := range d.Problems {
				fmt.Fprintf(e.stderr, "  %s: %s\n", d.Name, p)
			}
		}
		return e.fail("documents toolchain smoke", err)
	}
	fmt.Fprintf(e.stdout, "smoke passed: %d TeX files read, record written for this combination\n", res.Record.ClosureFiles)
	return 0
}
