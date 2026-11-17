package gmail

import (
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
)

// pacific is a zone west of UTC, where a date read as 00:00 UTC falls on the
// evening before in local time.
func pacific(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// The test clock starts on a Wednesday; this deadline is the Friday after it.
var friday = time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)

func (p *pipeEnv) remindersAt(loc *time.Location, y int, m time.Month, d, hh, mm int) []Reminder {
	p.t.Helper()
	at := time.Date(y, m, d, hh, mm, 0, 0, loc)
	p.clk.set(at)
	got, err := p.repo.Reminders(p.ctx, at, loc)
	if err != nil {
		p.t.Fatal(err)
	}
	return got
}

func keys(rs []Reminder) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Key)
	}
	return out
}

func TestReminders_TheEveningBeforeAndTheMorningOfADeadline(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeAssessment, employer: "Acme", role: "Backend Engineer", due: friday})
	cases := []struct {
		d, hh, mm int
		want      string
	}{
		{11, 18, 59, ""},
		{11, 19, 0, "gmail-reminder|t1|2027-03-12|eve"},
		{11, 23, 59, "gmail-reminder|t1|2027-03-12|eve"},
		{12, 8, 59, ""},
		{12, 9, 0, "gmail-reminder|t1|2027-03-12|day"},
		{12, 23, 59, "gmail-reminder|t1|2027-03-12|day"},
		{13, 9, 0, ""},
	}
	for _, c := range cases {
		got := keys(p.remindersAt(loc, 2027, 3, c.d, c.hh, c.mm))
		if want := []string{c.want}; c.want == "" && len(got) != 0 || c.want != "" && strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Mar %d %02d:%02d: %v, want %q", c.d, c.hh, c.mm, got, c.want)
		}
	}
	eve := p.remindersAt(loc, 2027, 3, 11, 19, 0)[0]
	if eve.Title != "Acme: online assessment, due tomorrow" || eve.Body != "Backend Engineer. Due Fri Mar 12." || eve.Link != "/gmail?thread=t1" || eve.ThreadID != "t1" {
		t.Fatalf("eve = %+v", eve)
	}
	if day := p.remindersAt(loc, 2027, 3, 12, 9, 0)[0]; day.Title != "Acme: online assessment, due today" {
		t.Fatalf("day = %+v", day)
	}
}

// A deadline with a time of day falls on its local day, and the morning's
// reminder is not sent once it passed.
func TestReminders_ADeadlineWithATimeEndsTheDaysReminder(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeActionNeeded, due: time.Date(2027, 3, 12, 15, 30, 0, 0, time.UTC)})
	if got := keys(p.remindersAt(loc, 2027, 3, 11, 19, 0)); len(got) != 1 || got[0] != "gmail-reminder|t1|2027-03-12|eve" {
		t.Fatalf("eve: %v", got)
	}
	if got := p.remindersAt(loc, 2027, 3, 11, 19, 0)[0].Body; got != "Due Fri Mar 12 07:30 PST." {
		t.Fatalf("body = %q", got)
	}
	if got := p.remindersAt(loc, 2027, 3, 12, 9, 0); len(got) != 0 {
		t.Fatalf("a reminder after the deadline: %v", keys(got))
	}
}

func TestReminders_OnlyOpenDeadlinesAreTodos(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(
		sig{id: "m1", thread: "done", typ: classify.TypeAssessment, due: friday},
		sig{id: "m2", thread: "dismissed", typ: classify.TypeReplyNeeded, due: friday},
		sig{id: "m3", thread: "interview", typ: classify.TypeInterview, due: friday},
		sig{id: "m4", thread: "nodue", typ: classify.TypeAssessment},
		sig{id: "m5", thread: "review", typ: classify.TypeAssessment, tier: classify.TierReview, reasons: "company_unknown", due: friday},
		sig{id: "m6", thread: "open", typ: classify.TypeReplyNeeded, due: friday},
	)
	if err := p.repo.MarkDone(p.ctx, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.repo.DismissThread(p.ctx, "dismissed"); err != nil {
		t.Fatal(err)
	}
	got := keys(p.remindersAt(loc, 2027, 3, 11, 19, 0))
	if len(got) != 1 || got[0] != "gmail-reminder|open|2027-03-12|eve" {
		t.Fatalf("reminders = %v, want only the open deadline", got)
	}
	if err := p.repo.ReopenDone(p.ctx, "done"); err != nil {
		t.Fatal(err)
	}
	if got := keys(p.remindersAt(loc, 2027, 3, 11, 19, 0)); len(got) != 2 {
		t.Fatalf("a reopened thread is a todo again: %v", got)
	}
}

// Tomorrow puts one reminder at 09:00 the next day in place of the slots up
// to then; the evening reminder that offered it is the last before it.
func TestReminders_SnoozeTakesThePlaceOfTheSlotsBeforeIt(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeAssessment, employer: "Acme", due: friday})
	p.remindersAt(loc, 2027, 3, 11, 19, 0)
	until, err := p.repo.Snooze(p.ctx, "t1", loc)
	if err != nil || !until.Equal(time.Date(2027, 3, 12, 9, 0, 0, 0, loc)) {
		t.Fatalf("Snooze = %v, %v", until, err)
	}
	if got := p.remindersAt(loc, 2027, 3, 12, 8, 59); len(got) != 0 {
		t.Fatalf("before the snooze: %v", keys(got))
	}
	got := p.remindersAt(loc, 2027, 3, 12, 9, 0)
	if len(got) != 1 || got[0].Key != "gmail-reminder|t1|2027-03-12|snooze-2027-03-12T17:00:00Z" || got[0].Title != "Acme: online assessment, due today" {
		t.Fatalf("at the snooze: %+v", got)
	}
	// Snoozed again on the day: one more the day after, which says it passed.
	if _, err := p.repo.Snooze(p.ctx, "t1", loc); err != nil {
		t.Fatal(err)
	}
	got = p.remindersAt(loc, 2027, 3, 13, 9, 30)
	if len(got) != 1 || got[0].Title != "Acme: online assessment, was due Fri Mar 12" {
		t.Fatalf("the day after: %+v", got)
	}
	if got := p.remindersAt(loc, 2027, 3, 13, 21, 30); len(got) != 0 {
		t.Fatalf("a snooze is caught up for 12 hours only: %v", keys(got))
	}
}

