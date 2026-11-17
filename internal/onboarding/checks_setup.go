package onboarding

import (
	"context"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/llm"
)

const testStaleAfter = 7 * 24 * time.Hour

// stepLeaf makes a doctor leaf out of a step's detector, so a doctor check and
// the setup status can never disagree.
func stepLeaf(id, stepID, fixCmd string) Check {
	return Check{ID: id, Required: mustStep(stepID).Required, Run: func(ctx context.Context, env *Env) Outcome {
		st := mustStep(stepID)
		d := Evaluate(ctx, env, st)
		switch d.Status {
		case StepComplete:
			return Outcome{Status: Pass, Summary: stepID + " is complete"}
		case StepSkipped:
			return Outcome{Status: Skipped, Summary: "skipped by the user"}
		}
		return Outcome{Status: Fail, Summary: summarize(d), Fix: fixCmd + "; guide: " + st.Guide}
	}}
}

func checkChrome(ctx context.Context, env *Env) Outcome { return chromeProbe(ctx, env) }
func checkDisk(ctx context.Context, env *Env) Outcome   { return diskProbe(ctx, env) }

func checkLLMProvider(ctx context.Context, env *Env) Outcome {
	const fix = "run `applysmith llm configure --preset <name> --key-stdin` (or --cli codex|claude), or sign in with your ChatGPT plan: `applysmith llm signin chatgpt`, then `applysmith llm configure --preset chatgpt`"
	if env.Router == nil || env.Store == nil {
		return Outcome{Status: Fail, Summary: "no provider is configured", Fix: fix}
	}
	s, err := llm.LoadSettings(ctx, env.Store)
	if err != nil {
		return Outcome{Status: Fail, Summary: "the provider settings cannot be read: " + err.Error(), Fix: fix}
	}
	if s.Offline {
		return Outcome{Status: Pass, Summary: "offline simulation is selected; every task is served by the fake provider"}
	}
	usable := 0
	var problems []string
	for _, p := range s.Providers {
		switch {
		case p.Kind == llm.KindFake:
			usable++
		case p.Kind == llm.KindCLI:
			if _, err := llm.CheckCLI(ctx, p.Program); err != nil {
				problems = append(problems, err.Error())
			} else {
				usable++
			}
		case p.Kind == llm.KindChatGPT:
			if st, err := env.Router.ChatGPTStatus(ctx); err != nil || !st.PlanUsage {
				problems = append(problems, "provider "+p.ID+" is not signed in to ChatGPT with plan usage")
			} else {
				usable++
			}
		case env.Vault != nil:
			if _, err := env.Vault.Test(llm.KeyName(p.ID)); err != nil {
				problems = append(problems, "provider "+p.ID+" has no key")
			} else {
				usable++
			}
		}
	}
	if usable == 0 {
		msg := "no provider has a key or a signed-in program"
		if len(problems) > 0 {
			msg += ": " + strings.Join(problems, "; ")
		}
		return Outcome{Status: Fail, Summary: msg, Fix: fix}
	}
	table, err := env.Router.Show(ctx)
	if err != nil {
		return Outcome{Status: Fail, Summary: err.Error(), Fix: "run `applysmith llm show`"}
	}
	var unrouted []string
	for _, id := range llm.CoreLoopTasks {
		if reason, ok := table.Unrouted[id]; ok {
			unrouted = append(unrouted, id+" ("+reason+")")
		}
	}
	if len(unrouted) > 0 {
		return Outcome{Status: Fail, Summary: "core tasks without a route: " + strings.Join(unrouted, ", "), Fix: fix}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("%d provider(s) usable, every core task routed", usable)}
}

func checkLLMTestAge(ctx context.Context, env *Env) Outcome {
	const fix = "run `applysmith llm test`"
	if env.Router == nil {
		return Outcome{Status: Fail, Summary: "no test call has run", Fix: fix}
	}
	ts, err := env.Router.TestStatus(ctx)
	switch {
	case err != nil:
		return Outcome{Status: Fail, Summary: "the saved test result cannot be read: " + err.Error(), Fix: fix}
	case ts.Result == nil:
		return Outcome{Status: Fail, Summary: "no test call has run", Fix: fix}
	case !ts.Result.OK():
		return Outcome{Status: Fail, Summary: fmt.Sprintf("the latest test failed with %s", ts.Result.ErrorClass), Fix: ts.Result.Fix}
	case ts.Stale:
		return Outcome{Status: Warn, Summary: "the provider configuration changed after the last test", Fix: fix}
	case ts.Age > testStaleAfter:
		return Outcome{Status: Warn, Summary: fmt.Sprintf("the last successful test is %d days old", int(ts.Age.Hours()/24)), Fix: fix}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("last test of %s/%s succeeded %s ago", ts.Result.Provider, ts.Result.Model, ts.Age.Round(time.Minute))}
}
