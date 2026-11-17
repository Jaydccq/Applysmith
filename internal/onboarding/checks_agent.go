package onboarding

import (
	"context"
	"errors"

	"applysmith/internal/agent"
)

// checkPi compares the installed pi with the pinned release. Anything else
// than a match turns the Ask Agent's model loop off while the router keeps
// answering, so the check warns instead of failing.
func checkPi(ctx context.Context, env *Env) Outcome {
	sup := agent.NewSupervisor(agent.SupervisorConfig{PiPath: env.getenv("APPLYSMITH_PI_PATH")})
	version, err := sup.Check(ctx)
	if err == nil {
		return Outcome{Status: Pass, Summary: "pi " + version}
	}
	var unavailable *agent.UnavailableError
	if errors.As(err, &unavailable) {
		return Outcome{Status: Warn, Summary: unavailable.Message + "; the Ask Agent answers only what the router recognises", Fix: unavailable.Fix}
	}
	return Outcome{Status: Warn, Summary: err.Error(), Fix: agent.InstallHint()}
}
