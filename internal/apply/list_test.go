package apply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/gates"
)

// listProbe records how the rows of a list used the browser lane.
type listProbe struct {
	mu          sync.Mutex
	browser     int
	maxBrowser  int
	rows        int
	maxRows     int
	order       []string
	driveStarts map[string]time.Time
	script      map[string]func(e *Exec) *stop
	// awaitNext, when set, is the list's row count: a row's tail then waits
	// (up to its tail duration) for the next row to enter, so an overlap the
	// lane allows is shown by the next row's entry, not by timing.
	awaitNext int
	entries   int
	next      chan struct{}
}

func newListProbe() *listProbe {
	return &listProbe{driveStarts: map[string]time.Time{}, script: map[string]func(*Exec) *stop{}}
}

// drive stands in for the browser stages: it holds the lane while it works,
// hands it back the way the session does, then keeps working on the tail.
func (p *listProbe) drive(browserWork, tail time.Duration) func(context.Context, *Exec) *stop {
	return func(ctx context.Context, e *Exec) *stop {
		url := e.Run.Input.URL
		p.mu.Lock()
		p.rows++
		p.maxRows = max(p.maxRows, p.rows)
		p.browser++
		p.maxBrowser = max(p.maxBrowser, p.browser)
		p.order = append(p.order, url)
		idx := p.entries
		p.entries++
		if p.next != nil {
			close(p.next)
		}
		p.next = make(chan struct{})
		entered := p.next
		p.mu.Unlock()
		time.Sleep(browserWork)
		var st *stop
		if f := p.script[url]; f != nil {
			st = f(e)
		}
		if st != nil {
			e.Run.Outcome = Outcome{Kind: OutcomeStopped, Stage: st.at, Class: st.class, Reason: st.reason, Board: st.board}
		}
		p.mu.Lock()
		p.browser--
		p.mu.Unlock()
		e.Run.browserDone()
		switch {
		case p.awaitNext == 0:
			time.Sleep(tail)
		case idx+1 < p.awaitNext:
			select {
			case <-entered:
			case <-time.After(tail):
			case <-ctx.Done():
			}
		}
		p.mu.Lock()
		p.rows--
		p.mu.Unlock()
		return st
	}
}

func listRows(n int) []ListRow {
	rows := make([]ListRow, n)
	for i := range rows {
		rows[i] = ListRow{URL: fmt.Sprintf("https://host%d.example.test/jobs/%d", i, 1000+i), Company: fmt.Sprintf("Company%c Labs", 'A'+i), Role: fmt.Sprintf("Engineer %c", 'A'+i)}
	}
	return rows
}

func listOpts() ListOptions {
	return ListOptions{Caller: gates.CallerApplyList, Tool: gates.ToolApplyFromList}
}

func TestApplyList_Pool_TwoInFlightOneBrowserLane(t *testing.T) {
	p := newListProbe()
	p.awaitNext = 5
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(15*time.Millisecond, 5*time.Second) })
	res := g.r.RunList(context.Background(), listRows(5), listOpts())
	for i, r := range res {
		if r.Run == nil || r.Run.Outcome.Kind != OutcomeFilled {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	if p.maxBrowser != 1 {
		t.Fatalf("%d rows used the browser at once", p.maxBrowser)
	}
	if p.maxRows != 2 {
		t.Fatalf("at most %d rows in flight, want 2 (one tail overlapping the next row)", p.maxRows)
	}
	if g.env.attached != 1 {
		t.Fatalf("the browser was attached %d times for one list", g.env.attached)
	}
}

func TestApplyList_Lane_CredentialStopHoldsLaneUntilLanded(t *testing.T) {
	p := newListProbe()
	rows := listRows(2)
	p.script[rows[0].URL] = func(*Exec) *stop {
		return &stop{class: ClassCredential, reason: "every saved password was refused", at: StageAccount}
	}
	// Row 1's tail waits a while for row 2, which a held lane keeps out.
	p.awaitNext = 2
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(5*time.Millisecond, 200*time.Millisecond) })
	res := g.r.RunList(context.Background(), rows, listOpts())
	if res[0].Run == nil || res[1].Run == nil {
		t.Fatalf("results %+v", res)
	}
	if p.maxRows != 1 {
		t.Fatalf("%d rows in flight after a credential stop: the lane was handed back before the row landed", p.maxRows)
	}
	// A clean stop hands the lane back at once: the next row overlaps its tail.
	p2 := newListProbe()
	p2.script[rows[0].URL] = func(*Exec) *stop {
		return &stop{class: ClassAnswerGap, reason: "a required question has no data", at: StageCheck}
	}
	p2.awaitNext = 2
	g2 := newRig(t, func(d *Deps) { d.Drive = p2.drive(5*time.Millisecond, 5*time.Second) })
	g2.r.RunList(context.Background(), rows, listOpts())
	if p2.maxRows != 2 {
		t.Fatalf("an answer-gap stop kept the lane: %d rows in flight", p2.maxRows)
	}
}

