package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"applysmith/internal/lazyregexp"
)

type candidate struct {
	provider   ProviderConfig
	model      string
	source     string // primary, fallback or override
	downgraded bool   // part of the cheaper head group of a downgraded order
	// authOnly marks the configured primary behind a downgraded head: it is
	// reached only after an auth failure on the head.
	authOnly bool
	// promoted marks a candidate that now goes ahead of one configured before
	// it, because that one is resting.
	promoted bool
	// kept marks the candidate an agent conversation stays on (affinity.go).
	kept bool
	// byMode is ReasonRotate or ReasonUsage on the candidate a group's mode
	// put in front of the one listed first (groups.go).
	byMode string
}

func (c candidate) name() string { return c.provider.ID + "/" + c.model }

// callState is what one call carries through its walk.
type callState struct {
	id       string
	task     Task
	res      Resolved
	settings Settings
	runID    string
	deadline time.Time
	cands    []candidate
	// attemptBase is the number of attempts an earlier part of the same call made.
	attemptBase int
	prompt      string // for debug capture only
	// won and attempts describe the successful attempt.
	won      *attempt
	attempts int
	// streams says the provider streams, which arms the stall watchdog.
	streams bool
	// searches is the number of web-search results the call asked for.
	searches int
	// prevKind is the kind of the attempt that failed last.
	prevKind Kind
	// noCapture keeps the call out of debug capture: agent turns carry the
	// tool results of the user's records.
	noCapture bool
}

// attempt is the live state of one attempt; the provider client fills usage,
// finish and status and calls touch on every stream event.
type attempt struct {
	cand    candidate
	key     string
	start   time.Time
	first   time.Time
	touchFn func()
	// firstFn runs once, at the first stream event.
	firstFn   func()
	delivered bool
	usage     Usage
	finish    string
	status    int
	text      string
	// estimated marks usage counted from text length.
	estimated bool
}

// touch records a stream event: it proves the stream is alive and, the first
// time, fixes the time to first token.
func (a *attempt) touch() {
	if a.first.IsZero() {
		a.first = time.Now()
		if a.firstFn != nil {
			a.firstFn()
		}
	}
	if a.touchFn != nil {
		a.touchFn()
	}
}

var (
	errStalled        = errors.New("stream stalled")
	errFirstToken     = errors.New("first token late")
	errAttemptTimeout = errors.New("attempt timed out")
)

func (cs *callState) remaining(now time.Time) time.Duration {
	if cs.deadline.IsZero() {
		return time.Duration(1<<62 - 1)
	}
	return cs.deadline.Sub(now)
}

// needsKey says whether the provider kind authenticates with an API key.
func needsKey(p ProviderConfig) bool { return p.Kind == KindOpenAI || p.Kind == KindAnthropic }

