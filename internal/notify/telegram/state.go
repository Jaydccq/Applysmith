package telegram

import (
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTS(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Pairing and token rules.
const (
	PairingTTL      = 10 * time.Minute
	MaxWrongCodes   = 5
	TokenTTL        = 20 * time.Minute
	pairingCodeSize = 5 // bytes, ten hexadecimal characters
)

// Errors of the durable state.
var (
	ErrPaired       = errors.New("telegram: a chat is already paired; unpair it first")
	ErrNotPaired    = errors.New("telegram: no chat is paired")
	ErrTokenUnknown = errors.New("telegram: that button is not valid")
	ErrTokenUsed    = errors.New("telegram: that action was already handled")
	ErrTokenExpired = errors.New("telegram: that action expired")
)

// State is the gateway's durable state in the workspace database.
type State struct {
	st  *store.Store
	now func() time.Time
}

// NewState returns the state store.
func NewState(st *store.Store, now func() time.Time) *State {
	if now == nil {
		now = time.Now
	}
	return &State{st: st, now: now}
}

// demo reports whether the workspace is a demo workspace.
func (s *State) demo() bool { return s != nil && s.st.Demo() }

// demoGuard refuses a Bot API request in a demo workspace and records it.
func (s *State) demoGuard(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.st.GuardDemo(ctx, gates.DemoTelegram, "api.telegram.org")
}

// Owner is the paired private chat and user.
type Owner struct {
	ChatID     int64
	UserID     int64
	Generation int64
	PairedAt   time.Time
}

// Owner returns the paired owner, if any.
func (s *State) Owner(ctx context.Context) (Owner, bool) {
	var o Owner
	var at string
	err := s.st.Read().QueryRowContext(ctx, `SELECT chat_id, user_id, generation, paired_at FROM telegram_owner WHERE id = 1`).Scan(&o.ChatID, &o.UserID, &o.Generation, &at)
	if err != nil {
		return Owner{}, false
	}
	o.PairedAt = parseTS(at)
	return o, true
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// PairingCode is a code just generated.
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// BeginPairing generates a one-time code and keeps only its hash. It fails
// while a chat is paired.
func (s *State) BeginPairing(ctx context.Context) (PairingCode, error) {
	var raw [pairingCodeSize]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return PairingCode{}, err
	}
	code := hex.EncodeToString(raw[:])
	expires := s.now().Add(PairingTTL)
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM telegram_owner`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrPaired
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_pairing (id, code_hash, expires_at, wrong, closed) VALUES (1, ?, ?, 0, 0)
			ON CONFLICT(id) DO UPDATE SET code_hash = excluded.code_hash, expires_at = excluded.expires_at, wrong = 0, closed = 0`,
			hashCode(code), ts(expires))
		return err
	})
	if err != nil {
		return PairingCode{}, err
	}
	return PairingCode{Code: code, ExpiresAt: expires}, nil
}

var codeShape = lazyregexp.New(`^[0-9a-f]{10}$`)

// ClaimResult says what a message did to the pairing.
type ClaimResult int

// Results of Claim.
const (
	// NotACode: the text is not shaped like a code, or no pairing is open.
	NotACode ClaimResult = iota
	// Claimed: the code was right and the chat is now the owner.
	Claimed
	// WrongCode: a code-shaped text that does not match; it counted.
	WrongCode
)

