package learn

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

func (e *env) approve(id int64) (Decision, error) { return e.s.Approve(context.Background(), id, "") }

func asConflict(t *testing.T, err error, reason string) {
	t.Helper()
	var c *ConflictError
	if !errors.As(err, &c) || c.Reason != reason {
		t.Fatalf("err = %v, want conflict %q", err, reason)
	}
}

func (e *env) entry(id string) match.Entry {
	e.t.Helper()
	l, err := e.bank.File()
	if err != nil {
		e.t.Fatal(err)
	}
	en, ok := l.Bank.Find(id)
	if !ok {
		e.t.Fatalf("entry %q is not in the bank", id)
	}
	return en
}

func TestAnswerBank_Candidates_ApplyNewEntry(t *testing.T) {
	e := newEnv(t)
	e.fills(3, "Preferred editor", "Vim")
	for i := 0; i < 3; i++ {
		u := fillUpload("jobs.example.com", "Preferred region", "")
		u.ControlKind, u.OptionLabel, u.Options = "select", "West", []string{"East", "West"}
		e.ingest(u)
	}
	e.abstains(3, "Shirt size")
	e.generate()
	byQ := map[string]Candidate{}
	for _, c := range e.candidates("") {
		byQ[c.SeedQuestion] = c
	}
	// fixed, seeded.
	d, err := e.approve(byQ["Preferred editor"].ID)
	if err != nil {
		t.Fatal(err)
	}
	en := e.entry(d.EntryID)
	if en.ID != byQ["Preferred editor"].EntryID || en.Kind != match.KindFixed || en.Answer != "Vim" || en.Description != "Preferred editor" ||
		len(en.Examples) != 1 || en.Examples[0] != "Preferred editor" || en.State != match.StateActive || en.CreatedBy != match.CreatedByAgent ||
		!en.NeedsReview || en.Placeholder || en.CreatedAt != baseTime.Format(time.RFC3339) || len(en.AllowedOptions) != 0 {
		t.Fatalf("%+v", en)
	}
	// pick-option, seeded: the chosen label is the preferred value; the option list is not copied.
	d, _ = e.approve(byQ["Preferred region"].ID)
	en = e.entry(d.EntryID)
	if en.Kind != match.KindPickOption || len(en.Preferred) != 1 || en.Preferred[0] != "West" || en.FallbackText != "West" || len(en.AllowedOptions) != 0 {
		t.Fatalf("%+v", en)
	}
	// Unseeded: the entry lands as a placeholder gap, not with an invented answer.
	d, _ = e.approve(byQ["Shirt size"].ID)
	en = e.entry(d.EntryID)
	if !en.Placeholder || en.Answer != "" || !en.NeedsReview || en.CreatedBy != match.CreatedByAgent {
		t.Fatalf("%+v", en)
	}
	// The result loads and validates as a bank.
	if l, err := answerbank.Parse(workspace.FileAnswerBank.Name(), []byte(e.bankText())); err != nil || len(l.Warnings) != 0 || len(l.Bank.Entries) != 3 {
		t.Fatalf("%+v %v", l.Warnings, err)
	}
	// Refused when the id is taken by another entry; the candidate stays pending.
	e.abstains(3, "Another open question")
	e.generate()
	var c Candidate
	for _, x := range e.candidates(StatePending) {
		c = x
	}
	e.writeBank(fixedEntry(c.EntryID, "x", []string{"unrelated"}))
	_, err = e.approve(c.ID)
	asConflict(t, err, ReasonDuplicateID)
	if e.only(StatePending).ID != c.ID {
		t.Fatal("a refused approval leaves the candidate pending")
	}
}