func TestApplyList_RowsConflict_SameHostCompanyPostingSerialized(t *testing.T) {
	a := ListRow{URL: "https://one.example.test/jobs/1", Company: "Acme Robotics"}
	cases := []struct {
		name string
		b    ListRow
		want bool
	}{
		{"same host", ListRow{URL: "https://one.example.test/jobs/2", Company: "Other Inc"}, true},
		{"same company on another host", ListRow{URL: "https://two.example.test/jobs/1", Company: "Acme Robotics Inc"}, true},
		{"same posting under another query", ListRow{URL: "https://one.example.test/jobs/1?utm=x", Company: ""}, true},
		{"unrelated", ListRow{URL: "https://two.example.test/jobs/9", Company: "Zephyr Labs"}, false},
	}
	for _, c := range cases {
		if got := rowsConflict(a, c.b); got != c.want {
			t.Errorf("%s: conflict %v, want %v", c.name, got, c.want)
		}
	}
	// Three rows on one host and two on others: same-host rows never overlap.
	p := newListProbe()
	var mu sync.Mutex
	onHost, maxOnHost := 0, 0
	g := newRig(t, func(d *Deps) {
		inner := p.drive(5*time.Millisecond, 30*time.Millisecond)
		d.Drive = func(ctx context.Context, e *Exec) *stop {
			if hostOf(e.Run.Input.URL) == "shared.example.test" {
				mu.Lock()
				onHost++
				maxOnHost = max(maxOnHost, onHost)
				mu.Unlock()
				defer func() { mu.Lock(); onHost--; mu.Unlock() }()
			}
			return inner(ctx, e)
		}
	})
	rows := []ListRow{
		{URL: "https://shared.example.test/jobs/1", Company: "North A", Role: "R1"},
		{URL: "https://other1.example.test/jobs/1", Company: "South B", Role: "R2"},
		{URL: "https://shared.example.test/jobs/2", Company: "East C", Role: "R3"},
		{URL: "https://other2.example.test/jobs/1", Company: "West D", Role: "R4"},
		{URL: "https://shared.example.test/jobs/3", Company: "Up E", Role: "R5"},
	}
	g.r.RunList(context.Background(), rows, listOpts())
	if maxOnHost != 1 {
		t.Fatalf("%d rows of one host were in flight together", maxOnHost)
	}
	var shared []string
	for _, u := range p.order {
		if hostOf(u) == "shared.example.test" {
			shared = append(shared, u)
		}
	}
	if fmt.Sprint(shared) != fmt.Sprint([]string{rows[0].URL, rows[2].URL, rows[4].URL}) {
		t.Fatalf("same-host rows ran out of list order: %v", shared)
	}
}

func TestApplyList_FirstRunID_NamesTheFirstRunOnly(t *testing.T) {
	p := newListProbe()
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
	opt := listOpts()
	opt.FirstRunID = g.r.NewRunID()
	var started []string
	var mu sync.Mutex
	opt.Started = func(_ int, id string) { mu.Lock(); started = append(started, id); mu.Unlock() }
	res := g.r.RunList(context.Background(), listRows(3), opt)
	if res[0].Run == nil || res[0].Run.ID != opt.FirstRunID || len(started) != 3 || started[0] != opt.FirstRunID {
		t.Fatalf("the first run is not the named one: %+v %v", res[0].Run, started)
	}
	if res[1].Run.ID == opt.FirstRunID || res[2].Run.ID == opt.FirstRunID || res[1].Run.ID == res[2].Run.ID {
		t.Fatalf("a later run reused the id: %s %s", res[1].Run.ID, res[2].Run.ID)
	}
}

