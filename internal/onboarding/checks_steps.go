package onboarding

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/credentials"
	"applysmith/internal/gmail"
	"applysmith/internal/vault"
)

// stepChecks are the leaves of the steps that were added after the first
// doctor: the story, the résumés, the answer bank, the connections and the
// last two steps. Each reads the same detector or stored fact as its step.
func stepChecks() []Check {
	return []Check{
		stepLeaf("story.narrative", "story", "run `applysmith setup apply story --file <path|->`"),
		{ID: "story.voice", Run: checkVoice},
		stepLeaf("resumes.files", "resumes", "upload a PDF and run `applysmith setup apply resumes --file <path|->`"),
		stepLeaf("answer-bank.blocking", "answer-bank", "answer the listed entries with `applysmith setup apply answer-bank --file <path|->`"),
		{ID: "browser.apply-window", Required: true, Budget: browserBudget, Run: checkApplyWindow},
		{ID: "browser.fingerprint", Required: true, Budget: browserBudget, Run: checkFingerprint},
		{ID: "browser.signins", Run: checkSignins},
		{ID: "gmail.login", Run: checkGmailLogin},
		{ID: "gmail.fetch", Run: checkGmailFetch},
		{ID: "credentials.default", Run: checkCredentialsDefault},
		{ID: "credentials.vault", Run: checkCredentialVault},
		{ID: "captcha.key", Run: checkCaptchaKey},
		stepLeaf("discovery.sources", "discovery", "run `applysmith setup apply discovery --file <path|->`"),
		stepLeaf("extension.pairing", "extension", "export the extension with `applysmith extension export <dir>` and pair it"),
		stepLeaf("orientation.ack", "orientation", "run `applysmith setup ack orientation`"),
	}
}

func checkVoice(ctx context.Context, env *Env) Outcome {
	if _, skip := env.decision(ctx, skipKey(voiceSkipID)); skip {
		return Outcome{Status: Skipped, Summary: "skipped by the user; drafts are not checked against your own style"}
	}
	if p := voiceProblem(env); p != "" {
		return Outcome{Status: Fail, Summary: p, Fix: "run `applysmith setup apply story --file <path|->` with voice samples, or `applysmith setup skip story.voice`"}
	}
	return Outcome{Status: Pass, Summary: "voice samples are in place"}
}

// browserEvidence is what the browser windows said: a live check when the run
// allows one, otherwise the last stored result.
func browserEvidence(ctx context.Context, env *Env) (BrowserReport, string, bool) {
	if env.Live && env.liveBrowser != nil {
		if r, err := env.liveBrowser(); err == nil {
			return r, "just now", true
		}
	}
	r, ok := env.browserReport(ctx)
	return r, "checked " + r.CheckedAt, ok
}

// browserFix is read at a terminal (the dashboard links the step instead), and
// `setup apply` writes nothing while a server holds the workspace.
const browserFix = "run `applysmith setup apply browser`, which opens the apply window if it is not open and checks it; while the app or `applysmith serve` holds this workspace, press Check the apply window in its setup instead"

func checkApplyWindow(ctx context.Context, env *Env) Outcome {
	r, when, ok := browserEvidence(ctx, env)
	if !ok {
		return Outcome{Status: Fail, Summary: "the apply window has not been checked", Fix: browserFix}
	}
	for _, w := range r.Windows {
		if w.Lane != "apply" {
			continue
		}
		if !w.Attached {
			return Outcome{Status: Fail, Summary: "the apply window could not be attached: " + w.Error, Fix: browserFix}
		}
		return Outcome{Status: Pass, Summary: "the apply window is attached (" + when + ")"}
	}
	return Outcome{Status: Fail, Summary: "the check did not include the apply window", Fix: browserFix}
}

func checkFingerprint(ctx context.Context, env *Env) Outcome {
	r, when, ok := browserEvidence(ctx, env)
	if !ok {
		return Outcome{Status: Fail, Summary: "the fingerprint has not been checked", Fix: browserFix}
	}
	if p := browserProblems(r); len(p) > 0 || len(r.Windows) == 0 {
		return Outcome{Status: Fail, Summary: strings.Join(p, "; "), Fix: "update Chrome and restart the app; the windows must not advertise automation. " + browserFix}
	}
	return Outcome{Status: Pass, Summary: "navigator.webdriver is false in every window and no user agent is headless (" + when + ")"}
}

func checkSignins(ctx context.Context, env *Env) Outcome {
	line, any := SignInLine(ctx, env)
	switch {
	case line == "":
		return Outcome{Status: Warn, Summary: "no sign-in result is stored", Fix: "run `applysmith browser check`"}
	case !any:
		return Outcome{Status: Warn, Summary: line, Fix: "sign in to the job sites you use in the apply window (`applysmith browser open <site url>`), then run `applysmith browser check`; each is optional"}
	}
	return Outcome{Status: Pass, Summary: line}
}

