// Clean-room: docs/provenance.md#CR-RUNS-BOARD
// Clean-room: docs/provenance.md#CR-AGENT-HANDOVER

package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func waiting(r *rig, id string, kind Kind, origin Origin, reason string) {
	r.t.Helper()
	r.open(id, origin, kind)
	r.move(id, NeedsYou, MoveArgs{Reason: reason})
}

func TestNotify_Resume_DoneAppliesToAUserOrAgentApplyRunThatWaitsForAClearableReason(t *testing.T) {
	cases := []struct {
		name   string
		kind   Kind
		origin Origin
		to     State
		reason string
		ok     bool
	}{
		{"sign-in", KindApply, OriginAgent, NeedsYou, "sign-in", true},
		{"a held confirmation has no Done action", KindApply, OriginAgent, NeedsYou, "confirmation", false},
		{"a possible submission has none either", KindApply, OriginAgent, NeedsYou, "may-have-submitted", false},
		{"a blocked entry", KindApply, OriginAgent, Blocked, "credential-refused", false},
		{"a run the user started", KindApply, OriginUser, NeedsYou, "sign-in", true},
		{"a run a schedule started", KindApply, OriginSchedule, NeedsYou, "sign-in", false},
		{"a scan", KindScan, OriginAgent, NeedsYou, "sign-in", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.open("r-1", c.origin, c.kind)
			e := r.move("r-1", c.to, MoveArgs{Reason: c.reason})
			if got := CanResume(e); got != c.ok {
				t.Fatalf("CanResume = %v, want %v", got, c.ok)
			}
			got, err := r.board.Resume(bg, "r-1", "Resumed from the dashboard.")
			if !c.ok {
				if !errors.Is(err, ErrStateChanged) {
					t.Fatalf("err = %v, want ErrStateChanged", err)
				}
				if now, _ := r.board.Get(bg, "r-1"); now.State != c.to || now.Revision != e.Revision {
					t.Fatalf("the entry changed: %+v", now)
				}
				return
			}
			if err != nil || got.State != Working || got.Reason != "" || got.Detail != "Resumed from the dashboard." {
				t.Fatalf("resumed = %+v %v", got, err)
			}
		})
	}
}

// "Submit this form" settles the hold of a filled form waiting at Submit in its
// kept tab, of a run the user or the Ask Agent started (AC-6), once: the
// second confirmation of a settled hold fails.
func TestNotify_Resume_SubmitThisFormSettlesAFilledFormWaitingInItsKeptTabOnce(t *testing.T) {
	cases := []struct {
		name   string
		origin Origin
		reason string
		tab    string
		ok     bool
	}{
		{"a filled form waiting at Submit", OriginUser, "confirmation", "tab-1", true},
		{"its tab is gone", OriginUser, "confirmation", "", false},
		{"a filled form of a run the Ask Agent started", OriginAgent, "confirmation", "tab-1", true},
		{"a stop for an answer has Done instead", OriginUser, "answer", "tab-1", false},
		{"a possible submission", OriginUser, "may-have-submitted", "tab-1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.open("r-1", c.origin, KindApply)
			e := r.move("r-1", NeedsYou, MoveArgs{Reason: c.reason, FocusTab: c.tab})
			if got := CanSubmitKept(e); got != c.ok {
				t.Fatalf("CanSubmitKept = %v, want %v", got, c.ok)
			}
			got, err := r.board.ResumeToSubmit(bg, "r-1", "Submit this form confirmed.")
			if !c.ok {
				if now, _ := r.board.Get(bg, "r-1"); !errors.Is(err, ErrStateChanged) || now.State != NeedsYou || now.Revision != e.Revision {
					t.Fatalf("err = %v, entry %+v", err, now)
				}
				return
			}
			if err != nil || got.State != Working || got.FocusTab != c.tab {
				t.Fatalf("resumed = %+v %v", got, err)
			}
			if _, err := r.board.ResumeToSubmit(bg, "r-1", "Submit this form confirmed again."); !errors.Is(err, ErrStateChanged) {
				t.Fatalf("a second confirmation of the settled hold: %v", err)
			}
		})
	}
}

func TestNotify_Resume_OfTwoSimultaneousPressesTheSecondLoses(t *testing.T) {
	r := newRig(t)
	waiting(r, "r-1", KindApply, OriginAgent, "sign-in")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { _, errs[i] = r.board.Resume(bg, "r-1", "") })
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrStateChanged):
			t.Fatalf("a press failed for another reason: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d presses won, want exactly one", won)
	}
}

func TestNotify_Resume_LeavesTheInboxAndWithdrawsTheAnnouncement(t *testing.T) {
	r := newRig(t)
	waiting(r, "r-1", KindApply, OriginAgent, "sign-in")
	r.process()
	if r.tg.count() != 1 {
		t.Fatalf("announced %d times", r.tg.count())
	}
	if _, err := r.board.Resume(bg, "r-1", ""); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.board.Inbox(bg); len(in) != 0 {
		t.Fatalf("the inbox still holds %+v", in)
	}
	r.process()
	r.tg.mu.Lock()
	defer r.tg.mu.Unlock()
	if len(r.tg.withdrawn) != 1 || r.tg.withdrawn[0].Why != Handled {
		t.Fatalf("withdrawn = %+v", r.tg.withdrawn)
	}
}

type tell struct {
	seen *[]string
	name string
}

func (o tell) Changed(context.Context, *Entry, Entry) { *o.seen = append(*o.seen, o.name) }
func (o tell) Retired(context.Context, Entry)         {}
