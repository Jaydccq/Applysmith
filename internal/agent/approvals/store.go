// Clean-room: docs/provenance.md#CR-AGENT-GRANTS

package approvals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// Limits from the specification.
const (
	// CardTTL is how long a card waits before it counts as declined.
	CardTTL = 12 * time.Hour
	// MaxLifetime is the longest a standing approval lives, whatever its scope.
	MaxLifetime = 24 * time.Hour
	// MaxActive is how many standing approvals may be active at once.
	MaxActive = 20
	// PurposeLimit is the most characters of the user's message a card shows,
	// the ellipsis included.
	PurposeLimit = 500
)

// Gates and reason codes of the audit rows this package writes.
const (
	GateCard     gates.Gate = "confirmation_card"
	GateStanding gates.Gate = "standing_approval"

	CodeCardApproved = "card_approved"
	CodeCardDeclined = "card_declined"
	CodeHoldExpired  = "hold_expired"
	// CodeOutsideContent is the code of a hold for outside content; the agent's
	// guard uses the same value.
	CodeOutsideContent  = "outside_content_read"
	CodeApprovalCreated = "approval_created"
	CodeApprovalRevoked = "approval_revoked"
	// GrantPrefix starts the code of a call a standing approval let through.
	GrantPrefix = "grant:"
)

// Errors the callers map onto their own answers.
var (
	// ErrNotFound means no such card or approval.
	ErrNotFound = store.ErrNotFound
	// ErrConflict means the card is not pending (it was handled or expired) or
	// the approval has already ended.
	ErrConflict = errors.New("approvals: the card is expired or already handled")
	// ErrNotPermitted means the actor may not make this decision.
	ErrNotPermitted = errors.New("approvals: this actor may not make this decision")
	// ErrIneligible means the call may only be approved once.
	ErrIneligible = errors.New("approvals: this call can only be approved once")
	// ErrLimit means MaxActive standing approvals are already active.
	ErrLimit = errors.New("approvals: 20 standing approvals are already active; revoke one first")
)

// Actor says whose hand made a decision.
type Actor string

const (
	// ActorDashboard is the dashboard session on the loopback listener, the
	// only actor that may approve for longer than once.
	ActorDashboard    Actor = "dashboard"
	ActorLocalToken   Actor = "local-token"
	ActorRemoteDevice Actor = "remote-device"
	ActorTelegram     Actor = "telegram"
	// ActorSystem is the clock or a restart.
	ActorSystem Actor = "system"
)

func (a Actor) caller() gates.Caller {
	switch a {
	case ActorTelegram:
		return gates.CallerTelegram
	case ActorLocalToken:
		return gates.CallerCLI
	case ActorSystem:
		return gates.CallerAskAgent
	}
	return gates.CallerDashboard
}

// Scope is how long an approval lasts.
type Scope string

const (
	Once         Scope = "once"
	Conversation Scope = "conversation"
	Today        Scope = "today"
)

// State of a card.
type State string

const (
	Pending  State = "pending"
	Approved State = "approved"
	Declined State = "declined"
	Expired  State = "expired"
)

// Card is a held call waiting for the user.
type Card struct {
	ID        string
	SessionID string
	TurnID    string
	CallID    string
	Channel   string
	Origin    string
	Tool      string
	Tier      int
	// Remote says the turn came from a remote device, so the call keeps the
	// remote tool envelope when the card is approved.
	Remote bool
	// Code and Reason are what the guard chain gave for the hold.
	Code    string
	Reason  string
	Summary string
	Company string
	Role    string
	Host    string
	// Target is what a standing approval would be bound to; empty when the
	// call can only be approved once.
	Target string
	// Args is the exact argument object the re-run uses and never leaves the
	// server; ArgsShown is the redacted text the dashboard shows.
	Args      string
	ArgsShown string
	ArgHash   string
	// Purpose is the user's own message, or the label of the button pressed.
	Purpose   string
	State     State
	DecidedBy Actor
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
}

// Scopes lists the lifetimes the card offers.
func (c Card) Scopes() []Scope {
	if c.Target == "" || c.Tier >= 4 {
		return []Scope{Once}
	}
	return []Scope{Once, Conversation, Today}
}

