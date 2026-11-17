// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// Model is the part of the LLM router evaluation uses. Evaluation names tasks,
// never providers or models.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
}

// PageRead is the text of a posting read from a URL.
type PageRead struct {
	Title, Company, Location, Compensation, Text string
	// Method is http or browser; Result is live, gone or unverified.
	Method, Result string
	// URL is the address of the posting the read found when it is not the
	// one asked for: where a tracking or short link landed, or the employer's
	// posting a job board's listing names in its apply link.
	URL string
}

// Reader reads a posting from its URL: the public ATS API for a supported ATS,
// otherwise a browser page read in the scan window.
type Reader interface {
	Read(ctx context.Context, url string) (PageRead, error)
}

// Inbox is the discovery inbox as evaluation sees it. Release returns a row to
// pending with a note, the charge saying what the failure may cost the row, and
// says whether the inbox drain takes the row again; Done records the outcome
// that was reached.
// ReleaseQueued returns every row still queued to pending, for a start with no
// job in flight, and says how many it moved.
type Inbox interface {
	Release(ctx context.Context, rowID int64, note string, c discovery.Charge) (bool, error)
	Done(ctx context.Context, rowID int64, r Result) error
	ReleaseQueued(ctx context.Context, note string) (int, error)
}

// Deps is everything an Evaluator needs.
type Deps struct {
	Store   *store.Store
	Tracker *tracker.Tracker
	Model   Model
	Prompts *prompts.Set
	// Profile loads the seeker; it is read for every job so edits apply at once.
	Profile func(ctx context.Context) (Profile, error)
	Reader  Reader
	// Blocked says whether the company is blocked: on the blocked-companies
	// list or in skip_companies, as a scan decides it.
	Blocked func(company string) bool
	// Knowledge retrieves snippets relevant to the posting; optional.
	Knowledge func(ctx context.Context, posting string) []Snippet
	Inbox     Inbox
	// ScanOutcome, when set, receives the outcome of a job that a scan run
	// queued (Request.ScanRun), once the job has ended.
	ScanOutcome func(ctx context.Context, scanRun, jobID string, r Result) error
	// Closed, when set, keeps the liveness result of a page read that found
	// the posting closed, with the inbox row the job was for (0 for none):
	// the result is stored and the posting's inbox rows expire instead of
	// going back to waiting.
	Closed func(ctx context.Context, url string, l discovery.Liveness, inboxRowID int64) error
	// Root is the workspace root; reports go to <root>/reports.
	Root string
	Now  func() time.Time
	Log  *slog.Logger
	// Emit receives run-stream events; optional.
	Emit func(Event)
	// IsFake says whether a provider id is the fake provider; the default asks
	// the LLM settings.
	IsFake func(ctx context.Context, providerID string) bool
	// CapReached reports whether the day's model spend limit is reached: the
	// daily cap or an evaluation task's budget. While it is, a quick screen's review waits for the user instead of
	// going on to the deep evaluation (F-8). Nil reads as not reached.
	CapReached func(ctx context.Context) bool
}

// JobTimeout is the whole-job deadline.
const JobTimeout = 20 * time.Minute

// Evaluator runs evaluation jobs.
type Evaluator struct{ d Deps }

// New returns an Evaluator, filling the optional dependencies.
func New(d Deps) (*Evaluator, error) {
	if d.Store == nil || d.Tracker == nil || d.Model == nil || d.Profile == nil {
		return nil, errors.New("evaluate: a store, tracker, model and profile source are required")
	}
	if d.Prompts == nil {
		d.Prompts = prompts.New("")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Blocked == nil {
		d.Blocked = func(string) bool { return false }
	}
	if d.CapReached == nil {
		d.CapReached = func(context.Context) bool { return false }
	}
	if d.IsFake == nil {
		st := d.Store
		d.IsFake = func(ctx context.Context, id string) bool {
			if id == llm.OfflineProviderID {
				return true
			}
			s, err := llm.LoadSettings(ctx, st)
			if err != nil {
				return false
			}
			p, ok := s.Provider(id)
			return ok && p.Kind == llm.KindFake
		}
	}
	if d.Reader != nil {
		d.Reader = demoReader{st: d.Store, inner: d.Reader}
	}
	return &Evaluator{d: d}, nil
}