func TestApplyList_SameJobLinks_OnlyFirstReachingFormWins(t *testing.T) {
	p := newListProbe()
	rows := []ListRow{
		{URL: "https://mirror1.example.test/jobs/11", Company: "Acme Robotics", Role: "Platform Engineer"},
		{URL: "https://mirror2.example.test/jobs/22", Company: "Acme Robotics", Role: "Platform Engineer"},
		{URL: "https://mirror3.example.test/jobs/33", Company: "Acme Robotics", Role: "Platform Engineer"},
	}
	// The first link never reaches the form, so the next one is tried.
	p.script[rows[0].URL] = func(*Exec) *stop { return &stop{class: ClassPosting, reason: "posting closed", at: StageResolve} }
	p.script[rows[1].URL] = func(*Exec) *stop { return &stop{class: ClassAnswerGap, reason: "a question", at: StageCheck} }
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
	res := g.r.RunList(context.Background(), rows, listOpts())
	if res[0].Run == nil || res[1].Run == nil {
		t.Fatalf("the first two links should run: %+v", res)
	}
	if res[2].Run != nil || res[2].Note == "" {
		t.Fatalf("the third link ran after the second reached the form: %+v", res[2])
	}
}

func TestApplyList_RetrySweep_EnvironmentOnlyOnceNeverWithSubmission(t *testing.T) {
	p := newListProbe()
	rows := listRows(5)
	var calls sync.Map
	// Row 0 stops on the environment once; rows 2 and 4 always do. Rows 1 and 3
	// reach the form in between, so no repair is spent twice in a row.
	p.script[rows[0].URL] = func(e *Exec) *stop {
		n, _ := calls.LoadOrStore(e.Run.Input.URL, new(atomic.Int32))
		if n.(*atomic.Int32).Add(1) == 1 {
			return &stop{class: ClassEnvironment, reason: "the browser closed under the row", at: StageFill}
		}
		return nil
	}
	always := func(*Exec) *stop { return &stop{class: ClassEnvironment, reason: "browser closed", at: StageFill} }
	p.script[rows[2].URL], p.script[rows[4].URL] = always, always
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
	opt := listOpts()
	opt.HasSubmission = func(_ context.Context, r ListRow) (bool, error) { return r.URL == rows[2].URL, nil }
	started, landed := map[string]bool{}, map[string]bool{}
	var mu sync.Mutex
	opt.Started = func(_ int, id string) { mu.Lock(); started[id] = true; mu.Unlock() }
	opt.Landed = func(r ListResult) {
		if r.Run != nil {
			mu.Lock()
			landed[r.Run.ID] = true
			mu.Unlock()
		}
	}
	res := g.r.RunList(context.Background(), rows, opt)
	if res[0].Run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("row 0 was not replaced by its retry: %+v", res[0].Run.Outcome)
	}
	// Every run that started lands, the second run of a retried row too: the
	// caller closes what it opened for it.
	if len(started) != 7 || len(landed) != len(started) || !landed[res[0].Run.ID] || !landed[res[4].Run.ID] {
		t.Fatalf("%d runs started, %d landed: a retried row's second run never landed", len(started), len(landed))
	}
	if res[4].Run.Outcome.Kind != OutcomeStopped {
		t.Fatalf("a second environment stop is not retried again: %+v", res[4].Run.Outcome)
	}
	for url, want := range map[string]int{rows[0].URL: 2, rows[2].URL: 1, rows[4].URL: 2} {
		if got := g.count(`SELECT count(*) FROM apply_runs WHERE posting_url = ?`, url); got != want {
			t.Errorf("%s ran %d times, want %d (the first run stays in the record; a row with a submission is not retried)", url, got, want)
		}
	}
}

