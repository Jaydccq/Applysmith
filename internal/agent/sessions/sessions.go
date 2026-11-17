// Package sessions stores the Ask Agent's conversations: sessions, their
// ordered messages and tool calls, one row per turn, the full-text index over
// message text, compaction checkpoints, the agent's settings, the idempotency
// claims of run-starting tools and the guard-chain audit. It decides nothing; the
// agent package owns the rules.
package sessions

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/store"
)

// Channel is where a conversation came from.
type Channel string

const (
	Dashboard Channel = "dashboard"
	Telegram  Channel = "telegram"
	Scheduled Channel = "scheduled"
	Digest    Channel = "digest"
)

// Valid reports whether c is one of the four channels.
func (c Channel) Valid() bool {
	switch c {
	case Dashboard, Telegram, Scheduled, Digest:
		return true
	}
	return false
}

// Role says what a message row is.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleSteer      Role = "steer"
	RoleToolCall   Role = "tool_call"
	RoleToolResult Role = "tool_result"
	RoleNote       Role = "note"
)

// Limits of the record.
const (
	// MaxTitle bounds a conversation title in characters.
	MaxTitle = 120
	// MaxResultBytes is how much of a tool result the record keeps.
	MaxResultBytes = 200 << 10
	// SnippetLimit is the most snippets a recall returns.
	SnippetLimit   = 5
	claimRetention = 30 * 24 * time.Hour
	timeLayout     = "2006-01-02T15:04:05.000Z"
)

// Session is one conversation.
type Session struct {
	ID              string
	Title           string
	Channel         Channel
	ParentID        string
	ParentMessageID int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Message is one row of the record.
type Message struct {
	ID         int64
	SessionID  string
	TurnID     string
	Role       Role
	Body       string
	ToolName   string
	ToolCallID string
	OK         bool
	SystemMade bool
	// OutsideContent marks text written by third parties. A later prompt that
	// contains the row starts untrusted.
	OutsideContent bool
	// Envelope is what a final answer carried besides its text; the zero value
	// is none.
	Envelope  Envelope
	CreatedAt time.Time
}

// Citation names the source of a claim and quotes the part that supports it.
type Citation struct {
	Label   string `json:"label"`
	Snippet string `json:"snippet,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Envelope is the part of an answer envelope the record keeps with the message.
// On a tool result, RunID names the run the call started; on a note, the run
// it tells of.
type Envelope struct {
	Citations   []Citation `json:"citations,omitempty"`
	Limitations []string   `json:"limitations,omitempty"`
	NextAction  string     `json:"nextAction,omitempty"`
	RunID       string     `json:"runId,omitempty"`
}

func (e Envelope) empty() bool {
	return len(e.Citations) == 0 && len(e.Limitations) == 0 && e.NextAction == "" && e.RunID == ""
}

func (e Envelope) encode() string {
	if e.empty() {
		return ""
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// decodeEnvelope reads a stored envelope; text that does not parse is none.
func decodeEnvelope(text string) Envelope {
	var e Envelope
	if text != "" {
		_ = json.Unmarshal([]byte(text), &e)
	}
	return e
}

// Turn is the record of one turn.
type Turn struct {
	ID        string
	SessionID string
	Channel   Channel
	Origin    string
	RuleID    string
	// Tier is the model tier the intent classifier chose, empty when it did not run.
	Tier       string
	Status     string
	Reason     string
	Detail     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Turn statuses and origins.
const (
	TurnRunning = "running"
	TurnFinal   = "final"
	TurnError   = "error"

	OriginModel  = "model"
	OriginRouter = "router"
	OriginButton = "button"
)

// Snippet is one recall hit.
type Snippet struct {
	SessionID string
	MessageID int64
	Channel   Channel
	At        time.Time
	Text      string
}

// Store is the conversation record over the workspace database.
type Store struct {
	db  *store.Store
	now func() time.Time
}

// New returns a Store; a nil clock reads time.Now.
func New(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (s *Store) stamp() string { return stampOf(s.now()) }

func stampOf(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(v string) time.Time {
	t, _ := time.Parse(timeLayout, v)
	return t
}

// Create starts an empty conversation.
func (s *Store) Create(ctx context.Context, ch Channel, title string) (Session, error) {
	if !ch.Valid() {
		return Session{}, fmt.Errorf("sessions: unknown channel %q", ch)
	}
	now := s.now()
	sess := Session{ID: store.NewID("ses"), Title: clip(title, MaxTitle), Channel: ch, CreatedAt: now, UpdatedAt: now}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO chat_sessions (id, title, channel, created_at, updated_at) VALUES (?,?,?,?,?)`,
			sess.ID, sess.Title, string(ch), stampOf(now), stampOf(now))
		return err
	})
	return sess, err
}