// Claim tries a message's text as a pairing code. Only a code-shaped text can
// count as an attempt, so ordinary chatter never closes the window.
func (s *State) Claim(ctx context.Context, chatID, userID int64, code string) (ClaimResult, error) {
	if !codeShape.MatchString(code) {
		return NotACode, nil
	}
	res := NotACode
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		var hash, exp string
		var wrong, closed int
		err := tx.QueryRowContext(ctx, `SELECT code_hash, expires_at, wrong, closed FROM telegram_pairing WHERE id = 1`).Scan(&hash, &exp, &wrong, &closed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if closed == 1 || !s.now().Before(parseTS(exp)) {
			return nil
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(hashCode(code))) == 1 {
			gen, err := nextGeneration(ctx, tx)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO telegram_owner (id, chat_id, user_id, generation, paired_at) VALUES (1, ?, ?, ?, ?)`,
				chatID, userID, gen, ts(s.now())); err != nil {
				return err
			}
			res = Claimed
			_, err = tx.ExecContext(ctx, `DELETE FROM telegram_pairing`)
			return err
		}
		wrong++
		res = WrongCode
		_, err = tx.ExecContext(ctx, `UPDATE telegram_pairing SET wrong = ?, closed = ? WHERE id = 1`, wrong, b2i(wrong >= MaxWrongCodes))
		return err
	})
	return res, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nextGeneration(ctx context.Context, tx *sql.Tx) (int64, error) {
	var cur string
	err := tx.QueryRowContext(ctx, `SELECT value FROM telegram_meta WHERE key = 'generation'`).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	n, _ := strconv.ParseInt(cur, 10, 64)
	n++
	_, err = tx.ExecContext(ctx, `INSERT INTO telegram_meta (key, value) VALUES ('generation', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(n, 10))
	return n, err
}

// Unpair removes the owner, any pairing code and every pending button token. The
// bot token is kept. The generation moves on so a button issued earlier can
// never match a later pairing.
func (s *State) Unpair(ctx context.Context) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := nextGeneration(ctx, tx); err != nil {
			return err
		}
		// The answers still owed and what sent messages belong to go too: an
		// unpaired chat is told nothing more, nor is a chat paired later.
		for _, q := range []string{`DELETE FROM telegram_owner`, `DELETE FROM telegram_pairing`, `DELETE FROM telegram_tokens`, `DELETE FROM telegram_chats`,
			`DELETE FROM telegram_turns`, `DELETE FROM telegram_anchors`, `DELETE FROM telegram_meta WHERE key LIKE 'pages:%'`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// Token is a single-use button token.
type Token struct {
	Action  string
	Payload string
	// Result is what the action came to, kept in place of the payload once the
	// token was used: the link of the run an evaluation started.
	Result string
}

// IssueToken creates a token bound to the chat and the current pairing.
func (s *State) IssueToken(ctx context.Context, chatID int64, action, payload string) (string, error) {
	o, ok := s.Owner(ctx)
	if !ok {
		return "", ErrNotPaired
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw[:])
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_tokens (token, chat_id, action, payload, generation, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			tok, chatID, action, payload, o.Generation, ts(s.now()))
		return err
	})
	return tok, err
}

// readToken reads a token and checks it. It must exist, belong to the chat and
// have been issued under the current pairing (ErrTokenUnknown), be unused
// (ErrTokenUsed, with the token and its result) and at most 20 minutes old
// (ErrTokenExpired).
func (s *State) readToken(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, token string, chatID int64) (Token, error) {
	var out Token
	var chat, gen int64
	var created string
	var used sql.NullString
	err := q.QueryRowContext(ctx, `SELECT chat_id, action, payload, generation, created_at, used_at FROM telegram_tokens WHERE token = ?`, token).
		Scan(&chat, &out.Action, &out.Payload, &gen, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenUnknown
	}
	if err != nil {
		return Token{}, err
	}
	o, paired := s.Owner(ctx)
	switch {
	case chat != chatID || !paired || gen != o.Generation:
		return Token{}, ErrTokenUnknown
	case used.Valid:
		out.Payload, out.Result = "", out.Payload
		return out, ErrTokenUsed
	case s.now().Sub(parseTS(created)) > TokenTTL:
		return Token{}, ErrTokenExpired
	}
	return out, nil
}

// Token reads a token without using it. A used token comes back with
// ErrTokenUsed and what its action came to.
func (s *State) Token(ctx context.Context, token string, chatID int64) (Token, error) {
	return s.readToken(ctx, s.st.Read(), token, chatID)
}

// UseToken consumes a token and keeps result, what its action came to. The
// token must pass the checks of Token.
func (s *State) UseToken(ctx context.Context, token string, chatID int64, result string) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := s.readToken(ctx, tx, token, chatID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE telegram_tokens SET used_at = ?, payload = ? WHERE token = ?`, ts(s.now()), result, token)
		return err
	})
}

// Audit records a handled update: its id, the action name and the chat id. The
// text of a message is never stored.
func (s *State) Audit(ctx context.Context, updateID int64, action string, chatID int64) {
	_ = s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_events (at, update_id, action, chat_id) VALUES (?, ?, ?, ?)`, ts(s.now()), updateID, action, chatID)
		return err
	})
}