// A suggestion seen on a group of checkboxes becomes a multi-select entry
// that ticks the options its answer names; an answer naming none of them is
// refused, and taking the approval back removes the entry it wrote.
func TestAnswerBank_Candidates_ACheckboxGroupTicksTheOptionsItsAnswerNames(t *testing.T) {
	const q = "Which of the following languages are you professionally proficient in?"
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		e.record(Event{Kind: KindAbstained, At: e.clk.now().Add(-time.Duration(i+1) * time.Minute), Host: "jobs.example.com", Question: q,
			ControlKind: "checkbox", Options: []string{"Python", "Java", "Go", "TypeScript", "C++"}, Outcome: "abstained", Reason: "no-entry", ReportNumber: int64(900 + i)})
	}
	e.generate()
	c := e.only(StatePending)
	_, err := e.s.Approve(context.Background(), c.ID, "Rust and Kotlin")
	asConflict(t, err, ReasonNotAnOption)
	d, err := e.s.Approve(context.Background(), c.ID, "Go and TypeScript every day, some C++")
	if err != nil {
		t.Fatal(err)
	}
	en := e.entry(d.EntryID)
	if en.Kind != match.KindMultiSelect || strings.Join(en.Selected, "|") != "Go|TypeScript|C++" || en.Placeholder || !en.NeedsReview || en.CreatedBy != match.CreatedByAgent {
		t.Fatalf("%+v", en)
	}
	if _, err := e.s.Unapprove(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.bank.File(); slices.ContainsFunc(l.Bank.Entries, func(x match.Entry) bool { return x.ID == d.EntryID }) {
		t.Fatal("the take-back leaves the entry it wrote")
	}
}

func TestAnswerBank_Candidates_ApplyExtendExamples(t *testing.T) {
	e := newEnv(t)
	e.writeBank(
		fixedEntry("availability", "Flexible", []string{"What are your preferred working hours?"}, "category: availability"),
		fixedEntry("other", "Yes", []string{"Are you open to travel?"}),
		fixedEntry("pinned-one", "Yes", []string{"Pinned base question?"}, "pinned: true"),
	)
	propose := func(entry, q string) Candidate {
		e.record(Event{Kind: KindMatch, At: baseTime.Add(-time.Hour), Host: "jobs.example.com", Question: q, ControlKind: "text", Lane: LaneModelOption, EntryID: entry, Value: "x"})
		e.generate()
		for _, c := range e.candidates(StatePending) {
			if c.SeedQuestion == q {
				return c
			}
		}
		t.Fatalf("no candidate for %q", q)
		return Candidate{}
	}
	before := e.entry("availability")
	c := propose("availability", "Which of these describes your preferred working hours?")
	d, err := e.approve(c.ID)
	if err != nil || d.EntryID != "availability" {
		t.Fatal(err)
	}
	after := e.entry("availability")
	if len(after.Examples) != 2 || after.Examples[1] != "Which of these describes your preferred working hours?" {
		t.Fatalf("%+v", after.Examples)
	}
	after.Examples = before.Examples
	if after.Answer != before.Answer || after.Category != before.Category || after.Description != before.Description || after.State != before.State || after.NeedsReview != before.NeedsReview {
		t.Fatalf("approval changed other fields: %+v vs %+v", after, before)
	}
	// Taking it back removes the phrasing and nothing else.
	if _, err := e.s.Unapprove(context.Background(), c.ID); err != nil || !reflect.DeepEqual(e.entry("availability"), before) {
		t.Fatalf("take-back: %v, %+v", err, e.entry("availability"))
	}
	e.approve(c.ID)
	// Scenario "Example collision refused": the phrasing is already an example of another entry.
	c2 := propose("availability", "Are you open to travel on short notice?")
	e.writeBank(
		fixedEntry("availability", "Flexible", []string{"What are your preferred working hours?"}),
		fixedEntry("other", "Yes", []string{"Are you open to travel on short notice"}),
	)
	_, err = e.approve(c2.ID)
	asConflict(t, err, ReasonExampleCollision)
	if e.only(StatePending).ID != c2.ID {
		t.Fatal("the candidate stays pending")
	}
	// Idempotent: the example is already there.
	e.writeBank(fixedEntry("availability", "Flexible", []string{"What are your preferred working hours?", "Are you open to travel on short notice!"}))
	text := e.bankText()
	if _, err := e.approve(c2.ID); err != nil || e.bankText() != text {
		t.Fatalf("an existing example is a no-op: %v", err)
	}
	// The target is gone: not found, still pending.
	c3 := propose("availability", "Yet another hours phrasing")
	e.writeBank(fixedEntry("other", "Yes", []string{"Are you open to travel?"}))
	_, err = e.approve(c3.ID)
	if !errors.Is(err, ErrNotFound) || e.only(StatePending).ID != c3.ID {
		t.Fatalf("%v", err)
	}
	// A pinned entry is never changed by an approval.
	e.writeBank(fixedEntry("availability", "Flexible", []string{"What are your preferred working hours?"}, "pinned: true"))
	c4 := Candidate{}
	e.exec(`UPDATE answer_candidates SET target_entry_id = 'availability' WHERE id = ?`, c3.ID)
	c4.ID = c3.ID
	_, err = e.approve(c4.ID)
	asConflict(t, err, ReasonPinned)
	if len(e.entry("availability").Examples) != 1 {
		t.Fatal("a pinned entry was changed")
	}
}