// walk tries the candidates in order, advancing only after the failure kinds
// that allow it. do performs one attempt and returns its error; it must call
// a.touch on stream events when the provider streams.
func (r *Router) walk(ctx context.Context, cs *callState, do func(ctx context.Context, a *attempt) error) error {
	var fails []AttemptError
	attemptNo := cs.attemptBase
	var previous *candidate
	captureOn := !cs.noCapture && r.captureOn(ctx)

	finish := func(last *Error) error {
		out := *last
		out.Task = cs.task.ID
		out.Attempts = fails
		return &out
	}
	exhausted := func(c candidate, msg string) error {
		attemptNo++
		row := r.baseRow(cs, c, attemptNo, previous)
		row.Outcome = string(KindDeadline)
		row.ErrorText = msg
		r.writeLedger(ctx, row, nil, false)
		fails = append(fails, AttemptError{Provider: c.provider.ID, Model: c.model, Kind: KindDeadline, Message: msg})
		return finish(&Error{Kind: KindDeadline, Message: msg})
	}

	authOK := false
	for i := 0; i < len(cs.cands); i++ {
		cand := cs.cands[i]
		if cand.authOnly && !authOK {
			continue
		}
		retried := false
	again:
		if rem := cs.remaining(time.Now()); rem < attemptFloor {
			return exhausted(cand, "the caller's deadline leaves too little time for "+cand.name())
		}
		if err := ctx.Err(); err != nil {
			fails = append(fails, AttemptError{Provider: cand.provider.ID, Model: cand.model, Kind: KindAborted, Message: "cancelled"})
			return finish(&Error{Kind: KindAborted, Message: "the caller cancelled"})
		}

		var key string
		var keyErr error
		if needsKey(cand.provider) {
			key, keyErr = r.apiKey(cand.provider.ID)
		} else if cand.provider.Kind == KindChatGPT {
			key, keyErr = r.chatgptToken(ctx)
		}

		if pr, ok := PresetByName(cand.provider.Preset); ok && pr.RequireUserPrice {
			if _, priced := cs.settings.Prices[cand.name()]; !priced {
				keyErr = &Error{Kind: KindUnconfigured, Message: "save USD per-million-token prices for " + cand.name() + " in Settings → Models before calling this regional API; its published CNY prices are not USD"}
			}
		}

		// A slot in the provider's cap; the wait counts toward the deadline.
		wctx, wcancel := ctx, context.CancelFunc(func() {})
		if !cs.deadline.IsZero() {
			wctx, wcancel = context.WithDeadline(ctx, cs.deadline)
		}
		release, werr := func() (func(), error) {
			if keyErr != nil {
				return func() {}, nil
			}
			return r.limiterFor(cand.provider.ID).acquire(wctx, cand.provider.Concurrency())
		}()
		wcancel()
		if werr != nil {
			if ctx.Err() != nil {
				fails = append(fails, AttemptError{Provider: cand.provider.ID, Model: cand.model, Kind: KindAborted, Message: "cancelled while waiting"})
				return finish(&Error{Kind: KindAborted, Message: "the caller cancelled while waiting for a slot"})
			}
			return exhausted(cand, "the deadline passed while waiting for a slot on "+cand.provider.ID)
		}
		if rem := cs.remaining(time.Now()); rem < attemptFloor {
			release()
			return exhausted(cand, "the caller's deadline leaves too little time for "+cand.name())
		}

		attemptNo++
		timeout, clamped := cs.res.Timeout, false
		if rem := cs.remaining(time.Now()); rem < timeout {
			timeout, clamped = rem, true
		}
		a := &attempt{cand: cand, key: key, start: time.Now()}
		var err error
		if keyErr != nil {
			err = keyErr
		} else {
			err = r.runAttempt(ctx, cs, a, timeout, do)
		}
		release()
		latency := time.Since(a.start)

		var fail *Error
		if err != nil {
			fail = r.classifyAttempt(ctx, cs, a, err)
		}
		row := r.baseRow(cs, cand, attemptNo, previous)
		row.Latency, row.Clamped = latency, clamped
		row.Usage, row.FinishReason, row.HTTPStatus = a.usage, a.finish, a.status
		if !a.first.IsZero() {
			t := a.first.Sub(a.start)
			row.TTFT = &t
		}
		cost := r.costing(ctx, cs.settings, cand.provider, cand.model, a.usage)
		if fail == nil {
			cost = cost.withSearch(cs.searches)
		}
		row.Billing, row.CostUSD = cost.Billing, cost.Cost
		row.Outcome = "ok"
		if fail != nil {
			row.Outcome = string(fail.Kind)
			if fail.Status != 0 {
				row.HTTPStatus = fail.Status
			}
			row.ErrorText = boundedText(fail.Message, key)
		}
		if a.estimated {
			row.ErrorText = strings.TrimSpace(row.ErrorText + " (usage estimated from text length)")
		}
		r.writeLedger(ctx, row, &capture{prompt: cs.prompt, completion: a.text}, captureOn)

		if fail == nil {
			r.noteSuccess(cand)
			cs.won, cs.attempts = a, attemptNo
			return nil
		}
		fail.Message = boundedText(fail.Message, key)
		r.noteFailure(cand, fail)
		fails = append(fails, AttemptError{Provider: cand.provider.ID, Model: cand.model, Kind: fail.Kind, Status: fail.Status, Message: fail.Message})
		c := cand
		previous, cs.prevKind = &c, fail.Kind

		// One bounded wait on the same candidate for a short 429 retry delay.
		if fail.Kind == KindRateLimited && !retried && fail.retryAfter > 0 {
			wait := time.Duration(fail.retryAfter) * time.Second
			if wait <= maxRetryAfter && cs.remaining(time.Now()) >= wait+attemptFloor {
				if r.sleep(ctx, wait) == nil {
					retried = true
					goto again
				}
			}
		}
		advance := advances[fail.Kind] && !a.delivered
		// A head that cannot be called at all (rejected or missing key) hands
		// the call to the configured primary: an over-cap call does not fail.
		if cand.downgraded && (fail.Kind == KindAuth || fail.Kind == KindUnconfigured) {
			advance, authOK = true, true
		}
		hasNext := false
		for _, n := range cs.cands[i+1:] {
			hasNext = hasNext || !n.authOnly || authOK
		}
		if !advance || !hasNext {
			return finish(fail)
		}
	}
	return finish(&Error{Kind: KindNoRoute, Message: "no candidate"})
}

