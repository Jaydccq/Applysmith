// Clean-room: docs/provenance.md#CR-STATES
// Written from the application-tracker spec alone.

package tracker

import (
	"fmt"
	"slices"
)

// Actor is who makes a change. The server derives it from the authenticated
// ingress; no request body supplies it.
type Actor string

// The five actors that can change a status.
const (
	ActorUser       Actor = "user"
	ActorGmail      Actor = "gmail"
	ActorApplyChain Actor = "apply_chain"
	ActorAgent      Actor = "agent"
	ActorEvaluation Actor = "evaluation"
)

// ActorSystem is a scheduled job of the product. It is not one of the five
// actors of the transition table and may only archive and unarchive rows; every
// other operation refuses it as an unknown actor.
const ActorSystem Actor = "system"

// Actors lists the five actors that can change a status.
func Actors() []Actor {
	return []Actor{ActorUser, ActorGmail, ActorApplyChain, ActorAgent, ActorEvaluation}
}

func (a Actor) valid() bool { return slices.Contains(Actors(), a) }

// automated actors may never regress or reopen a row.
func (a Actor) automated() bool { return a.valid() && a != ActorUser }

type transition struct {
	from, to []Status
	actors   []Actor
}

// transitions is the spec's table for an existing row. The user's corrections
// outside it are handled by Decide.
var transitions = []transition{
	{[]Status{Evaluated}, []Status{Skipped}, []Actor{ActorUser, ActorAgent}},
	{[]Status{Skipped}, []Status{Evaluated}, []Actor{ActorUser, ActorAgent, ActorEvaluation}},
	{[]Status{Evaluated, Skipped}, []Status{Applied}, []Actor{ActorUser, ActorGmail, ActorApplyChain, ActorAgent}},
	// A posting the user discarded may still be applied to on their explicit
	// start (owner decision, docs/architecture/apply.md); the chain records the
	// submission it made. No list selects a discarded row.
	{[]Status{Discarded}, []Status{Applied}, []Actor{ActorApplyChain}},
	{[]Status{Evaluated, Skipped, Applied}, []Status{Responded, Interview, Rejected}, []Actor{ActorUser, ActorGmail}},
	{[]Status{Responded}, []Status{Interview, Rejected}, []Actor{ActorUser, ActorGmail}},
	{[]Status{Interview}, []Status{Rejected}, []Actor{ActorUser, ActorGmail}},
	{[]Status{Evaluated, Skipped, Applied, Responded, Interview}, []Status{Offer}, []Actor{ActorUser}},
	{[]Status{Offer}, []Status{Rejected}, []Actor{ActorUser}},
	// The agent discards a row on the user's word (owner decision,
	// docs/architecture/tracker.md).
	{[]Status{Evaluated, Skipped, Applied, Responded, Interview, Offer}, []Status{Discarded}, []Actor{ActorUser, ActorAgent}},
}

// newRows is the spec's table for a row that does not exist yet.
var newRows = []transition{
	{nil, []Status{Evaluated, Skipped}, []Actor{ActorEvaluation}},
	{nil, Statuses(), []Actor{ActorUser}},
	{nil, []Status{Applied}, []Actor{ActorApplyChain}},
}

// Verdict is the outcome of a transition check.
type Verdict int

const (
	// Noop: the row already has the status; nothing is written.
	Noop Verdict = iota
	// Allow: the transition table permits the change for the actor.
	Allow
	// Correct: only the user may make the change, recorded as a correction.
	Correct
	// Refuse: the actor may not make the change.
	Refuse
)

// Decision is the result of Decide.
type Decision struct {
	Verdict Verdict
	// Reason explains a refusal.
	Reason string
	// InferredApplied marks a change from evaluated or skipped straight past
	// applied, because the row was never marked applied.
	InferredApplied bool
}

func listed(rules []transition, from *Status, to Status, actor Actor) bool {
	for _, r := range rules {
		if (from == nil) != (r.from == nil) {
			continue
		}
		if from != nil && !slices.Contains(r.from, *from) {
			continue
		}
		if slices.Contains(r.to, to) && slices.Contains(r.actors, actor) {
			return true
		}
	}
	return false
}

// Decide checks a status change on an existing row.
func Decide(actor Actor, from, to Status) Decision {
	switch {
	case !actor.valid():
		return Decision{Verdict: Refuse, Reason: fmt.Sprintf("unknown actor %q", actor)}
	case !from.Valid() || !to.Valid():
		return Decision{Verdict: Refuse, Reason: "unknown status"}
	case from == to:
		return Decision{Verdict: Noop}
	case listed(transitions, &from, to, actor):
		inferred := (from == Evaluated || from == Skipped) &&
			(to == Responded || to == Interview || to == Offer || to == Rejected)
		return Decision{Verdict: Allow, InferredApplied: inferred}
	case actor == ActorUser:
		return Decision{Verdict: Correct}
	case actor.automated() && (from.Terminal() || from.Order() >= Applied.Order() && to.Order() <= from.Order()):
		return Decision{Verdict: Refuse, Reason: fmt.Sprintf("an automatic update never moves a row back or reopens a closed one (%s to %s)", from, to)}
	}
	return Decision{Verdict: Refuse, Reason: fmt.Sprintf("actor %s may not move a row from %s to %s", actor, from, to)}
}

// DecideNew checks the status a new row may start in.
func DecideNew(actor Actor, to Status) Decision {
	switch {
	case !actor.valid():
		return Decision{Verdict: Refuse, Reason: fmt.Sprintf("unknown actor %q", actor)}
	case !to.Valid():
		return Decision{Verdict: Refuse, Reason: "unknown status"}
	case listed(newRows, nil, to, actor):
		return Decision{Verdict: Allow}
	}
	return Decision{Verdict: Refuse, Reason: fmt.Sprintf("actor %s may not create a row as %s", actor, to)}
}

// Targets returns the statuses the user can move a row to as ordinary
// transitions, and separately those reachable only as a correction.
func Targets(from Status) (ordinary, correction []Status) {
	for _, to := range Statuses() {
		if to == from {
			continue
		}
		if listed(transitions, &from, to, ActorUser) {
			ordinary = append(ordinary, to)
		} else {
			correction = append(correction, to)
		}
	}
	return ordinary, correction
}
