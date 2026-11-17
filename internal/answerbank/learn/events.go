package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// Upload kinds.
const (
	UploadUncertainty   = "uncertainty"
	UploadCorrection    = "correction"
	UploadUserReported  = "user-reported"
	UploadManualFill    = "manual-fill"
	UploadUnsafeControl = "unsafe-control"
	// UploadFrameUnreachable is a frame of the page the fill could not enter.
	// It names no control: its control kind is the tag FrameControlKind, its
	// question the frame's origin and path, its reason the failure outcome.
	UploadFrameUnreachable = "frame-unreachable"
)

// FrameControlKind tags a frame-unreachable event; no other kind may carry it.
const FrameControlKind = "iframe"

// frameReasons are the outcomes of a frame the fill failed to enter.
var frameReasons = []string{"failed-probe", "failed-inject", "failed-no-listener"}

// Correction origins. Anything else, the apply chain's own answer included, is
// not the user correcting the system.
const (
	OriginUserEdit   = "user-edit"
	OriginManualFill = "manual-fill"
)

// MaxBatch is the most events one upload may carry.
const MaxBatch = 200

// futureSlack is how far past the server clock an event time may be.
const futureSlack = time.Hour

// ErrBatchSize marks an upload of no events or of more than MaxBatch.
var ErrBatchSize = fmt.Errorf("an upload holds 1 to %d events", MaxBatch)

// Upload is one event as a sender states it.
type Upload struct {
	EventID  string
	Kind     string
	Host     string
	Question string
	// SectionPath is the section captions around the control, outermost
	// first. An uncertainty, a correction and a manual fill keep it, joined
	// by " > ", unless the control is value-withheld.
	SectionPath  []string
	ControlKind  string
	InputType    string
	Autocomplete string
	Options      []string
	At           time.Time
	Reason       string
	// Before is the value the system wrote; After the value the control holds
	// now; OptionLabel the chosen option's label.
	Before       string
	After        string
	OptionLabel  string
	EntryID      string
	Origin       string
	ReportNumber int64
	RunID        string
	Report       *Report
	// Selector names the control inside its run. It is used only to look up
	// what the system wrote there and is never stored.
	Selector string
	// TrustedInput is the sender's statement that the value came from a trusted
	// user input event. Without it a manual fill is never ground truth.
	TrustedInput bool
}

// Rejection names an event that was not stored, and why.
type Rejection struct {
	EventID string
	Reason  string
}

// IngestResult reports an upload. Accepted counts the events the server holds
// from it, new or already held; Duplicates names the ones it already held.
type IngestResult struct {
	Accepted   int
	Duplicates []string
	Rejected   []Rejection
}