func TestAnswerBank_Candidates_ApprovalIsAtomic(t *testing.T) {
	e := newEnv(t)
	e.abstains(3, heardQ)
	e.generate()
	c := e.only(StatePending)
	if _, err := e.approve(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	// A bank that does not load: the write fails and the candidate stays pending.
	e.writeBank()
	good := e.bankText()
	if err := writeRaw(e, "version: 7\nentries: []\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.approve(c.ID); err == nil {
		t.Fatal("approval against a broken bank must fail")
	}
	if got := e.only(StatePending); got.ID != c.ID || e.count("answer_events WHERE kind = 'candidate-approved'") != 0 {
		t.Fatal("a failed approval leaves the candidate pending and records nothing")
	}
	if err := writeRaw(e, good); err != nil {
		t.Fatal(err)
	}
	d, err := e.approve(c.ID)
	if err != nil || d.Candidate.State != StateApproved || d.Candidate.DecidedAt == nil {
		t.Fatalf("%+v %v", d, err)
	}
	// Only a pending candidate can be decided; the second call is refused whole.
	text := e.bankText()
	_, err = e.approve(c.ID)
	asConflict(t, err, ReasonNotPending)
	_, err = e.s.Reject(context.Background(), c.ID)
	asConflict(t, err, ReasonNotPending)
	if e.bankText() != text {
		t.Fatal("a refused approval wrote the bank")
	}
	// Two concurrent approvals write once.
	e.abstains(3, "Another open question")
	e.generate()
	c2 := e.only(StatePending)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := e.approve(c2.ID); errs <- err }()
	}
	var ok, refused int
	for i := 0; i < 2; i++ {
		if err := <-errs; err == nil {
			ok++
		} else {
			refused++
		}
	}
	if ok != 1 || refused != 1 || strings.Count(e.bankText(), c2.EntryID) != 1 {
		t.Fatalf("ok %d refused %d\n%s", ok, refused, e.bankText())
	}
}

func writeRaw(e *env, text string) error {
	return os.WriteFile(e.ws.Path(workspace.FileAnswerBank), []byte(text), 0o600)
}

func TestAnswerBank_Candidates_ApprovalIgnoresPreexistingIssues(t *testing.T) {
	e := newEnv(t)
	// The bank already holds a problem: a fixed entry with an empty body that is
	// not marked placeholder. Every save of the whole bank would fail on it.
	e.writeBank("  - id: broken\n    description: d\n    kind: fixed\n    answer: \"\"\n")
	e.abstains(3, heardQ)
	e.generate()
	c := e.only(StatePending)
	if _, err := e.approve(c.ID); err != nil {
		t.Fatalf("a problem the bank already had must not hold the approval hostage: %v", err)
	}
	if !strings.Contains(e.bankText(), c.EntryID) {
		t.Fatal("the entry was not written")
	}
	// A problem the candidate itself introduces still refuses it.
	e.abstains(3, "Another open question")
	e.generate()
	var bad Candidate
	for _, x := range e.candidates(StatePending) {
		bad = x
	}
	e.exec(`UPDATE answer_candidates SET entry_id = '' WHERE id = ?`, bad.ID)
	if _, err := e.approve(bad.ID); err == nil || !strings.Contains(err.Error(), "validation_failed") {
		t.Fatalf("%v", err)
	}
	if e.only(StatePending).ID != bad.ID {
		t.Fatal("still pending")
	}
}

