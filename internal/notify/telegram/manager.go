package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// TokenSecret is the keychain name of the bot token.
const TokenSecret = "telegram-bot-token"

// Failure classes of token setup.
var (
	// ErrTokenShape: the text is not shaped like a bot token. The text is not
	// repeated.
	ErrTokenShape = errors.New("telegram: that is not a bot token; create one with BotFather and paste it whole")
	// ErrAuth: Telegram refused the token (failure class auth).
	ErrAuth = errors.New("telegram: Telegram refused the token (auth)")
	// ErrNoToken: no bot token is stored, so no chat can be paired yet.
	ErrNoToken = errors.New("telegram: store the bot token first")
)

// Manager sets up the bot: the token, the pairing and the status. The CLI and the
// dashboard use it; it needs no running gateway.
type Manager struct {
	State *State
	Vault *vault.Vault
	API   string
	HTTP  *http.Client
	// Changed tells a running gateway that the token or the pairing changed.
	Changed func()
}

// TokenSource reads the token from the keychain or its environment override.
func TokenSource(v *vault.Vault) func() (string, error) {
	return func() (string, error) { return v.Get(TokenSecret) }
}

func (m *Manager) changed() {
	if m.Changed != nil {
		m.Changed()
	}
}

// SetToken checks the token's shape, validates it with getMe and stores it in the
// keychain, then records the bot's username. A token that fails is rejected
// without being echoed and nothing is stored.
func (m *Manager) SetToken(ctx context.Context, token string) (username string, err error) {
	if !ValidTokenShape(token) {
		return "", ErrTokenShape
	}
	c := NewClient(m.API, nil, m.HTTP).WithToken(token)
	c.guard = m.State.demoGuard
	me, err := c.GetMe(ctx)
	var demo *store.DemoRefusal
	if errors.As(err, &demo) {
		return "", err
	}
	if err != nil {
		var api *APIError
		if errors.As(err, &api) && api.IsAuth() {
			return "", ErrAuth
		}
		// The client's errors never carry the token; say so plainly anyway.
		return "", fmt.Errorf("telegram: the token could not be checked: %s", vault.Redact(err.Error()))
	}
	if _, err := m.Vault.Set(TokenSecret, token); err != nil {
		return "", fmt.Errorf("telegram: the token could not be stored: %w", err)
	}
	if err := m.State.SetBot(ctx, me.Username); err != nil {
		return "", err
	}
	m.changed()
	return me.Username, nil
}

// ClearToken deletes the token and the recorded bot name; a running poller
// stops. The pairing stays, so storing a token again resumes.
func (m *Manager) ClearToken(ctx context.Context) error {
	if err := m.Vault.Delete(TokenSecret); err != nil && !vault.IsCode(err, vault.CodeNotFound) {
		return err
	}
	if err := m.State.ClearBot(ctx); err != nil {
		return err
	}
	m.changed()
	return nil
}

// BeginPairing creates a one-time code and names the bot it is sent to. It
// fails with ErrNoToken before a token is stored and with ErrPaired while a
// chat is paired. A bot whose name was never recorded (a token
// from the environment, or one stored before the name was) is asked for it, so
// the code can come with a link to the bot; a failed ask leaves the name empty.
func (m *Manager) BeginPairing(ctx context.Context) (PairingCode, string, error) {
	token, err := m.Vault.Get(TokenSecret)
	if vault.IsCode(err, vault.CodeNotFound) {
		return PairingCode{}, "", ErrNoToken
	}
	if err != nil {
		return PairingCode{}, "", err
	}
	code, err := m.State.BeginPairing(ctx)
	if err != nil {
		return code, "", err
	}
	bot := m.State.Bot(ctx)
	if bot == "" {
		c := NewClient(m.API, nil, m.HTTP).WithToken(token)
		c.guard = m.State.demoGuard
		if me, err := c.GetMe(ctx); err == nil && me.Username != "" && m.State.SetBot(ctx, me.Username) == nil {
			bot = me.Username
		}
	}
	return code, bot, nil
}

// StartLink opens bot in Telegram with code as its start parameter: pressing
// Start sends "/start <code>", which the gateway takes as the pairing code. It
// is empty while the bot's name is unknown.
func StartLink(bot, code string) string {
	if bot == "" {
		return ""
	}
	return "https://t.me/" + url.PathEscape(bot) + "?start=" + url.QueryEscape(code)
}

// Unpair removes the owner binding and every pending code and button. The bot
// token stays.
func (m *Manager) Unpair(ctx context.Context) error {
	if err := m.State.Unpair(ctx); err != nil {
		return err
	}
	m.changed()
	return nil
}
