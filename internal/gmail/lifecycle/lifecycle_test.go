package lifecycle

import (
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
)

var now = time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

func in(id, typ string, at time.Time) Signal {
	return Signal{MessageID: id, Type: typ, Tier: classify.TierActionable, Direction: classify.DirIncoming, Received: at}
}

func derive(t *testing.T, sigs ...Signal) View {
	t.Helper()
	v, ok := Derive(sigs, now)
	if !ok {
		t.Fatal("the thread is not in the inbox")
	}
	return v
}

func TestInbox_LifecycleLatestIncomingWinsTieRank(t *testing.T) {
	v := derive(t, in("a", classify.TypeApplied, ago(2*day)), in("b", classify.TypeInterview, ago(1*day)))
	if v.State != StateInterview {
		t.Errorf("newest event: %s", v.State)
	}
	// The older rejection loses to the newer receipt: the latest event decides.
	v = derive(t, in("a", classify.TypeRejection, ago(2*day)), in("b", classify.TypeApplied, ago(1*day)))
	if v.State != StateApplied {
		t.Errorf("latest, not strongest: %s", v.State)
	}
	// Equal times break by rank: offer, rejected, interview, assessment, responded, applied.
	order := []string{classify.TypeApplied, classify.TypeReplyNeeded, classify.TypeAssessment, classify.TypeInterview, classify.TypeRejection, classify.TypeOffer}
	want := []string{StateApplied, StateResponded, StateAssessment, StateInterview, StateRejected, StateOffer}
	for i := 1; i < len(order); i++ {
		v := derive(t, in("x", order[i-1], ago(day)), in("y", order[i], ago(day)))
		w := derive(t, in("y", order[i], ago(day)), in("x", order[i-1], ago(day)))
		if v.State != want[i] || w.State != want[i] {
			t.Errorf("%s vs %s: %s / %s, want %s", order[i-1], order[i], v.State, w.State, want[i])
		}
	}
}

func TestInbox_OutgoingAndLowTiersNeverAdvanceTheState(t *testing.T) {
	out := in("o", classify.TypeInterview, ago(1*day))
	out.Direction = classify.DirOutgoing
	low := in("l", classify.TypeOffer, ago(1*day))
	low.Tier = classify.TierLow
	sup := in("s", classify.TypeOffer, ago(1*day))
	sup.Tier = classify.TierSuppressed
	v := derive(t, in("a", classify.TypeApplied, ago(3*day)), out, low, sup)
	if v.State != StateApplied {
		t.Errorf("state = %s", v.State)
	}
	if _, ok := Derive([]Signal{out, low, sup}, now); ok {
		t.Error("a thread of outgoing, low and suppressed signals is in the inbox")
	}
}

func TestInbox_ReviewPromotedByThreadContinuity(t *testing.T) {
	rev := in("r", classify.TypeInterview, ago(1*day))
	rev.Tier = classify.TierReview
	v := derive(t, in("a", classify.TypeApplied, ago(3*day)), rev)
	if v.State != StateInterview || v.LatestRule != ContinuityRule {
		t.Errorf("promoted: %s %s", v.State, v.LatestRule)
	}
	// An assessment-submitted confirmation is never promoted.
	done := in("d", classify.TypeAssessment, ago(1*day))
	done.Tier, done.RuleID = classify.TierReview, classify.RuleAssessmentSubmitted
	v = derive(t, in("a", classify.TypeApplied, ago(3*day)), done)
	if v.State != StateApplied {
		t.Errorf("assessment confirmation promoted: %s", v.State)
	}
	// With no actionable incoming signal the review rows stay out and the lane is review.
	v = derive(t, rev)
	if v.State != StateNone || v.Lane != LaneReview || v.Owner != OwnerUnclear {
		t.Errorf("review only: %+v", v)
	}
}