// Unauthorized counts the attempts from anyone but the owner.
func (s *State) Unauthorized(ctx context.Context) int {
	var n int
	_ = s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM telegram_events WHERE action = 'unauthorized'`).Scan(&n)
	return n
}

// SetBot records the bot's public name.
func (s *State) SetBot(ctx context.Context, username string) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_bot (id, username, updated_at) VALUES (1, ?, ?)
			ON CONFLICT(id) DO UPDATE SET username = excluded.username, updated_at = excluded.updated_at`, username, ts(s.now()))
		return err
	})
}

// ClearBot forgets the bot's name.
func (s *State) ClearBot(ctx context.Context) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM telegram_bot`)
		return err
	})
}

// Bot returns the recorded username.
func (s *State) Bot(ctx context.Context) string {
	var u string
	_ = s.st.Read().QueryRowContext(ctx, `SELECT username FROM telegram_bot WHERE id = 1`).Scan(&u)
	return u
}

// Chat session of the rolling conversation.

// ChatSession returns the conversation id of the chat and when it was last used.
func (s *State) ChatSession(ctx context.Context, chatID int64) (string, time.Time) {
	var id, at string
	if err := s.st.Read().QueryRowContext(ctx, `SELECT session_id, last_at FROM telegram_chats WHERE chat_id = ?`, chatID).Scan(&id, &at); err != nil {
		return "", time.Time{}
	}
	return id, parseTS(at)
}

// SetChatSession records the conversation of the chat and touches it.
func (s *State) SetChatSession(ctx context.Context, chatID int64, sessionID string) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_chats (chat_id, session_id, last_at) VALUES (?, ?, ?)
			ON CONFLICT(chat_id) DO UPDATE SET session_id = excluded.session_id, last_at = excluded.last_at`, chatID, sessionID, ts(s.now()))
		return err
	})
}

// Retain deletes receipts, audit rows, used or old tokens, delivered turns and
// the sent pages of a digest that never finished after 14 days, and anchors
// after 30: a reply to an older message reaches the chat's own conversation
// with the message quoted.
func (s *State) Retain(ctx context.Context) error {
	cut := ts(s.now().Add(-14 * 24 * time.Hour))
	anchors := ts(s.now().Add(-30 * 24 * time.Hour))
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []struct{ sql, cut string }{
			{`DELETE FROM telegram_receipts WHERE updated_at < ?`, cut},
			{`DELETE FROM telegram_events WHERE at < ?`, cut},
			{`DELETE FROM telegram_tokens WHERE created_at < ?`, cut},
			{`DELETE FROM telegram_turns WHERE done_at < ?`, cut},
			{`DELETE FROM telegram_meta WHERE key LIKE 'pages:%' AND value < ?`, cut},
			{`DELETE FROM telegram_anchors WHERE created_at < ?`, anchors},
		} {
			if _, err := tx.ExecContext(ctx, q.sql, q.cut); err != nil {
				return err
			}
		}
		return nil
	})
}

func pagesKey(id int64) string { return "pages:" + strconv.FormatInt(id, 10) }

// PagesSent is how many pages of the announcement id are in the chat and the
// message of the last one; zero when none is on record.
func (s *State) PagesSent(ctx context.Context, id int64) (n int, last int64) {
	var v string
	if err := s.st.Read().QueryRowContext(ctx, `SELECT value FROM telegram_meta WHERE key = ?`, pagesKey(id)).Scan(&v); err != nil {
		return 0, 0
	}
	// The value starts with its time, which Retain compares.
	if f := strings.Split(v, "|"); len(f) == 3 {
		n, _ = strconv.Atoi(f[1])
		last, _ = strconv.ParseInt(f[2], 10, 64)
	}
	return n, last
}

// SetPagesSent records that the first n pages of the announcement id are in
// the chat, the last as the message last.
func (s *State) SetPagesSent(ctx context.Context, id int64, n int, last int64) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			pagesKey(id), ts(s.now())+"|"+strconv.Itoa(n)+"|"+strconv.FormatInt(last, 10))
		return err
	})
}