func TestApplyList_Drive_NamedRetryCoolingRepairAndDeadline(t *testing.T) {
	// A host that refused the saved passwords is cooled for the rest of the list.
	p := newListProbe()
	rows := []ListRow{
		{URL: "https://shared.example.test/jobs/1", Company: "North A", Role: "R1"},
		{URL: "https://shared.example.test/jobs/2", Company: "East C", Role: "R2"},
		{URL: "https://elsewhere.example.test/jobs/1", Company: "South B", Role: "R3", Named: true},
	}
	p.script[rows[0].URL] = func(e *Exec) *stop {
		e.Run.Signins = append(e.Run.Signins, SigninRecord{Host: "shared.example.test", Tried: []string{"settings-default", "alternate-1"}, Refusals: 2, Exhausted: true})
		return &stop{class: ClassCredential, reason: "refused", at: StageAccount, board: "credential-refused"}
	}
	var named atomic.Bool
	p.script[rows[2].URL] = func(e *Exec) *stop { named.Store(e.Run.Input.NamedRetry); return nil }
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
	res := g.r.RunList(context.Background(), rows, listOpts())
	if res[1].Run != nil || res[1].Note == "" {
		t.Fatalf("the later row on a cooled host ran: %+v", res[1])
	}
	if !named.Load() {
		t.Fatal("the row the user named did not run as a named retry")
	}
	if res[2].Run == nil {
		t.Fatal("a row of another host must still run")
	}

	// A blocked preflight attempts nothing; a list past its deadline starts nothing.
	g2 := newRig(t, nil)
	g2.env.bankN = 0
	res = g2.r.RunList(context.Background(), listRows(3), listOpts())
	mustStop(t, res[0].Run, StagePreflight, ClassEnvironment)
	for _, r := range res[1:] {
		if r.Run != nil || r.Note == "" {
			t.Fatalf("preflight blocked but a later row ran: %+v", r)
		}
	}
	if g2.env.attached != 1 {
		t.Fatalf("attached %d times", g2.env.attached)
	}
	p3 := newListProbe()
	g3 := newRig(t, func(d *Deps) { d.Drive = p3.drive(time.Millisecond, 0) })
	opt := listOpts()
	opt.Deadline = t0.Add(-time.Minute)
	for _, r := range g3.r.RunList(context.Background(), listRows(2), opt) {
		if r.Run != nil || r.Note != NoteDeadline {
			t.Fatalf("a row started after the deadline: %+v", r)
		}
	}
	// A cancelled list starts no further row.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range g3.r.RunList(ctx, listRows(2), listOpts()) {
		if r.Run != nil {
			t.Fatalf("a cancelled list started a row: %+v", r)
		}
	}
}

// A host's later rows in a list are skipped only after a real refusal there,
// with a note that says which: a refused saved password, lockout wording or a
// same-day hold. A credential stop that tried no password (none saved, a new
// account needed, Google sign-in only) or that came after a sign-in skips
// nothing.
func TestApplyList_OnlyARealRefusalSkipsTheHostsLaterRows(t *testing.T) {
	const host = "shared.example.test"
	for _, c := range []struct {
		name string
		rec  SigninRecord
		says string // "" when the later row runs
	}{
		{"no password tried", SigninRecord{Host: host, Session: "not available: no-cookies"}, ""},
		{"signed in, then stopped at a code", SigninRecord{Host: host, Tried: []string{"settings-default"}, Succeeded: "settings-default"}, ""},
		{"one refused password", SigninRecord{Host: host, Tried: []string{"settings-default"}, Refusals: 1}, "refused"},
		{"every saved password refused", SigninRecord{Host: host, Tried: []string{"settings-default", "alternate-1"}, Refusals: 2, Exhausted: true}, "refused"},
		{"lockout wording", SigninRecord{Host: host, Tried: []string{"settings-default"}, Refusals: 1, Lockout: true, Exhausted: true}, "lockout"},
		{"a same-day hold", SigninRecord{Host: host, Held: true}, "hold"},
	} {
		p := newListProbe()
		rows := []ListRow{{URL: "https://" + host + "/jobs/1", Company: "North A", Role: "R1"}, {URL: "https://" + host + "/jobs/2", Company: "East C", Role: "R2"}}
		p.script[rows[0].URL] = func(e *Exec) *stop {
			e.Run.Signins = append(e.Run.Signins, c.rec)
			return &stop{class: ClassCredential, reason: "stopped at the sign-in", at: StageAccount, board: "sign-in"}
		}
		g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
		res := g.r.RunList(context.Background(), rows, listOpts())
		switch {
		case c.says == "" && (res[1].Run == nil || res[1].Note != ""):
			t.Errorf("%s: the later row was skipped though no password was refused: %+v", c.name, res[1])
		case c.says != "" && (res[1].Run != nil || !strings.Contains(res[1].Note, host) || !strings.Contains(res[1].Note, c.says)):
			t.Errorf("%s: later row %+v, want it skipped with a note naming %s and %q", c.name, res[1], host, c.says)
		}
	}
}

