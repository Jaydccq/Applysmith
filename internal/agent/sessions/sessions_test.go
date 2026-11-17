package sessions

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { c.t = c.t.Add(time.Second); return c.t }

func open(t *testing.T) (*Store, *store.Store, *clock) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := &clock{t: time.Now().UTC().Add(-time.Hour)}
	return New(st, c.now), st, c
}

func say(t *testing.T, s *Store, id string, role Role, body string) Message {
	t.Helper()
	m, err := s.Append(context.Background(), Message{SessionID: id, TurnID: "t1", Role: role, Body: body, OK: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSessions_CreateListRenameDelete(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	a, err := s.Create(ctx, Dashboard, "First")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Create(ctx, Telegram, "")
	if _, err := s.Create(ctx, Channel("email"), ""); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	list, total, err := s.List(ctx, ListQuery{})
	if err != nil || total != 2 || len(list) != 2 || list[0].ID != b.ID {
		t.Fatalf("list = %v total %d err %v; want newest first", list, total, err)
	}
	if only, total, _ := s.List(ctx, ListQuery{Channel: Telegram}); total != 1 || only[0].ID != b.ID {
		t.Fatalf("channel filter = %v", only)
	}
	got, err := s.Rename(ctx, a.ID, "  Renamed  ")
	if err != nil || got.Title != "Renamed" {
		t.Fatalf("rename = %+v, %v", got, err)
	}
	if _, err := s.Rename(ctx, "ses_missing", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rename of a missing session = %v", err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted session still readable: %v", err)
	}
	if err := s.Delete(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestSessions_ListSearchMatchesTitleAndMessageText(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, Dashboard, "Offer comparison")
	b, _ := s.Create(ctx, Telegram, "")
	c, _ := s.Create(ctx, Dashboard, "")
	say(t, s, b.ID, RoleUser, "What is the relocation budget for Exampleville?")
	say(t, s, c.ID, RoleToolResult, "relocation appears only in tool output")
	say(t, s, c.ID, RoleUser, "unrelated")

	ids := func(q ListQuery) []string {
		list, total, err := s.List(ctx, q)
		if err != nil || total != len(list) {
			t.Fatalf("%+v: %v total %d len %d", q, err, total, len(list))
		}
		var out []string
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		q    ListQuery
		want []string
	}{
		{"title substring, any case", ListQuery{Search: "COMPARISON"}, []string{a.ID}},
		{"message word prefix", ListQuery{Search: "reloc"}, []string{b.ID}},
		{"every word must appear", ListQuery{Search: "relocation zanzibar"}, nil},
		{"tool output is not searched", ListQuery{Search: "output"}, nil},
		{"punctuation is plain text", ListQuery{Search: `relocation" OR "unrelated`}, nil},
		{"search with a channel", ListQuery{Search: "relocation", Channel: Dashboard}, nil},
		{"blank search lists all", ListQuery{Search: "  "}, []string{c.ID, b.ID, a.ID}},
	} {
		if got := ids(tc.q); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	page, total, _ := s.List(ctx, ListQuery{Search: "e", Limit: 1})
	if len(page) > 1 || total < len(page) {
		t.Errorf("paged search: %d rows of %d", len(page), total)
	}
}

func TestRecall_ReturnsAtMostFiveAndTreatsOperatorsAsText(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, Dashboard, "")
	for i := 0; i < 8; i++ {
		say(t, s, a.ID, RoleUser, "salary band discussion")
	}
	hits, err := s.Search(ctx, "salary", "")
	if err != nil || len(hits) != SnippetLimit {
		t.Fatalf("%d hits, %v; want %d", len(hits), err, SnippetLimit)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].MessageID < hits[i-1].MessageID {
			t.Fatalf("recall is not oldest first: %+v", hits)
		}
	}
	for _, q := range []string{`salary" OR "x`, `salary AND`, `sal*`, `NEAR(salary band)`, `(`, `"`, `   `, ``} {
		if _, err := s.Search(ctx, q, ""); err != nil {
			t.Errorf("query %q failed: %v", q, err)
		}
	}
	if hits, _ := s.Search(ctx, `salary band`, ""); len(hits) == 0 {
		t.Error("two plain words were not matched")
	}
	if hits, _ := s.Search(ctx, `salary OR band`, ""); len(hits) != 0 {
		t.Error("OR acted as an operator instead of a word")
	}
}

func TestRecall_DeleteRemovesTheIndexEntries(t *testing.T) {
	s, st, _ := open(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, Dashboard, "")
	say(t, s, a.ID, RoleUser, "zanzibar itinerary notes")
	if hits, _ := s.Search(ctx, "zanzibar", ""); len(hits) != 1 {
		t.Fatalf("hits before delete = %v", hits)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, "zanzibar", ""); len(hits) != 0 {
		t.Fatalf("deleted conversation still recalled: %+v", hits)
	}
	var n int
	if err := st.Read().QueryRow(`SELECT count(*) FROM chat_messages_fts WHERE chat_messages_fts MATCH 'zanzibar'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("index rows after delete = %d, %v", n, err)
	}
	if err := st.Read().QueryRow(`SELECT count(*) FROM chat_messages`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("message rows after delete = %d, %v", n, err)
	}
}

func TestFork_CopiesHistoryUpToTheMessageAndLeavesTheParentAlone(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	p, _ := s.Create(ctx, Telegram, "Plan")
	say(t, s, p.ID, RoleUser, "m1")
	m2 := say(t, s, p.ID, RoleAssistant, "m2")
	say(t, s, p.ID, RoleUser, "m3")
	if ok, err := s.Claim(ctx, "autofill_application", "call_9"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	before, _ := s.Get(ctx, p.ID)

	f, err := s.Fork(ctx, p.ID, m2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID == p.ID || f.ParentID != p.ID || f.ParentMessageID != m2.ID || f.Channel != Dashboard {
		t.Fatalf("fork = %+v", f)
	}
	msgs, _ := s.Messages(ctx, f.ID)
	if len(msgs) != 2 || msgs[0].Body != "m1" || msgs[1].Body != "m2" {
		t.Fatalf("fork history = %+v", msgs)
	}
	say(t, s, f.ID, RoleUser, "only in the fork")
	parent, _ := s.Messages(ctx, p.ID)
	if len(parent) != 3 {
		t.Fatalf("the parent has %d messages after a new message in the fork", len(parent))
	}
	after, _ := s.Get(ctx, p.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("the parent's update time moved")
	}
	if ok, _ := s.Claim(ctx, "autofill_application", "call_9"); ok {
		t.Error("a claim was not kept (the fork must not reset claims)")
	}
	other, _ := s.Create(ctx, Dashboard, "")
	if _, err := s.Fork(ctx, other.ID, m2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a message of another conversation = %v", err)
	}
	if _, err := s.Fork(ctx, "ses_missing", m2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missing parent = %v", err)
	}
}

func TestStandIns_AreAddedOnceForCallsWithoutAResult(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, Dashboard, "")
	call := func(turn, id, name string) {
		if _, err := s.Append(ctx, Message{SessionID: a.ID, TurnID: turn, Role: RoleToolCall, ToolName: name, ToolCallID: id, Body: "{}", OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	call("t1", "call_1", "scan_portal")
	call("t1", "call_2", "tracker_query")
	call("t2", "call_1", "tracker_query") // pi ids repeat across turns
	if added, err := s.AppendResult(ctx, Message{SessionID: a.ID, TurnID: "t1", ToolCallID: "call_2", Body: "3 rows", OK: true}); err != nil || !added {
		t.Fatal(added, err)
	}

	n, err := s.RecordStandIns(ctx, a.ID, "stand-in text")
	if err != nil || n != 2 {
		t.Fatalf("stand-ins = %d, %v; want 2", n, err)
	}
	if n, _ := s.RecordStandIns(ctx, a.ID, "stand-in text"); n != 0 {
		t.Fatalf("a second pass added %d", n)
	}
	var made []Message
	msgs, _ := s.Messages(ctx, a.ID)
	for _, m := range msgs {
		if m.Role == RoleToolResult && m.SystemMade {
			made = append(made, m)
		}
	}
	if len(made) != 2 || made[0].TurnID != "t1" || made[0].ToolCallID != "call_1" || made[1].TurnID != "t2" || made[0].OK {
		t.Fatalf("system-made results = %+v", made)
	}
	if added, _ := s.AppendResult(ctx, Message{SessionID: a.ID, TurnID: "t1", ToolCallID: "call_1", Body: "late real result", OK: true}); added {
		t.Error("a call got a second result")
	}
}

func TestTurns_RecordAndInterruptedOnesAreEnded(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, Dashboard, "")
	if err := s.StartTurn(ctx, Turn{ID: "turn_1", SessionID: a.ID, Channel: Dashboard, Origin: OriginModel}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartTurn(ctx, Turn{ID: "turn_2", SessionID: a.ID, Channel: Dashboard, Origin: OriginModel}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTurn(ctx, "turn_1", TurnFinal, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTurn(ctx, "turn_1", TurnError, "stuck", ""); err != nil {
		t.Fatal(err)
	}
	ids, err := s.EndInterruptedTurns(ctx, "runtime_unavailable", "the server stopped")
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("interrupted = %v, %v", ids, err)
	}
	turns, _ := s.Turns(ctx, a.ID)
	if len(turns) != 2 || turns[0].Status != TurnFinal || turns[0].Reason != "" {
		t.Fatalf("first turn = %+v: a finished turn must not be changed", turns[0])
	}
	if turns[1].Status != TurnError || turns[1].Reason != "runtime_unavailable" || turns[1].FinishedAt == nil {
		t.Fatalf("second turn = %+v", turns[1])
	}
}

func TestClaims_AreExclusiveSettleAndExpire(t *testing.T) {
	s, st, c := open(t)
	ctx := context.Background()
	if ok, err := s.Claim(ctx, "autofill_application", "k1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := s.Claim(ctx, "autofill_application", "k1"); ok {
		t.Fatal("a pair was claimed twice")
	}
	if ok, _ := s.Claim(ctx, "apply_from_list", "k1"); !ok {
		t.Fatal("the same key under another tool must be free")
	}
	if err := s.SettleClaim(ctx, "autofill_application", "k1", "timed out"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Claim(ctx, "autofill_application", "k1"); ok {
		t.Fatal("a settled claim can be claimed again")
	}
	c.t = c.t.Add(31 * 24 * time.Hour)
	if err := s.PruneClaims(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Read().QueryRow(`SELECT count(*) FROM agent_claims`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d claims kept past 30 days", n)
	}
}

func TestDecisions_AreAppendOnlyAndPrunedAfterRetention(t *testing.T) {
	s, st, _ := open(t)
	ctx := context.Background()
	now := time.Now()
	d := gates.Decision{Gate: "channel_envelope", Verdict: gates.Deny, Code: "submit_dashboard_only", Reason: "Telegram cannot submit"}
	row := gates.NewAuditRow(now.UnixMilli(), d, gates.AuditContext{Tool: "apply_from_list", TurnID: "turn_1", ConversationID: "ses_1", Caller: gates.CallerTelegram, RiskTier: "4", ArgFingerprint: "abc"})
	old := gates.NewAuditRow(now.Add(-200*24*time.Hour).UnixMilli(), d, gates.AuditContext{Tool: "apply_from_list", TurnID: "turn_old", Caller: gates.CallerTelegram})
	young := gates.NewAuditRow(now.Add(-100*24*time.Hour).UnixMilli(), d, gates.AuditContext{Tool: "apply_from_list", TurnID: "turn_young", Caller: gates.CallerTelegram})
	for _, r := range []gates.AuditRow{row, old, young} {
		if err := s.AppendDecision(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Decisions(ctx, "turn_1")
	if err != nil || len(got) != 1 || got[0].Code != "submit_dashboard_only" || got[0].RiskTier != "4" || got[0].Caller != gates.CallerTelegram || got[0].ArgHash != "abc" {
		t.Fatalf("decisions = %+v, %v", got, err)
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE gate_decisions SET code = 'x'`)
		return err
	})
	if err == nil {
		t.Fatal("a decision row was rewritten")
	}
	if err := s.PruneDecisions(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Decisions(ctx, "turn_old"); len(rows) != 0 {
		t.Error("a row past 180 days was kept")
	}
	if rows, _ := s.Decisions(ctx, "turn_young"); len(rows) != 1 {
		t.Error("a row younger than 180 days was pruned")
	}
}

// The prune removes a row only once it is older than the retention period: a
// row exactly 180 days old is kept.
func TestDecisions_PruneBoundaryIsARowOlderThanTheRetention(t *testing.T) {
	s, _, c := open(t)
	ctx := context.Background()
	c.t = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	pruneAt := c.t.Add(time.Second) // the instant the prune reads from the clock
	d := gates.Decision{Gate: "channel_envelope", Verdict: gates.Deny, Code: "submit_dashboard_only", Reason: "Telegram cannot submit"}
	ages := map[string]time.Duration{
		"turn_below": gates.AuditRetentionDays*24*time.Hour - time.Millisecond,
		"turn_at":    gates.AuditRetentionDays * 24 * time.Hour,
		"turn_above": gates.AuditRetentionDays*24*time.Hour + time.Millisecond,
	}
	for turn, age := range ages {
		if err := s.AppendDecision(ctx, gates.NewAuditRow(pruneAt.Add(-age).UnixMilli(), d, gates.AuditContext{TurnID: turn, Caller: gates.CallerTelegram})); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneDecisions(ctx); err != nil {
		t.Fatal(err)
	}
	for turn, kept := range map[string]int{"turn_below": 1, "turn_at": 1, "turn_above": 0} {
		if rows, err := s.Decisions(ctx, turn); err != nil || len(rows) != kept {
			t.Errorf("%s: %d rows after the prune (%v), want %d", turn, len(rows), err, kept)
		}
	}
}

func TestEnvelope_IsKeptWithTheTurnsLastAssistantMessageAndForks(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	c, _ := s.Create(ctx, Dashboard, "Cited")
	say(t, s, c.ID, RoleUser, "question")
	say(t, s, c.ID, RoleAssistant, "step text")
	last := say(t, s, c.ID, RoleAssistant, "final text")
	env := Envelope{Citations: []Citation{{Label: "profile#employment", Snippet: "Led a survey"}}, Limitations: []string{"no salary data"}, NextAction: "Review the draft"}
	if err := s.KeepEnvelope(ctx, c.ID, "t1", env); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepEnvelope(ctx, c.ID, "t9", env); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ctx, c.ID)
	for _, m := range msgs {
		want := m.ID == last.ID
		if got := len(m.Envelope.Citations) == 1 && m.Envelope.Citations[0] == env.Citations[0] && m.Envelope.NextAction == env.NextAction; got != want {
			t.Errorf("message %q carries the envelope = %v; want %v", m.Body, got, want)
		}
	}
	f, err := s.Fork(ctx, c.ID, last.ID)
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := s.Messages(ctx, f.ID)
	if got := copied[len(copied)-1].Envelope; len(got.Citations) != 1 || got.Limitations[0] != "no salary data" {
		t.Fatalf("the fork lost the envelope: %+v", got)
	}
}

// completeBetweenReads commits the answer and terminal state immediately before
// the detail reader asks for turns: the observed API failure's exact ordering.
type completeBetweenReads struct {
	querier
	complete func()
}

func (q completeBetweenReads) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM chat_turns") {
		q.complete()
	}
	return q.querier.QueryContext(ctx, query, args...)
}

