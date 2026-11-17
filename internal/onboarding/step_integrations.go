package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"applysmith/internal/api"
	"applysmith/internal/gmail"
	"applysmith/internal/notify/telegram"
)

// The secrets these steps look for. Presence is all they read.
const (
	secretSigninDefault = "signin-default"
	secretCaptchaKey    = "twocaptcha-key"
)

// Keys of the workspace_meta decisions the integration steps keep.
const (
	extensionCheckinKey = api.ExtensionCheckinKey
	orientationScope    = "orientation-1"
)

// vaultHas reports whether a named secret is stored, without reading it.
func (e *Env) vaultHas(name string) bool {
	if e.Vault == nil {
		return false
	}
	return e.Vault.Has(name)
}

func humanOnly(instruction string) Writer {
	return func(_ context.Context, _ *Env, input json.RawMessage, _ bool) (Applied, error) {
		if err := decodeInput(input, nil, &struct{}{}); err != nil {
			return Applied{}, err
		}
		return Applied{}, &HumanError{Instruction: instruction}
	}
}

// gmailHealth reads the offline connection state; it makes no network call.
func gmailHealth(ctx context.Context, env *Env) (gmail.Health, error) {
	return gmail.Open(env.Store, env.Vault, gmail.Options{Now: env.now}).Service.Health(ctx)
}

func detectGmail(ctx context.Context, env *Env) Detection {
	if env.Vault == nil || env.Store == nil {
		return missing("the workspace database and keychain are not open; complete the workspace step first")
	}
	// The dashboard (Env.InServer) connects in Settings, Gmail; a terminal
	// reader gets the command.
	connect := "run `" + gmail.ConnectCommand + "` and paste an app password from your Google Account"
	if env.InServer {
		connect = "enter your Gmail address and an app password from your Google Account in Settings, Gmail and press Connect"
	}
	h, err := gmailHealth(ctx, env)
	switch {
	case err != nil:
		return missing("the Gmail state could not be read: " + err.Error())
	case !h.Connected:
		return missing("Gmail is not connected: " + connect)
	case h.Login == gmail.LoginRefused:
		return verdict(reasonf(ReasonInvalid, "password", "Gmail refuses the stored app password: create a new one and "+strings.TrimPrefix(connect, "run ")))
	case h.Login == gmail.LoginUnknown:
		return verdict(reasonf(ReasonInvalid, "password", "the keychain could not be read for the app password"))
	}
	return verdict()
}

func detectTelegram(ctx context.Context, env *Env) Detection {
	if env.Vault == nil || env.Store == nil {
		return missing("the workspace database and keychain are not open; complete the workspace step first")
	}
	// The dashboard (Env.InServer) does both in Settings, Telegram; a terminal
	// reader gets the commands.
	token, pair := "run `applysmith telegram set-token --stdin`", "run `applysmith telegram pair` and send the code"
	if env.InServer {
		token, pair = "save its token in Settings, Telegram", "press Get a pairing code in Settings, Telegram and send the code"
	}
	var rs []Reason
	if !env.vaultHas(telegram.TokenSecret) {
		rs = append(rs, reasonf(ReasonBlank, "bot-token", "no bot token is stored: create a bot with BotFather and "+token))
	}
	if _, ok := telegram.NewState(env.Store, env.now).Owner(ctx); !ok {
		rs = append(rs, reasonf(ReasonBlank, "pairing", "no chat is paired: "+pair+" to your bot in a private chat"))
	}
	if len(rs) == 2 {
		return Detection{Status: StepMissing, Reasons: rs}
	}
	return verdict(rs...)
}

// credentialDefaultField is the reason field of a missing shared default.
const credentialDefaultField = "default"

// detectCredentials reads whether runs have a way to sign in: the shared
// default sign-in, or Sign in with Google turned on.
func detectCredentials(ctx context.Context, env *Env) Detection {
	if env.Vault == nil || env.Store == nil {
		return missing("the workspace database and keychain are not open; complete the workspace step first")
	}
	var email string
	_ = env.Store.Read().QueryRowContext(ctx, `SELECT COALESCE(email, '') FROM credential_index WHERE name = ?`, secretSigninDefault).Scan(&email)
	if env.vaultHas(secretSigninDefault) && strings.TrimSpace(email) != "" || bankService(env).Settings(ctx).AllowGoogleSignIn {
		return verdict()
	}
	return Detection{Status: StepMissing, Reasons: []Reason{reasonf(ReasonBlank, credentialDefaultField, "no way to sign in to application sites: store a shared default sign-in (an email and a password), or turn on Sign in with Google, in Settings, Sign-in credentials")}}
}

func detectCaptcha(_ context.Context, env *Env) Detection {
	if env.Vault == nil {
		return missing("the keychain is not open; complete the workspace step first")
	}
	if !env.vaultHas(secretCaptchaKey) {
		return missing("no 2Captcha key is stored")
	}
	return verdict()
}

func detectExtension(ctx context.Context, env *Env) Detection {
	if _, ok := env.decision(ctx, extensionCheckinKey); !ok {
		return missing("the server has not received a check-in from the paired extension: load it in Chrome and enter the pairing code")
	}
	return verdict()
}

func scopeOrientation(context.Context, *Env) (string, error) { return orientationScope, nil }

// detectOrientation is open until the user acknowledges it; the acknowledgement
// is the only thing that closes it.
func detectOrientation(context.Context, *Env) Detection {
	return verdict(reasonf(ReasonReview, "orientation", "read the daily loop and the steps you skipped, then acknowledge"))
}

func applyOrientation(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	if err := decodeInput(raw, nil, &struct{}{}); err != nil {
		return Applied{}, err
	}
	return Applied{Step: "orientation", DryRun: dry, Detection: Evaluate(ctx, env, mustStep("orientation")),
		Notes: []string{"acknowledge with `applysmith setup ack orientation` or the button in the wizard"}}, nil
}

// SkippedSteps lists the optional steps that are skipped or incomplete, with
// what each costs; orientation shows them.
func SkippedSteps(ctx context.Context, env *Env) []string {
	var out []string
	for _, s := range Catalog() {
		if s.Required {
			continue
		}
		if st := Evaluate(ctx, env, s).Status; st != StepComplete {
			out = append(out, fmt.Sprintf("%s (%s): %s", s.ID, st, s.SkipCost))
		}
	}
	return out
}
