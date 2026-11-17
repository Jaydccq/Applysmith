package apply

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/answerbank/learn"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// ErrTooLarge is the receiving side rejecting a batch as too large.
var ErrTooLarge = errors.New("apply: the learning batch is too large")

const (
	maxEventBatch   = 200
	maxEventOptions = 200
)

// Provenance of a learning event the chain produced. ProvenanceBank opens the
// provenance of a check answer that is a bank entry's own, cited by the
// grounded rung and verified by the matcher; the entry id follows it.
// ProvenanceKnowledge opens that of a grounded answer citing a section of the
// user's knowledge base; the section's ref follows it (F-10).
const (
	ProvenanceCheck     = "check"
	ProvenanceBank      = "check:bank:"
	ProvenanceKnowledge = "check:kb:"
)

// provenance is the check provenance of an answer it wrote, naming the entry
// for one cited from the bank and the section for one citing the knowledge
// base.
func (w answerWrite) provenance() string {
	switch {
	case w.entry != "":
		return ProvenanceBank + w.entry
	case w.knowledge != "":
		return ProvenanceKnowledge + w.knowledge
	}
	return ProvenanceCheck
}

// LearnEvent is an engine event with the chain's provenance. A chain answer is
// provenance and never a correction: a correction is the user's ground truth.
type LearnEvent struct {
	engine.LearningEvent
	Provenance   string `json:"provenance,omitempty"`
	Trace        string `json:"trace,omitempty"`
	ReportNumber int64  `json:"reportNumber,omitempty"`
	RunID        string `json:"runId"`
	// EntryID is the answer-bank entry whose answer the run wrote into the
	// control a person's edit changed (entryNames), so the learning service
	// takes the edit as a correction of that entry.
	EntryID string `json:"entryId,omitempty"`
}

// bankWrite is a value the run wrote into a control from an answer-bank entry,
// as the page held it once the run was done with the page.
type bankWrite struct {
	// page is the top page's scheme, host and path.
	page, selector, question string
	// values are the value read back from the control and the chosen option's label.
	values  []string
	entryID string
}

// bankWrites lists the values the run wrote from answer-bank entries: every
// control the run filled whose matcher result filled it from an entry, and
// every one the check answered with an entry's own answer (ProvenanceBank). A
// value the check wrote otherwise counts only when it is the matcher result's
// own answer, so a fact's, a model's, the fix's or the profile's value names
// no entry, and neither does one the page held before the run.
func (s *session) bankWrites() []bankWrite {
	var out []bankWrite
	for _, ps := range s.pages {
		page, _ := pageOf(ps.report.URL)
		for _, r := range ps.rows {
			if r.Outcome != "filled" {
				continue
			}
			if id, cited := strings.CutPrefix(ps.answered[r.Selector], ProvenanceBank); cited {
				out = append(out, bankWrite{page: page, selector: r.Selector, question: r.Label, values: []string{r.Value, r.OptionLabel}, entryID: id})
				continue
			}
			m, ok := s.r.d.Page.Matches.get(ps.runID, r.Selector)
			if !ok || !(learn.Routed{EntryID: m.R.EntryID, Outcome: m.R.Outcome}).Fills() {
				continue
			}
			if ps.answered[r.Selector] != "" && normValue(firstNonEmpty(r.OptionLabel, r.Value)) != normValue(firstNonEmpty(m.R.ChosenLabel, m.R.Answer)) {
				continue
			}
			out = append(out, bankWrite{page: page, selector: r.Selector, question: r.Label, values: []string{r.Value, r.OptionLabel}, entryID: m.R.EntryID})
		}
	}
	return out
}

// held reports whether the write left v in its control.
func (w bankWrite) held(v string) bool {
	k := normValue(v)
	return k != "" && slices.ContainsFunc(w.values, func(x string) bool { return normValue(x) == k })
}

// entryNames names, for each edit a person makes after a run, the entry whose
// answer the edit replaced. A write names its entry for one edit only, so a
// control changed twice counts once against the entry; an edit built again
// after a failed post names it again.
type entryNames struct {
	writes []bankWrite
	// by maps a write to the event that named its entry.
	by map[int]string
}

