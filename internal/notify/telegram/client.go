// Package telegram is the Telegram gateway: the Bot API client, the pairing
// of one owner chat, the single long-polling poller with its lease, durable
// update receipts, the commands and buttons, and the outbound queue that keeps
// the gateway inside Telegram's rate limits. The bot token lives in the
// keychain and is read only to make a request; no error, log line or row holds
// it.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/vault"
)

// DefaultAPI is the Bot API host.
const DefaultAPI = "https://api.telegram.org"

// Timeouts of the client. Every call ends at a deadline of its own, so a host
// that stops answering costs one call and not the queue behind it.
const (
	CallTimeout     = 10 * time.Second
	LongPollSeconds = 25
	// LongPollTimeout is the long poll's deadline: the wait it asks Telegram
	// for and the time one call may take on top.
	LongPollTimeout = LongPollSeconds*time.Second + CallTimeout
)

var tokenShape = lazyregexp.New(`^\d{5,15}:[A-Za-z0-9_-]{30,}$`)

// ValidTokenShape reports whether s looks like a bot token.
func ValidTokenShape(s string) bool { return tokenShape.MatchString(s) }

// User is a Telegram user.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is an incoming or sent message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	// ReplyTo is the message this one replies to, as Telegram quotes it.
	ReplyTo *Message `json:"reply_to_message"`
}

// Callback is a tap on an inline button.
type Callback struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one item of getUpdates.
type Update struct {
	UpdateID int64     `json:"update_id"`
	Message  *Message  `json:"message"`
	Callback *Callback `json:"callback_query"`
}

// Button is one inline keyboard button.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Markup is an inline keyboard.
type Markup struct {
	Rows [][]Button `json:"inline_keyboard"`
}

// APIError is a refusal by the Bot API.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, vault.Redact(e.Description))
}

// IsAuth reports a refused token.
func (e *APIError) IsAuth() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusNotFound
}

// IsConflict reports another poller holding the updates.
func (e *APIError) IsConflict() bool { return e.Code == http.StatusConflict }

// Client calls the Bot API. The token is fetched for each request and never
// kept.
type Client struct {
	base  string
	token func() (string, error)
	http  *http.Client
	// timeout bounds one call and pollTimeout the long poll.
	timeout, pollTimeout time.Duration
	// guard refuses every request of a demo workspace; nil allows all.
	guard func(ctx context.Context) error
}

// NewClient returns a client; an empty base is the public API.
func NewClient(base string, token func() (string, error), hc *http.Client) *Client {
	if base == "" {
		base = DefaultAPI
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: base, token: token, http: hc, timeout: CallTimeout, pollTimeout: LongPollTimeout}
}

// WithToken returns a client that uses this token, for validating one before it
// is stored.
func (c *Client) WithToken(tok string) *Client {
	cp := *c
	cp.token = func() (string, error) { return tok, nil }
	return &cp
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	return c.callWithin(ctx, c.timeout, method, params, out)
}

// callWithin makes one call that ends after d at the latest.
func (c *Client) callWithin(ctx context.Context, d time.Duration, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if c.guard != nil {
		if err := c.guard(ctx); err != nil {
			return err
		}
	}
	tok, err := c.token()
	if err != nil {
		return err
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+tok+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: the request could not be built", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The error of net/http carries the request URL, which holds the token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: %s", method, vault.Redact(err.Error()))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: the response could not be read", method)
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return &APIError{Method: method, Code: resp.StatusCode, Description: "the response was not JSON"}
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Method: method, Code: code, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// GetMe validates the token and returns the bot.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates from offset on.
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var ups []Update
	err := c.callWithin(ctx, c.pollTimeout, "getUpdates", map[string]any{"offset": offset, "timeout": LongPollSeconds, "allowed_updates": []string{"message", "callback_query"}}, &ups)
	return ups, err
}

// SendMessage sends text, in Telegram's HTML when parseMode is "HTML", as a
// reply to the message replyTo when it is not zero (sent all the same when
// that message is gone); markup may be nil.
func (c *Client) SendMessage(ctx context.Context, chat int64, text, parseMode string, replyTo int64, markup *Markup) (int64, error) {
	p := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
	if parseMode != "" {
		p["parse_mode"] = parseMode
	}
	if replyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	if markup != nil {
		p["reply_markup"] = markup
	}
	var m Message
	if err := c.call(ctx, "sendMessage", p, &m); err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

// EditMessageText replaces a message's text and, with a nil markup, removes its
// buttons.
func (c *Client) EditMessageText(ctx context.Context, chat, message int64, text string, markup *Markup) error {
	p := map[string]any{"chat_id": chat, "message_id": message, "text": text, "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	} else {
		p["reply_markup"] = Markup{Rows: [][]Button{}}
	}
	return c.call(ctx, "editMessageText", p, nil)
}

// Command is one entry of the command menu Telegram shows beside the message
// box.
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands replaces the bot's command menu.
func (c *Client) SetMyCommands(ctx context.Context, cmds []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

// AnswerCallbackQuery acknowledges a tap, with an optional toast.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", p, nil)
}

// SendChatAction shows the typing indicator.
func (c *Client) SendChatAction(ctx context.Context, chat int64) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"}, nil)
}

func formatID(chat, msg int64) string {
	return strconv.FormatInt(chat, 10) + ":" + strconv.FormatInt(msg, 10)
}

func parseID(ext string) (chat, msg int64, ok bool) {
	for i := 0; i < len(ext); i++ {
		if ext[i] == ':' {
			c, err1 := strconv.ParseInt(ext[:i], 10, 64)
			m, err2 := strconv.ParseInt(ext[i+1:], 10, 64)
			return c, m, err1 == nil && err2 == nil
		}
	}
	return 0, 0, false
}
