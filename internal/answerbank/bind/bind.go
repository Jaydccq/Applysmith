// Package bind connects the pure answer matcher to the rest of the product. It
// is the one place that reads what a match depends on (the profile, the résumé
// text, the runtime settings, the knowledge base, the resolved posting and the
// clock), runs the matcher and the model lanes over it, and records what
// happened (usage telemetry and the values the system itself is about to
// write). The in-page engine and the browser extension reach it through the
// same Run.
package bind

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/documents"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

// ComposeSources are the user's own facts for a composed answer: the profile and
// CV reductions of assist.SourcesFromProfile, the host's bank entries, the
// evidence lines a tailored document may claim (documents.ApplicantEvidence),
// which the composer selects from per question, and the CV's credential,
// publication, language, award, research and volunteering sections and
// education headings (documents.ReadApplicantRecords), which no tailored
// document reads. Every composer path (autofill,
// the extension's Answer tab, the Ask Agent draft) reads its facts here. The
// seeded template CV is no evidence: its lines describe what to write.
func ComposeSources(p workspace.Profile, cv string, entries []match.Entry) assist.Sources {
	if workspace.IsTemplate(workspace.FileCV, []byte(cv)) {
		cv = ""
	}
	src := assist.SourcesFromProfile(p, cv, entries)
	for _, l := range documents.ApplicantEvidence(p, cv) {
		src.Evidence = append(src.Evidence, assist.EvidenceLine{Ref: l.Source, Entry: l.Entry, Text: l.Text})
	}
	rec := documents.ReadApplicantRecords(cv)
	for _, l := range rec.Sections {
		src.Evidence = append(src.Evidence, assist.EvidenceLine{Ref: l.Source, Entry: l.Entry, Text: l.Text})
		src.Credentials = append(src.Credentials, l.Text)
	}
	src.Credentials = append(src.Credentials, rec.Education...)
	return src
}

// Evidence retrieves the user's knowledge snippets for a question, and reads
// the sections of the user's own documents whole (F-10). *kb.Service
// satisfies it.
type Evidence interface {
	Search(ctx context.Context, q kb.Query) (kb.Result, error)
	OwnSections(ctx context.Context) ([]kb.OwnSection, error)
}

// ErrRunRequired marks a match call that names no run. It is a caller bug:
// without the run the values the engine writes cannot be remembered, and a
// later manual fill equal to one would count as the user's own.
var ErrRunRequired = errors.New("a match call must name its run")

// Jobs resolves the location of the posting a page belongs to, from the job
// context the product already holds. It returns "" when none is known.
type Jobs interface {
	Location(ctx context.Context, postingURL string) string
	// History is the companies the user applied to and interviewed with, as
	// relationship facts; nil when they cannot be read.
	History(ctx context.Context) []match.RelationshipFact
}

// Telemetry takes usage events. *learn.Service satisfies it; it is best effort
// and never blocks the match.
type Telemetry interface {
	Record(ctx context.Context, evs ...learn.Event)
}

// WriteLog remembers the value the system is about to write into a control, per
// run, so that a later manual fill equal to it is not learned as the user's
// own. Nil in Deps means nothing is remembered.
type WriteLog interface {
	RecordSystemWrites(ctx context.Context, runID string, writes ...learn.SystemWrite) error
}

// Deps are the collaborators of a Binding. Bank and Workspace are required; a
// nil Assist runs no model lane, and a nil Evidence, Jobs, Telemetry or Writes
// contributes nothing.
type Deps struct {
	Bank      *answerbank.Service
	Workspace *workspace.Workspace
	Assist    *assist.Service
	Evidence  Evidence
	Jobs      Jobs
	Telemetry Telemetry
	Writes    WriteLog
	Now       func() time.Time
	Log       *slog.Logger
	// Observe, when set, receives every answered request after it was
	// recorded, so the apply chain's check can read the matcher's own results
	// (entry ids included) beside what the engine wrote.
	Observe func(req Request, out Outcome)
}

// Binding answers questions for one workspace. It is safe for concurrent use.
type Binding struct{ d Deps }

// New returns a Binding.
func New(d Deps) *Binding {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Binding{d: d}
}

// Posting is the posting context of a request. Location is the page's own
// hint; the resolved job context outranks it.
type Posting struct {
	Company, Title, Description, Location string
	// URL is the page that identifies the posting; ApplicationURL and JobURL are
	// the pages that may declare the company's website.
	URL, ApplicationURL, JobURL string
}