// entryOf is the entry behind the manual fill ev: the write same picks for
// ev's control that left there the value ev replaced. Writes of two entries
// that both fit name neither, and so does an edit that carries no replaced
// value or one whose values the learning service withholds.
func (n *entryNames) entryOf(ev engine.LearningEvent, same func(bankWrite) bool) string {
	if learn.ClassifyEdit(ev.Question, ev.ControlKind, ev.InputType, ev.Autocomplete, ev.Options, ev.After, ev.OptionLabel, ev.Before) != learn.WithheldNone {
		return ""
	}
	at := -1
	for i, w := range n.writes {
		if !same(w) || !w.held(ev.Before) || n.by[i] != "" && n.by[i] != ev.EventID {
			continue
		}
		if at >= 0 && w.entryID != n.writes[at].entryID {
			return ""
		}
		if at < 0 {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	if n.by == nil {
		n.by = map[int]string{}
	}
	n.by[at] = ev.EventID
	return n.writes[at].entryID
}

// eventUUID is the lowercase UUID the receiver requires of an event id, derived
// from the name so a repeated post of the same event is a duplicate, not a new
// event.
func eventUUID(name string) string {
	h := sha256.Sum256([]byte(name))
	b := h[:16]
	b[6], b[8] = b[6]&0x0f|0x50, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// capOptions limits an event's option list to 200 while keeping the option
// that carries the value the event is about.
func capOptions(ev engine.LearningEvent) engine.LearningEvent {
	if len(ev.Options) <= maxEventOptions {
		return ev
	}
	keep := ev.OptionLabel
	if keep == "" {
		keep = ev.Current
	}
	out := make([]string, 0, maxEventOptions)
	has := false
	for _, o := range ev.Options {
		if o == keep && keep != "" {
			has = true
		}
	}
	for _, o := range ev.Options {
		if len(out) >= maxEventOptions-b2i(has && !slices.Contains(out, keep)) {
			break
		}
		out = append(out, o)
	}
	if has && !slices.Contains(out, keep) {
		out = append(out, keep)
	}
	ev.Options = out
	return ev
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PostBatches posts events in batches of at most 200 and, when the receiver
// rejects a batch as too large, splits it in half and retries until every
// event is accepted or a single event fails alone. It returns how many were
// accepted and the events that failed.
func PostBatches(ctx context.Context, p Poster, events []LearnEvent) (posted int, failed []LearnEvent, err error) {
	var post func([]LearnEvent) error
	post = func(batch []LearnEvent) error {
		if len(batch) == 0 {
			return nil
		}
		e := p.PostEvents(ctx, batch)
		switch {
		case e == nil:
			posted += len(batch)
			return nil
		case errors.Is(e, ErrTooLarge) && len(batch) > 1:
			mid := len(batch) / 2
			if err := post(batch[:mid]); err != nil {
				return err
			}
			return post(batch[mid:])
		case errors.Is(e, ErrTooLarge):
			failed = append(failed, batch...)
			return nil
		}
		return e
	}
	for i := 0; i < len(events); i += maxEventBatch {
		if e := post(events[i:min(i+maxEventBatch, len(events))]); e != nil {
			return posted, failed, e
		}
	}
	return posted, failed, nil
}

// collectEvents takes the engine's events from a page fill and tags them: an
// event whose selector the check answered carries check provenance, and a
// correction run carries its trace on each. An event from a form inside a
// blank or srcdoc frame has no host of its own (its location is about:) and
// takes the page's, as the hand-off recorder's frame events do; the learning
// service refuses an event without one.
func (s *session) collectEvents(res *engine.PageResult, ps *pageState, answered map[string]string) {
	run := s.e.Run
	var evs []engine.LearningEvent
	if res.Top.Summary != nil {
		evs = append(evs, res.Top.Summary.Events...)
	}
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil {
			evs = append(evs, f.Status.Summary.Events...)
		}
	}
	_, pageHost := pageOf(ps.report.URL)
	mk := func(ev engine.LearningEvent, prov string) LearnEvent {
		if ev.Host == "" {
			ev.Host = pageHost
		}
		le := LearnEvent{LearningEvent: capOptions(ev), Provenance: prov, RunID: run.ID}
		if run.Report != nil {
			le.ReportNumber = run.Report.Number
		}
		if run.Correction {
			le.Trace = gates.CorrectionTrace
		}
		return le
	}
	for _, ev := range evs {
		// A list the computer-use rung opened names the options it read, as the
		// engine's event names those of a list it opened (CO-1).
		if m, ok := s.r.d.Page.Matches.get(res.RunID, ev.Selector); ok && m.listed && len(ev.Options) == 0 {
			var options []string
			for _, o := range m.Q.Options {
				options = append(options, o.Label)
			}
			if learn.Classify(ev.Question, ev.ControlKind, ev.InputType, ev.Autocomplete, options) == learn.WithheldNone {
				ev.Options = options
			}
		}
		s.events = append(s.events, mk(ev, answered[ev.Selector]))
	}
}

// Lane is where a stopped run's lesson goes.
type Lane string

// The learning lanes by failure class. A machine that was not ready, a site
// that was down and a posting with nothing to apply to are not lessons about
// applying.
const (
	LaneNone       Lane = ""
	LaneUserAction Lane = "user-action"
	LaneCandidate  Lane = "answer-bank-candidate"
	LaneWorkItem   Lane = "work-item"
)

// LearningLane maps a failure class to its lane: credential to the needs-you
// inbox as a user action (so does verification, a code only the person can
// enter), answer-gap to an answer-bank candidate, defect to a
// developer work item that carries the run's snapshot path.
func LearningLane(c Class) Lane {
	switch c {
	case ClassCredential, ClassVerification:
		return LaneUserAction
	case ClassAnswerGap:
		return LaneCandidate
	case ClassDefect:
		return LaneWorkItem
	}
	return LaneNone
}

// laneNote is the stage-line suffix that names the lane of a stopped run.
func laneNote(run *Run) string {
	if run.Outcome.Kind != OutcomeStopped {
		return ""
	}
	lane := LearningLane(run.Outcome.Class)
	if challengeOutcome(run.Outcome) {
		// A bot check left to the person is theirs to clear, not a defect to fix.
		lane = LaneUserAction
	}
	run.LearningLane = lane
	switch {
	case lane == LaneNone:
		return "; no learning lane for class " + string(run.Outcome.Class)
	case lane == LaneWorkItem && run.Snapshot != "":
		return fmt.Sprintf("; lane %s (snapshot %s)", lane, run.Snapshot)
	}
	return "; lane " + string(lane)
}

// runLearn is the learn stage: it always runs and never raises. A failed post
// is a failed stage line and the run still returns.
func (r *Runner) runLearn(e *Exec) {
	start := r.d.Now()
	var events []LearnEvent
	if e.sess != nil {
		events = e.sess.events
	}
	e.Run.LearningEvents = len(events)
	lane := laneNote(e.Run)
	if len(events) == 0 {
		r.record(e, StageLearn, StatusOK, "no learning events"+lane, start)
		return
	}
	if r.d.Page.Poster == nil {
		r.record(e, StageLearn, StatusOK, fmt.Sprintf("%d events held; no receiver is configured%s", len(events), lane), start)
		return
	}
	posted, failed, err := PostBatches(context.WithoutCancel(e.Ctx), r.d.Page.Poster, events)
	switch {
	case err != nil:
		r.record(e, StageLearn, StatusFailed, fmt.Sprintf("%d of %d events posted: %v%s", posted, len(events), err, lane), start)
	case len(failed) > 0:
		r.record(e, StageLearn, StatusFailed, fmt.Sprintf("%d of %d events posted; %d rejected as too large%s", posted, len(events), len(failed), lane), start)
	default:
		r.record(e, StageLearn, StatusOK, fmt.Sprintf("%d events posted%s", posted, lane), start)
	}
}