var (
	// An event id is a lowercase UUID, a run id is the id the server issued for
	// a run, and an entry id follows the bank's id grammar (lowercase words
	// joined by single hyphens) and names an entry of the user's or the
	// defaults bank. Anything else is rejected, so no id field can carry free
	// text.
	eventIDRE = lazyregexp.New(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	runIDRE   = lazyregexp.New(`^run_[0-9a-f]{24}$`)
	// applyRunIDRE is the id the application chain issues for its own runs.
	applyRunIDRE = lazyregexp.New(`^run_\d{8}T\d{6}\.\d{3}_[0-9a-f]{6}$`)
	entryIDRE    = lazyregexp.New(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	hostRE       = lazyregexp.New(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	attrRE       = lazyregexp.New(`^[A-Za-z0-9 ,_-]{0,60}$`)
)

// runShaped reports an id in the shape either issuer gives a run: the server's
// or the application chain's. Every place that takes a run id reads it here.
func runShaped(id string) bool { return runIDRE.MatchString(id) || applyRunIDRE.MatchString(id) }

// maxEntryIDRunes and maxSelectorRunes bound the two identifiers.
const (
	maxEntryIDRunes  = 80
	maxSelectorRunes = 500
)

// ReasonAlreadySatisfied marks the observe-only record of a required control
// the page had already answered while the run wrote nothing there. It is kept
// as an upload only: it never becomes an abstain and never reaches triage.
const ReasonAlreadySatisfied = "already-satisfied"

// uploadReasons is the closed vocabulary of the reason on an upload: the
// matcher's abstain reasons, the ones the engine adds for a control it could
// not drive (a list without the answer's option or one that would not open, a
// model draft it refused to type) or one the page showed only after the fill
// (revealed-after-run), and the observe-only already-satisfied record.
var uploadReasons = []string{
	string(match.ReasonNoEntry), string(match.ReasonInputMismatch), string(match.ReasonNarrativeDeferred), string(match.ReasonPlaceholderEmpty),
	string(match.ReasonAmbiguous), string(match.ReasonLowConfidence), string(match.ReasonDestructiveOption), string(match.ReasonSectionPolicy),
	string(match.ReasonContextPolicy), string(match.ReasonDuplicateLink), string(match.ReasonSelfIDPolicy), string(match.ReasonAskMe),
	string(match.ReasonAIInstruction), string(match.ReasonUnsupported), string(match.ReasonResponseMismatch), string(match.ReasonModelAbstain),
	string(match.ReasonBudgetExhausted), string(match.ReasonNoRoute), string(match.ReasonSensitiveCategory),
	string(match.ReasonNoGrounding), string(match.ReasonUpstreamError), string(match.ReasonTooManyOptions),
	"failed", "review-flagged", "unrecognised", "unsafe-click", "option-not-found", "listbox-not-opened", "unsafe-to-compose", "revealed-after-run",
	ReasonAlreadySatisfied,
}

// HostOf reduces a host or a URL to a lowercase host name. It returns "" for
// text that is not one, so a path, query or credential never reaches a record.
func HostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "/") || strings.Contains(raw, "?") || strings.Contains(raw, "#") || strings.Contains(raw, "@") {
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		raw = u.Hostname()
	} else if i := strings.LastIndexByte(raw, ':'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.ToLower(strings.TrimSuffix(raw, "."))
	if !hostRE.MatchString(raw) {
		return ""
	}
	return raw
}

// sameAnswer compares two answers as foldAnswer folds them.
func sameAnswer(a, b string) bool { return foldAnswer(a) == foldAnswer(b) }

// prepared is an upload after the server's checks and sanitization.
type prepared struct {
	u        Upload
	host     string
	question string
	section  string
	options  []string
	withheld Withheld
	// after and label are the value and chosen label to store, empty when the
	// control is value-withheld.
	after, label, before string
	truth                bool
	counted              bool
	report               *Report
}

// prepare applies every rule the server enforces on its own, whatever the
// sender did. The returned string is the rejection reason, empty when the
// event is accepted.
func (s *Service) prepare(ctx context.Context, u Upload, entries *entryLookup) (prepared, string) {
	p := prepared{u: u}
	switch {
	case !eventIDRE.MatchString(u.EventID):
		return p, "invalid-event-id"
	case !slices.Contains([]string{UploadUncertainty, UploadCorrection, UploadUserReported, UploadManualFill, UploadUnsafeControl, UploadFrameUnreachable}, u.Kind):
		return p, "unknown-kind"
	case u.RunID != "" && !runShaped(u.RunID):
		return p, "invalid-run-id"
	case u.EntryID != "" && (len(u.EntryID) > maxEntryIDRunes || !entryIDRE.MatchString(u.EntryID)):
		return p, "invalid-entry-id"
	case u.Kind == UploadFrameUnreachable && !slices.Contains(frameReasons, u.Reason),
		u.Kind != UploadFrameUnreachable && u.Reason != "" && !slices.Contains(uploadReasons, u.Reason):
		return p, "invalid-reason"
	case len([]rune(u.Selector)) > maxSelectorRunes:
		return p, "invalid-selector"
	}
	if u.RunID != "" && !s.RunIssued(ctx, u.RunID) {
		return p, "unknown-run"
	}
	if u.EntryID != "" && !entries.has(u.EntryID) {
		return p, "unknown-entry-id"
	}
	if p.host = HostOf(u.Host); p.host == "" {
		return p, "invalid-host"
	}
	if p.question = cleanText(u.Question, maxQuestionRunes+1); p.question == "" {
		return p, "question-required"
	} else if len([]rune(p.question)) > maxQuestionRunes {
		return p, "question-too-long"
	}
	if frame := u.Kind == UploadFrameUnreachable; frame != (u.ControlKind == FrameControlKind) || (!frame && !match.ControlKind(u.ControlKind).Valid()) {
		return p, "invalid-control-kind"
	}
	if len(u.Options) > MaxOptions {
		return p, "too-many-options"
	}
	for _, o := range u.Options {
		if len([]rune(o)) > maxOptionRunes {
			return p, "option-too-long"
		}
	}
	p.options = cleanOptions(u.Options)
	switch {
	case u.At.IsZero():
		return p, "invalid-time"
	case u.At.After(s.now().Add(futureSlack)):
		return p, "time-in-future"
	case u.ReportNumber < 0:
		return p, "invalid-report-number"
	// A picked label is bounded like a value and stored cut as an option is
	// (below): the sender cut the option it was picked from the same way.
	case len([]rune(u.Before)) > maxValueRunes || len([]rune(u.After)) > maxValueRunes || len([]rune(u.OptionLabel)) > maxValueRunes:
		return p, "value-too-long"
	case !attrRE.MatchString(u.InputType) || !attrRE.MatchString(u.Autocomplete):
		return p, "invalid-control-attribute"
	}
	if reason := checkKind(u); reason != "" {
		return p, reason
	}
	inputType := u.InputType
	if inputType == "" && u.Report != nil {
		inputType = snippetInputType(u.Report.Snippet)
	}
	var withheldLabel string
	p.withheld, withheldLabel = classifyEdit(p.question, u.ControlKind, inputType, u.Autocomplete, p.options, u.After, u.OptionLabel, u.Before)
	if p.withheld == WithheldNone {
		p.after, p.label, p.before = cleanText(u.After, maxValueRunes), cleanText(u.OptionLabel, maxOptionRunes), cleanText(u.Before, maxValueRunes)
		p.section = cleanText(strings.Join(u.SectionPath, " > "), maxQuestionRunes)
	} else {
		// Of a withheld control only the category survives: whatever the page
		// put in the label, beside it or in the option list is dropped.
		p.options, p.question = nil, withheldLabel
	}
	final := u.OptionLabel
	if final == "" {
		final = u.After
	}
	switch u.Kind {
	case UploadManualFill:
		if u.Before != "" && sameAnswer(u.Before, final) {
			return p, "not-an-edit"
		}
		// The rules the engine ran before it kept the capture run again here, save the two
		// that read what an upload does not carry (CaptureCandidate). An empty or
		// placeholder value is stored and never learned: the candidate stage excludes it.
		if why := CaptureDrop(CaptureCandidate{Question: u.Question, Value: final, ControlKind: u.ControlKind}); why != "" && why != "empty-value" && why != "placeholder" && p.withheld == WithheldNone {
			return p, "capture-dropped: " + why
		}
		// A fill outside any run cannot be checked against the system's writes,
		// so it is never ground truth.
		p.truth = u.TrustedInput && u.RunID != "" && !ValueContradictsLabel(u.Question, final) && !s.systemWrote(ctx, u)
		p.counted = u.Before != "" && u.EntryID != "" && p.truth
	case UploadCorrection:
		p.counted = u.EntryID != "" && u.Before != "" && !sameAnswer(u.Before, final)
	case UploadUserReported:
		r, err := SanitizeReport(*u.Report)
		if err != nil {
			return p, "report-rejected: " + strings.TrimPrefix(err.Error(), ErrReportRejected.Error()+": ")
		}
		if p.withheld != WithheldNone {
			r = withheldReport(r, inputType, withheldLabel)
		}
		p.report = &r
	}
	return p, ""
}

// entryLookup reads the ids of the user's and the defaults bank once, and only
// when an upload names an entry.
type entryLookup struct {
	bank *answerbank.Service
	ids  map[string]bool
}

func (l *entryLookup) has(id string) bool {
	if l.ids == nil {
		l.ids = l.bank.EntryIDs()
	}
	return l.ids[id]
}

// checkKind enforces what each kind may and must carry.
func checkKind(u Upload) string {
	hasValues := u.Before != "" || u.After != "" || u.OptionLabel != ""
	if u.Report != nil && u.Kind != UploadUserReported {
		return "report-not-allowed"
	}
	if u.Origin != "" && u.Kind != UploadCorrection {
		return "origin-not-allowed"
	}
	switch u.Kind {
	case UploadUncertainty:
		if u.Reason == "" {
			return "reason-required"
		}
		if hasValues {
			return "values-not-allowed"
		}
	case UploadCorrection:
		if u.Origin != OriginUserEdit && u.Origin != OriginManualFill {
			return "origin-not-allowed"
		}
	case UploadUserReported:
		if u.Report == nil {
			return "report-required"
		}
		if hasValues {
			return "correction-on-report"
		}
	case UploadUnsafeControl:
		if hasValues || len(u.Options) > 0 {
			return "unsafe-control-carries-values"
		}
	case UploadFrameUnreachable:
		if hasValues || len(u.Options) > 0 || u.EntryID != "" {
			return "frame-carries-values"
		}
	}
	return ""
}

// Ingest stores an upload. It is idempotent by event id, and the response
// reports what was stored; candidate generation is the caller's to schedule
// afterwards (AfterIngest) and never delays it.
func (s *Service) Ingest(ctx context.Context, ups []Upload) (IngestResult, error) {
	var res IngestResult
	if len(ups) == 0 || len(ups) > MaxBatch {
		return res, ErrBatchSize
	}
	var ok []prepared
	entries := &entryLookup{bank: s.bank}
	for _, u := range ups {
		p, reason := s.prepare(ctx, u, entries)
		if reason != "" {
			res.Rejected = append(res.Rejected, Rejection{EventID: u.EventID, Reason: reason})
			continue
		}
		ok = append(ok, p)
	}
	now := s.now()
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		for _, p := range ok {
			fresh, err := s.insertUpload(ctx, tx, p, now)
			if err != nil {
				return err
			}
			if !fresh {
				res.Duplicates = append(res.Duplicates, p.u.EventID)
			}
		}
		return nil
	})
	if err != nil {
		return IngestResult{}, err
	}
	res.Accepted = len(ok)
	return res, nil
}