const sessionColumns = `id, title, channel, COALESCE(parent_id, ''), COALESCE(parent_message_id, 0), created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanSession(r scanner) (Session, error) {
	var x Session
	var ch, created, updated string
	if err := r.Scan(&x.ID, &x.Title, &ch, &x.ParentID, &x.ParentMessageID, &created, &updated); err != nil {
		return Session{}, err
	}
	x.Channel, x.CreatedAt, x.UpdatedAt = Channel(ch), parseStamp(created), parseStamp(updated)
	return x, nil
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getSession(ctx context.Context, q querier, id string) (Session, error) {
	x, err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM chat_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, store.ErrNotFound
	}
	return x, err
}

// Get returns one conversation; an unknown id is store.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	return getSession(ctx, s.db.Read(), id)
}

// Detail reads the session, messages and turns from one database snapshot.
// A completed turn must never be paired with messages read before its answer.
func (s *Store) Detail(ctx context.Context, id string) (Session, []Message, []Turn, error) {
	tx, err := s.db.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Session{}, nil, nil, err
	}
	defer tx.Rollback()
	return readDetail(ctx, tx, id)
}

func readDetail(ctx context.Context, q querier, id string) (Session, []Message, []Turn, error) {
	sess, err := getSession(ctx, q, id)
	if err != nil {
		return Session{}, nil, nil, err
	}
	msgs, err := readMessages(ctx, q, id, 0)
	if err != nil {
		return Session{}, nil, nil, err
	}
	turns, err := readTurns(ctx, q, id)
	return sess, msgs, turns, err
}

// ListQuery filters the conversation list.
type ListQuery struct {
	Channel Channel
	// Search keeps the conversations whose title contains the text or whose
	// user, assistant or steering messages contain all of its words, each
	// matched as a prefix.
	Search string
	Limit  int
	Offset int
}

// List returns conversations, most recently updated first, and the total.
func (s *Store) List(ctx context.Context, q ListQuery) ([]Session, int, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	var conds []string
	args := []any{}
	if q.Channel != "" {
		conds, args = append(conds, "channel = ?"), append(args, string(q.Channel))
	}
	if text := strings.TrimSpace(q.Search); text != "" {
		cond, a := searchCondition(text)
		conds, args = append(conds, cond), append(args, a...)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.db.Read().QueryRowContext(ctx, `SELECT count(*) FROM chat_sessions`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+sessionColumns+` FROM chat_sessions`+where+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, q.Limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// searchCondition matches the title by substring and the messages by the
// full-text index. A text with no word characters can only match a title.
func searchCondition(text string) (string, []any) {
	title := "instr(lower(title), lower(?)) > 0"
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return title, []any{text}
	}
	if len(words) > 12 {
		words = words[:12]
	}
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return "(" + title + ` OR id IN (SELECT m.session_id FROM chat_messages_fts JOIN chat_messages m ON m.id = chat_messages_fts.rowid
		WHERE chat_messages_fts MATCH ?))`, []any{text, strings.Join(words, " ")}
}

// Rename changes a conversation's title.
func (s *Store) Rename(ctx context.Context, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE chat_sessions SET title = ?, updated_at = ? WHERE id = ?`, clip(title, MaxTitle), s.stamp(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, id)
}

