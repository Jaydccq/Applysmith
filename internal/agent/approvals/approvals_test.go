// Clean-room: docs/provenance.md#CR-AGENT-GRANTS

package approvals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type auditLog struct {
	mu   sync.Mutex
	rows []gates.AuditRow
}

func (a *auditLog) AppendDecision(_ context.Context, r gates.AuditRow) error {
	a.mu.Lock()
	a.rows = append(a.rows, r)
	a.mu.Unlock()
	return nil
}

func (a *auditLog) codes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, r := range a.rows {
		out = append(out, string(r.Gate)+"/"+r.Code)
	}
	return out
}

type rig struct {
	s     *Store
	db    *store.Store
	clock *fakeClock
	audit *auditLog
}

func newRig(t *testing.T, loc *time.Location) *rig {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &fakeClock{t: time.Date(2027, 3, 3, 8, 0, 0, 0, time.UTC)}
	audit := &auditLog{}
	return &rig{s: New(db, audit, clock.now, loc), db: db, clock: clock, audit: audit}
}

// session inserts the conversation a card belongs to.
func (r *rig) session(t *testing.T, id string) {
	t.Helper()
	at := stamp(r.clock.now())
	err := r.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO chat_sessions (id, title, channel, created_at, updated_at) VALUES (?, '', 'dashboard', ?, ?)`, id, at, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) deleteSession(t *testing.T, id string) {
	t.Helper()
	err := r.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM chat_sessions WHERE id = ?`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func card(session, tool, args string) NewCard {
	target, _ := Target(tool, json.RawMessage(args))
	return NewCard{SessionID: session, TurnID: "turn_1", CallID: "call_1", Channel: "dashboard", Origin: "model", Tool: tool, Tier: 3,
		Code: "outside_content_read", Reason: "outside content was read", Summary: tool, Target: target, Args: args, ArgsShown: args, ArgHash: "h", Purpose: "do it"}
}

var bg = context.Background()

func TestTarget_EligibilityTable(t *testing.T) {
	cases := []struct {
		name, tool, args string
		target           string
		ok               bool
	}{
		{"save_fact is bound to the tool", "save_fact", `{"body":"x"}`, "save_fact", true},
		{"amend_fact is bound to the tool", "amend_fact", `{"id":"f1","body":"x"}`, "amend_fact", true},
		{"record_story_answer is bound to the tool", "record_story_answer", `{}`, "record_story_answer", true},
		{"tailor_resume binds the application id", "tailor_resume", `{"application_id":"app_7"}`, "app_7", true},
		{"a numeric application id is accepted", "tailor_resume", `{"application_id":42}`, "42", true},
		{"a fractional application id is not", "tailor_resume", `{"application_id":4.2}`, "", false},
		{"tailor_resume without the id", "tailor_resume", `{}`, "", false},
		{"evaluate_job binds the host", "evaluate_job", `{"url":"https://Boards.Example.test/acme/jobs/1?x=1"}`, "boards.example.test", true},
		{"a leading www is ignored", "evaluate_job", `{"url":"https://www.boards.example.test/a"}`, "boards.example.test", true},
		{"a non-http posting is refused", "evaluate_job", `{"url":"file:///etc/hosts"}`, "", false},
		{"user-info is refused", "evaluate_job", `{"url":"https://user:pw@boards.example.test/a"}`, "", false},
		{"a missing url is refused", "evaluate_job", `{}`, "", false},
		{"scan_portal binds the source", "scan_portal", `{"source":"greenhouse"}`, "greenhouse", true},
		{"autofill binds the exact url", "autofill_application", `{"url":"https://www.Jobs.example.test:8443/a/b?id=3#frag"}`, "https://jobs.example.test:8443/a/b?id=3", true},
		{"autofill with submit false", "autofill_application", `{"url":"https://jobs.example.test/a","submit":false}`, "https://jobs.example.test/a", true},
		{"autofill with submit true is never eligible", "autofill_application", `{"url":"https://jobs.example.test/a","submit":true}`, "", false},
		{"submit as text is not false", "autofill_application", `{"url":"https://jobs.example.test/a","submit":"false"}`, "", false},
		{"evaluate_job with submit true", "evaluate_job", `{"url":"https://boards.example.test/a","submit":true}`, "", false},
		{"apply_from_list", "apply_from_list", `{}`, "", false},
		{"apply_from_list with submit false", "apply_from_list", `{"submit":false}`, "", false},
		{"prepare_registration", "prepare_registration", `{"url":"https://jobs.example.test/a"}`, "", false},
		{"answer_bank_save", "answer_bank_save", `{}`, "", false},
		{"forget_fact", "forget_fact", `{"id":"f1"}`, "", false},
		{"batch_skip_list", "batch_skip_list", `{}`, "", false},
		{"manage_scheduled_prompt", "manage_scheduled_prompt", `{}`, "", false},
		{"not an object", "save_fact", `[1]`, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Target(c.tool, json.RawMessage(c.args))
			if got != c.target || ok != c.ok {
				t.Fatalf("Target(%s, %s) = %q, %v; want %q, %v", c.tool, c.args, got, ok, c.target, c.ok)
			}
		})
	}
}