func checkGmailLogin(ctx context.Context, env *Env) Outcome {
	if env.Vault == nil || env.Store == nil {
		return Outcome{Status: Fail, Summary: "the workspace database is not open", Fix: "complete the workspace step"}
	}
	h, err := gmailHealth(ctx, env)
	switch {
	case err != nil:
		return Outcome{Status: Fail, Summary: "the Gmail state could not be read: " + err.Error()}
	case !h.Connected:
		return Outcome{Status: Fail, Summary: "Gmail is not connected", Fix: gmailConnectFix}
	case h.Login == gmail.LoginRefused:
		return Outcome{Status: Fail, Summary: "Gmail refused the stored app password: " + h.LoginError, Fix: gmailConnectFix}
	case h.Login == gmail.LoginUnknown:
		return Outcome{Status: Fail, Summary: "the keychain could not be read for the app password", Fix: "unlock the login keychain and run `applysmith doctor --only gmail` again"}
	}
	return Outcome{Status: Pass, Summary: "an app password is stored and Gmail has not refused it"}
}

// gmailConnectFix is the fix that names the terminal's connect command.
const gmailConnectFix = "create an app password in your Google Account (Security, App passwords) and run `" + gmail.ConnectCommand + "`, or enter it in Settings, Gmail"

func checkGmailFetch(ctx context.Context, env *Env) Outcome {
	if !env.Live {
		return Outcome{Status: Skipped, Summary: "a live read of the mailbox, run only by `applysmith doctor --live`"}
	}
	if env.Store == nil || env.Vault == nil {
		return Outcome{Status: Fail, Summary: "the workspace database is not open", Fix: "complete the workspace step"}
	}
	fetch := env.GmailFetch
	if fetch == nil {
		fetch = gmail.Open(env.Store, env.Vault, gmail.Options{}).Service.Test
	}
	n, err := fetch(ctx)
	if err != nil {
		fix := "run `applysmith gmail test` again"
		if gmail.IsAuthFailure(err) {
			fix = gmailConnectFix
		}
		return Outcome{Status: Fail, Summary: "the test fetch failed: " + err.Error(), Fix: fix}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("the test fetch found %d messages in the last day", n)}
}

func checkCredentialsDefault(ctx context.Context, env *Env) Outcome {
	for _, r := range detectCredentials(ctx, env).Reasons {
		if r.Field == credentialDefaultField || r.Field == "" {
			return Outcome{Status: Fail, Summary: r.Text, Fix: "run `applysmith credentials set-default --email <address> --password-stdin`; the password is read from standard input or a hidden prompt and goes to the keychain only. Or turn on Sign in with Google in Settings, Sign-in credentials"}
		}
	}
	return Outcome{Status: Pass, Summary: "runs can sign in: a shared default sign-in is stored or Sign in with Google is on"}
}

func checkCaptchaKey(ctx context.Context, env *Env) Outcome {
	if d := detectCaptcha(ctx, env); d.Status != StepComplete {
		return Outcome{Status: Fail, Summary: d.Reasons[0].Text, Fix: captchaHuman}
	}
	return Outcome{Status: Pass, Summary: "a 2Captcha key is stored; its balance is read only inside an apply run"}
}

// checkCredentialVault reads the credential layer without writing anything: how
// many secrets the environment provides, index rows whose secret is gone (stale),
// and environment variable names that two stored secrets share. A secret that a
// feature needs is reported by that feature's own leaf (llm.provider,
// telegram.token, captcha.key); an absent optional secret is not a finding.
// No value is read into the report.
func checkCredentialVault(ctx context.Context, env *Env) Outcome {
	if env.Store == nil || env.Vault == nil {
		return Outcome{Status: Skipped, Summary: "the workspace database and keychain are not open"}
	}
	entries, err := credentials.New(env.Store, env.Vault, env.now).Entries(ctx)
	if err != nil {
		return Outcome{Status: Fail, Summary: "the credential index cannot be read", Fix: "run `applysmith doctor --only workspace.migrations`"}
	}
	var stale, names, trouble []string
	fromEnv := 0
	for _, e := range entries {
		if e.Indexed || e.Present {
			names = append(names, e.Name)
		}
		switch {
		case e.Source == vault.SourceEnv:
			fromEnv++
		case e.Problem != "":
			trouble = append(trouble, e.Name)
		case e.Indexed && e.HasSecret && !e.Present:
			stale = append(stale, e.Name)
		}
	}
	var collisions []string
	for variable, group := range vault.EnvCollisions(names) {
		collisions = append(collisions, variable+" (shared by "+strings.Join(group, ", ")+")")
	}
	sort.Strings(collisions)
	switch {
	case len(stale) > 0:
		return Outcome{Status: Warn, Summary: fmt.Sprintf("%d index entries claim a secret that is gone: %s", len(stale), strings.Join(stale, ", ")),
			Fix: "remove each with `applysmith secrets delete <name>` or store it again"}
	case len(collisions) > 0:
		return Outcome{Status: Warn, Summary: "two stored secrets map to one environment variable: " + strings.Join(collisions, "; "),
			Fix: "rename the site or tenant, or remove one of the entries"}
	case len(trouble) > 0:
		return Outcome{Status: Warn, Summary: "the keychain could not be read for " + strings.Join(trouble, ", "), Fix: "run `applysmith doctor --only runtime.keychain`"}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("secrets from the environment: %d; no stale entry, no variable collision", fromEnv)}
}
