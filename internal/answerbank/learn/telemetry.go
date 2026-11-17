package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// Event kinds of the usage telemetry.
const (
	KindMatch             = "match"
	KindApplied           = "applied"
	KindAbstained         = "abstained"
	KindUserCorrected     = "user-corrected"
	KindCandidateApproved = "candidate-approved"
	KindCandidateRejected = "candidate-rejected"
	KindAbstainReason     = "abstain-reason"
)

// Lanes that produce a result.
const (
	LaneAnswerBank  = "answer-bank"
	LaneModelOption = "model-option"
	LaneComposed    = "composed"
	LaneManual      = "manual"
)

// EventRetention is how long telemetry events are kept.
const EventRetention = 180 * 24 * time.Hour

// Bounds on what one event stores.
const (
	maxQuestionRunes = 1000
	maxValueRunes    = 4000
	maxOptionRunes   = 200
	MaxOptions       = 25
)

var (
	eventKinds = []string{KindMatch, KindApplied, KindAbstained, KindUserCorrected, KindCandidateApproved, KindCandidateRejected, KindAbstainReason}
	lanes      = []string{"", LaneAnswerBank, LaneModelOption, LaneComposed, LaneManual}
	// storedAbstainReasons are the only reasons an abstain-reason event is
	// written for.
	storedAbstainReasons = []match.Reason{match.ReasonNoGrounding, match.ReasonModelAbstain, match.ReasonUnsupported,
		match.ReasonResponseMismatch, match.ReasonSensitiveCategory}
)

// Event is one telemetry event as a caller states it. Record decides what is
// stored: a value is kept only where a consumer needs it and never for a
// value-withheld control.
type Event struct {
	Kind         string
	At           time.Time
	Host         string
	Question     string
	SectionPath  []string
	ControlKind  string
	InputType    string
	Autocomplete string
	Options      []string
	EntryID      string
	AnswerKey    string
	Outcome      string
	Confidence   *float64
	Lane         string
	Reason       string
	Value        string
	OptionLabel  string
	RunID        string
	ReportNumber int64
}

// eventRow is an event as stored.
type eventRow struct {
	Kind, Host, Question, SectionPath, ControlKind string
	At                                             time.Time
	Options                                        []string
	EntryID, AnswerKey, Outcome, Lane, Reason      string
	Confidence                                     *float64
	Value, OptionLabel                             *string
	Withheld                                       Withheld
	Origin                                         string
	Counted                                        bool
	RunID                                          string
	ReportNumber                                   *int64
	FillID                                         *int64
}

func cleanText(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Join(strings.Fields(StripURLsIn(s)), " ")
	return clampRunes(s, n)
}

func cleanOptions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, o := range in {
		if o = cleanText(o, maxOptionRunes); o != "" {
			out = append(out, o)
		}
		if len(out) == MaxOptions {
			break
		}
	}
	return out
}

// shapedID returns id when it has the shape of its grammar and "" otherwise.
func shapedID(id string, re *lazyregexp.Regexp) string {
	if id == "" || len(id) > maxEntryIDRunes || !re.MatchString(id) {
		return ""
	}
	return id
}

