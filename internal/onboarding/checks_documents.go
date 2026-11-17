package onboarding

import (
	"context"
	"strings"

	"applysmith/internal/documents/toolchain"
)

// ToolchainConfig locates the document toolchain for this environment.
func ToolchainConfig(env *Env) toolchain.Config {
	return toolchain.Config{
		Root:       toolchain.DefaultRoot(configDir(env)),
		SearchDirs: toolchain.SearchDirs(env.getenv),
	}
}

// checkDocumentToolchain is the rendering leaf of the résumé step. It reads
// only and is never required: a valid uploaded résumé finishes the step while
// new PDF generation is unavailable.
func checkDocumentToolchain(ctx context.Context, env *Env) Outcome {
	rep, _ := toolchain.Probe(ctx, ToolchainConfig(env))
	return documentToolchainOutcome(rep)
}

func documentToolchainOutcome(rep toolchain.Report) Outcome {
	switch rep.State {
	case toolchain.StateReady:
		return Outcome{Status: Pass, Summary: "both document templates compile and verify with the installed tools"}
	case toolchain.StateVerificationNeeded:
		return Outcome{Status: Warn, Summary: "the tools are installed but the synthetic templates have not been compiled for this combination (" + rep.Smoke + ")", Fix: rep.Fix}
	}
	return Outcome{Status: Warn,
		Summary: toolchain.CodeUnavailable + ": new PDF generation needs " + strings.Join(rep.Missing, ", ") + "; existing résumés and every other feature keep working",
		Fix:     rep.Fix + "; installed outside the app, not part of it: " + externalSizes()}
}

func externalSizes() string {
	var parts []string
	for _, x := range toolchain.SetupBudget().External {
		parts = append(parts, x.Name+" "+x.Size)
	}
	return strings.Join(parts, "; ")
}