// Delete removes a conversation with its messages, turns and index entries.
// Its idempotency claims stay: a claim guards a run that outlives the chat.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := getSession(ctx, tx, id); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM chat_checkpoints WHERE session_id = ?`,
			`DELETE FROM chat_messages WHERE session_id = ?`,
			`DELETE FROM chat_turns WHERE session_id = ?`,
			`DELETE FROM chat_sessions WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Append adds a message at the end of a conversation and moves its update time.
func (s *Store) Append(ctx context.Context, m Message) (Message, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.now()
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return insertMessage(ctx, tx, &m, "INSERT")
	})
	return m, err
}

// KeepEnvelope stores the envelope of a final answer with the turn's last
// assistant message. A turn with no assistant message keeps nothing.
func (s *Store) KeepEnvelope(ctx context.Context, sessionID, turnID string, e Envelope) error {
	if e.empty() {
		return nil
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE chat_messages SET envelope = ? WHERE id = (
			SELECT MAX(id) FROM chat_messages WHERE session_id = ? AND turn_id = ? AND role = 'assistant')`, e.encode(), sessionID, turnID)
		return err
	})
}

// AppendResult adds a tool result unless the call already has one, and
// reports whether it was added.
func (s *Store) AppendResult(ctx context.Context, m Message) (bool, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.now()
	}
	m.Role = RoleToolResult
	added := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := insertMessage(ctx, tx, &m, "INSERT OR IGNORE"); err != nil {
			return err
		}
		added = m.ID != 0
		return nil
	})
	return added, err
}

func insertMessage(ctx context.Context, tx *sql.Tx, m *Message, verb string) error {
	res, err := tx.ExecContext(ctx, verb+` INTO chat_messages (session_id, turn_id, role, body, tool_name, tool_call_id, ok, system_made, outside_content, envelope, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, m.SessionID, m.TurnID, string(m.Role), m.Body, m.ToolName, m.ToolCallID, b2i(m.OK), b2i(m.SystemMade), b2i(m.OutsideContent), m.Envelope.encode(), stampOf(m.CreatedAt))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		m.ID = 0
		return nil
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat_sessions SET updated_at = ? WHERE id = ?`, stampOf(m.CreatedAt), m.SessionID)
	return err
}

const messageColumns = `id, session_id, turn_id, role, body, tool_name, tool_call_id, ok, system_made, outside_content, envelope, created_at`

func scanMessage(r scanner) (Message, error) {
	var m Message
	var role, created, envelope string
	var ok, sys, outside int
	if err := r.Scan(&m.ID, &m.SessionID, &m.TurnID, &role, &m.Body, &m.ToolName, &m.ToolCallID, &ok, &sys, &outside, &envelope, &created); err != nil {
		return Message{}, err
	}
	m.Role, m.OK, m.SystemMade, m.OutsideContent, m.Envelope, m.CreatedAt = Role(role), ok == 1, sys == 1, outside == 1, decodeEnvelope(envelope), parseStamp(created)
	return m, nil
}

// Messages returns a conversation's rows in order.
func (s *Store) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	return s.MessagesAfter(ctx, sessionID, 0)
}

// MessagesAfter returns a conversation's rows that follow the row with the
// given id, in order: what a checkpoint that covers up to that row leaves.
func (s *Store) MessagesAfter(ctx context.Context, sessionID string, after int64) ([]Message, error) {
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	return readMessages(ctx, s.db.Read(), sessionID, after)
}

