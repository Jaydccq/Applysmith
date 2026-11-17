package learn

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"sync"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/store"
)

// stampLayout is the one timestamp format of the learning tables: UTC with
// millisecond precision, so the text sorts as time.
const stampLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

func parseStamp(s string) time.Time {
	t, _ := time.Parse(stampLayout, s)
	return t
}

// ioTimeout bounds one best-effort database access.
const ioTimeout = 5 * time.Second

// Routed is what the matcher does with one question.
type Routed struct {
	EntryID string
	Answer  string
	Outcome match.Outcome
	// Reason says why an abstain abstained.
	Reason match.Reason
	// ByParent marks a follow-up the answer before it settles ("N/A" under a
	// No): it has no entry of its own, and EntryID is that answer's.
	ByParent bool
	// Own marks an answer the user wrote into the bank for the question, as
	// written (match.Bank.OwnAnswer): not one the profile derives, a load
	// stamped or a model gave.
	Own bool
	// Source is where the matcher took the answer from.
	Source match.Source
}

// Fills reports a result that writes an answer from an entry.
func (r Routed) Fills() bool {
	if r.EntryID == "" {
		return false
	}
	switch r.Outcome {
	case match.OutcomeFilled, match.OutcomeFilledReview, match.OutcomeFilledModel:
		return true
	}
	return false
}

// RouteFunc runs a matcher over its bank for the last of questions asked in
// page order. Facts are the profile values the fill reads beside the bank and
// posting the application the questions were asked for: the Router, which says
// what a run's question gets now, passes them; adding an entry and generating
// candidates compare with the bank's own entries and pass none. The Router
// keeps one matcher for all its questions, so each host's entries are indexed
// once.
type RouteFunc func(m *match.Matcher, facts match.Facts, host string, posting match.Posting, qs ...match.Question) Routed

// Options replaces the outside world of the service (tests).
type Options struct {
	Now   func() time.Time
	Log   *slog.Logger
	Rand  io.Reader
	Route RouteFunc
	// Facts reads the profile values the fill passes the matcher; nil routes
	// without them.
	Facts func() match.Facts
	// Review sorts one waiting new-entry candidate and may draft its answer
	// (ReviewPending). It is a model call, so it is made outside every lock and
	// transaction; nil leaves every candidate unreviewed.
	Review func(ctx context.Context, c Candidate) (Review, error)
	// Reviewed is told after a review pass stored a review, so the dashboard
	// reads the suggestions again; nil tells no one.
	Reviewed func()
	// Generated is told after a generation pass created, re-proposed or
	// refreshed a suggestion, for the same reason; nil tells no one.
	Generated func()
}

// Service owns the learning tables of the workspace database. It reads and
// writes the bank only through the answerbank service.
type Service struct {
	st    *store.Store
	bank  *answerbank.Service
	log   *slog.Logger
	now   func() time.Time
	rand  io.Reader
	route RouteFunc
	facts func() match.Facts

	// review, reviewed and generated are Options.Review, Options.Reviewed and
	// Options.Generated. life ends when the service closes; a background review
	// runs on it.
	review    func(ctx context.Context, c Candidate) (Review, error)
	reviewed  func()
	generated func()
	life      context.Context
	stop      context.CancelFunc

	keyMu sync.Mutex
	key   []byte // the HMAC key of the system-write digests, loaded on first use

	// mu serialises generation, approval and rejection, so two decisions on one
	// candidate can never both write the bank. It guards presses too.
	mu      sync.Mutex
	presses map[int64]press      // the last keep or leave press of each fill (UndoEdit)
	forgot  map[string]forgotten // the entries Forget deleted, by id (Unforget)

	bgMu      sync.Mutex
	bgRunning bool
	bgDirty   bool
	bgWG      sync.WaitGroup

	// revMu guards the one review that runs (ReviewPending): revAgain asks it
	// for one more pass.
	revMu      sync.Mutex
	revRunning bool
	revAgain   bool

	// events carries telemetry to the one goroutine that writes it, so a slow or
	// locked database never delays the caller.
	evMu     sync.Mutex
	events   chan []eventRow
	evClosed bool
	evPend   sync.WaitGroup
	evDone   chan struct{}
	// beforeBackground is a test seam run first in every background pass.
	beforeBackground func()
}