func TestInbox_LaneRules(t *testing.T) {
	future, past := now.Add(7*day), now.Add(-2*day)
	with := func(s Signal, due time.Time) Signal { s.Due = due; return s }
	reply := func(at time.Time) Signal {
		s := in("u", classify.TypeReplyNeeded, at)
		s.Direction = classify.DirOutgoing
		return s
	}
	for _, c := range []struct {
		name  string
		sigs  []Signal
		lane  string
		owner string
		draft string
		next  string
	}{
		{"receipt", []Signal{in("a", classify.TypeApplied, ago(1*day))}, LaneReceiptOnly, OwnerNone, "", "Application received"},
		{"offer", []Signal{in("a", classify.TypeOffer, ago(1*day))}, LaneClosed, OwnerNone, "", ""},
		{"rejection", []Signal{in("a", classify.TypeRejection, ago(1*day))}, LaneClosed, OwnerNone, "", ""},
		{"interview ahead", []Signal{with(in("a", classify.TypeInterview, ago(1*day)), future)}, LanePrepare, OwnerUser, "", ""},
		{"interview, no date, fresh", []Signal{in("a", classify.TypeInterview, ago(3*day))}, LaneWaiting, OwnerCompany, "", ""},
		{"interview silent 14 days", []Signal{in("a", classify.TypeInterview, ago(14*day))}, LaneDoNow, OwnerUser, DraftFollowUp, ""},
		{"interview past due, 7 days silent", []Signal{with(in("a", classify.TypeInterview, ago(7*day)), past)}, LaneDoNow, OwnerUser, DraftFollowUp, ""},
		{"interview past due, 6 days silent", []Signal{with(in("a", classify.TypeInterview, ago(6*day)), past)}, LaneWaiting, OwnerCompany, "", ""},
		{"reply needed", []Signal{in("a", classify.TypeReplyNeeded, ago(1*day))}, LaneDoNow, OwnerUser, DraftReply, ""},
		{"action required", []Signal{in("a", classify.TypeActionNeeded, ago(1*day))}, LaneDoNow, OwnerUser, DraftReply, ""},
		{"assessment", []Signal{in("a", classify.TypeAssessment, ago(1*day))}, LaneDoNow, OwnerUser, "", ""},
		{"user replied after the request", []Signal{in("a", classify.TypeReplyNeeded, ago(2*day)), reply(ago(1 * day))}, LaneWaiting, OwnerCompany, "", ""},
		{"user reply before the request", []Signal{reply(ago(3 * day)), in("a", classify.TypeReplyNeeded, ago(2*day))}, LaneDoNow, OwnerUser, DraftReply, ""},
	} {
		v := derive(t, c.sigs...)
		if v.Lane != c.lane || v.Owner != c.owner || v.DraftKind != c.draft || c.next != "" && v.NextAction != c.next {
			t.Errorf("%s: lane %s owner %s draft %q next %q", c.name, v.Lane, v.Owner, v.DraftKind, v.NextAction)
		}
	}
	rev := in("r", classify.TypeInterview, ago(1*day))
	rev.Tier = classify.TierReview
	if v := derive(t, rev); v.Lane != LaneReview {
		t.Errorf("review tier: %s", v.Lane)
	}
}

func TestInbox_ThresholdsAtTheBoundary(t *testing.T) {
	// Interview follow-up at 14 days, not at 13 days 23 hours.
	if v := derive(t, in("a", classify.TypeInterview, ago(14*day-time.Hour))); v.Lane != LaneWaiting {
		t.Errorf("13 days 23 hours: %s", v.Lane)
	}
	if v := derive(t, in("a", classify.TypeInterview, ago(14*day))); v.Lane != LaneDoNow {
		t.Errorf("14 days: %s", v.Lane)
	}
	// A request expires at 14 days of silence.
	if v := derive(t, in("a", classify.TypeAssessment, ago(14*day-time.Hour))); v.Lane != LaneDoNow {
		t.Errorf("assessment at 13 days: %s", v.Lane)
	}
	if v := derive(t, in("a", classify.TypeAssessment, ago(14*day))); v.Lane != LaneClosed || v.NextAction != "Expired" {
		t.Errorf("assessment at 14 days: %s %s", v.Lane, v.NextAction)
	}
	// A follow-up expires at 28 days.
	if v := derive(t, in("a", classify.TypeInterview, ago(28*day-time.Hour))); v.Lane != LaneDoNow || v.DraftKind != DraftFollowUp {
		t.Errorf("follow-up at 27 days: %s", v.Lane)
	}
	if v := derive(t, in("a", classify.TypeInterview, ago(28*day))); v.Lane != LaneClosed || v.NextAction != "Expired" {
		t.Errorf("follow-up at 28 days: %s", v.Lane)
	}
	// Urgent when the due time is within 48 hours.
	due := func(d time.Duration) View {
		s := in("a", classify.TypeAssessment, ago(1*day))
		s.Due = now.Add(d)
		return derive(t, s)
	}
	if v := due(48 * time.Hour); v.Attention != AttentionUrgent {
		t.Errorf("due in 48h: %s", v.Attention)
	}
	if v := due(48*time.Hour + time.Minute); v.Attention != AttentionAction {
		t.Errorf("due in 48h and a minute: %s", v.Attention)
	}
}

