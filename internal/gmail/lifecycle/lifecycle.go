// Package lifecycle derives the state, action lane, owner and attention of one
// Gmail thread from its stored signals. The rules are fixed and table-tested;
// a model never decides a lane.
package lifecycle

import (
	"slices"
	"time"

	"applysmith/internal/gmail/classify"
)

// Lifecycle states, in rank order from the weakest.
const (
	StateNone       = ""
	StateApplied    = "applied"
	StateResponded  = "responded"
	StateAssessment = "assessment"
	StateInterview  = "interview"
	StateRejected   = "rejected"
	StateOffer      = "offer"
)

// Action lanes.
const (
	LaneDoNow       = "do_now"
	LanePrepare     = "prepare"
	LaneWaiting     = "waiting"
	LaneClosed      = "closed"
	LaneReceiptOnly = "receipt_only"
	LaneReview      = "review"
)

// Lanes lists the lanes in digest order.
var Lanes = []string{LaneDoNow, LanePrepare, LaneReview, LaneWaiting, LaneClosed, LaneReceiptOnly}

// Action owners.
const (
	OwnerUser    = "user"
	OwnerCompany = "company"
	OwnerNone    = "none"
	OwnerUnclear = "unclear"
)

// Attention levels.
const (
	AttentionUrgent = "urgent"
	AttentionAction = "action"
	AttentionStale  = "stale"
	AttentionInfo   = "info"
)

// Draft kinds.
const (
	DraftNone     = ""
	DraftReply    = "reply"
	DraftFollowUp = "follow_up"
)

// ContinuityRule is the rule id under which a review-tier incoming signal
// counts for the state of a thread that also holds an actionable one.
const ContinuityRule = "thread_continuity"

// Thresholds of the lane rules.
const (
	StaleDays          = 14
	PastDueSilenceDays = 7
	ExpireRequestDays  = 14
	ExpireFollowUpDays = 28
	UrgentWithin       = 48 * time.Hour
	day                = 24 * time.Hour
)

// Signal is the part of a stored signal the lifecycle reads.
type Signal struct {
	MessageID string
	Type      string
	Tier      string
	Direction string
	RuleID    string
	Received  time.Time
	// Due is zero when the signal states none.
	Due time.Time
}

// View is the derived reading of one thread.
type View struct {
	State      string
	Lane       string
	Owner      string
	Attention  string
	NextAction string
	DraftKind  string
	// StalenessDays is the whole days since the latest incoming event.
	StalenessDays int
	// Latest is the incoming event that decided the state.
	Latest Signal
	// LatestRule is the rule id the event counted under: its own, or
	// ContinuityRule when it was promoted from the review tier.
	LatestRule string
	// UserReplied says the user's own mail came after the latest event.
	UserReplied bool
	// Due is the stated time of the latest event, zero when none.
	Due time.Time
}

var stateOf = map[string]string{
	classify.TypeApplied:      StateApplied,
	classify.TypeReplyNeeded:  StateResponded,
	classify.TypeActionNeeded: StateResponded,
	classify.TypeAssessment:   StateAssessment,
	classify.TypeInterview:    StateInterview,
	classify.TypeReminder:     StateInterview,
	classify.TypeRejection:    StateRejected,
	classify.TypeOffer:        StateOffer,
}

// StateOf maps a signal type to the state it reaches; the empty string for a
// type that reaches none.
func StateOf(signalType string) string { return stateOf[signalType] }

// Rank orders the states for equal received times: offer, rejected,
// interview, assessment, responded, applied.
func Rank(state string) int {
	switch state {
	case StateOffer:
		return 6
	case StateRejected:
		return 5
	case StateInterview:
		return 4
	case StateAssessment:
		return 3
	case StateResponded:
		return 2
	case StateApplied:
		return 1
	}
	return 0
}

func submitted(s Signal) bool {
	return s.Type == classify.TypeAssessment && s.RuleID == classify.RuleAssessmentSubmitted
}

// typed reports whether the signal's type reaches a lifecycle state.
func typed(s Signal) bool { return StateOf(s.Type) != StateNone }

// Derive reads one thread. ok is false when the thread holds no incoming
// signal of tier actionable or review, which keeps it out of the inbox.
func Derive(signals []Signal, now time.Time) (View, bool) {
	var actionable, reviewable []Signal
	var outgoing []Signal
	shown := false
	for _, s := range signals {
		switch {
		case s.Tier == classify.TierSuppressed || s.Tier == classify.TierLow:
		case s.Direction == classify.DirOutgoing:
			outgoing = append(outgoing, s)
		case s.Tier == classify.TierActionable && typed(s):
			actionable = append(actionable, s)
			shown = true
		case s.Tier == classify.TierActionable || s.Tier == classify.TierReview:
			shown = true
			if s.Tier == classify.TierReview && typed(s) {
				reviewable = append(reviewable, s)
			}
		}
	}
	if !shown {
		return View{}, false
	}
	events := actionable
	promoted := map[string]bool{}
	if len(actionable) > 0 {
		for _, s := range reviewable {
			if !submitted(s) {
				events = append(events, s)
				promoted[s.MessageID] = true
			}
		}
	}
	if len(events) == 0 {
		return View{Lane: LaneReview, Owner: OwnerUnclear, Attention: AttentionInfo, NextAction: "Review this thread"}, true
	}
	latest := events[0]
	for _, s := range events[1:] {
		if s.Received.After(latest.Received) || s.Received.Equal(latest.Received) && Rank(StateOf(s.Type)) > Rank(StateOf(latest.Type)) {
			latest = s
		}
	}
	v := View{State: StateOf(latest.Type), Latest: latest, LatestRule: latest.RuleID, Due: latest.Due}
	if promoted[latest.MessageID] {
		v.LatestRule = ContinuityRule
	}
	v.StalenessDays = max(int(now.Sub(latest.Received)/day), 0)
	for _, o := range outgoing {
		if o.Received.After(latest.Received) {
			v.UserReplied = true
		}
	}
	lane(&v, latest, now)
	expire(&v, now)
	v.Attention = attention(v, now)
	return v, true
}