// LastAnswer returns the text of the conversation's latest assistant message,
// empty when it has none.
func (s *Store) LastAnswer(ctx context.Context, sessionID string) (string, error) {
	var body string
	err := s.db.Read().QueryRowContext(ctx, `SELECT body FROM chat_messages WHERE session_id = ? AND role = 'assistant' ORDER BY id DESC LIMIT 1`, sessionID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

func readMessages(ctx context.Context, q querier, sessionID string, after int64) ([]Message, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+messageColumns+` FROM chat_messages WHERE session_id = ? AND id > ? ORDER BY id`, sessionID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Fork starts a new conversation that holds the parent's messages up to and
// including upTo. The parent is not touched, and nothing but messages is
// copied: no pending confirmation and no idempotency claim.
func (s *Store) Fork(ctx context.Context, parentID string, upTo int64) (Session, error) {
	var out Session
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		parent, err := getSession(ctx, tx, parentID)
		if err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT session_id FROM chat_messages WHERE id = ?`, upTo).Scan(&owner); err != nil || owner != parentID {
			return fmt.Errorf("%w: message %d is not in this conversation", store.ErrNotFound, upTo)
		}
		now := s.now()
		out = Session{ID: store.NewID("ses"), Title: clip(strings.TrimSpace(parent.Title+" (fork)"), MaxTitle), Channel: Dashboard,
			ParentID: parentID, ParentMessageID: upTo, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_sessions (id, title, channel, parent_id, parent_message_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			out.ID, out.Title, string(out.Channel), parentID, upTo, stampOf(now), stampOf(now)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_messages (session_id, turn_id, role, body, tool_name, tool_call_id, ok, system_made, outside_content, envelope, created_at)
			SELECT ?, turn_id, role, body, tool_name, tool_call_id, ok, system_made, outside_content, envelope, created_at FROM chat_messages
			WHERE session_id = ? AND id <= ? ORDER BY id`, out.ID, parentID, upTo)
		return err
	})
	return out, err
}

// RecordStandIns gives every tool call that has no result a result of the
// system-made type with the given text, and returns how many it added. It is
// meant for a conversation with no turn running.
func (s *Store) RecordStandIns(ctx context.Context, sessionID, text string) (int, error) {
	added := 0
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.turn_id, c.tool_name, c.tool_call_id FROM chat_messages c
			WHERE c.session_id = ? AND c.role = 'tool_call' AND NOT EXISTS (
				SELECT 1 FROM chat_messages r WHERE r.session_id = c.session_id AND r.role = 'tool_result'
				AND r.turn_id = c.turn_id AND r.tool_call_id = c.tool_call_id) ORDER BY c.id`, sessionID)
		if err != nil {
			return err
		}
		var orphans []Message
		for rows.Next() {
			m := Message{SessionID: sessionID, Role: RoleToolResult, Body: text, SystemMade: true}
			if err := rows.Scan(&m.TurnID, &m.ToolName, &m.ToolCallID); err != nil {
				_ = rows.Close()
				return err
			}
			orphans = append(orphans, m)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, m := range orphans {
			m.CreatedAt = s.now()
			if err := insertMessage(ctx, tx, &m, "INSERT OR IGNORE"); err != nil {
				return err
			}
			if m.ID != 0 {
				added++
			}
		}
		return nil
	})
	return added, err
}

// StartTurn records a turn that has begun.
func (s *Store) StartTurn(ctx context.Context, t Turn) error {
	if t.StartedAt.IsZero() {
		t.StartedAt = s.now()
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO chat_turns (id, session_id, channel, origin, rule_id, status, started_at) VALUES (?,?,?,?,?,'running',?)`,
			t.ID, t.SessionID, string(t.Channel), t.Origin, t.RuleID, stampOf(t.StartedAt))
		return err
	})
}

// SetTurnHarness records the harness version a turn ran with.
func (s *Store) SetTurnHarness(ctx context.Context, id string, version int64) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE chat_turns SET harness_version = ? WHERE id = ?`, version, id)
		return err
	})
}

// SetTurnTier records the tier the intent classifier chose for a turn.
func (s *Store) SetTurnTier(ctx context.Context, id, tier string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE chat_turns SET tier = ? WHERE id = ?`, tier, id)
		return err
	})
}