// runAttempt runs one attempt under the row timeout (clamped by the deadline)
// and, for streaming providers, the stall watchdog.
func (r *Router) runAttempt(ctx context.Context, cs *callState, a *attempt, timeout time.Duration, do func(context.Context, *attempt) error) error {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// silent is set while a stall budget cut to the row timeout has heard
	// nothing: both watches then end together, and the silence is the stall.
	var silent atomic.Bool
	limit := time.AfterFunc(timeout, func() {
		if silent.Load() {
			cancel(errStalled)
			return
		}
		cancel(errAttemptTimeout)
	})
	defer limit.Stop()
	// A web-search call is not streamed, so its first byte is its last: no
	// stall and no first-token watch, only the row timeout.
	if k := a.cand.provider.Kind; cs.streams && cs.searches == 0 && (k == KindOpenAI || k == KindAnthropic || k == KindChatGPT) {
		stall := min(r.stall, timeout)
		silent.Store(stall == timeout)
		w := time.AfterFunc(stall, func() { cancel(errStalled) })
		defer w.Stop()
		a.touchFn = func() { silent.Store(false); w.Reset(stall) }
		if ft := cs.res.FirstToken; ft > 0 && ft < timeout {
			f := time.AfterFunc(ft, func() { cancel(errFirstToken) })
			defer f.Stop()
			a.firstFn = func() { f.Stop() }
		}
	}
	err := do(actx, a)
	if err != nil {
		if c := context.Cause(actx); c == errStalled || c == errAttemptTimeout || c == errFirstToken {
			return c
		}
	}
	return err
}

// classifyAttempt turns an attempt error into its kind, reading the cause of
// the context before the error text.
func (r *Router) classifyAttempt(ctx context.Context, cs *callState, a *attempt, err error) *Error {
	switch {
	case ctx.Err() != nil:
		return &Error{Kind: KindAborted, Message: "the caller cancelled"}
	case errors.Is(err, errFirstToken):
		return &Error{Kind: KindFirstTokenLate, Message: fmt.Sprintf("no stream event within the first-token deadline of %s", cs.res.FirstToken)}
	case errors.Is(err, errStalled):
		return &Error{Kind: KindStreamStalled, Message: "no stream event within the silence budget"}
	case errors.Is(err, errAttemptTimeout):
		return &Error{Kind: KindTimeout, Message: "the attempt exceeded its timeout"}
	}
	e := classifyError(err)
	out := *e
	return &out
}