func lane(v *View, latest Signal, now time.Time) {
	set := func(l, owner, next, draft string) { v.Lane, v.Owner, v.NextAction, v.DraftKind = l, owner, next, draft }
	waiting := func(next string) { set(LaneWaiting, OwnerCompany, next, DraftNone) }
	dueAhead := !v.Due.IsZero() && v.Due.After(now)
	switch {
	case v.State == StateOffer:
		set(LaneClosed, OwnerNone, "Offer received", DraftNone)
	case v.State == StateRejected:
		set(LaneClosed, OwnerNone, "Rejected", DraftNone)
	case v.State == StateInterview && dueAhead:
		set(LanePrepare, OwnerUser, "Prepare for the interview", DraftNone)
	case submitted(latest):
		if v.StalenessDays >= StaleDays && !v.UserReplied {
			set(LaneDoNow, OwnerUser, "Follow up on the submitted assessment", DraftFollowUp)
		} else {
			waiting("Assessment submitted; waiting for the company")
		}
	case v.State == StateInterview:
		pastDue := !v.Due.IsZero() && !dueAhead
		switch {
		case v.UserReplied:
			waiting("Waiting to hear back after the interview")
		case v.StalenessDays >= StaleDays || pastDue && v.StalenessDays >= PastDueSilenceDays:
			set(LaneDoNow, OwnerUser, "Follow up after the interview", DraftFollowUp)
		default:
			waiting("Waiting to hear back after the interview")
		}
	case latest.Type == classify.TypeReplyNeeded || latest.Type == classify.TypeActionNeeded:
		if v.UserReplied {
			waiting("You replied; waiting for the company")
		} else {
			set(LaneDoNow, OwnerUser, "Reply to the message", DraftReply)
		}
	case v.State == StateAssessment:
		if v.UserReplied {
			waiting("You replied; waiting for the company")
		} else {
			set(LaneDoNow, OwnerUser, "Complete the assessment", DraftNone)
		}
	case v.UserReplied, v.State == StateResponded:
		waiting("Waiting for the company")
	case v.State == StateApplied:
		set(LaneReceiptOnly, OwnerNone, "Application received", DraftNone)
	default:
		set(LaneReview, OwnerUnclear, "Review this thread", DraftNone)
	}
}

// expire closes user-owned work that nobody answered: a request after 14 days
// of company silence, a follow-up after 28, unless its stated time is ahead.
func expire(v *View, now time.Time) {
	if v.Lane != LaneDoNow || v.Owner != OwnerUser {
		return
	}
	if !v.Due.IsZero() && v.Due.After(now) {
		return
	}
	limit := ExpireRequestDays
	if v.DraftKind == DraftFollowUp {
		limit = ExpireFollowUpDays
	}
	if v.StalenessDays >= limit {
		v.Lane, v.Owner, v.NextAction, v.DraftKind = LaneClosed, OwnerNone, "Expired", DraftNone
	}
}

// Actionable reports whether the thread asks something of the user, which is
// what marking it done ends.
func (v View) Actionable() bool {
	return v.Lane == LaneDoNow || v.Lane == LanePrepare || v.Lane == LaneReview
}

// Done is the reading of a thread the user marked done: the action is over, so
// it leaves the lanes that ask for one and rests in the closed lane with its
// state kept.
func (v View) Done() View {
	v.Lane, v.Owner, v.Attention, v.NextAction, v.DraftKind = LaneClosed, OwnerNone, AttentionInfo, "Marked done", DraftNone
	return v
}

func attention(v View, now time.Time) string {
	acting := v.Lane == LaneDoNow || v.Lane == LanePrepare
	switch {
	case v.State == StateOffer || v.State == StateRejected:
		return AttentionUrgent
	case acting && !v.Due.IsZero() && v.Due.Sub(now) <= UrgentWithin:
		return AttentionUrgent
	case acting:
		return AttentionAction
	case v.State == StateApplied && v.StalenessDays >= StaleDays:
		return AttentionStale
	}
	return AttentionInfo
}

// LaneOrder is the position of a lane in digest order; unknown lanes sort last.
func LaneOrder(lane string) int {
	if i := slices.Index(Lanes, lane); i >= 0 {
		return i
	}
	return len(Lanes)
}