func TestAnswerBank_Candidates_Reject(t *testing.T) {
	e := newEnv(t)
	e.abstains(3, heardQ)
	e.generate()
	c := e.only(StatePending)
	text := e.bankText()
	d, err := e.s.Reject(context.Background(), c.ID)
	if err != nil || d.Candidate.State != StateRejected || d.Candidate.DecidedAt == nil || e.bankText() != text {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := e.s.Reject(context.Background(), c.ID); err == nil {
		t.Fatal("only a pending candidate can be rejected")
	}
	if _, err := e.s.Reject(context.Background(), 4242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// The key suppresses re-proposals until the evidence doubles.
	e.abstains(1, heardQ)
	e.generate()
	if len(e.candidates(StatePending)) != 0 {
		t.Fatal("suppressed")
	}
}

// Undo of a rejection (flow round 12, U-1, U-2): the suggestion waits again in
// its place, undecided, and the rejection's event is withdrawn. A suggestion
// no longer rejected, or one the bank settled since, stays as it is, and a
// second undo changes nothing.
func TestAnswerBank_Candidates_RestoreUndoesARejection(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		press  func(e *env, c Candidate)
		reason string // the refusal; empty when the suggestion is restored
	}{
		{"rejected", func(e *env, c Candidate) { e.s.Reject(ctx, c.ID) }, ""},
		{"approved", func(e *env, c Candidate) { e.approve(c.ID) }, ReasonNotRejected},
		{"answered by an entry since", func(e *env, c Candidate) {
			e.s.Reject(ctx, c.ID)
			e.writeBank(fixedEntry("own-answer", "A colleague", []string{c.SeedQuestion}))
		}, ReasonEntryChanged},
		{"its entry id taken since", func(e *env, c Candidate) {
			e.s.Reject(ctx, c.ID)
			e.writeBank(fixedEntry(c.EntryID, "x", []string{"unrelated"}))
		}, ReasonEntryChanged},
	} {
		e := newEnv(t)
		e.abstains(3, heardQ)
		e.generate()
		e.clk.advance(time.Minute)
		e.abstains(3, "Another open question")
		e.generate()
		e.clk.advance(time.Minute)
		before := e.candidates("")
		older := before[1]
		c.press(e, older)
		decided := e.candidates("")
		_, err := e.s.Restore(ctx, older.ID)
		want := before
		if c.reason != "" {
			asConflict(t, err, c.reason)
			want = decided
		} else if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		e.s.Flush()
		rejectedEvents := 0
		if c.reason == ReasonEntryChanged {
			rejectedEvents = 1
		}
		if got := e.candidates(""); !reflect.DeepEqual(got, want) || e.count("answer_events WHERE kind = 'candidate-rejected'") != rejectedEvents {
			t.Errorf("%s: candidates = %+v, want %+v", c.name, got, want)
		}
		if _, err := e.s.Restore(ctx, older.ID); err == nil || !reflect.DeepEqual(e.candidates(""), want) {
			t.Errorf("%s: a second undo = %v", c.name, err)
		}
	}
}

// Undo of an approval: what it wrote leaves the bank, the suggestion waits
// again as it did (with the user's own answer as its proposal) and the
// take-back is recorded. One whose entry was edited since, one not approved,
// or one whose bank write fails stays as it is.
func TestAnswerBank_Candidates_UnapproveTakesAnApprovalBack(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		press  func(e *env, c Candidate)
		reason string // the refusal, or unwritable; empty when the approval is taken back
	}{
		{"approved", func(e *env, c Candidate) { e.approve(c.ID) }, ""},
		{"approved with the user's answer", func(e *env, c Candidate) { e.s.Approve(ctx, c.ID, "A friend") }, ""},
		{"rejected", func(e *env, c Candidate) { e.s.Reject(ctx, c.ID) }, ReasonNotApproved},
		{"its entry edited since", func(e *env, c Candidate) {
			d, _ := e.approve(c.ID)
			l, _ := e.bank.File()
			for i := range l.Bank.Entries {
				if l.Bank.Entries[i].ID == d.EntryID {
					l.Bank.Entries[i].Answer = "Own words"
				}
			}
			if _, err := e.bank.Save(l.Bank, l.Revision); err != nil {
				t.Fatal(err)
			}
		}, ReasonEntryChanged},
		{"the bank cannot be written", func(e *env, c Candidate) {
			e.approve(c.ID)
			if err := os.Chmod(e.ws.Root, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(e.ws.Root, 0o700) })
		}, "unwritable"},
	} {
		e := newEnv(t)
		e.abstains(3, heardQ)
		e.generate()
		waiting := e.only(StatePending)
		c.press(e, waiting)
		decided := e.candidates("")
		_, err := e.s.Unapprove(ctx, waiting.ID)
		e.s.Flush()
		l, _ := e.bank.File()
		_, held := l.Bank.Find(waiting.EntryID)
		want, taken := decided, 0
		switch c.reason {
		case "":
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			want, taken = []Candidate{waiting}, 1
			want[0].ProposedAnswer = decided[0].ProposedAnswer
		case "unwritable":
			if refused := new(ConflictError); err == nil || errors.As(err, &refused) {
				t.Errorf("%s: err = %v, want the write's failure", c.name, err)
			}
		default:
			asConflict(t, err, c.reason)
		}
		if got := e.candidates(""); !reflect.DeepEqual(got, want) || held != (c.reason == ReasonEntryChanged || c.reason == "unwritable") ||
			e.count("answer_events WHERE kind = 'candidate-approved' AND outcome = ?", OutcomeTakenBack) != taken {
			t.Errorf("%s: candidates = %+v, want %+v; entry held %v", c.name, got, want, held)
		}
	}
}