// ForgetPages drops the record of an announcement whose pages are all sent.
func (s *State) ForgetPages(ctx context.Context, id int64) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM telegram_meta WHERE key = ?`, pagesKey(id))
		return err
	})
}

// Turn is an Ask Agent turn a message of the chat started, kept until its
// answer is in the chat.
type Turn struct {
	ID      string
	Update  int64
	Chat    int64
	Session string
	// ReplyTo is the owner's message the answer replies to; zero for none.
	ReplyTo int64
	// Sent counts the answer's pages Telegram accepted.
	Sent int
}

const turnColumns = `turn_id, update_id, chat_id, session_id, reply_to, sent`

func scanTurn(row interface{ Scan(...any) error }) (Turn, error) {
	var t Turn
	err := row.Scan(&t.ID, &t.Update, &t.Chat, &t.Session, &t.ReplyTo, &t.Sent)
	return t, err
}

// AddTurn records a turn the chat started.
func (s *State) AddTurn(ctx context.Context, t Turn) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_turns (`+turnColumns+`, created_at) VALUES (?, ?, ?, ?, ?, 0, ?)`,
			t.ID, t.Update, t.Chat, t.Session, t.ReplyTo, ts(s.now()))
		return err
	})
}

// TurnOfUpdate is the turn an update started, if it started one.
func (s *State) TurnOfUpdate(ctx context.Context, updateID int64) (Turn, bool) {
	t, err := scanTurn(s.st.Read().QueryRowContext(ctx, `SELECT `+turnColumns+` FROM telegram_turns WHERE update_id = ?`, updateID))
	return t, err == nil
}

// OpenTurns lists the turns whose answer is not in the chat yet, oldest first.
func (s *State) OpenTurns(ctx context.Context) ([]Turn, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT `+turnColumns+` FROM telegram_turns WHERE done_at IS NULL ORDER BY created_at, turn_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TurnOf reads a recorded turn and whether its answer is done with.
func (s *State) TurnOf(ctx context.Context, turnID string) (t Turn, done, found bool) {
	var doneAt sql.NullString
	err := s.st.Read().QueryRowContext(ctx, `SELECT `+turnColumns+`, done_at FROM telegram_turns WHERE turn_id = ?`, turnID).
		Scan(&t.ID, &t.Update, &t.Chat, &t.Session, &t.ReplyTo, &t.Sent, &doneAt)
	return t, doneAt.Valid, err == nil
}

// RunningTurn is the chat's newest turn whose answer is not in the chat yet.
func (s *State) RunningTurn(ctx context.Context, chat int64) (Turn, bool) {
	t, err := scanTurn(s.st.Read().QueryRowContext(ctx, `SELECT `+turnColumns+` FROM telegram_turns WHERE chat_id = ? AND done_at IS NULL
		ORDER BY created_at DESC, turn_id DESC LIMIT 1`, chat))
	return t, err == nil
}

// TurnSent records that the first n pages of a turn's answer are in the chat.
func (s *State) TurnSent(ctx context.Context, turnID string, n int) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE telegram_turns SET sent = ? WHERE turn_id = ?`, n, turnID)
		return err
	})
}

// TurnDone records that a turn's answer is in the chat, or was given up.
func (s *State) TurnDone(ctx context.Context, turnID string) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE telegram_turns SET done_at = ? WHERE turn_id = ? AND done_at IS NULL`, ts(s.now()), turnID)
		return err
	})
}

// Anchor is what a message the bot sent belongs to.
type Anchor struct {
	// Session is the Ask Agent conversation a reply to the message continues.
	Session string
	// Thread is the Gmail thread the message is about.
	Thread string
}

// SetAnchor records what a sent message belongs to. A message that belongs to
// nothing is not recorded.
func (s *State) SetAnchor(ctx context.Context, chat, message int64, a Anchor) error {
	if a.Session == "" && a.Thread == "" {
		return nil
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO telegram_anchors (chat_id, message_id, session_id, thread, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(chat_id, message_id) DO UPDATE SET session_id = excluded.session_id, thread = excluded.thread`,
			chat, message, a.Session, a.Thread, ts(s.now()))
		return err
	})
}

// AnchorOf is what a message of the chat belongs to.
func (s *State) AnchorOf(ctx context.Context, chat, message int64) (Anchor, bool) {
	var a Anchor
	err := s.st.Read().QueryRowContext(ctx, `SELECT session_id, thread FROM telegram_anchors WHERE chat_id = ? AND message_id = ?`, chat, message).
		Scan(&a.Session, &a.Thread)
	if errors.Is(err, sql.ErrNoRows) {
		return Anchor{}, false
	}
	return a, err == nil
}
