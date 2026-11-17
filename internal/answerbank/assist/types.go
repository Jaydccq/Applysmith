package assist

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Task ids of the routed model registry.
const (
	TaskOption           = "option-pick"
	TaskNarrative        = "narrative"
	TaskExplainer        = "company-explainer"
	TaskSuggestionReview = "suggestion-review"
)

// Mode is the grounding mode of free-text composition.
type Mode string

const (
	ModeStrict    Mode = "strict"
	ModeFlex      Mode = "flex"
	ModeDangerous Mode = "dangerous"
)

// Settings are the runtime settings the lanes read on every request.
type Settings struct {
	// LLMFallbackEnabled switches the grounded option fallback on. The
	// composer needs only a route.
	LLMFallbackEnabled bool
	// Mode is the grounding mode. Autofill never passes dangerous: the caller
	// clamps it, as the answer-bank settings do.
	Mode Mode
}

// Model is the routed model. *llm.Router satisfies it; every call it makes is
// recorded in the call ledger.
type Model interface {
	Resolve(ctx context.Context, taskID string) (llm.Resolved, error)
	Complete(ctx context.Context, req llm.Request) (*llm.Response, error)
}

// Knowledge is the knowledge-base capability the lanes read: the top snippets
// for a question and the company brief of a declared site. *kb.Service
// satisfies it.
type Knowledge interface {
	Search(ctx context.Context, q kb.Query) (kb.Result, error)
	EnsureBrief(ctx context.Context, req kb.BriefRequest) (*kb.Brief, error)
}

// LiveFacts is the web search the live-fact lane answers from. It is injected
// because the search service sits above this package. Lookup returns the cited
// findings; the lane derives the typed text from them and never composes one.
type LiveFacts interface {
	// Available reports whether a search source is configured, asking no model.
	Available(ctx context.Context) bool
	// Lookup answers one question from web findings alone. An answer that did
	// not pass citation enforcement, or whose sources do not answer, is
	// ErrLiveFactRefused; no source is ErrLiveFactNoSource.
	Lookup(ctx context.Context, question string) (LiveFact, error)
}

// LiveFact is a cited reply: Text carries numbered markers such as [1] that
// refer to Sources.
type LiveFact struct {
	Text    string
	Sources []LiveFactSource
	// Via names the search that served it.
	Via string
}

// LiveFactSource is one numbered page of a LiveFact.
type LiveFactSource struct {
	N   int
	URL string
}

var (
	// ErrLiveFactNoSource means no search source is configured.
	ErrLiveFactNoSource = errors.New("assist: no web-search source is configured")
	// ErrLiveFactRefused means the findings gave no cited answer.
	ErrLiveFactRefused = errors.New("assist: the web findings gave no cited answer")
)

// Deps are the collaborators of a Service.
type Deps struct {
	Model     Model
	Knowledge Knowledge
	// LiveFacts answers a live-fact question; nil leaves every such control
	// blank.
	LiveFacts LiveFacts
	// Embedder ranks the user's answered entries against a question for the
	// recognition lane; nil ranks them by shared words.
	Embedder kb.Embedder
	Now      func() time.Time
	Log      *slog.Logger
	// Store persists the 24-hour reply cache, so a restart between two scans of a
	// form does not change a model answer. Nil keeps the cache in memory.
	Store *store.Store
	// EnrichTimeout overrides the five-second budget of the posting and company
	// context (tests).
	EnrichTimeout time.Duration
}

func (d Deps) enrichTimeout() time.Duration {
	if d.EnrichTimeout > 0 {
		return d.EnrichTimeout
	}
	return enrichTimeout
}

// ProfileField is one value of the user's profile, tagged with the category
// that decides when it may be offered as evidence.
type ProfileField struct {
	// Ref names the field in a citation ("profile#city").
	Ref      string
	Label    string
	Value    string
	Category Category
}

// Category is the descriptor class of a profile field: a field is offered only
// to a question about its own category, except the non-sensitive identity
// fields, which are always offered.
type Category string

const (
	CatName       Category = "name"
	CatCity       Category = "city"
	CatCountry    Category = "country"
	CatDegree     Category = "degree"
	CatSchool     Category = "school"
	CatEduDates   Category = "education-dates"
	CatGPA        Category = "gpa"
	CatEmployment Category = "employment"
	CatSkills     Category = "skills"
	CatSalary     Category = "salary"
	CatWorkAuth   Category = "work-authorization"
	CatLocation   Category = "location"
)

// Correction is an answer the user gave in place of an earlier one for the same
// question; it outranks every other fact.
type Correction struct {
	Ref      string
	Question string
	Value    string
}

// EvidenceLine is one line of the user's own CV or profile, as the document
// pipeline reads it, with the dated entry or project it belongs to.
type EvidenceLine struct {
	// Ref names the source in a citation ("cv.md#<heading>").
	Ref string
	// Entry labels the role or project ("Title, Organization (current role)",
	// "Project: name"); empty for a summary or skills line.
	Entry string
	Text  string
}

// Sources are the user's own facts for one request.
type Sources struct {
	// Bank is the bank's entries for the request host.
	Bank    []match.Entry
	Profile []ProfileField
	// CV are the CV's lines of four words or more, for the option lane.
	CV []string
	// Evidence are the CV and profile lines a tailored document may claim; the
	// composer selects from them per question.
	Evidence    []EvidenceLine
	Corrections []Correction
	// WorkAuthStatus is the recorded work-authorization statement; the only
	// text an immigration claim in a composed answer may rest on.
	WorkAuthStatus string
	// Relocation is the relocation policy with the home address, when set.
	Relocation *match.RelocationPolicy
	// Identity are the facts every composed answer may rest on: the name, the
	// school and degree, a headline.
	Identity []string
	// Credentials are every degree, certification, license, spoken language
	// and publication the applicant recorded. A composed answer may claim one
	// whether or not the question selected the evidence line that states it,
	// and only these and the other applicant facts ground a credential claim.
	Credentials []string
	// DegreeEnd is the end of the most recent degree, "YYYY-MM" or "YYYY".
	DegreeEnd string
	Degree    string
}

// Service runs the model-assisted lanes. The reply cache is persistent when the
// Deps carry a store. The session table is deliberately in memory: a session is
// a ten-minute idle window, so a restart simply starts a new one. A Service is
// safe for concurrent use.
type Service struct {
	deps     Deps
	sessions *Sessions
	cache    *ReplyCache
}

// New returns a Service. The session table and the reply cache live as long as
// the Service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{deps: d, sessions: NewSessions(d.Now), cache: NewReplyCache(d.Now, d.Store, d.Log)}
}
