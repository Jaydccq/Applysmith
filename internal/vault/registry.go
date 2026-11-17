package vault

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Row is one kind of secret. Adding a secret kind means adding a row here and
// a row to the reference page.
type Row struct {
	Kind            string // stable identifier of the kind
	Pattern         *lazyregexp.Regexp
	NamePattern     string // human form, e.g. signin-site-<host>
	Description     string
	AccountTemplate string // keychain account, with the workspace namespace
	EnvPattern      string // environment override
	SetupStep       string // setup step id that sets it; empty when none does
	Required        bool
}

const (
	accountTemplate = "<workspaceId>:<name>"
	envPrefix       = "APPLYSMITH_SECRET_"
	envPattern      = envPrefix + "<NAME>"
	serviceName     = "applysmith"
)

// APIToken is the local API bearer token, created at first start and rotated
// by `applysmith token rotate`.
const APIToken = "api-token"

// CaptchaKey is the captcha solver's account key.
const CaptchaKey = "twocaptcha-key"

func row(kind, namePattern, re, description, step string, required bool) Row {
	return Row{
		Kind: kind, NamePattern: namePattern, Pattern: lazyregexp.New("^" + re + "$"),
		Description: description, AccountTemplate: accountTemplate, EnvPattern: envPattern,
		SetupStep: step, Required: required,
	}
}

var registry = []Row{
	row("provider_plan", "provider-plan-<provider>-<key|cookie|workspace>", `provider-plan-((minimax-global|minimax-cn)-(key|cookie)|(glm-zai|glm-cn)-key|opencode-go-(key|cookie|workspace))`, "Optional provider subscription credential or workspace identifier; separate from model API keys", "", false),
	row("signin_default", "signin-default", `signin-default`,
		"Shared default sign-in password for application sites", "credentials", false),
	row("signin_alternate", "signin-alternate-<id>", `signin-alternate-[0-9a-f]{12}`,
		"Alternate shared sign-in password; the id is random so the name says nothing about the password", "credentials", false),
	row("signin_site", "signin-site-<host>", `signin-site-[a-z0-9]+([.-][a-z0-9]+)*`,
		"Per-site override password; the host is lowercased", "credentials", false),
	row("tenant", "tenant-<ats>-<tenant>", `tenant-[a-z0-9]+-[a-z0-9]+([._-][a-z0-9]+)*`,
		"Per-tenant password for a named applicant-tracking system", "credentials", false),
	row("api_key", "api-key-<provider>", `api-key-[a-z0-9][a-z0-9_-]*`,
		"API key for a language-model, discovery, search or hiring-signal provider", "llm", false),
	row("token", "chatgpt-refresh", `chatgpt-refresh`, "Sign in with ChatGPT refresh token; rotates on every refresh and belongs to the server", "", false),
	row("token", "chatgpt-access", `chatgpt-access`, "Sign in with ChatGPT access token, valid for one hour", "", false),
	row("token", "gmail-app-password", `gmail-app-password`, "Google app password that signs in to Gmail over IMAP", "gmail", false),
	row("token", "telegram-bot-token", `telegram-bot-token`, "Telegram bot token", "telegram", false),
	row("token", "twocaptcha-key", `twocaptcha-key`, "Captcha solver key", "captcha", false),
	row("token", "github-token", `github-token`, "GitHub token for knowledge sources", "", false),
	row("token", "cloudflared-tunnel-token", `cloudflared-tunnel-token`, "Named-tunnel token for remote access", "", false),
	row("token", APIToken, `api-token`, "Local API bearer token", "", true),
}

// Registry returns every registered kind, in documentation order.
func Registry() []Row { return append([]Row(nil), registry...) }

var nameShape = lazyregexp.New(`^[a-z0-9][a-z0-9._-]{0,199}$`)

// Lookup validates a secret name and returns its registry row. A bad name is
// rejected before any keychain access.
func Lookup(name string) (Row, error) {
	if !nameShape.MatchString(name) {
		return Row{}, &Error{Code: CodeNameInvalid, Name: name,
			Message: "a secret name is 1 to 200 characters of lowercase letters, digits, dot, underscore and hyphen, starting with a letter or digit"}
	}
	for _, r := range registry {
		if r.Pattern.MatchString(name) {
			return r, nil
		}
	}
	// A new secret is a row in registry and in docs/setup/reference.md.
	return Row{}, &Error{Code: CodeNameInvalid, Name: name, Message: "not a name in the secret registry"}
}

// EnvName is the environment variable that supplies the secret: the name
// uppercased with every non-alphanumeric character replaced by an underscore.
func EnvName(name string) string {
	var b strings.Builder
	b.WriteString(envPrefix)
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// EnvCollisions groups names that map to the same environment variable; the
// doctor reports each group of two or more.
func EnvCollisions(names []string) map[string][]string {
	byVar := map[string][]string{}
	for _, n := range names {
		v := EnvName(n)
		byVar[v] = append(byVar[v], n)
	}
	for v, ns := range byVar {
		if len(ns) < 2 {
			delete(byVar, v)
		}
	}
	return byVar
}

func unavailableMessage(name string) string {
	return fmt.Sprintf("No OS keychain is available on this platform. Set the secret through the environment variable %s (for example APPLYSMITH_SECRET_TWOCAPTCHA_KEY) and restart the server.", EnvName(name))
}

const deniedMessage = "The OS keychain refused access. Unlock the keychain or allow access when asked, then retry; see the setup guide's note on rebuilt unsigned binaries."