func ptr[T any](v T) *T { return &v }

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// insertEvent reports whether the event was written: one whose fill already
// has an event is ignored.
func insertEvent(ctx context.Context, tx *sql.Tx, e eventRow) (bool, error) {
	opts, _ := json.Marshal(nonNilStrings(e.Options))
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO answer_events
		(kind, at, host, question, section_path, control_kind, options_json, entry_id, answer_key, outcome, confidence, lane, reason,
		 value, option_label, withheld, origin, counted, run_id, report_number, fill_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Kind, stamp(e.At), e.Host, e.Question, e.SectionPath, e.ControlKind, string(opts), e.EntryID, e.AnswerKey, e.Outcome, e.Confidence, e.Lane, e.Reason,
		e.Value, e.OptionLabel, string(e.Withheld), e.Origin, e.Counted, e.RunID, e.ReportNumber, e.FillID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// eventQueue bounds the telemetry waiting for the writer; beyond it events
// are dropped with a warning.
const eventQueue = 256

// Record queues telemetry events for writing. It is best effort by design: it
// never waits for the database, a failed write is logged, and nothing reaches
// the caller, so telemetry can not block or fail matching.
func (s *Service) Record(_ context.Context, evs ...Event) {
	var rows []eventRow
	for _, e := range evs {
		if row, ok := s.rowFor(e); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return
	}
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if s.evClosed {
		return
	}
	s.evPend.Add(1)
	select {
	case s.events <- rows:
	default:
		s.evPend.Done()
		s.log.Warn("answer-bank telemetry dropped: the writer is behind", "events", len(rows))
	}
}

// Flush waits until every event queued so far has been written or has failed.
func (s *Service) Flush() { s.evPend.Wait() }

func (s *Service) writeEvents() {
	defer close(s.evDone)
	for rows := range s.events {
		ctx, cancel := context.WithTimeout(context.Background(), ioTimeout)
		err := s.st.Write(ctx, func(tx *sql.Tx) error {
			for _, r := range rows {
				if _, err := insertEvent(ctx, tx, r); err != nil {
					return err
				}
			}
			return nil
		})
		cancel()
		if err != nil {
			s.log.Warn("answer-bank telemetry write failed", "error", err.Error())
		}
		s.evPend.Done()
	}
}

// rowFor decides what is stored of an event.
func (s *Service) rowFor(e Event) (eventRow, bool) {
	if !slices.Contains(eventKinds, e.Kind) || !slices.Contains(lanes, e.Lane) {
		return eventRow{}, false
	}
	if e.Kind == KindAbstainReason && !slices.Contains(storedAbstainReasons, match.Reason(e.Reason)) {
		return eventRow{}, false
	}
	at := e.At
	if at.IsZero() {
		at = s.now()
	}
	question := cleanText(e.Question, maxQuestionRunes)
	options := cleanOptions(e.Options)
	withheld, category := classify(question, e.ControlKind, e.InputType, e.Autocomplete, options)
	if withheld == WithheldNone && valueIsSelfIDOption(options, e.Value) {
		withheld, category = WithheldSelfID, selfIDOther
	}
	row := eventRow{
		Kind: e.Kind, At: at, Host: HostOf(e.Host), Question: question, SectionPath: cleanText(strings.Join(e.SectionPath, " > "), maxQuestionRunes),
		ControlKind: clampRunes(e.ControlKind, 30), Options: options, EntryID: shapedID(e.EntryID, entryIDRE), AnswerKey: clampRunes(e.AnswerKey, 100),
		Outcome: clampRunes(e.Outcome, 40), Confidence: e.Confidence, Lane: e.Lane, Reason: clampRunes(e.Reason, 40), Withheld: withheld,
	}
	if runShaped(e.RunID) {
		row.RunID = e.RunID
	}
	if withheld != WithheldNone {
		row.Question, row.Options = category, nil
	}
	if e.ReportNumber > 0 {
		row.ReportNumber = ptr(e.ReportNumber)
	}
	// A model's answer cannot be read back from the bank as an entry's can, so
	// a composed text or a model's pick is kept: what the run chose.
	keepsValue := e.Kind == KindUserCorrected || e.Kind == KindMatch && (e.Lane == LaneComposed || e.Lane == LaneModelOption)
	if keepsValue && row.Withheld == WithheldNone {
		row.Value = nonEmpty(cleanText(e.Value, maxValueRunes))
		row.OptionLabel = nonEmpty(cleanText(e.OptionLabel, maxOptionRunes))
	}
	return row, true
}

// EventsFor converts the result of one question into telemetry events: a match
// when the result fills, an abstain otherwise, and for a model-lane abstain the
// stored abstain-reason event. q is the question as the matcher read it.
func EventsFor(host string, q match.Question, r match.Result, at time.Time, runID string, report int64) []Event {
	base := Event{At: at, Host: host, Question: q.Text, SectionPath: q.SectionPath, ControlKind: string(q.Kind), EntryID: r.EntryID,
		AnswerKey: r.AnswerKey, Outcome: string(r.Outcome), RunID: runID, ReportNumber: report, Lane: LaneAnswerBank}
	for _, o := range q.Options {
		base.Options = append(base.Options, o.Label)
	}
	switch r.Source {
	case match.SourceModelOption, match.SourceModelEntry:
		// A recognised entry is the model lane's too; its entry id is what lets
		// the candidates flow propose the question as an example of that entry.
		base.Lane = LaneModelOption
	case match.SourceComposed:
		base.Lane = LaneComposed
	}
	if r.Confidence > 0 {
		base.Confidence = ptr(r.Confidence)
	}
	switch r.Outcome {
	case match.OutcomeFilled, match.OutcomeFilledReview, match.OutcomeFilledModel:
		base.Kind, base.Value, base.OptionLabel = KindMatch, r.Answer, r.ChosenLabel
		return []Event{base}
	}
	base.Kind, base.Reason = KindAbstained, string(r.Reason)
	out := []Event{base}
	if slices.Contains(storedAbstainReasons, r.Reason) {
		diag := base
		diag.Kind = KindAbstainReason
		out = append(out, diag)
	}
	return out
}

// Prune deletes the events, manual fills, upload records and system writes older than the
// retention (a fill takes its linked correction event with it), then settles the support of pending candidates whose evidence went with them.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	cutoff := stamp(s.now().Add(-EventRetention))
	var deleted int64
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM answer_events WHERE at < ?`, cutoff)
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		if _, err := tx.ExecContext(ctx, `DELETE FROM manual_fills WHERE at < ?`, cutoff); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM learning_uploads WHERE received_at < ?`, cutoff); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM system_writes WHERE recorded_at < ?`, cutoff); err != nil {
			return err
		}
		return recountPending(ctx, tx)
	})
	return deleted, err
}

// Usage is the per-entry usage derived from events.
type Usage struct {
	EntryID           string
	Matches           int
	Applies           int
	Abstains          int
	Corrections       int
	LastMatchedAt     string
	LastAppliedAt     string
	LastCorrectedAt   string
	LastActivityAt    string
	AverageConfidence *float64
	ExampleQuestions  []string
}

const maxExampleQuestions = 5

// usageEvents bounds the scan that finds the latest question texts.
const usageEvents = 50000

// UsageWindow is how far back the dashboard counts usage.
const UsageWindow = 30 * 24 * time.Hour

// Usage derives the usage of every bank entry from events. The counters and the
// mean confidence cover the last window (every retained event when window is not
// positive); the last-used times cover all retained events, so an entry idle for
// longer than the window still shows when it was last used. Every entry of the
// bank appears, with zero counters when it has no events; ids no longer in the
// bank are left out; rows are ordered by last activity, newest first. It reads
// the bank and the events and writes nothing.
func (s *Service) Usage(ctx context.Context, window time.Duration) ([]Usage, error) {
	l, err := s.bank.File()
	if err != nil {
		return nil, err
	}
	rows := make([]Usage, 0, len(l.Bank.Entries))
	at := map[string]int{}
	for _, e := range l.Bank.Entries {
		if _, dup := at[e.ID]; dup {
			continue
		}
		at[e.ID] = len(rows)
		rows = append(rows, Usage{EntryID: e.ID, ExampleQuestions: []string{}})
	}
	since := ""
	if window > 0 {
		since = stamp(s.now().Add(-window))
	}
	q, err := s.st.Read().QueryContext(ctx, `SELECT entry_id, kind, coalesce(sum(at >= ?), 0), max(at), sum(CASE WHEN at >= ? THEN confidence END), coalesce(sum(at >= ? AND confidence IS NOT NULL), 0) FROM answer_events
		WHERE entry_id <> '' AND kind IN ('match', 'applied', 'abstained', 'user-corrected') AND (kind <> 'user-corrected' OR counted = 1)
		GROUP BY entry_id, kind`, since, since, since)
	if err != nil {
		return nil, err
	}
	sums, counts := map[string]float64{}, map[string]int{}
	for q.Next() {
		var id, kind, last string
		var n, nconf int
		var sum sql.NullFloat64
		if err := q.Scan(&id, &kind, &n, &last, &sum, &nconf); err != nil {
			q.Close()
			return nil, err
		}
		i, ok := at[id]
		if !ok {
			continue
		}
		u := &rows[i]
		switch kind {
		case KindMatch:
			u.Matches, u.LastMatchedAt = n, last
		case KindApplied:
			u.Applies, u.LastAppliedAt = n, last
		case KindAbstained:
			u.Abstains = n
		case KindUserCorrected:
			u.Corrections, u.LastCorrectedAt = n, last
		}
		if last > u.LastActivityAt {
			u.LastActivityAt = last
		}
		sums[id] += sum.Float64
		counts[id] += nconf
	}
	if err := q.Close(); err != nil {
		return nil, err
	}
	for id, n := range counts {
		if n > 0 {
			rows[at[id]].AverageConfidence = ptr(sums[id] / float64(n))
		}
	}
	if err := s.exampleQuestions(ctx, rows, at); err != nil {
		return nil, err
	}
	slices.SortStableFunc(rows, func(a, b Usage) int { return strings.Compare(b.LastActivityAt, a.LastActivityAt) })
	return rows, nil
}

func (s *Service) exampleQuestions(ctx context.Context, rows []Usage, at map[string]int) error {
	// Within the latest usageEvents events, each entry's distinct questions by
	// their newest use, newest first, at most maxExampleQuestions of them.
	q, err := s.st.Read().QueryContext(ctx, `WITH recent AS (SELECT entry_id, question, at, id FROM answer_events
		WHERE entry_id <> '' AND question <> '' AND withheld = '' AND kind IN ('match', 'applied', 'abstained', 'user-corrected')
		ORDER BY at DESC, id DESC LIMIT ?),
		newest AS (SELECT entry_id, question, at, id, ROW_NUMBER() OVER (PARTITION BY entry_id, question ORDER BY at DESC, id DESC) AS d FROM recent),
		ranked AS (SELECT entry_id, question, ROW_NUMBER() OVER (PARTITION BY entry_id ORDER BY at DESC, id DESC) AS n FROM newest WHERE d = 1)
		SELECT entry_id, question FROM ranked WHERE n <= ? ORDER BY entry_id, n`, usageEvents, maxExampleQuestions)
	if err != nil {
		return err
	}
	defer q.Close()
	for q.Next() {
		var id, text string
		if err := q.Scan(&id, &text); err != nil {
			return err
		}
		i, ok := at[id]
		if !ok || len(rows[i].ExampleQuestions) >= maxExampleQuestions || slices.Contains(rows[i].ExampleQuestions, text) {
			continue
		}
		rows[i].ExampleQuestions = append(rows[i].ExampleQuestions, text)
	}
	if err := q.Err(); err != nil {
		return err
	}
	// Newest was read first; the list reads oldest to newest.
	for i := range rows {
		slices.Reverse(rows[i].ExampleQuestions)
	}
	return nil
}
