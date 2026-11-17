package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Why a page that shows no job cards is not an empty result.
const (
	blockNone      = ""
	blockSignIn    = "sign-in"
	blockChallenge = "challenge"
	blockRateLimit = "rate-limited"
)

var (
	// A challenge is a wall in front of the board; the words are the ones the
	// boards' walls use, read from the page only while no card has rendered.
	challengePageRE = lazyregexp.New(`(?i)verify you are (a )?human|are you a (human|robot)|unusual traffic|security check|captcha|access denied|just a moment|checking your browser|additional verification|press (&|and) hold|automated queries|you have been blocked|cf-chl`)
	rateLimitPageRE = lazyregexp.New(`(?i)too many requests|rate limit|error 429|\b429\b.{0,30}(error|too many)|slow down`)
	signInURLRE     = lazyregexp.New(`(?i)/(?:login(?:_input\.htm)?|log-in|signin|sign-in|authwall|checkpoint|uas/login|accounts/login|profile/login|auth)(?:/|$|\?)`)
	transientNavRE  = lazyregexp.New(`(?i)navigation in progress|ERR_ABORTED`)
)

// errTransientNav is a navigation the site interrupted by appending tracking
// parameters after load. It is retried, never a challenge or a failure.
var errTransientNav = errors.New("navigation in progress")

// errIdentity is a result page whose live URL contradicts the search.
var errIdentity = errors.New("the live URL contradicts the requested search")

// extractResult is what a card extractor returns: the card containers seen
// and the rows read from them.
type extractResult struct {
	URL   string            `json:"url"`
	Cards int               `json:"cards"`
	Rows  []json.RawMessage `json:"rows"`
}

// rawCard is one list card as the page shows it.
type rawCard struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Company  string `json:"company"`
	Location string `json:"location"`
	Href     string `json:"href"`
	Age      string `json:"age"`
	Datetime string `json:"datetime"`
	Salary   string `json:"salary"`
	Easy     bool   `json:"easy"`
	Extra    string `json:"extra"`
}

type pageProbe struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Password bool   `json:"password"`
	Dialog   bool   `json:"dialog"`
}

// pageSpec is one list page request.
type pageSpec struct {
	url     string
	extract string
	// signInText matches the text of a signed-out page that shows no cards.
	signInText *lazyregexp.Regexp
	// guard reports whether the live URL still names the requested search.
	guard func(live string) bool
	// scrollCards scrolls a virtualised list until its card count settles.
	scrollCards string
	// once looks at the page a single time, for a page whose list arrives
	// through an API and not in the markup.
	once bool
	// stayOn is the path prefix a signed-in visit stays on; a redirect off
	// it means the visit is signed out.
	stayOn string
}

type listRead struct {
	Nav   Nav
	Live  string
	Cards int
	Rows  []json.RawMessage
	Block string
	// Signal is the words or the address that made the page a wall.
	Signal string
	// Cleared is the verdict of a Cloudflare checkbox press that did not
	// clear the wall.
	Cleared string
}

