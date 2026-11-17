// Clean-room: docs/provenance.md#CR-STATES
// Written from the application-tracker spec alone.

// Package tracker owns the application rows in SQLite: the status table, the
// transition rules per actor, the append-only events, score and duplicate
// rules, dedup, archive and export.
package tracker

import (
	"fmt"
	"strings"
)

// Status is a tracker status. Values are lowercase ASCII with no extra text.
type Status string

// The eight statuses, in table order.
const (
	Evaluated Status = "evaluated"
	Skipped   Status = "skipped"
	Applied   Status = "applied"
	Responded Status = "responded"
	Interview Status = "interview"
	Offer     Status = "offer"
	Rejected  Status = "rejected"
	Discarded Status = "discarded"
)

// class puts a status into exactly one of the groups. The zero value is
// deliberately invalid, so a status added to the table without a class fails
// the classification test.
type class int

const (
	classUnset class = iota
	classOpen        // no application yet
	classLive        // submitted and still open
	classClosedSubmitted
	classClosedUnsubmitted
)

type statusInfo struct {
	status Status
	order  int
	class  class
}

// statusTable is the one place statuses are defined. Every list, group and
// ordering below is derived from it.
var statusTable = []statusInfo{
	{Evaluated, 0, classOpen},              // a report exists; not applied to or skipped
	{Skipped, 0, classOpen},                // recommended against, gated, screened out or skipped by the user
	{Applied, 1, classLive},                // an application was submitted
	{Responded, 2, classLive},              // the employer replied after the application
	{Interview, 3, classLive},              // an interview or assessment is scheduled or done
	{Offer, 4, classLive},                  // an offer was received
	{Rejected, 5, classClosedSubmitted},    // the employer declined
	{Discarded, 0, classClosedUnsubmitted}, // the user closed the row without a rejection
}

func lookup(s Status) (statusInfo, bool) {
	for _, info := range statusTable {
		if info.status == s {
			return info, true
		}
	}
	return statusInfo{}, false
}

// Statuses returns every status in table order.
func Statuses() []Status {
	out := make([]Status, len(statusTable))
	for i, info := range statusTable {
		out[i] = info.status
	}
	return out
}

// Valid reports whether s is in the table.
func (s Status) Valid() bool { _, ok := lookup(s); return ok }

// Order ranks a status by how far an application got. evaluated, skipped and
// discarded share the lowest rank; rejected follows everything else. It breaks
// ties between rows and decides what counts as moving backward for automated
// actors, and it is never evidence of which change came last.
func (s Status) Order() int { info, _ := lookup(s); return info.order }

// Submitted reports whether an application was submitted: applied, responded,
// interview, offer or rejected.
func (s Status) Submitted() bool {
	info, _ := lookup(s)
	return info.class == classLive || info.class == classClosedSubmitted
}

// Active reports whether a submitted application is still open.
func (s Status) Active() bool { info, _ := lookup(s); return info.class == classLive }

// Decided reports whether a decision was recorded: submitted, or discarded.
func (s Status) Decided() bool {
	info, _ := lookup(s)
	return s.Submitted() || info.class == classClosedUnsubmitted
}

// Terminal reports whether an automated actor may never leave the status, but
// for the apply chain recording its own submission on a discarded row.
func (s Status) Terminal() bool { return s == Rejected || s == Discarded }

// ParseStatus validates text against the table; the error lists the valid
// statuses.
func ParseStatus(text string) (Status, error) {
	if s := Status(text); s.Valid() {
		return s, nil
	}
	return "", fmt.Errorf("%w: unknown status %q; valid statuses are %s", ErrInvalid, text, statusList())
}

func statusList() string {
	names := make([]string, len(statusTable))
	for i, info := range statusTable {
		names[i] = string(info.status)
	}
	return strings.Join(names, ", ")
}