func TestSessions_DetailKeepsCompletionAndMessagesInOneSnapshot(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate_reads_reproduce_failure", true: "snapshot_preserves_running_view"}[snapshot], func(t *testing.T) {
			s, db, _ := open(t)
			ctx := context.Background()
			sess, err := s.Create(ctx, Dashboard, "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.StartTurn(ctx, Turn{ID: "turn_snapshot", SessionID: sess.ID, Channel: Dashboard, Origin: "model"}); err != nil {
				t.Fatal(err)
			}
			say(t, s, sess.ID, RoleUser, "hello")
			var q querier = db.Read()
			if snapshot {
				tx, err := db.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				q = tx
			}
			q = completeBetweenReads{querier: q, complete: func() {
				say(t, s, sess.ID, RoleAssistant, "configure the model route")
				if err := s.FinishTurn(ctx, "turn_snapshot", TurnFinal, "", ""); err != nil {
					t.Fatal(err)
				}
			}}
			_, msgs, turns, err := readDetail(ctx, q, sess.ID)
			if err != nil || len(msgs) != 1 || len(turns) != 1 {
				t.Fatalf("messages=%+v turns=%+v error=%v", msgs, turns, err)
			}
			want := TurnFinal
			if snapshot {
				want = TurnRunning
			}
			if turns[0].Status != want {
				t.Fatalf("status=%s want=%s", turns[0].Status, want)
			}
			// A fresh request must see both committed rows, with no stale reader
			// state carried over from the previous snapshot.
			_, msgs, turns, err = s.Detail(ctx, sess.ID)
			if err != nil || len(msgs) != 2 || msgs[1].Role != RoleAssistant || len(turns) != 1 || turns[0].Status != TurnFinal {
				t.Fatalf("fresh messages=%+v turns=%+v error=%v", msgs, turns, err)
			}
		})
	}
	s, _, _ := open(t)
	if _, _, _, err := s.Detail(context.Background(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing detail=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := s.Detail(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled detail=%v", err)
	}
}