// cards decodes the rows as list cards; a row that is not one is dropped.
func (lr listRead) cards() []rawCard {
	out := make([]rawCard, 0, len(lr.Rows))
	for _, raw := range lr.Rows {
		var c rawCard
		if json.Unmarshal(raw, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// ThrottleStatus reports an HTTP status a site holds a reader back with: 429,
// and the 999 LinkedIn answers instead.
func ThrottleStatus(code int) bool { return code == 429 || code == 999 }

// RateLimitPage reports a page that says its site is throttling the reader: the
// words in its title, or in a text too short to be anything else.
func RateLimitPage(title, text string) bool {
	return rateLimitPageRE.MatchString(title) || (len(text) < 600 && rateLimitPageRE.MatchString(text))
}

// classify decides why a page with no cards is blocked, by precedence: a
// sign-in URL, a challenge, a rate limit, then a signed-out page.
func classifyPage(nav Nav, p pageProbe, signInText *lazyregexp.Regexp) (block, signal string) {
	live := p.URL
	if live == "" {
		live = nav.URL
	}
	text := p.Title + " " + p.Text
	path := ""
	if u, err := url.Parse(live); err == nil {
		path = u.Path + "/"
	}
	switch {
	case signInURLRE.MatchString(path):
		return blockSignIn, "sign-in URL " + path
	case nav.Status == 403:
		return blockChallenge, "HTTP 403 access denied"
	case challengePageRE.MatchString(text):
		return blockChallenge, challengePageRE.FindString(text)
	case strings.Contains(strings.ToLower(live), "/captcha"):
		return blockChallenge, "captcha URL"
	case ThrottleStatus(nav.Status):
		return blockRateLimit, fmt.Sprintf("HTTP %d", nav.Status)
	case RateLimitPage(p.Title, p.Text):
		return blockRateLimit, rateLimitPageRE.FindString(p.Title + " " + p.Text)
	case p.Password && len(p.Text) < 1500:
		return blockSignIn, "password field"
	case signInText != nil && signInText.MatchString(p.Text):
		return blockSignIn, signInText.FindString(p.Text)
	}
	return blockNone, ""
}

// ensureTab opens the list tab on first use and again after it died.
func (b *bscan) ensureTab(ctx context.Context) error {
	if b.tab != nil {
		return nil
	}
	t, err := b.win.OpenTab(ctx, "scan-"+b.r.req.Source, b.host())
	if err != nil {
		return err
	}
	b.tab = t
	return nil
}

// dropTab discards a tab that failed, so the retry opens a fresh one.
func (b *bscan) dropTab() {
	if b.tab != nil {
		b.tab.Close()
		b.tab = nil
	}
}

func (b *bscan) readOnce(ctx context.Context, ps pageSpec) (listRead, error) {
	var lr listRead
	if err := b.ensureTab(ctx); err != nil {
		return lr, err
	}
	b.r.addDetail("list-opens", 1)
	nav, err := b.tab.Goto(ctx, ps.url)
	if err != nil {
		return lr, err
	}
	lr.Nav = nav
	if nav.Failed != "" {
		if transientNavRE.MatchString(nav.Failed) {
			return lr, errTransientNav
		}
		return lr, fmt.Errorf("the page did not load: %s", nav.Failed)
	}
	if ps.stayOn != "" && !strings.HasPrefix(pathOf(nav.URL), ps.stayOn) {
		lr.Block, lr.Live, lr.Signal = blockSignIn, nav.URL, "redirected off "+ps.stayOn
		return lr, nil
	}
	// These protocol walls are conclusive even if the document cannot be read.
	if nav.Status == 403 || ThrottleStatus(nav.Status) {
		lr.Live = nav.URL
		lr.Block, lr.Signal = classifyPage(nav, pageProbe{}, ps.signInText)
		return lr, nil
	}
	deadline := time.Now().Add(b.env.contentWait())
	for {
		var ex extractResult
		if err := b.tab.Eval(ctx, ps.extract, &ex); err != nil {
			return lr, err
		}
		lr.Live, lr.Cards, lr.Rows = ex.URL, ex.Cards, ex.Rows
		if ex.Cards > 0 {
			break
		}
		var pr pageProbe
		if err := b.tab.Eval(ctx, probeJS, &pr); err != nil {
			return lr, err
		}
		if lr.Live == "" {
			lr.Live = pr.URL
		}
		if lr.Block, lr.Signal = classifyPage(nav, pr, ps.signInText); lr.Block != blockNone || nav.Status >= 400 || ps.once || time.Now().After(deadline) {
			break
		}
		if err := b.env.sleep(ctx, b.env.poll()); err != nil {
			return lr, err
		}
	}
	// A rendered server error is not a successful empty freshness window.
	// Challenge/sign-in classification above keeps its no-retry precedence.
	if lr.Block == blockNone && nav.Status >= 400 {
		err := fmt.Errorf("the main document answered HTTP %d", nav.Status)
		if nav.Status >= 500 {
			return lr, &TransientError{URL: ps.url, Status: nav.Status, Err: err}
		}
		return lr, err
	}
	if lr.Live == "" {
		lr.Live = nav.URL
	}
	if ps.scrollCards != "" && lr.Cards > 0 {
		for i := 0; i < 4; i++ {
			if err := b.tab.Eval(ctx, scrollEndJS(ps.scrollCards), nil); err != nil {
				return lr, err
			}
			if err := b.env.sleep(ctx, b.env.poll()); err != nil {
				return lr, err
			}
			var ex extractResult
			if err := b.tab.Eval(ctx, ps.extract, &ex); err != nil {
				return lr, err
			}
			grown := ex.Cards > lr.Cards
			lr.Cards, lr.Rows = ex.Cards, ex.Rows
			if !grown {
				break
			}
		}
	}
	// A signed-out page can also leave the path after it loaded (Jobright
	// rewrites the address to "/").
	if ps.stayOn != "" && lr.Block == blockNone && !strings.HasPrefix(pathOf(lr.Live), ps.stayOn) {
		lr.Block, lr.Signal = blockSignIn, "redirected off "+ps.stayOn
		return lr, nil
	}
	if ps.guard != nil && lr.Cards > 0 && !ps.guard(lr.Live) {
		return lr, errIdentity
	}
	return lr, nil
}

// readList reads one list page. A transient navigation is retried at most
// twice, any other failure once; the second failure leaves the page
// `page-failed` and the run goes on. Only a host with no network, a cancelled
// run and a sign-in wall end the whole scan.
func (b *bscan) readList(ctx context.Context, ps pageSpec) (listRead, error) {
	start := b.r.repo.now()
	var lr listRead
	var err error
	transient, failures, attempts := 0, 0, 0
	for {
		attempts++
		lr, err = b.readOnce(ctx, ps)
		if err == nil && lr.Block == blockChallenge && b.clearChallenge(ctx, &lr) {
			continue
		}
		switch {
		case err == nil, ctx.Err() != nil, errors.Is(err, ErrOffline):
		case errors.Is(err, errTransientNav) && transient < maxTransientNav:
			transient++
			b.r.addDetail("retries", 1)
			continue
		default:
			failures++
			b.dropTab()
			if failures < maxPageAttempts {
				b.r.addDetail("retries", 1)
				continue
			}
		}
		break
	}
	b.reads++
	b.attempt(ps.url, lr, err, start, attempts)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrOffline) {
			b.r.addDetail("page-failed", 1)
			b.r.noteError("%s: %s", hostOf(ps.url), firstLine(err))
		}
		return lr, err
	}
	return lr, nil
}

// clearChallenge presses the checkbox of a Cloudflare challenge on the list
// tab, at most maxChallengeClears attempts a run. True when the page is to be
// read again: the wall went away, or the attempt failed and another remains,
// which then meets a fresh widget (a press that came too early can leave a
// widget no later press passes). The last failed attempt's verdict joins the
// page.
func (b *bscan) clearChallenge(ctx context.Context, lr *listRead) bool {
	if b.clears >= maxChallengeClears || b.tab == nil {
		return false
	}
	res, err := b.tab.ClearChallenge(ctx)
	if err == nil && !res.Present {
		return false
	}
	b.clears++
	b.r.addDetail("challenge-clicks", res.Clicks)
	if err == nil && res.Solved {
		b.r.addDetail("challenges-cleared", 1)
		return true
	}
	if err == nil && b.clears < maxChallengeClears {
		return true
	}
	lr.Cleared = res.Reason
	if err != nil {
		lr.Cleared = firstLine(err)
	}
	return false
}

// attempt appends the bounded health record of a list page.
func (b *bscan) attempt(rawURL string, lr listRead, err error, start time.Time, n int) {
	st := browserAttemptStatus(lr, err)
	u, _ := url.Parse(rawURL)
	a := Attempt{Status: st, HTTP: lr.Nav.Status, LatencyMS: b.r.repo.now().Sub(start).Milliseconds(), N: n}
	if u != nil {
		a.Host, a.Path = u.Host, u.Path
	}
	b.r.mu.Lock()
	defer b.r.mu.Unlock()
	b.r.rec.Attempts = append(b.r.rec.Attempts, a)
	if over := len(b.r.rec.Attempts) - maxAttempts; over > 0 {
		b.r.rec.Attempts = append([]Attempt(nil), b.r.rec.Attempts[over:]...)
	}
}

// browserAttemptStatus picks the status by the precedence challenge,
// rate-limited, timeout, error, empty, ok: a page with a challenge and no
// items is a challenge and never empty.
func browserAttemptStatus(lr listRead, err error) string {
	switch {
	case lr.Block == blockChallenge:
		return AttemptChallenge
	case lr.Block == blockRateLimit:
		return AttemptRateLimited
	case errors.Is(err, context.DeadlineExceeded):
		return AttemptTimeout
	case err != nil || lr.Block == blockSignIn:
		return AttemptError
	case lr.Cards == 0 || len(lr.Rows) == 0:
		return AttemptEmpty
	}
	return AttemptOK
}

// pageEnds turns a blocked list page into the end of the run.
func (b *bscan) pageEnds(lr listRead, rawURL string) *endRun {
	switch lr.Block {
	case blockSignIn:
		return &endRun{status: RunNeedsSignIn, reason: ReasonNeedsSignIn, err: signInDetail(b.r.req.Source)}
	case blockChallenge, blockRateLimit:
		cleared := ""
		if lr.Cleared != "" {
			cleared = ", checkbox " + lr.Cleared
		}
		return &endRun{status: RunBlocked, reason: lr.Block, err: fmt.Errorf("%s answered with a %s (matched %q, %d job cards on the page%s); the run stops without retrying",
			hostOf(rawURL), strings.ReplaceAll(lr.Block, "-", " "), lr.Signal, lr.Cards, cleared)}
	}
	return nil
}

// jsString quotes a Go string as a JavaScript string literal.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// firstLine is the first line of an error: a browser call log follows it,
// and the last line of that log says nothing about the failure.
func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