// NewCard is what the guard chain knows when it holds a call.
type NewCard struct {
	SessionID, TurnID, CallID, Channel, Origin, Tool string
	Tier                                             int
	Code, Reason, Summary, Company, Role, Host       string
	Target, Args, ArgsShown, ArgHash, Purpose        string
	Remote                                           bool
}

// Approval is a standing approval.
type Approval struct {
	ID         string
	CardID     string
	Tool       string
	Target     string
	Scope      Scope
	SessionID  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Uses       int
	LastUsedAt *time.Time
}

// Auditor receives the audit rows; the conversation record's store is one.
type Auditor interface {
	AppendDecision(ctx context.Context, r gates.AuditRow) error
}

// Store keeps the cards and the approvals.
type Store struct {
	db    *store.Store
	audit Auditor
	now   func() time.Time
	loc   *time.Location
	// OnExpired is called with the cards a sweep, a restart or a read just
	// expired, after their audit rows are written.
	OnExpired func(ctx context.Context, cards []Card)
}

// New returns a Store. A nil clock reads time.Now and a nil location is the
// machine's local zone, which decides where "today" ends.
func New(db *store.Store, audit Auditor, now func() time.Time, loc *time.Location) *Store {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		loc = time.Local
	}
	return &Store{db: db, audit: audit, now: now, loc: loc}
}

const timeLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(v string) time.Time {
	t, _ := time.Parse(timeLayout, v)
	return t
}

const cardCols = `id, session_id, turn_id, call_id, channel, origin, tool, tier, code, reason, summary, company, role, host, target,
	args, args_shown, arg_hash, purpose, state, decided_by, created_at, expires_at, decided_at, remote`

type scanner interface{ Scan(...any) error }

func scanCard(r scanner) (Card, error) {
	var c Card
	var created, expires string
	var decided sql.NullString
	var state, by string
	var remote int
	err := r.Scan(&c.ID, &c.SessionID, &c.TurnID, &c.CallID, &c.Channel, &c.Origin, &c.Tool, &c.Tier, &c.Code, &c.Reason, &c.Summary,
		&c.Company, &c.Role, &c.Host, &c.Target, &c.Args, &c.ArgsShown, &c.ArgHash, &c.Purpose, &state, &by, &created, &expires, &decided, &remote)
	if err != nil {
		return Card{}, err
	}
	c.State, c.DecidedBy, c.Remote = State(state), Actor(by), remote != 0
	c.CreatedAt, c.ExpiresAt = parseStamp(created), parseStamp(expires)
	if decided.Valid {
		t := parseStamp(decided.String)
		c.DecidedAt = &t
	}
	return c, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func getCard(ctx context.Context, q querier, id string) (Card, error) {
	c, err := scanCard(q.QueryRowContext(ctx, `SELECT `+cardCols+` FROM agent_confirmations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, ErrNotFound
	}
	return c, err
}

// Create records a held call as a pending card. A call already waiting in the
// same conversation with the same arguments returns its card, so a model that
// retries does not stack cards.
func (s *Store) Create(ctx context.Context, n NewCard) (Card, error) {
	now := s.now()
	var out Card
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		c, err := scanCard(tx.QueryRowContext(ctx, `SELECT `+cardCols+` FROM agent_confirmations
			WHERE session_id = ? AND tool = ? AND args = ? AND state = 'pending' AND expires_at > ?`, n.SessionID, n.Tool, n.Args, stamp(now)))
		if err == nil {
			out = c
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out = Card{ID: store.NewID("conf"), SessionID: n.SessionID, TurnID: n.TurnID, CallID: n.CallID, Channel: n.Channel, Origin: n.Origin, Tool: n.Tool,
			Tier: n.Tier, Code: n.Code, Reason: n.Reason, Summary: n.Summary, Company: n.Company, Role: n.Role, Host: n.Host, Target: n.Target,
			Args: n.Args, ArgsShown: n.ArgsShown, ArgHash: n.ArgHash, Purpose: n.Purpose, State: Pending, CreatedAt: now, ExpiresAt: now.Add(CardTTL), Remote: n.Remote}
		remote := 0
		if out.Remote {
			remote = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_confirmations (`+cardCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
			out.ID, out.SessionID, out.TurnID, out.CallID, out.Channel, out.Origin, out.Tool, out.Tier, out.Code, out.Reason, out.Summary,
			out.Company, out.Role, out.Host, out.Target, out.Args, out.ArgsShown, out.ArgHash, out.Purpose, string(out.State), "", stamp(out.CreatedAt), stamp(out.ExpiresAt), remote)
		return err
	})
	return out, err
}