func TestReminders_SnoozeRefusesAThreadWithoutAnOpenDeadline(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeInterview, due: friday})
	if _, err := p.repo.Snooze(p.ctx, "t1", loc); !errors.Is(err, ErrNoDeadline) {
		t.Fatalf("interview: %v", err)
	}
	if _, err := p.repo.Snooze(p.ctx, "nope", loc); !errors.Is(err, ErrNoThread) {
		t.Fatalf("unknown: %v", err)
	}
	var n int
	if err := p.st.Read().QueryRow(`SELECT count(*) FROM gmail_reminder_snoozes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d snoozes stored (%v)", n, err)
	}
}

// Each thread action says what it did and has its undo; one that would change
// nothing is a refusal that names why.
func TestThreadActions_EachSaysWhatItDidAndHasItsUndo(t *testing.T) {
	p := newPipeEnv(t)
	loc := pacific(t)
	p.seed(
		sig{id: "m1", thread: "t1", typ: classify.TypeAssessment, employer: "Acme", due: friday},
		sig{id: "m2", thread: "rc", typ: classify.TypeApplied},
	)
	p.clk.set(time.Date(2027, 3, 10, 12, 0, 0, 0, loc))
	todo := func() bool {
		t.Helper()
		todos, err := p.repo.Todos(p.ctx, p.clk.now(), loc)
		if err != nil {
			t.Fatal(err)
		}
		return len(todos) == 1 && todos[0].Thread.ID == "t1"
	}
	act := func(action, id, want string, open bool) {
		t.Helper()
		said, err := p.repo.Act(p.ctx, action, id, loc)
		if err != nil || said != want || todo() != open {
			t.Fatalf("%s %s = %q, %v; open %v, want %q open %v", action, id, said, err, todo(), want, open)
		}
	}
	refused := func(action, id, why string, reason error) {
		t.Helper()
		_, err := p.repo.Act(p.ctx, action, id, loc)
		var r *Refusal
		if !errors.As(err, &r) || r.Why != why || reason != nil && !errors.Is(err, reason) {
			t.Fatalf("%s %s = %v, want the refusal %q", action, id, err, why)
		}
	}
	act(ActDone, "t1", "Marked done. New mail in this thread brings it back.", false)
	act(ActReopen, "t1", "Reopened.", true)
	act(ActDismiss, "t1", "Set aside as not job mail. New mail in this thread brings it back.", false)
	act(ActRestore, "t1", "Put back with the job mail.", true)
	refused(ActRestore, "t1", "nothing in this thread was set aside", nil)
	act(ActSnooze, "t1", "I will remind you again Thu Mar 11 at 09:00.", true)
	refused(ActDone, "rc", "this thread asks nothing of you now; nothing changed", ErrNotActionable)
	refused(ActSnooze, "rc", "this thread has no open deadline now; nothing changed", ErrNoDeadline)
	refused(ActDone, "nope", "this thread is no longer in Applysmith", ErrNoThread)
	if _, err := p.repo.Act(p.ctx, "send", "t1", loc); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("send = %v", err)
	}
	th, facts, err := p.repo.ThreadFacts(p.ctx, "t1")
	if err != nil || th.Employer != "Acme" || !strings.Contains(facts, "Subject: Update from Acme") || !strings.Contains(facts, "Body: body of m1") {
		t.Fatalf("facts of %q (%v):\n%s", th.ID, err, facts)
	}
}