func (r *Router) baseRow(cs *callState, c candidate, attemptNo int, previous *candidate) LedgerRow {
	row := LedgerRow{CallID: cs.id, StartedAt: r.now(), Task: cs.task.ID, ProviderID: c.provider.ID, ProviderKind: c.provider.Kind,
		Model: c.model, Attempt: attemptNo, CandidateSource: c.source, Downgraded: c.downgraded, RunID: cs.runID, Billing: BillingUnpriced}
	switch {
	case previous != nil && previous.name() == c.name():
		row.Reason = ReasonRetry
	case previous != nil:
		row.Replaced, row.Reason = previous.name(), ReasonFallback
		if cs.prevKind == KindFirstTokenLate {
			row.Reason = ReasonFirstToken
		}
	case c.downgraded:
		row.Reason = ReasonCapDowngrade
	case c.kept:
		row.Reason = ReasonAffinity
	case c.byMode != "":
		row.Reason = c.byMode
	case c.promoted:
		row.Reason = ReasonRested
	}
	return row
}

// Rest is a candidate that sits out after a failure. A resting candidate goes
// behind the ones that are not resting but is never dropped from a walk, so a
// rest can only reorder; if every candidate rests, the configured order stands.
type Rest struct {
	Provider string
	// Model is empty when the whole provider rests (out of credit, a dead key).
	Model    string
	Why      string
	Until    time.Time
	Failures int
}

// Why a candidate rests.
const (
	RestQuota       = "quota"
	RestRateLimit   = "rate-limit"
	RestUnavailable = "unavailable"
	RestDeadKey     = "dead-key"
	RestModelGone   = "model-gone"
)

const (
	restBase = time.Minute
	// restRateMax and restFaultMax cap the doubling of a short 429 and of a
	// timeout, 5xx or connection failure.
	restRateMax  = 30 * time.Minute
	restFaultMax = 10 * time.Minute
	// restRetryMax caps a delay the server asked for with Retry-After.
	restRetryMax = time.Hour
	// restQuota is the rest of an account out of credit that named no reset
	// time; restQuotaMax caps one that did.
	restQuota    = 30 * time.Minute
	restQuotaMax = 24 * time.Hour
	restDeadKey  = 30 * time.Minute
	restGone     = 30 * time.Minute
	// restForget is how long after a rest ended its failure count still grows;
	// a failure later than that starts again at one.
	restForget = 30 * time.Minute
)

// deadKeyText matches the replies of a key that was revoked or never valid.
var deadKeyText = lazyregexp.New(`(?i)invalid[_ ](api[_ ]?)?key|incorrect api key|api key (not valid|expired|revoked|is invalid)|revoked|deactivated|authentication[_ ]failed|invalid[_ ]x-api-key`)

