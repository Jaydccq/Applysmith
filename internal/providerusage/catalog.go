// Package providerusage owns optional subscription monitoring. It never executes
// a CLI or imports native OAuth credentials into Applysmith's vault.
package providerusage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"applysmith/internal/lazyregexp"
)

type Definition struct {
	ID, Name, Mode, Program, LoginCommand, ConsoleURL, Note string
	CookieSupported                                         bool
	authFile                                                string
}

var definitions = []Definition{
	{ID: "minimax-global", Name: "MiniMax (Global)", Mode: "subscription", ConsoleURL: "https://platform.minimax.io/subscribe/token-plan", CookieSupported: true, Note: "Token Plan key preferred; cookie is for the legacy console. Credentials are region-specific and separate from model API keys."},
	{ID: "minimax-cn", Name: "MiniMax (China)", Mode: "subscription", ConsoleURL: "https://platform.minimaxi.com/subscribe/coding-plan", CookieSupported: true, Note: "Token Plan key preferred; cookie is for the legacy console. Credentials are region-specific and separate from model API keys."},
	{ID: "glm-zai", Name: "GLM Coding Plan (Z.AI)", Mode: "subscription", ConsoleURL: "https://z.ai/manage-apikey/apikey-list", Note: "Save the Z.AI Coding Plan key; it is sent only to api.z.ai when you refresh."},
	{ID: "glm-cn", Name: "GLM Coding Plan (BigModel)", Mode: "subscription", ConsoleURL: "https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys", Note: "Save the BigModel Coding Plan key; it is sent only to open.bigmodel.cn when you refresh."},
	{ID: "opencode-go", Name: "OpenCode Go", Mode: "subscription", ConsoleURL: "https://opencode.ai/auth", CookieSupported: true, Note: "Save a Go monitoring key, or a legacy console cookie plus workspace ID. The key takes precedence. Model provider keys are configured separately. Native OpenCode credentials are not imported."},
	{ID: "opencode", Name: "OpenCode", Mode: "native", Program: "opencode", LoginCommand: "opencode auth login", ConsoleURL: "https://opencode.ai/docs/cli/#auth", authFile: ".local/share/opencode/auth.json"},
	{ID: "devin", Name: "Devin", Mode: "native", Program: "devin", LoginCommand: "devin auth login", ConsoleURL: "https://docs.devin.ai/work-with-devin/devin-cli"},
	{ID: "claude", Name: "Claude", Mode: "native", Program: "claude", LoginCommand: "claude auth login", ConsoleURL: "https://code.claude.com/docs/en/authentication", authFile: ".claude/.credentials.json"},
	{ID: "codex", Name: "Codex", Mode: "native", Program: "codex", LoginCommand: "codex login", ConsoleURL: "https://developers.openai.com/codex/auth", authFile: ".codex/auth.json"},
	{ID: "gemini", Name: "Gemini", Mode: "native", Program: "gemini", LoginCommand: "gemini", ConsoleURL: "https://geminicli.com/docs/get-started/authentication/", authFile: ".gemini/oauth_creds.json"},
	{ID: "antigravity", Name: "Antigravity", Mode: "native", Program: "agy", LoginCommand: "agy", ConsoleURL: "https://antigravity.google/", Note: "Start agy and complete its browser sign-in. New sessions use that host's native account."},
	{ID: "grok", Name: "Grok (xAI)", Mode: "native", Program: "grok", LoginCommand: "grok login", ConsoleURL: "https://docs.x.ai/build/cli/reference", authFile: ".grok/auth.json"},
	{ID: "cursor", Name: "Cursor", Mode: "native", Program: "cursor", LoginCommand: "cursor", ConsoleURL: "https://cursor.com/dashboard", authFile: ".cursor/auth.json", Note: "Open Cursor and sign in there. Applysmith does not read Cursor's session database or change its login."},
}

func Definitions() []Definition { return append([]Definition(nil), definitions...) }
func Lookup(id string) (Definition, bool) {
	for _, d := range definitions {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}
func SecretName(id, kind string) string { return "provider-plan-" + id + "-" + kind }

var workspaceID = lazyregexp.New(`^(wrk|wk)_[A-Za-z0-9]{1,100}$`)

// ValidateCredential rejects header injection before any secret is stored.
func ValidateCredential(d Definition, kind, value string) error {
	if d.Mode != "subscription" {
		return errors.New("native accounts are managed in their own application")
	}
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("enter a single-line credential of 1 to 2048 bytes")
	}
	switch kind {
	case "key":
		if strings.ContainsAny(value, " \t") {
			return errors.New("API keys cannot contain whitespace")
		}
	case "cookie":
		if !d.CookieSupported {
			return errors.New("this provider accepts a plan API key only")
		}
		req := &httpCookieValidator{value: value}
		if !req.valid() {
			return errors.New("paste a Cookie header containing name=value pairs")
		}
	case "workspace":
		if d.ID != "opencode-go" || !workspaceID.MatchString(value) {
			return errors.New("workspace must be an OpenCode wrk_ or wk_ identifier")
		}
	default:
		return errors.New("credential kind must be key, cookie or workspace")
	}
	return nil
}

type Window struct {
	Name                                string
	Used, Limit, Remaining, UsedPercent *float64
	ResetsAt                            time.Time
}
type Usage struct {
	Status, Message string
	CheckedAt       time.Time
	Windows         []Window
}
type Cache struct {
	mu         sync.Mutex
	entries    map[string]Usage
	active     map[string]uint64
	generation map[string]uint64
}

func (c *Cache) Get(id string) (Usage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.entries[id]
	return u, ok
}
func (c *Cache) Clear(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
	if c.generation == nil {
		c.generation = map[string]uint64{}
	}
	c.generation[id]++
}
func (c *Cache) Begin(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.active[id]; busy {
		return false
	}
	if c.active == nil {
		c.active = map[string]uint64{}
	}
	c.active[id] = c.generation[id]
	return true
}
func (c *Cache) End(id string, u Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	generation, active := c.active[id]
	if active && generation == c.generation[id] {
		if c.entries == nil {
			c.entries = map[string]Usage{}
		}
		c.entries[id] = u
	}
	delete(c.active, id)
}
func Unknown(status, message string) Usage {
	return Usage{Status: status, Message: message, Windows: []Window{}}
}

type NativeState struct{ Installed, AuthFilePresent bool }

// DetectNative tests fixed program names and fixed file locations only. File
// existence is deliberately distinct from an authenticated, valid session.
func DetectNative(d Definition, home string, lookPath func(string) (string, error)) NativeState {
	_, err := lookPath(d.Program)
	s := NativeState{Installed: err == nil}
	if d.authFile != "" {
		fi, err := os.Stat(filepath.Join(home, filepath.FromSlash(d.authFile)))
		s.AuthFilePresent = err == nil && fi.Mode().IsRegular()
	}
	if !s.Installed && d.ID == "cursor" {
		fi, err := os.Stat("/Applications/Cursor.app")
		s.Installed = err == nil && fi.IsDir()
	}
	return s
}
func Native(d Definition) NativeState {
	home, _ := os.UserHomeDir()
	return DetectNative(d, home, exec.LookPath)
}
func Now() time.Time { return time.Now().UTC().Truncate(time.Second) }