func TestCard_ExpiresTwelveHoursAfterCreationOnAFakeClock(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	c, err := r.s.Create(bg, card("s1", "save_fact", `{"body":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ExpiresAt.Equal(c.CreatedAt.Add(12 * time.Hour)) {
		t.Fatalf("expires %s after %s", c.ExpiresAt, c.CreatedAt)
	}
	var seen []Card
	r.s.OnExpired = func(_ context.Context, cs []Card) { seen = append(seen, cs...) }

	r.clock.advance(12*time.Hour - time.Millisecond)
	if pend, _ := r.s.Pending(bg); len(pend) != 1 {
		t.Fatalf("one millisecond before the end the card is gone: %d pending", len(pend))
	}
	r.clock.advance(time.Millisecond)
	if pend, _ := r.s.Pending(bg); len(pend) != 0 {
		t.Fatalf("the card is still pending at 12 hours: %d", len(pend))
	}
	if len(seen) != 1 || seen[0].State != Expired {
		t.Fatalf("the expiry hook saw %+v", seen)
	}
	if _, _, err := r.s.Approve(bg, c.ID, ActorDashboard, Once); !errors.Is(err, ErrConflict) {
		t.Fatalf("approving an expired card: %v, want conflict", err)
	}
	if _, err := r.s.Decline(bg, c.ID, ActorDashboard); !errors.Is(err, ErrConflict) {
		t.Fatalf("declining an expired card: %v, want conflict", err)
	}
	if n := countCode(r.audit, GateCard, CodeHoldExpired); n != 1 {
		t.Fatalf("hold_expired audit rows = %d, want 1: %v", n, r.audit.codes())
	}
}

func TestCard_ApprovingAtTheDeadlineExpiresItInstead(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	c, _ := r.s.Create(bg, card("s1", "save_fact", `{}`))
	r.clock.advance(CardTTL)
	if _, _, err := r.s.Approve(bg, c.ID, ActorDashboard, Once); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	got, _ := r.s.Get(bg, c.ID)
	if got.State != Expired {
		t.Fatalf("state = %s, want expired", got.State)
	}
	if n := countCode(r.audit, GateCard, CodeHoldExpired); n != 1 {
		t.Fatalf("hold_expired rows = %d", n)
	}
}

func TestCard_RestartExpiresEveryPendingCard(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	c1, _ := r.s.Create(bg, card("s1", "save_fact", `{"n":1}`))
	r.clock.advance(time.Hour)
	c2, _ := r.s.Create(bg, card("s1", "save_fact", `{"n":2}`))
	var seen []string
	r.s.OnExpired = func(_ context.Context, cs []Card) {
		for _, c := range cs {
			seen = append(seen, c.ID)
		}
	}
	if _, err := r.s.ExpireAll(bg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{c1.ID, c2.ID}) {
		t.Fatalf("the hook saw %v", seen)
	}
	for _, id := range seen {
		if _, _, err := r.s.Approve(bg, id, ActorDashboard, Once); !errors.Is(err, ErrConflict) {
			t.Fatalf("approve after restart: %v", err)
		}
	}
}

func TestCard_ARepeatedHoldReturnsTheWaitingCard(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	a, _ := r.s.Create(bg, card("s1", "save_fact", `{"body":"x"}`))
	b, _ := r.s.Create(bg, card("s1", "save_fact", `{"body":"x"}`))
	other, _ := r.s.Create(bg, card("s1", "save_fact", `{"body":"y"}`))
	if a.ID != b.ID || a.ID == other.ID {
		t.Fatalf("ids %s %s %s", a.ID, b.ID, other.ID)
	}
	if pend, _ := r.s.Pending(bg); len(pend) != 2 {
		t.Fatalf("pending = %d, want 2", len(pend))
	}
}

func TestApprove_WhoMayApproveWhat(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		tier    int
		target  bool
		by      Actor
		scope   Scope
		want    error
	}{
		{"dashboard once", "dashboard", 3, true, ActorDashboard, Once, nil},
		{"dashboard today", "dashboard", 3, true, ActorDashboard, Today, nil},
		{"dashboard conversation", "dashboard", 2, true, ActorDashboard, Conversation, nil},
		{"dashboard widens an ineligible card", "dashboard", 3, false, ActorDashboard, Today, ErrIneligible},
		{"dashboard submit once", "dashboard", 4, false, ActorDashboard, Once, nil},
		{"dashboard submit wider", "dashboard", 4, false, ActorDashboard, Today, ErrIneligible},
		{"dashboard submit with a target is still approved once only", "dashboard", 4, true, ActorDashboard, Today, ErrIneligible},
		{"dashboard submit with a target, for the conversation", "dashboard", 4, true, ActorDashboard, Conversation, ErrIneligible},
		{"telegram once on its own channel", "telegram", 3, true, ActorTelegram, Once, nil},
		{"telegram may not widen", "telegram", 3, true, ActorTelegram, Today, ErrNotPermitted},
		{"telegram may not widen conversation", "telegram", 2, true, ActorTelegram, Conversation, ErrNotPermitted},
		{"telegram may not approve a dashboard card", "dashboard", 3, true, ActorTelegram, Once, ErrNotPermitted},
		{"telegram may not approve a submit", "telegram", 4, false, ActorTelegram, Once, ErrNotPermitted},
		{"remote device once", "dashboard", 3, true, ActorRemoteDevice, Once, nil},
		{"remote device may not approve an outside-content hold", "dashboard", 3, true, ActorRemoteDevice, Once, ErrNotPermitted},
		{"dashboard approves an outside-content hold", "dashboard", 3, true, ActorDashboard, Once, nil},
		{"remote device may not widen", "dashboard", 3, true, ActorRemoteDevice, Today, ErrNotPermitted},
		{"remote device may not confirm a submit", "dashboard", 4, false, ActorRemoteDevice, Once, ErrNotPermitted},
		{"local token once", "dashboard", 3, true, ActorLocalToken, Once, nil},
		{"local token may not widen", "dashboard", 3, true, ActorLocalToken, Today, ErrNotPermitted},
		{"local token may not confirm a submit", "dashboard", 4, false, ActorLocalToken, Once, ErrNotPermitted},
		{"system never approves", "dashboard", 3, true, ActorSystem, Today, ErrNotPermitted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, time.UTC)
			r.session(t, "s1")
			n := card("s1", "save_fact", `{}`)
			n.Channel, n.Tier = c.channel, c.tier
			if c.name == "remote device once" {
				n.Code = "intent_not_stated" // not held for outside content, which a device may not approve
			}
			if !c.target {
				n.Target = ""
			}
			held, err := r.s.Create(bg, n)
			if err != nil {
				t.Fatal(err)
			}
			_, ap, err := r.s.Approve(bg, held.ID, c.by, c.scope)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			active, _ := r.s.Active(bg)
			wantAny := c.want == nil && c.scope != Once
			if (ap != nil) != wantAny || (len(active) == 1) != wantAny {
				t.Fatalf("approval created = %v, active = %d, want created %v", ap != nil, len(active), wantAny)
			}
			after, _ := r.s.Get(bg, held.ID)
			if (c.want == nil) != (after.State == Approved) {
				t.Fatalf("state after = %s for %v", after.State, c.want)
			}
		})
	}
}

func TestStanding_LifetimeEndsAtTheEarliestOfScopeAnd24Hours(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	cases := []struct {
		name  string
		start time.Time
		scope Scope
		end   time.Time
	}{
		{"today at 08:00 ends at the next midnight", time.Date(2027, 3, 3, 8, 0, 0, 0, ny), Today, time.Date(2027, 3, 4, 0, 0, 0, 0, ny)},
		{"today a minute before midnight", time.Date(2027, 3, 3, 23, 59, 0, 0, ny), Today, time.Date(2027, 3, 4, 0, 0, 0, 0, ny)},
		{"today on a 25-hour day is capped at 24 hours", time.Date(2027, 11, 7, 0, 30, 0, 0, ny), Today, time.Date(2027, 11, 7, 0, 30, 0, 0, ny).Add(24 * time.Hour)},
		{"conversation lasts 24 hours at most", time.Date(2027, 3, 3, 8, 0, 0, 0, ny), Conversation, time.Date(2027, 3, 3, 8, 0, 0, 0, ny).Add(24 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, ny)
			r.clock.t = c.start
			r.session(t, "s1")
			held, _ := r.s.Create(bg, card("s1", "save_fact", `{}`))
			_, ap, err := r.s.Approve(bg, held.ID, ActorDashboard, c.scope)
			if err != nil || ap == nil {
				t.Fatalf("approve: %v %v", ap, err)
			}
			if !ap.ExpiresAt.Equal(c.end) {
				t.Fatalf("ends %s, want %s", ap.ExpiresAt.In(ny), c.end.In(ny))
			}
			r.clock.t = c.end.Add(-time.Millisecond)
			if _, ok, _ := r.s.Covers(bg, "save_fact", "save_fact", "s1"); !ok {
				t.Fatal("not covered just before the end")
			}
			r.clock.t = c.end
			if _, ok, _ := r.s.Covers(bg, "save_fact", "save_fact", "s1"); ok {
				t.Fatal("still covered at the end")
			}
			if act, _ := r.s.Active(bg); len(act) != 0 {
				t.Fatalf("active after the end: %d", len(act))
			}
		})
	}
}

func TestStanding_CoversOnlyTheExactToolAndTargetAndScope(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	r.session(t, "fork")
	eval := card("s1", "evaluate_job", `{"url":"https://boards.example.test/a"}`)
	held, _ := r.s.Create(bg, eval)
	if _, _, err := r.s.Approve(bg, held.ID, ActorDashboard, Today); err != nil {
		t.Fatal(err)
	}
	conv, _ := r.s.Create(bg, card("s1", "scan_portal", `{"source":"greenhouse"}`))
	if _, _, err := r.s.Approve(bg, conv.ID, ActorDashboard, Conversation); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, tool, target, session string
		want                        bool
	}{
		{"same host in the same conversation", "evaluate_job", "boards.example.test", "s1", true},
		{"a today approval follows to another conversation", "evaluate_job", "boards.example.test", "other", true},
		{"another host", "evaluate_job", "jobs.example.test", "s1", false},
		{"another tool on the host", "scan_portal", "boards.example.test", "s1", false},
		{"conversation scope in its conversation", "scan_portal", "greenhouse", "s1", true},
		{"conversation scope does not pass to a fork", "scan_portal", "greenhouse", "fork", false},
		{"another source", "scan_portal", "lever", "s1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok, err := r.s.Covers(bg, c.tool, c.target, c.session)
			if err != nil || ok != c.want {
				t.Fatalf("covers = %v, %v; want %v", ok, err, c.want)
			}
		})
	}
	act, _ := r.s.Active(bg)
	uses := 0
	for _, a := range act {
		uses += a.Uses
	}
	if uses != 3 || countCode(r.audit, GateStanding, "") != 3+2 {
		t.Fatalf("uses = %d; audit = %v", uses, r.audit.codes())
	}
}

func TestStanding_ConversationApprovalEndsWithItsConversation(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	held, _ := r.s.Create(bg, card("s1", "save_fact", `{}`))
	if _, _, err := r.s.Approve(bg, held.ID, ActorDashboard, Conversation); err != nil {
		t.Fatal(err)
	}
	r.deleteSession(t, "s1")
	if act, _ := r.s.Active(bg); len(act) != 0 {
		t.Fatalf("active after the conversation was deleted: %d", len(act))
	}
	if _, ok, _ := r.s.Covers(bg, "save_fact", "save_fact", "s1"); ok {
		t.Fatal("covered after the conversation ended")
	}
}

func TestStanding_RevokeAppliesToTheNextCallAndIsAudited(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	held, _ := r.s.Create(bg, card("s1", "save_fact", `{}`))
	_, ap, _ := r.s.Approve(bg, held.ID, ActorDashboard, Today)
	if _, ok, _ := r.s.Covers(bg, "save_fact", "save_fact", "s1"); !ok {
		t.Fatal("not covered before the revoke")
	}
	if _, err := r.s.Revoke(bg, ap.ID, ActorRemoteDevice); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.s.Covers(bg, "save_fact", "save_fact", "s1"); ok {
		t.Fatal("covered after the revoke")
	}
	if _, err := r.s.Revoke(bg, ap.ID, ActorDashboard); !errors.Is(err, ErrConflict) {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := r.s.Revoke(bg, "grant_missing", ActorDashboard); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revoke: %v", err)
	}
	want := []string{"standing_approval/" + CodeApprovalCreated, "standing_approval/" + GrantPrefix + ap.ID, "standing_approval/" + CodeApprovalRevoked}
	var got []string
	for _, c := range r.audit.codes() {
		if strings.HasPrefix(c, "standing_approval/") {
			got = append(got, c)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("standing audit = %v, want %v", got, want)
	}
}

func TestStanding_AtMostTwentyAreActive(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	for i := 0; i < MaxActive; i++ {
		held, _ := r.s.Create(bg, card("s1", "scan_portal", `{"source":"src`+string(rune('a'+i))+`"}`))
		if _, _, err := r.s.Approve(bg, held.ID, ActorDashboard, Today); err != nil {
			t.Fatalf("approval %d: %v", i, err)
		}
	}
	extra, _ := r.s.Create(bg, card("s1", "scan_portal", `{"source":"zzz"}`))
	if _, _, err := r.s.Approve(bg, extra.ID, ActorDashboard, Today); !errors.Is(err, ErrLimit) {
		t.Fatalf("21st approval: %v, want the limit", err)
	}
	after, _ := r.s.Get(bg, extra.ID)
	if after.State != Pending {
		t.Fatalf("the card was consumed by a refused approval: %s", after.State)
	}
	if _, _, err := r.s.Approve(bg, extra.ID, ActorDashboard, Once); err != nil {
		t.Fatalf("a one-time approval is not limited: %v", err)
	}
}

func TestStanding_ApprovedAndDeclinedCardsAreAudited(t *testing.T) {
	r := newRig(t, time.UTC)
	r.session(t, "s1")
	fromTelegram := card("s1", "save_fact", `{"n":1}`)
	fromTelegram.Channel = "telegram"
	a, _ := r.s.Create(bg, fromTelegram)
	b, _ := r.s.Create(bg, card("s1", "save_fact", `{"n":2}`))
	if _, _, err := r.s.Approve(bg, a.ID, ActorTelegram, Once); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Decline(bg, b.ID, ActorDashboard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Approve(bg, a.ID, ActorDashboard, Once); !errors.Is(err, ErrConflict) {
		t.Fatalf("approving twice: %v, want conflict", err)
	}
	got := r.audit.codes()
	want := []string{"confirmation_card/" + CodeCardApproved, "confirmation_card/" + CodeCardDeclined}
	if !slices.Equal(got, want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	rows := r.audit.rows
	if rows[0].Caller != gates.CallerTelegram || rows[1].Caller != gates.CallerDashboard || rows[0].ArgHash != "h" {
		t.Fatalf("rows = %+v", rows)
	}
}

func countCode(a *auditLog, gate gates.Gate, code string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, r := range a.rows {
		if r.Gate == gate && (code == "" || r.Code == code) {
			n++
		}
	}
	return n
}