func TestApplyList_RepeatedEnvironmentFault_AbortsTheList(t *testing.T) {
	p := newListProbe()
	rows := listRows(4)
	for _, r := range rows[:2] {
		p.script[r.URL] = func(*Exec) *stop {
			return &stop{class: ClassEnvironment, reason: "the apply tab could not be opened", at: StageAccount}
		}
	}
	g := newRig(t, func(d *Deps) { d.Drive = p.drive(time.Millisecond, 0) })
	res := g.r.RunList(context.Background(), rows, listOpts())
	if g.env.attached != 2 {
		t.Fatalf("the browser was attached %d times; one re-attach is allowed", g.env.attached)
	}
	for _, r := range res[2:] {
		if r.Run != nil || r.Note == "" {
			t.Fatalf("a row ran after the list aborted: %+v", r)
		}
	}

	// A row that reaches the form after the repair re-arms one more.
	p2 := newListProbe()
	p2.script[rows[0].URL] = func(*Exec) *stop { return &stop{class: ClassEnvironment, reason: "browser closed", at: StageAccount} }
	p2.script[rows[2].URL] = func(*Exec) *stop { return &stop{class: ClassEnvironment, reason: "browser closed", at: StageAccount} }
	g2 := newRig(t, func(d *Deps) { d.Drive = p2.drive(time.Millisecond, 0) })
	res = g2.r.RunList(context.Background(), rows, listOpts())
	if res[3].Run == nil {
		t.Fatalf("the list aborted although a row reached the form between the faults: %+v", res[3])
	}
	if g2.env.attached != 3 {
		t.Fatalf("attached %d times, want the first attach and two repairs", g2.env.attached)
	}
}

func TestApplyList_Journal_StartedRowIsNeverRerunAfterRestart(t *testing.T) {
	g := newRig(t, nil)
	now := t0
	j := NewBatchJournal(g.st, func() time.Time { return now })
	rows := listRows(5)
	ctx := context.Background()
	if err := j.Create(ctx, "list_1", BatchMeta{Caller: gates.CallerApplyList, Tool: gates.ToolApplyFromList, Submit: true, Explicit: true, Deadline: t0.Add(time.Hour)}, rows); err != nil {
		t.Fatal(err)
	}
	b := j.For("list_1")
	for i := range 2 {
		if err := b.Started(ctx, i); err != nil {
			t.Fatal(err)
		}
		b.Attach(ctx, i, fmt.Sprintf("run_%d", i))
		b.Ended(ctx, i, "")
	}
	if err := b.Started(ctx, 2); err != nil { // the app restarts while row 2 is started
		t.Fatal(err)
	}
	open, err := j.Recover(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("recover: %v %+v", err, open)
	}
	if fmt.Sprint(open[0].Index) != "[3 4]" || !open[0].Meta.Submit || !open[0].Meta.Explicit {
		t.Fatalf("resume at %v with %+v", open[0].Index, open[0].Meta)
	}
	// The list's own reader names the started row, and only it, as possibly
	// submitted.
	began, err := loadInterrupted(ctx, g.st.Read())
	if err != nil || len(began) != 1 || began[0].Key != postingKey(rows[2].URL) {
		t.Fatalf("interrupted rows %+v %v, want only the started row", began, err)
	}
	// Never past the original deadline.
	now = t0.Add(2 * time.Hour)
	open, err = j.Recover(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("a batch past its deadline resumed: %v %+v", err, open)
	}
	if g.count(`SELECT count(*) FROM apply_batches WHERE closed_at IS NOT NULL`) != 1 {
		t.Fatal("the expired batch was not closed")
	}
	// Closed batches are kept 7 days.
	now = t0.Add(9 * 24 * time.Hour)
	if err := j.Close(ctx, "list_1"); err != nil {
		t.Fatal(err)
	}
	if g.count(`SELECT count(*) FROM apply_batches`) != 0 || g.count(`SELECT count(*) FROM apply_batch_rows`) != 0 {
		t.Fatal("a batch closed more than 7 days ago is still journaled")
	}
}