// Get returns a card in any state.
func (s *Store) Get(ctx context.Context, id string) (Card, error) {
	return getCard(ctx, s.db.Read(), id)
}

// Pending lists the cards still waiting, oldest first, after expiring those
// that ran out.
func (s *Store) Pending(ctx context.Context) ([]Card, error) {
	if _, err := s.ExpireDue(ctx); err != nil {
		return nil, err
	}
	return s.list(ctx, `SELECT `+cardCols+` FROM agent_confirmations WHERE state = 'pending' ORDER BY created_at, id`)
}

// OfSession lists the newest limit cards of one conversation in any state,
// oldest first. It writes nothing: a pending card past its expiry is reported
// as expired, and the next sweep records it so.
func (s *Store) OfSession(ctx context.Context, sessionID string, limit int) ([]Card, error) {
	cards, err := s.list(ctx, `SELECT `+cardCols+` FROM (SELECT * FROM agent_confirmations WHERE session_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)
		ORDER BY created_at, id`, sessionID, limit)
	now := s.now()
	for i, c := range cards {
		if c.State == Pending && !now.Before(c.ExpiresAt) {
			cards[i].State = Expired
		}
	}
	return cards, err
}

func (s *Store) list(ctx context.Context, query string, args ...any) ([]Card, error) {
	rows, err := s.db.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ExpireDue marks every pending card past its expiry as expired and returns
// them. Each is audited as hold_expired and counts as declined.
func (s *Store) ExpireDue(ctx context.Context) ([]Card, error) {
	return s.expire(ctx, true, "the card waited 12 hours without an answer")
}

// ExpireAll marks every pending card as expired, for a server that is starting
// after the one that held them stopped.
func (s *Store) ExpireAll(ctx context.Context) ([]Card, error) {
	return s.expire(ctx, false, "the server restarted while the card was waiting")
}

func (s *Store) expire(ctx context.Context, dueOnly bool, reason string) ([]Card, error) {
	now := stamp(s.now())
	query := `SELECT ` + cardCols + ` FROM agent_confirmations WHERE state = 'pending' AND (? = 0 OR expires_at <= ?) ORDER BY created_at, id`
	var cards []Card
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		cards = nil
		rows, err := tx.QueryContext(ctx, query, b2i(dueOnly), now)
		if err != nil {
			return err
		}
		for rows.Next() {
			c, err := scanCard(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			cards = append(cards, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for i := range cards {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_confirmations SET state = 'expired', decided_by = ?, decided_at = ? WHERE id = ? AND state = 'pending'`,
				string(ActorSystem), now, cards[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(cards) == 0 {
		return nil, err
	}
	for i := range cards {
		cards[i].State, cards[i].DecidedBy = Expired, ActorSystem
		if err := s.record(ctx, cards[i], GateCard, gates.Deny, CodeHoldExpired, reason, ActorSystem); err != nil {
			return cards, err
		}
	}
	if s.OnExpired != nil {
		s.OnExpired(ctx, cards)
	}
	return cards, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Decline marks a pending card declined. Anyone who can see the card may
// decline it.
func (s *Store) Decline(ctx context.Context, id string, by Actor) (Card, error) {
	card, expired, err := s.decide(ctx, id, by, Declined, "", nil)
	if err != nil {
		return Card{}, err
	}
	if expired {
		return Card{}, ErrConflict
	}
	return card, s.record(ctx, card, GateCard, gates.Deny, CodeCardDeclined, "the user declined the held call", by)
}

// Approve moves a pending card to approved and, for a scope wider than once,
// creates the standing approval. Only the dashboard session may choose a wider
// scope. Telegram may approve once, and only a card of its own channel below
// tier 4; a submit (tier 4) needs the dashboard session. The caller runs the
// held call afterwards, once.
func (s *Store) Approve(ctx context.Context, id string, by Actor, scope Scope) (Card, *Approval, error) {
	if scope == "" {
		scope = Once
	}
	if !slices.Contains([]Scope{Once, Conversation, Today}, scope) {
		return Card{}, nil, fmt.Errorf("approvals: unknown scope %q", scope)
	}
	var ap *Approval
	card, expired, err := s.decide(ctx, id, by, Approved, scope, &ap)
	if err != nil {
		return Card{}, nil, err
	}
	if expired {
		return Card{}, nil, ErrConflict
	}
	if err := s.record(ctx, card, GateCard, gates.Allow, CodeCardApproved, fmt.Sprintf("approved by %s for %s", by, scope), by); err != nil {
		return card, ap, err
	}
	if ap != nil {
		err = s.record(ctx, card, GateStanding, gates.Allow, CodeApprovalCreated,
			fmt.Sprintf("standing approval %s for %s, %s, ends %s", ap.ID, ap.Tool, ap.Scope, ap.ExpiresAt.UTC().Format(time.RFC3339)), by)
	}
	return card, ap, err
}

// decide moves a pending card to a final state inside one write. expired is
// true when the card had run out and was marked expired instead.
func (s *Store) decide(ctx context.Context, id string, by Actor, to State, scope Scope, ap **Approval) (card Card, expired bool, err error) {
	now := s.now()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		card, expired = Card{}, false
		c, err := getCard(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.State != Pending {
			return ErrConflict
		}
		if !c.ExpiresAt.After(now) {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_confirmations SET state = 'expired', decided_by = ?, decided_at = ? WHERE id = ?`,
				string(ActorSystem), stamp(now), id); err != nil {
				return err
			}
			c.State, c.DecidedBy = Expired, ActorSystem
			card, expired = c, true
			return nil
		}
		if to == Approved {
			if err := permitted(c, by, scope); err != nil {
				return err
			}
			if scope != Once {
				made, err := s.insertApproval(ctx, tx, c, scope, now)
				if err != nil {
					return err
				}
				*ap = &made
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_confirmations SET state = ?, decided_by = ?, decided_at = ? WHERE id = ?`,
			string(to), string(by), stamp(now), id); err != nil {
			return err
		}
		c.State, c.DecidedBy = to, by
		t := now
		c.DecidedAt = &t
		card = c
		return nil
	})
	if err == nil && expired {
		// The write above committed the expiry; its audit row follows.
		if err := s.record(ctx, card, GateCard, gates.Deny, CodeHoldExpired, "the card waited 12 hours without an answer", ActorSystem); err != nil {
			return card, true, err
		}
		if s.OnExpired != nil {
			s.OnExpired(ctx, []Card{card})
		}
	}
	return card, expired, err
}

// permitted applies the rules about who may approve what.
func permitted(c Card, by Actor, scope Scope) error {
	switch {
	case scope != Once && by != ActorDashboard:
		return ErrNotPermitted
	case by == ActorTelegram && (c.Channel != "telegram" || c.Tier >= 4):
		return ErrNotPermitted
	case c.Tier >= 4 && by != ActorDashboard:
		return ErrNotPermitted
	case by == ActorRemoteDevice && c.Code == CodeOutsideContent:
		// Outside content was read in the turn and could have chosen the
		// action: the person at the desktop decides, not a phone.
		return ErrNotPermitted
	case scope != Once && (c.Target == "" || c.Tier >= 4):
		// A submit is approved once, never standing, whatever target it has.
		return ErrIneligible
	}
	return nil
}

// insertApproval creates the standing approval a card is approved with.
func (s *Store) insertApproval(ctx context.Context, tx *sql.Tx, c Card, scope Scope, now time.Time) (Approval, error) {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_approvals WHERE revoked_at IS NULL AND expires_at > ?`, stamp(now)).Scan(&active); err != nil {
		return Approval{}, err
	}
	if active >= MaxActive {
		return Approval{}, ErrLimit
	}
	a := Approval{ID: store.NewID("grant"), CardID: c.ID, Tool: c.Tool, Target: c.Target, Scope: scope, CreatedAt: now, ExpiresAt: now.Add(MaxLifetime)}
	var session any
	if scope == Today {
		local := now.In(s.loc)
		if midnight := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, s.loc); midnight.Before(a.ExpiresAt) {
			a.ExpiresAt = midnight
		}
	} else {
		a.SessionID = c.SessionID
		session = c.SessionID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_approvals (id, card_id, tool, target, scope, session_id, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.CardID, a.Tool, a.Target, string(a.Scope), session, stamp(a.CreatedAt), stamp(a.ExpiresAt))
	return a, err
}

const approvalCols = `id, card_id, tool, target, scope, COALESCE(session_id, ''), created_at, expires_at, uses, last_used_at`

func scanApproval(r scanner) (Approval, error) {
	var a Approval
	var scope, created, expires string
	var used sql.NullString
	if err := r.Scan(&a.ID, &a.CardID, &a.Tool, &a.Target, &scope, &a.SessionID, &created, &expires, &a.Uses, &used); err != nil {
		return Approval{}, err
	}
	a.Scope, a.CreatedAt, a.ExpiresAt = Scope(scope), parseStamp(created), parseStamp(expires)
	if used.Valid {
		t := parseStamp(used.String)
		a.LastUsedAt = &t
	}
	return a, nil
}

// Active lists the standing approvals that have not ended, oldest first.
func (s *Store) Active(ctx context.Context) ([]Approval, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+approvalCols+` FROM agent_approvals WHERE revoked_at IS NULL AND expires_at > ? ORDER BY created_at, id`, stamp(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Covers finds the active approval for exactly this tool and target, usable in
// this conversation, counts and audits the use, and returns it. A
// conversation-scoped approval covers its own conversation only.
func (s *Store) Covers(ctx context.Context, tool, target, sessionID string) (Approval, bool, error) {
	now := s.now()
	var a Approval
	found := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		found = false
		row, err := scanApproval(tx.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM agent_approvals
			WHERE tool = ? AND target = ? AND revoked_at IS NULL AND expires_at > ? AND (scope = 'today' OR session_id = ?)
			ORDER BY created_at, id LIMIT 1`, tool, target, stamp(now), sessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_approvals SET uses = uses + 1, last_used_at = ? WHERE id = ?`, stamp(now), row.ID); err != nil {
			return err
		}
		row.Uses++
		t := now
		row.LastUsedAt = &t
		a, found = row, true
		return nil
	})
	if err != nil || !found {
		return Approval{}, false, err
	}
	row := gates.NewAuditRow(now.UnixMilli(), gates.Decision{Gate: GateStanding, Verdict: gates.AllowWithTrace, Code: GrantPrefix + a.ID,
		Reason: fmt.Sprintf("standing approval for %s covered the call", a.Tool)}, gates.AuditContext{Tool: tool, ConversationID: sessionID, Caller: gates.CallerAskAgent})
	if err := s.audit.AppendDecision(ctx, row); err != nil {
		return Approval{}, false, err
	}
	return a, true, nil
}

// Revoke ends a standing approval at once. The next matching call is held.
func (s *Store) Revoke(ctx context.Context, id string, by Actor) (Approval, error) {
	now := s.now()
	var a Approval
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		row, err := scanApproval(tx.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM agent_approvals WHERE id = ? AND revoked_at IS NULL AND expires_at > ?`, id, stamp(now)))
		if errors.Is(err, sql.ErrNoRows) {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_approvals WHERE id = ?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_approvals SET revoked_at = ? WHERE id = ?`, stamp(now), id); err != nil {
			return err
		}
		a = row
		return nil
	})
	if err != nil {
		return Approval{}, err
	}
	row := gates.NewAuditRow(now.UnixMilli(), gates.Decision{Gate: GateStanding, Verdict: gates.Deny, Code: CodeApprovalRevoked,
		Reason: fmt.Sprintf("%s revoked the standing approval %s for %s", by, a.ID, a.Tool)}, gates.AuditContext{Tool: a.Tool, ConversationID: a.SessionID, Caller: by.caller()})
	return a, s.audit.AppendDecision(ctx, row)
}

func (s *Store) record(ctx context.Context, c Card, gate gates.Gate, v gates.Verdict, code, reason string, by Actor) error {
	row := gates.NewAuditRow(s.now().UnixMilli(), gates.Decision{Gate: gate, Verdict: v, Code: code, Reason: reason}, gates.AuditContext{
		Tool: c.Tool, TurnID: c.TurnID, ConversationID: c.SessionID, RiskTier: fmt.Sprint(c.Tier), Caller: by.caller(), ArgFingerprint: c.ArgHash,
	})
	return s.audit.AppendDecision(ctx, row)
}