func TestInbox_AssessmentSubmittedWaitsThenFollowUp(t *testing.T) {
	sub := func(at time.Time) Signal {
		s := in("a", classify.TypeAssessment, at)
		s.RuleID = classify.RuleAssessmentSubmitted
		return s
	}
	if v := derive(t, sub(ago(5*day))); v.Lane != LaneWaiting || v.Owner != OwnerCompany {
		t.Errorf("fresh: %+v", v)
	}
	if v := derive(t, sub(ago(14*day))); v.Lane != LaneDoNow || v.DraftKind != DraftFollowUp || v.Owner != OwnerUser {
		t.Errorf("14 days: %+v", v)
	}
}

func TestInbox_SilentUserWorkExpires(t *testing.T) {
	a := in("a", classify.TypeAssessment, ago(15*day))
	if v := derive(t, a); v.Lane != LaneClosed || v.NextAction != "Expired" || v.Owner != OwnerNone {
		t.Errorf("silent request: %+v", v)
	}
	a.Due = now.Add(day)
	if v := derive(t, a); v.Lane != LaneDoNow {
		t.Errorf("a due time still ahead keeps it: %+v", v)
	}
}

func TestInbox_AttentionLevels(t *testing.T) {
	for _, c := range []struct {
		name string
		sigs []Signal
		want string
	}{
		{"offer", []Signal{in("a", classify.TypeOffer, ago(day))}, AttentionUrgent},
		{"rejection", []Signal{in("a", classify.TypeRejection, ago(day))}, AttentionUrgent},
		{"reply needed", []Signal{in("a", classify.TypeReplyNeeded, ago(day))}, AttentionAction},
		{"fresh receipt", []Signal{in("a", classify.TypeApplied, ago(day))}, AttentionInfo},
		{"stale receipt", []Signal{in("a", classify.TypeApplied, ago(14*day))}, AttentionStale},
		{"expired", []Signal{in("a", classify.TypeAssessment, ago(20*day))}, AttentionInfo},
	} {
		if v := derive(t, c.sigs...); v.Attention != c.want {
			t.Errorf("%s: %s, want %s", c.name, v.Attention, c.want)
		}
	}
}

func TestInbox_EventToStateMap(t *testing.T) {
	for typ, want := range map[string]string{
		classify.TypeApplied: StateApplied, classify.TypeReplyNeeded: StateResponded, classify.TypeActionNeeded: StateResponded,
		classify.TypeAssessment: StateAssessment, classify.TypeInterview: StateInterview, classify.TypeReminder: StateInterview,
		classify.TypeRejection: StateRejected, classify.TypeOffer: StateOffer,
		classify.TypeReview: StateNone, classify.TypeLowPriority: StateNone,
	} {
		if got := StateOf(typ); got != want {
			t.Errorf("%s reaches %q, want %q", typ, got, want)
		}
	}
	// An assessment confirmation is an assessment signal that leaves the thread waiting.
	sub := in("a", classify.TypeAssessment, ago(day))
	sub.RuleID = classify.RuleAssessmentSubmitted
	if v := derive(t, sub); v.State != StateAssessment || v.Lane != LaneWaiting {
		t.Errorf("assessment submitted: %+v", v)
	}
}