// Request is one match call.
type Request struct {
	// RunID names the fill the call belongs to, a run the server issued; the
	// system writes are recorded against it. A request without one is refused.
	RunID string
	// RecordAs is the application run the usage events and system writes are
	// recorded under when RunID is an engine fill's own id; RunID when empty.
	RecordAs  string
	Host      string
	Posting   Posting
	Questions []match.Question
	// CoverLetterAttached and ResumeAttached are the run's observations; nil is
	// not observed.
	CoverLetterAttached, ResumeAttached *bool
}

// Outcome is the answered request: one result per question, in order, after
// the model lanes, with the questions as the matcher read them.
type Outcome struct {
	Results  []match.Result
	Prepared []match.Question
	// Own[i] reports whether Results[i] is the user's own bank answer
	// (match.Bank.OwnAnswer).
	Own []bool
}

// inputs is everything one match depends on, read once.
type inputs struct {
	req      match.Request
	bank     match.Bank
	settings answerbank.Settings
	profile  workspace.Profile
	cv       string
}

// build reads the matcher's inputs from the workspace as they are now. A
// profile that is missing or cannot be read reads as absent: its facts are
// empty and the matcher behaves as if the signal never existed.
func (b *Binding) build(ctx context.Context, req Request) (inputs, error) {
	loaded, err := b.d.Bank.Bank()
	if err != nil {
		return inputs{}, err
	}
	in := inputs{bank: loaded.Bank, settings: b.d.Bank.Settings(ctx), cv: b.cvText()}
	prof, haveProfile := b.d.Bank.Profile()
	if haveProfile {
		in.profile = prof
	}
	questions := make([]match.Question, len(req.Questions))
	for i, q := range req.Questions {
		questions[i] = askable(q)
	}
	y, m, d := b.d.Now().Date()
	in.req = match.Request{
		Host: req.Host, Questions: questions,
		Posting: match.Posting{
			Company: req.Posting.Company, Title: req.Posting.Title, Description: req.Posting.Description,
			Location: b.postingLocation(ctx, req.Posting),
		},
		CoverLetterAttached: req.CoverLetterAttached, ResumeAttached: req.ResumeAttached,
		Today:                       match.Date{Year: y, Month: int(m), Day: d},
		AutoAcceptLegalDeclarations: in.settings.AutoAcceptLegalDeclarations,
	}
	if haveProfile {
		in.req.Facts = FactsFromProfile(prof)
	}
	if b.d.Jobs != nil {
		in.req.Facts.Relationships = append(in.req.Facts.Relationships, b.d.Jobs.History(ctx)...)
	}
	in.req.Facts.Skills = withCVSkills(in.req.Facts.Skills, in.cv)
	in.req.Facts.Evidence = b.evidence(ctx, in.cv, questions)
	in.req.Facts.Knowledge, in.req.Facts.KnowledgeRead = b.knowledge(ctx, in.cv, questions)
	if in.req.Facts.KnowledgeRead && in.settings.KnowledgeSemanticRetrieval && b.d.Evidence != nil {
		in.req.Facts.KnowledgeNear = b.near(ctx, questions)
	}
	return in, nil
}

// Run builds the matcher's inputs, matches every question, offers the abstains
// to the model lanes, and records usage and system writes. It fails only when
// the answer bank cannot be loaded or the request breaks the matcher's bounds.
func (b *Binding) Run(ctx context.Context, req Request) (Outcome, error) {
	if req.RunID == "" {
		return Outcome{}, ErrRunRequired
	}
	in, err := b.build(ctx, req)
	if err != nil {
		return Outcome{}, err
	}
	m := match.New(in.bank, match.DefaultStages())
	resp, err := m.MatchContext(ctx, in.req)
	if err != nil {
		return Outcome{}, err
	}
	results := resp.Results
	if b.d.Assist != nil {
		results = b.d.Assist.Run(ctx, assist.RunInput{
			Req: in.req, Response: resp,
			Pin: func(ctx context.Context, i int, entryID string) (match.Result, bool) {
				r, err := m.MatchPinned(ctx, in.req, in.req.Questions[i], entryID)
				return r, err == nil
			},
			Settings:       assist.Settings{LLMFallbackEnabled: in.settings.LLMFallbackEnabled, Mode: assist.Mode(in.settings.AutofillGroundingMode())},
			Sources:        ComposeSources(in.profile, in.cv, in.bank.EntriesForHost(req.Host)),
			ApplicationURL: firstNonEmpty(req.Posting.ApplicationURL, req.Posting.URL), JobURL: req.Posting.JobURL,
		})
	}
	out := Outcome{Results: results, Prepared: resp.Prepared, Own: ownAnswers(in.bank, results)}
	b.record(ctx, req, out)
	if b.d.Observe != nil {
		b.d.Observe(req, out)
	}
	return out, nil
}

