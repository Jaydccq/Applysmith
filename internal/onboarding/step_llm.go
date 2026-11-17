package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/vault"
)

// CLILimits is the statement shown before a command-line preset is saved.
const CLILimits = "A command-line preset supports plain completion only: the Ask Agent will run its deterministic router only, and embeddings are disabled (knowledge search falls back to lexical search)."

// setupPresetNames are the presets the wizard offers (llm.SetupPresets): the
// signed-in command-line tools when cli, else the one-key API presets.
func setupPresetNames(cli bool) []string {
	var out []string
	for _, p := range llm.SetupPresets() {
		if (p.Kind == llm.KindCLI) == cli {
			out = append(out, p.Name)
		}
	}
	return out
}

var (
	apiPresets = setupPresetNames(false)
	cliPresets = setupPresetNames(true)
)

var llmFields = []Field{
	{Name: "preset", Type: "string", Doc: "A one-key provider preset; its API key must already be stored (`applysmith llm keys set <name>`) or supplied by APPLYSMITH_SECRET_API_KEY_<NAME>. The chatgpt preset takes no key: it needs your ChatGPT plan signed in (`applysmith llm signin chatgpt`, or Sign in with ChatGPT on this step).", Enum: apiPresets},
	{Name: "cli", Type: "string", Doc: "A signed-in command-line tool instead of a key.", Enum: cliPresets},
	{Name: "offline", Type: "bool", Doc: "Offline simulation for a user with no key yet: every task is served by the fake provider and reports say so."},
	{Name: "fallback", Type: "string", Doc: "Optional second preset whose model becomes the first fallback."},
	{Name: "acknowledge_cli_limits", Type: "bool", Doc: "Required with cli: you have read that " + CLILimits},
}

type llmInput struct {
	Preset   string `json:"preset"`
	CLI      string `json:"cli"`
	Offline  bool   `json:"offline"`
	Fallback string `json:"fallback"`
	Ack      bool   `json:"acknowledge_cli_limits"`
}

func applyLLM(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in llmInput
	if err := decodeInput(raw, llmFields, &in); err != nil {
		return Applied{}, err
	}
	fe := FieldErrors{}
	chosen := 0
	for _, set := range []bool{in.Preset != "", in.CLI != "", in.Offline} {
		if set {
			chosen++
		}
	}
	if chosen != 1 {
		fe.add("preset", "choose exactly one of preset, cli or offline")
	}
	if in.Preset != "" && !inList(in.Preset, apiPresets) {
		fe.add("preset", "setup offers "+strings.Join(apiPresets, ", "))
	}
	if in.CLI != "" && !inList(in.CLI, cliPresets) {
		fe.add("cli", "must be one of "+strings.Join(cliPresets, ", "))
	}
	if in.CLI != "" && !in.Ack {
		fe.add("acknowledge_cli_limits", CLILimits+" Set it to true to continue.")
	}
	if in.Fallback != "" && in.Offline {
		fe.add("fallback", "offline simulation serves every task itself; a fallback would be dropped")
	}
	if all := append(slices.Clone(apiPresets), cliPresets...); in.Fallback != "" && !inList(in.Fallback, all) {
		fe.add("fallback", "setup offers "+strings.Join(all, ", "))
	}
	if len(fe) > 0 {
		return Applied{}, fe
	}
	if env.Router == nil || env.Store == nil {
		return Applied{}, &EnvError{Message: errNoDatabase.Error()}
	}
	if dry {
		return Applied{Step: "llm", DryRun: true, Detection: Evaluate(ctx, env, mustStep("llm"))}, nil
	}
	name := in.Preset
	switch {
	case in.Offline:
		if err := env.Router.SetOffline(ctx, true); err != nil {
			return Applied{}, &EnvError{Message: "offline mode could not be saved", Err: err}
		}
	default:
		if in.CLI != "" {
			name = in.CLI
		}
		if err := env.Router.ConfigurePreset(ctx, name, llm.ConfigureOptions{Fallback: in.Fallback}); err != nil {
			var se *llm.SetupError
			if errors.As(err, &se) {
				return Applied{}, &HumanError{Instruction: se.Message}
			}
			return Applied{}, err
		}
	}
	res, err := env.Router.Test(ctx, "", "")
	if err != nil {
		return Applied{}, &EnvError{Message: "the test call could not run", Err: err}
	}
	out := Applied{Step: "llm", Wrote: true, Detection: Evaluate(ctx, env, mustStep("llm")), Notes: []string{TestLine(res)}}
	if in.CLI != "" {
		out.Notes = append(out.Notes, CLILimits)
	}
	if in.Offline {
		out.Notes = append(out.Notes, "Offline simulation: no model is called, and reports made this way say so.")
	}
	return out, nil
}