func restKey(provider, model string) string {
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

// restKind says whether a failure rests its candidate, why, and whether the
// whole provider rests. Auth and bad-request failures do not rest: that would
// only hide a configuration error. The exceptions are a key that is clearly
// dead and an address that cannot be reached, which fail the same way for
// every model.
func restKind(f *Error) (why string, wide bool) {
	switch f.Kind {
	case KindQuota:
		return RestQuota, true
	case KindRateLimited:
		return RestRateLimit, false
	case KindNetwork:
		return RestUnavailable, f.Unreached
	case KindOverloaded, KindTimeout, KindStreamStalled:
		return RestUnavailable, false
	case KindModelGone:
		return RestModelGone, false
	case KindAuth:
		if f.Status == http.StatusUnauthorized || deadKeyText.MatchString(f.Message) {
			return RestDeadKey, true
		}
	}
	return "", false
}

func backoff(n int, base, limit time.Duration) time.Duration {
	if n > 16 {
		n = 16
	}
	return min(base<<(n-1), limit)
}

// restLength is how long the nth failure in a row rests: the server's own
// delay when it gave one, else a doubling from a minute.
func restLength(f *Error, why string, n int) time.Duration {
	hint := time.Duration(f.retryAfter) * time.Second
	switch why {
	case RestQuota:
		if hint > 0 {
			return min(hint, restQuotaMax)
		}
		return restQuota
	case RestRateLimit:
		if hint > 0 {
			return min(hint, restRetryMax)
		}
		return backoff(n, restBase, restRateMax)
	case RestUnavailable:
		if hint > 0 {
			return min(hint, restRetryMax)
		}
		return backoff(n, restBase, restFaultMax)
	case RestDeadKey:
		return restDeadKey
	}
	return restGone
}

// noteFailure rests the candidate that just failed, when the failure calls for
// it.
func (r *Router) noteFailure(c candidate, f *Error) {
	why, wide := restKind(f)
	if why == "" {
		return
	}
	model := c.model
	if wide {
		model = ""
	}
	key, now := restKey(c.provider.ID, model), r.now()
	r.restMu.Lock()
	defer r.restMu.Unlock()
	n := 1
	if prior, ok := r.rests[key]; ok && now.Before(prior.Until.Add(restForget)) {
		n = prior.Failures + 1
	}
	r.rests[key] = Rest{Provider: c.provider.ID, Model: model, Why: why, Failures: n, Until: now.Add(restLength(f, why, n))}
}

// noteSuccess ends the rests of a candidate that answered: its own and its
// provider's.
func (r *Router) noteSuccess(c candidate) {
	r.restMu.Lock()
	defer r.restMu.Unlock()
	delete(r.rests, restKey(c.provider.ID, c.model))
	delete(r.rests, restKey(c.provider.ID, ""))
}

// restOf is the rest that keeps a candidate at the back now, if any.
func (r *Router) restOf(c candidate) (Rest, bool) {
	now := r.now()
	r.restMu.Lock()
	defer r.restMu.Unlock()
	for _, k := range []string{restKey(c.provider.ID, c.model), restKey(c.provider.ID, "")} {
		if rest, ok := r.rests[k]; ok && now.Before(rest.Until) {
			return rest, true
		}
	}
	return Rest{}, false
}

// Rests lists the candidates resting now, by provider and model.
func (r *Router) Rests() []Rest {
	now := r.now()
	r.restMu.Lock()
	defer r.restMu.Unlock()
	var out []Rest
	for k, rest := range r.rests {
		switch {
		case now.Before(rest.Until):
			out = append(out, rest)
		case !now.Before(rest.Until.Add(restForget)):
			delete(r.rests, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Provider+"/"+out[i].Model < out[j].Provider+"/"+out[j].Model
	})
	return out
}

// ClearRests ends every rest of a provider, for a key that was just replaced.
func (r *Router) ClearRests(provider string) {
	r.restMu.Lock()
	defer r.restMu.Unlock()
	for k, rest := range r.rests {
		if rest.Provider == provider {
			delete(r.rests, k)
		}
	}
}

// restOrder moves the resting candidates behind the others, keeping the
// configured order inside each group. The configured primary behind a
// downgraded head stays last: it is reached only after an auth failure.
func (r *Router) restOrder(cands []candidate) []candidate {
	if len(cands) < 2 {
		return cands
	}
	rank := make([]int, len(cands))
	anyResting := false
	for i, c := range cands {
		_, resting := r.restOf(c)
		switch {
		case c.authOnly:
			rank[i] = 2
		case resting:
			rank[i], anyResting = 1, true
		}
	}
	if !anyResting {
		return cands
	}
	type placed struct {
		c    candidate
		rank int
	}
	ps := make([]placed, len(cands))
	behind := false
	for i, c := range cands {
		if rank[i] == 1 {
			behind = true
		} else if rank[i] == 0 && behind {
			c.promoted = true
		}
		ps[i] = placed{c, rank[i]}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].rank < ps[j].rank })
	out := make([]candidate, len(ps))
	for i, p := range ps {
		out[i] = p.c
	}
	return out
}