// Preview matches the questions against the bank and the profile alone, for a
// person trying a question out. No model lane runs, nothing is recorded as
// usage or as a system write, and no run is needed, so the bank, the telemetry
// and the learning data stay exactly as they were.
func (b *Binding) Preview(ctx context.Context, req Request) (Outcome, error) {
	in, err := b.build(ctx, req)
	if err != nil {
		return Outcome{}, err
	}
	resp, err := match.New(in.bank, match.DefaultStages()).MatchContext(ctx, in.req)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Results: resp.Results, Prepared: resp.Prepared, Own: ownAnswers(in.bank, resp.Results)}, nil
}

// askable gives the question a lone checkbox carries in its own label. A lone
// checkbox has no legend or container title, so its text arrives empty and the
// only wording it has is the option beside the box. The label is asked as the
// question, so the matcher's declaration, marketing and self-identification
// rules judge the same sentence the user reads. The engine's writer still
// enforces the consent rules before any tick, whatever the matcher answers.
func askable(q match.Question) match.Question {
	if q.Kind == match.KindCheckbox && strings.TrimSpace(q.Text) == "" && len(q.Options) == 1 {
		q.Text = strings.TrimSpace(q.Options[0].Label)
	}
	return q
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// postingLocation is the location of the resolved job context, otherwise the
// page's hint. The hint is page truth for this request, so it is honoured even
// when no job context could be resolved, and over a stored place that settles
// no country when it settles one ("NIH-Bethesda" stored, the board's office
// address given: Black Canyon).
func (b *Binding) postingLocation(ctx context.Context, p Posting) string {
	hint := strings.TrimSpace(p.Location)
	if b.d.Jobs != nil && strings.TrimSpace(p.URL) != "" {
		if loc := strings.TrimSpace(b.d.Jobs.Location(ctx, p.URL)); loc != "" {
			if _, ok := match.LocationCountry(loc); !ok {
				if _, ok := match.LocationCountry(hint); ok {
					return hint
				}
			}
			return loc
		}
	}
	return hint
}

// record writes the usage telemetry of the request and remembers the values the
// engine will write.
func (b *Binding) record(ctx context.Context, req Request, out Outcome) {
	at := b.d.Now()
	run := cmp.Or(req.RecordAs, req.RunID)
	var events []learn.Event
	var writes []learn.SystemWrite
	for i, r := range out.Results {
		q := out.Prepared[i]
		if strings.TrimSpace(q.Text) == "" {
			continue
		}
		events = append(events, learn.EventsFor(req.Host, q, r, at, run, 0)...)
		for _, v := range written(r) {
			writes = append(writes, learn.SystemWrite{Selector: r.Selector, Value: v})
		}
	}
	if b.d.Writes != nil && len(writes) > 0 {
		if err := b.d.Writes.RecordSystemWrites(ctx, run, writes...); err != nil {
			b.d.Log.Warn("system write not recorded", "err", err)
		}
	}
	if b.d.Telemetry != nil && len(events) > 0 {
		b.d.Telemetry.Record(ctx, events...)
	}
}

// written lists the values a result makes the engine put into its control: the
// chosen option labels, else the answer.
func written(r match.Result) []string {
	switch r.Outcome {
	case match.OutcomeFilled, match.OutcomeFilledReview, match.OutcomeFilledModel:
	default:
		return nil
	}
	if r.Source == match.SourceComposed {
		// A composed draft waits for the user; the engine does not write it.
		return nil
	}
	switch {
	case len(r.ChosenLabels) > 0:
		return r.ChosenLabels
	case r.ChosenLabel != "":
		return []string{r.ChosenLabel}
	case r.Answer != "":
		return []string{r.Answer}
	}
	return nil
}

func ownAnswers(b match.Bank, results []match.Result) []bool {
	own := make([]bool, len(results))
	for i, r := range results {
		own[i] = b.OwnAnswer(r)
	}
	return own
}