func TestApply_Trigger_HashFromTurnMessageSurvivesRestart(t *testing.T) {
	g := newRig(t, nil)
	ctx := context.Background()
	const message = "apply to today's list"
	hash := gates.MessageHash(g.r.d.Salt, message)
	j := NewBatchJournal(g.st, func() time.Time { return t0 })
	rows := listRows(2)
	meta := BatchMeta{Caller: gates.CallerApplyList, Tool: gates.ToolApplyFromList, Deadline: t0.Add(time.Hour), ConversationID: "ses_1", MessageHash: hash}
	if err := j.Create(ctx, "list_t", meta, rows); err != nil {
		t.Fatal(err)
	}
	// The process restarts: the batch is read back and its rows run from the
	// journaled trigger alone, with no message text.
	open, err := j.Recover(ctx)
	if err != nil || len(open) != 1 || open[0].Meta.MessageHash != hash || open[0].Meta.ConversationID != "ses_1" {
		t.Fatalf("recover: %v %+v", err, open)
	}
	opt := listOpts()
	opt.BatchID, opt.ConversationID, opt.UserMessageHash = "list_t", open[0].Meta.ConversationID, open[0].Meta.MessageHash
	for _, r := range g.r.RunList(ctx, open[0].Rows, opt) {
		if r.Run == nil || r.Run.MessageHash != hash {
			t.Fatalf("a resumed row lost the trigger: %+v", r)
		}
	}
	if n := g.count(`SELECT count(*) FROM apply_runs WHERE trigger_message_hash = ? AND trigger_conversation_id = 'ses_1'`, hash); n != 2 {
		t.Fatalf("%d run records carry the trigger", n)
	}
	if n := g.count(`SELECT count(*) FROM gate_decisions WHERE user_message_hash = ?`, hash); n == 0 {
		t.Fatal("no gate decision of the resumed rows carries the hash")
	}
	// The text itself is nowhere in the journal.
	if n := g.count(`SELECT count(*) FROM apply_batches WHERE request LIKE ? OR trigger_message_hash LIKE ? OR trigger_conversation_id LIKE ?`, "%today%", "%today%", "%today%"); n != 0 {
		t.Fatal("the message text was journaled")
	}
}

func TestApplyList_Journal_DriverRecordsStartedBeforeTheChainRuns(t *testing.T) {
	g := newRig(t, nil)
	j := NewBatchJournal(g.st, func() time.Time { return t0 })
	rows := listRows(2)
	ctx := context.Background()
	if err := j.Create(ctx, "list_2", BatchMeta{Caller: gates.CallerApplyList, Tool: gates.ToolApplyFromList, Deadline: t0.Add(time.Hour)}, rows); err != nil {
		t.Fatal(err)
	}
	var seen []string
	g.r.d.Drive = func(ctx context.Context, e *Exec) *stop {
		var st string
		_ = g.st.Read().QueryRow(`SELECT state FROM apply_batch_rows WHERE batch_id = 'list_2' AND url = ?`, e.Run.Input.URL).Scan(&st)
		seen = append(seen, st)
		e.Run.browserDone()
		return nil
	}
	opt := listOpts()
	opt.BatchID, opt.Journal = "list_2", j.For("list_2")
	g.r.RunList(ctx, rows, opt)
	if fmt.Sprint(seen) != "[started started]" {
		t.Fatalf("states while the chain ran: %v", seen)
	}
	if g.count(`SELECT count(*) FROM apply_batch_rows WHERE batch_id = 'list_2' AND state = 'ended' AND run_id <> ''`) != 2 {
		t.Fatal("rows did not end with their run ids")
	}
}