// TestLine is one line describing a test result, without any text of the call.
func TestLine(r llm.TestResult) string {
	if r.OK() {
		return fmt.Sprintf("test call to %s/%s succeeded in %d ms", r.Provider, r.Model, r.LatencyMS)
	}
	target := r.Provider
	if r.Model != "" {
		target += "/" + r.Model
	}
	return fmt.Sprintf("test call to %s failed (%s): %s", target, r.ErrorClass, r.Fix)
}

func detectLLM(ctx context.Context, env *Env) Detection {
	if env.Router == nil || env.Store == nil {
		return missing("no provider is configured: choose one, or offline until you have a key")
	}
	s, err := llm.LoadSettings(ctx, env.Store)
	if err != nil {
		return verdict(reasonf(ReasonInvalid, "llm", "the provider settings cannot be read: "+err.Error()))
	}
	if !s.Offline && s.Preset == "" {
		return missing("no provider is configured: choose one, or offline until you have a key")
	}
	var rs []Reason
	var warnings []string
	// The wizard tests with its own button and takes the key in its own field.
	test, keyHow, choose := "run `applysmith llm test`", "pipe it to `applysmith llm keys set "+s.Preset+"`", "run `applysmith llm configure`"
	if env.InServer {
		test, keyHow, choose = "press Save and test", "paste it on this step", "choose a provider in the Language-model provider step"
	}
	if s.Offline {
		warnings = append(warnings, "Offline: no model is called, so evaluations and written answers are simulated, and reports say so. Real evaluation needs a model: "+choose+" when you have a key.")
	} else if p, ok := llm.PresetByName(s.Preset); ok && p.Kind == llm.KindChatGPT {
		if st, err := env.Router.ChatGPTStatus(ctx); err != nil || !st.PlanUsage {
			rs = append(rs, reasonf(ReasonBlank, "chatgpt", "not signed in to ChatGPT with plan usage: sign in under Settings, Providers or run `applysmith llm signin chatgpt`"))
		}
	} else if ok && p.Kind != llm.KindCLI && env.Vault != nil {
		if _, err := env.Vault.Test(llm.KeyName(p.Name)); err != nil {
			rs = append(rs, reasonf(ReasonBlank, "api-key-"+p.Name, "no API key is stored for "+p.Label+": create one at "+p.KeyURL+" and "+keyHow))
		}
	}
	ts, err := env.Router.TestStatus(ctx)
	switch {
	case err != nil:
		rs = append(rs, reasonf(ReasonInvalid, "llm", "the saved test result cannot be read: "+err.Error()))
	case ts.Result == nil:
		rs = append(rs, reasonf(ReasonBlank, "test", "no test call has run yet: "+test))
	case !ts.Result.OK():
		fix := ts.Result.Fix
		if p, ok := llm.PresetByName(ts.Result.Provider); env.InServer && ts.Result.ErrorClass == llm.ClassAuth && ok && p.Kind != llm.KindCLI && p.Kind != llm.KindChatGPT {
			fix = "the key was refused; paste a new one on this step and press Save and test"
		}
		rs = append(rs, reasonf(ReasonInvalid, "test", fmt.Sprintf("the latest test failed with %s: %s", ts.Result.ErrorClass, fix)))
	case ts.Stale:
		rs = append(rs, reasonf(ReasonInvalid, "test", "the provider configuration changed after the last test: "+test))
	}
	d := verdict(rs...)
	d.Warnings = warnings
	return d
}

// SecretPresence says whether a secret is stored and when it was saved. It
// never carries the value.
type SecretPresence struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	SavedAt string `json:"saved_at,omitempty"`
}

// LLMSecrets reports the key of the active API preset, by presence only.
func LLMSecrets(ctx context.Context, env *Env) []SecretPresence {
	if env.Store == nil || env.Vault == nil {
		return nil
	}
	s, err := llm.LoadSettings(ctx, env.Store)
	if err != nil || s.Offline {
		return nil
	}
	p, ok := llm.PresetByName(s.Preset)
	if !ok || p.Kind == llm.KindCLI || p.Kind == llm.KindChatGPT {
		return nil
	}
	name := llm.KeyName(p.Name)
	_, err = env.Vault.Test(name)
	at, _ := env.decision(ctx, secretKey(name))
	return []SecretPresence{{Name: name, Present: err == nil, SavedAt: at}}
}

// SaveProviderKey stores a provider key through the router (which validates
// it) and records when it was saved.
func SaveProviderKey(ctx context.Context, env *Env, provider, key string) error {
	if env.Router == nil || env.Store == nil {
		return &EnvError{Message: errNoDatabase.Error()}
	}
	if err := env.Router.SetKey(provider, key); err != nil {
		var ve *vault.Error
		if errors.As(err, &ve) {
			return &EnvError{Message: ve.Message}
		}
		return FieldErrors{"key": err.Error()}
	}
	return env.setDecision(ctx, secretKey(llm.KeyName(provider)), env.now().UTC().Format(time.RFC3339))
}