// New builds the service over an open store and the bank service.
func New(st *store.Store, bank *answerbank.Service, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Route == nil {
		o.Route = matcherRoute(o.Now)
	}
	s := &Service{st: st, bank: bank, log: o.Log, now: o.Now, rand: o.Rand, route: o.Route, facts: o.Facts, review: o.Review, reviewed: o.Reviewed, generated: o.Generated,
		events: make(chan []eventRow, eventQueue), evDone: make(chan struct{})}
	s.life, s.stop = context.WithCancel(context.Background())
	go s.writeEvents()
	return s
}

// profileFacts are the profile values the Router routes with.
func (s *Service) profileFacts() match.Facts {
	if s.facts == nil {
		return match.Facts{}
	}
	return s.facts()
}

// matcherRoute runs the real matcher over the stamped bank for the last of
// questions asked in page order.
func matcherRoute(now func() time.Time) RouteFunc {
	return func(m *match.Matcher, facts match.Facts, host string, posting match.Posting, qs ...match.Question) Routed {
		t := now().UTC()
		req := match.Request{Host: host, Posting: posting, Today: match.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()}, Questions: qs, Facts: facts}
		resp, err := m.Match(req)
		if err != nil || len(qs) == 0 || len(resp.Results) != len(qs) {
			return Routed{}
		}
		r := resp.Results[len(qs)-1]
		ans := r.Answer
		if r.ChosenLabel != "" {
			ans = r.ChosenLabel
		}
		out := Routed{EntryID: r.EntryID, Answer: ans, Outcome: r.Outcome, Reason: r.Reason, Own: m.Bank().OwnAnswer(r), Source: r.Source}
		// A follow-up the answer before it decided is answered through that answer's entry.
		if len(qs) > 1 && r.Source == match.SourceConditional && r.EntryID == "" && r.Outcome != match.OutcomeAbstained {
			out.EntryID, out.ByParent = resp.Results[len(qs)-2].EntryID, true
		}
		return out
	}
}

// routeBank routes over bank with a matcher of its own.
func (s *Service) routeBank(bank match.Bank, facts match.Facts, host string, posting match.Posting, qs ...match.Question) Routed {
	return s.route(match.New(bank, match.DefaultStages()), facts, host, posting, qs...)
}

// Settle waits for the background generation and review that are running.
func (s *Service) Settle() { s.bgWG.Wait() }

// Close ends a background review, waits for background generation and writes
// the queued telemetry. Call it before the store closes.
func (s *Service) Close() {
	s.stop()
	s.Settle()
	s.evMu.Lock()
	if !s.evClosed {
		s.evClosed = true
		close(s.events)
	}
	s.evMu.Unlock()
	<-s.evDone
}

// AfterIngest schedules candidate generation after the response of an upload
// has been decided. Concurrent calls coalesce into one more pass, and a
// failure is logged only.
func (s *Service) AfterIngest() {
	s.bgMu.Lock()
	if s.bgRunning {
		s.bgDirty = true
		s.bgMu.Unlock()
		return
	}
	s.bgRunning = true
	s.bgWG.Add(1)
	s.bgMu.Unlock()
	go func() {
		defer s.bgWG.Done()
		if s.beforeBackground != nil {
			s.beforeBackground()
		}
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if _, err := s.Generate(ctx, GenerateOptions{}); err != nil {
				s.log.Warn("answer-bank candidate generation failed", "error", err.Error())
			}
			cancel()
			s.bgMu.Lock()
			if !s.bgDirty {
				s.bgRunning = false
				s.bgMu.Unlock()
				return
			}
			s.bgDirty = false
			s.bgMu.Unlock()
		}
	}()
}

// ReviewLater reviews the waiting candidates in the background
// (ReviewPending) until the service closes, so a caller never waits on a model.
// Without Options.Review it does nothing.
func (s *Service) ReviewLater() {
	if s.review == nil || s.life.Err() != nil {
		return
	}
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		if _, err := s.ReviewPending(s.life); err != nil && s.life.Err() == nil {
			s.log.Warn("answer-bank suggestion review failed", "error", err.Error())
		}
	}()
}