func (s *Service) insertUpload(ctx context.Context, tx *sql.Tx, p prepared, now time.Time) (bool, error) {
	u := p.u
	var reportJSON *string
	if p.report != nil {
		b, _ := json.Marshal(p.report)
		reportJSON = ptr(string(b))
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO learning_uploads (event_id, kind, host, question, control_kind, at, received_at, reason, report_number, run_id, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (event_id) DO NOTHING`,
		u.EventID, u.Kind, p.host, p.question, u.ControlKind, stamp(u.At), stamp(now), u.Reason, reportNumber(u), u.RunID, reportJSON)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	base := eventRow{Host: p.host, Question: p.question, SectionPath: p.section, ControlKind: u.ControlKind, Options: p.options, At: u.At, EntryID: u.EntryID,
		RunID: u.RunID, ReportNumber: reportNumber(u), Withheld: p.withheld}
	switch u.Kind {
	case UploadUncertainty:
		if u.Reason == ReasonAlreadySatisfied {
			return true, nil
		}
		e := base
		e.Kind, e.Outcome, e.Reason = KindAbstained, string(match.OutcomeAbstained), u.Reason
		_, err = insertEvent(ctx, tx, e)
	case UploadCorrection:
		e := base
		e.Kind, e.Origin, e.Counted, e.Lane, e.Reason = KindUserCorrected, u.Origin, p.counted, LaneManual, u.Reason
		e.Value, e.OptionLabel = nonEmpty(p.after), nonEmpty(p.label)
		_, err = insertEvent(ctx, tx, e)
	case UploadManualFill:
		err = s.insertFill(ctx, tx, p, now)
	}
	return true, err
}

func reportNumber(u Upload) *int64 {
	if u.ReportNumber > 0 {
		return ptr(u.ReportNumber)
	}
	return nil
}

func (s *Service) insertFill(ctx context.Context, tx *sql.Tx, p prepared, now time.Time) error {
	u := p.u
	opts, _ := json.Marshal(nonNilStrings(p.options))
	res, err := tx.ExecContext(ctx, `INSERT INTO manual_fills (event_id, at, received_at, host, question, section_path, control_kind, options_json, value, option_label, withheld, ground_truth, prior_value, entry_id, run_id, report_number)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.EventID, stamp(u.At), stamp(now), p.host, p.question, p.section, u.ControlKind, string(opts), nonEmpty(p.after), nonEmpty(p.label), string(p.withheld), p.truth,
		nonEmpty(p.before), u.EntryID, u.RunID, reportNumber(u))
	if err != nil {
		return err
	}
	if u.Before == "" || u.EntryID == "" {
		return nil
	}
	// A manual fill over a value the system wrote is a correction for the entry
	// that produced it.
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	e := eventRow{Kind: KindUserCorrected, At: u.At, Host: p.host, Question: p.question, SectionPath: p.section, ControlKind: u.ControlKind, Options: p.options,
		EntryID: u.EntryID, Origin: OriginManualFill, Counted: p.counted, Lane: LaneManual, RunID: u.RunID, ReportNumber: reportNumber(u),
		Withheld: p.withheld, Value: nonEmpty(p.after), OptionLabel: nonEmpty(p.label), FillID: &id}
	_, err = insertEvent(ctx, tx, e)
	return err
}