// FinishTurn ends a running turn. Only the first call for a turn counts.
func (s *Store) FinishTurn(ctx context.Context, id, status, reason, detail string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE chat_turns SET status = ?, reason = ?, detail = ?, finished_at = ? WHERE id = ? AND status = 'running'`,
			status, reason, clip(detail, 300), s.stamp(), id)
		return err
	})
}

// EndInterruptedTurns ends every turn left running by a process that stopped,
// as runtime_unavailable, and returns their session ids.
func (s *Store) EndInterruptedTurns(ctx context.Context, reason, detail string) ([]string, error) {
	var ids []string
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT session_id FROM chat_turns WHERE status = 'running'`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_turns SET status = 'error', reason = ?, detail = ?, finished_at = ? WHERE status = 'running'`, reason, detail, s.stamp())
		return err
	})
	return ids, err
}

// Turns returns a conversation's turns in order.
func (s *Store) Turns(ctx context.Context, sessionID string) ([]Turn, error) {
	return readTurns(ctx, s.db.Read(), sessionID)
}

func readTurns(ctx context.Context, q querier, sessionID string) ([]Turn, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, session_id, channel, origin, rule_id, tier, status, reason, detail, started_at, finished_at
		FROM chat_turns WHERE session_id = ? ORDER BY started_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var t Turn
		var ch, started string
		var finished sql.NullString
		if err := rows.Scan(&t.ID, &t.SessionID, &ch, &t.Origin, &t.RuleID, &t.Tier, &t.Status, &t.Reason, &t.Detail, &started, &finished); err != nil {
			return nil, err
		}
		t.Channel, t.StartedAt = Channel(ch), parseStamp(started)
		if finished.Valid {
			f := parseStamp(finished.String)
			t.FinishedAt = &f
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Search returns the SnippetLimit best-ranked snippets of what users and the
// agent said, oldest first, never from a deleted conversation. exclude names a
// conversation to leave out (the one asking), empty for none. The snippets
// carry no highlight marks: the model copied the brackets into its answers
// ("[Otter].ai").
func (s *Store) Search(ctx context.Context, query, exclude string) ([]Snippet, error) {
	match := matchExpression(query)
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.Read().QueryContext(ctx, `SELECT m.id, m.session_id, c.channel, m.created_at,
			snippet(chat_messages_fts, 0, '', '', '...', 24)
		FROM chat_messages_fts JOIN chat_messages m ON m.id = chat_messages_fts.rowid
		JOIN chat_sessions c ON c.id = m.session_id
		WHERE chat_messages_fts MATCH ? AND m.session_id <> ?
		ORDER BY rank, m.id DESC LIMIT ?`, match, exclude, SnippetLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		var x Snippet
		var ch, at string
		if err := rows.Scan(&x.MessageID, &x.SessionID, &ch, &at, &x.Text); err != nil {
			return nil, err
		}
		x.Channel, x.At = Channel(ch), parseStamp(at)
		out = append(out, x)
	}
	slices.SortStableFunc(out, func(a, b Snippet) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.MessageID, b.MessageID)) })
	return out, rows.Err()
}

// matchExpression turns free text into an FTS5 query of quoted words, so
// operators and punctuation typed by a user or a model are plain text.
func matchExpression(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > 12 {
		words = words[:12]
	}
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " ")
}

// Claim writes the claim of (tool, key) and reports whether it was new. A
// claimed pair stays claimed whatever became of the call.
func (s *Store) Claim(ctx context.Context, tool, key string) (bool, error) {
	fresh := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_claims (tool, key, claimed_at) VALUES (?,?,?)`, tool, key, s.stamp())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		fresh = n == 1
		return err
	})
	return fresh, err
}

// SettleClaim records how a claimed call ended.
func (s *Store) SettleClaim(ctx context.Context, tool, key, outcome string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_claims SET settled_at = ?, outcome = ? WHERE tool = ? AND key = ?`, s.stamp(), clip(outcome, 120), tool, key)
		return err
	})
}

// PruneClaims removes claims older than 30 days.
func (s *Store) PruneClaims(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_claims WHERE claimed_at < ?`, stampOf(s.now().Add(-claimRetention)))
		return err
	})
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
