package apply

import (
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Owners of a stopped row: who has the next step. The owner comes from the
// run's own outcome, never from a model.
const (
	OwnerUser        = "user"
	OwnerPosting     = "posting"
	OwnerEnvironment = "environment"
	OwnerDefect      = "defect"
)

// userBoard are the board reasons of a stop only the person can finish.
var userBoard = map[string]bool{
	"sign-in": true, "answer": true, "challenge": true, "review": true, "one-time-code": true,
	"may-have-submitted": true, "needs-user": true, "credential-refused": true, "lockout": true,
}

// challengeOutcome reports a stop on a bot check the person clears in the page.
// The board reason is not stored with a run, so the run's own reason words
// stand in for it once the outcome is read back.
func challengeOutcome(o Outcome) bool {
	return o.Board == "challenge" || challengeRE.MatchString(o.Reason)
}

// OwnerOf names who has the next step of a stopped outcome, or "" for a run
// that ended filled or submitted.
func OwnerOf(o Outcome) string {
	if o.Kind != OutcomeStopped {
		return ""
	}
	if userBoard[o.Board] || challengeOutcome(o) {
		return OwnerUser
	}
	switch o.Class {
	case ClassCredential, ClassAnswerGap, ClassVerification:
		return OwnerUser
	case ClassPosting:
		return OwnerPosting
	case ClassEnvironment, ClassUnavailable:
		return OwnerEnvironment
	}
	return OwnerDefect
}

var shapeDigitsRE = lazyregexp.New(`[0-9]+`)

// DefectShape is the key of a defect: stage, class and the reason with its
// digits collapsed, so two rows that failed the same way are one defect.
func DefectShape(o Outcome) string {
	return string(o.Stage) + "|" + string(o.Class) + "|" + shapeDigitsRE.ReplaceAllString(o.Reason, "#")
}

// Defect is one distinct defect of a list with the companies it hit and the
// newest snapshot directory.
type Defect struct {
	Stage     Stage
	Class     Class
	Reason    string
	Companies []string
	Snapshot  string
}

// OwnerGroup lists the companies of the stopped rows an owner has the next
// step of.
type OwnerGroup struct {
	Owner     string
	Companies []string
}

// ListAnswer is the list's account of its stopped rows.
type ListAnswer struct {
	Owners  []OwnerGroup
	Defects []Defect
}

var ownerOrder = []string{OwnerUser, OwnerPosting, OwnerEnvironment, OwnerDefect}

// Answer groups the stopped rows by owner and lists each distinct defect once.
// A row that has no run (skipped, not attempted) is not a stop. snapshotDir
// names the directory a run's snapshot lands in.
func Answer(results []ListResult, snapshotDir func(runID string) string) ListAnswer {
	byOwner := map[string][]string{}
	defects := map[string]*Defect{}
	var order []string
	for _, r := range results {
		if r.Run == nil {
			continue
		}
		o := r.Run.Outcome
		owner := OwnerOf(o)
		if owner == "" {
			continue
		}
		name := r.Row.Company
		if name == "" {
			name = hostOf(r.Row.URL)
		}
		byOwner[owner] = appendUnique(byOwner[owner], name)
		if owner != OwnerDefect {
			continue
		}
		key := DefectShape(o)
		d := defects[key]
		if d == nil {
			d = &Defect{Stage: o.Stage, Class: o.Class, Reason: shapeDigitsRE.ReplaceAllString(o.Reason, "#")}
			defects[key] = d
			order = append(order, key)
		}
		d.Companies = appendUnique(d.Companies, name)
		if snapshotDir != nil {
			d.Snapshot = snapshotDir(r.Run.ID)
		}
	}
	var out ListAnswer
	for _, owner := range ownerOrder {
		if cs := byOwner[owner]; len(cs) > 0 {
			sort.Strings(cs)
			out.Owners = append(out.Owners, OwnerGroup{Owner: owner, Companies: cs})
		}
	}
	for _, k := range order {
		out.Defects = append(out.Defects, *defects[k])
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return list
		}
	}
	return append(list, s)
}