func TestAnswerBank_Candidates_ListOrder(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"Question alpha", "Question beta", "Question gamma", "Question delta", "Question epsilon"} {
		e.abstains(3, q)
		e.generate()
		e.clk.advance(time.Minute)
	}
	ids := map[string]int64{}
	for _, c := range e.candidates("") {
		ids[c.SeedQuestion] = c.ID
	}
	// beta approved, gamma then alpha rejected, delta and epsilon pending.
	if _, err := e.approve(ids["Question beta"]); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Question gamma", "Question alpha"} {
		if _, err := e.s.Reject(context.Background(), ids[q]); err != nil {
			t.Fatal(err)
		}
		e.clk.advance(time.Minute)
	}
	var order []string
	for _, c := range e.candidates("") {
		order = append(order, c.State+":"+c.SeedQuestion)
	}
	got := strings.Join(order, ",")
	// Pending first, then approved, then rejected; the newest proposal first
	// among the pending, the newest decision among the decided.
	if !strings.HasPrefix(got, "pending:") || !strings.HasSuffix(got, ",approved:Question beta,rejected:Question alpha,rejected:Question gamma") {
		t.Fatalf("%s", got)
	}
	if n := len(e.candidates(StatePending)); n != 2 {
		t.Fatalf("filter: %d", n)
	}
	pend := e.candidates(StatePending)
	if !pend[0].UpdatedAt.After(pend[1].UpdatedAt) && !pend[0].UpdatedAt.Equal(pend[1].UpdatedAt) {
		t.Fatalf("newest first: %+v", pend)
	}
}
