package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePage is what a fake tab shows after a navigation.
type fakePage struct {
	nav     Nav
	err     error // Goto fails with this
	block   bool  // Goto waits for its context to end
	extract extractResult
	probe   pageProbe
	detail  detailRead
	text    pageText
	click   clickResult
	overlay overlayState
	popups  []string
	// clear is what pressing a challenge checkbox does (no-challenge when unset).
	clear ChallengeClear
	// eval overrides the answer to a script, by marker.
	eval map[string]any
}

// fakeBrowser is a scan window whose pages come from a router.
type fakeBrowser struct {
	t     *testing.T
	mu    sync.Mutex
	route func(rawURL string) fakePage

	gotos     []string
	evals     map[string]int
	scripts   []string
	deadlines []time.Duration
	opened    int
	closed    int
	open      int
	maxOpen   int
	openErr   error
	// failOpenAfter makes OpenTab fail once this many tabs were opened (0: never).
	failOpenAfter int

	sleeps []time.Duration
}

func newFakeBrowser(t *testing.T, route func(rawURL string) fakePage) *fakeBrowser {
	return &fakeBrowser{t: t, route: route, evals: map[string]int{}}
}

func (fb *fakeBrowser) OpenTab(_ context.Context, owner, _ string) (Tab, error) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.openErr != nil {
		return nil, fb.openErr
	}
	if fb.failOpenAfter > 0 && fb.opened >= fb.failOpenAfter {
		return nil, errors.New("the browser went away")
	}
	fb.opened++
	fb.open++
	fb.maxOpen = max(fb.maxOpen, fb.open)
	return &fakeTab{fb: fb, owner: owner}, nil
}

func (fb *fakeBrowser) sleep(_ context.Context, d time.Duration) error {
	fb.mu.Lock()
	fb.sleeps = append(fb.sleeps, d)
	fb.mu.Unlock()
	return nil
}

func (fb *fakeBrowser) rand(lo, hi time.Duration) time.Duration {
	return lo
}

func (fb *fakeBrowser) visited() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.gotos...)
}

func (fb *fakeBrowser) count(marker string) int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.evals[marker]
}

type fakeTab struct {
	fb     *fakeBrowser
	owner  string
	page   fakePage
	url    string
	closed bool
	// clicked is set once a click script ran; popups appear only then.
	clicked bool
}

func (ft *fakeTab) Goto(ctx context.Context, rawURL string) (Nav, error) {
	ft.fb.mu.Lock()
	ft.fb.gotos = append(ft.fb.gotos, rawURL)
	pg := ft.fb.route(rawURL)
	if dl, ok := ctx.Deadline(); ok {
		ft.fb.deadlines = append(ft.fb.deadlines, time.Until(dl))
	}
	ft.fb.mu.Unlock()
	if pg.block {
		<-ctx.Done()
		return Nav{}, ctx.Err()
	}
	if pg.err != nil {
		return Nav{}, pg.err
	}
	ft.page, ft.url = pg, rawURL
	n := pg.nav
	if n.URL == "" {
		n.URL = rawURL
	}
	ft.page.nav = n
	return n, nil
}

var markerRE = regexp.MustCompile(`/\*aj:([a-z0-9-]+)\*/`)

func (ft *fakeTab) Eval(ctx context.Context, expr string, out any) error {
	m := markerRE.FindStringSubmatch(expr)
	marker := ""
	if m != nil {
		marker = m[1]
	}
	ft.fb.mu.Lock()
	ft.fb.evals[marker]++
	ft.fb.scripts = append(ft.fb.scripts, expr)
	ft.fb.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok && marker == "detail" {
		ft.fb.mu.Lock()
		ft.fb.deadlines = append(ft.fb.deadlines, time.Until(dl))
		ft.fb.mu.Unlock()
	}
	if marker == "click" {
		ft.clicked = true
	}
	pg := ft.page
	var v any
	if o, ok := pg.eval[marker]; ok {
		v = o
		if f, isFn := o.(func() any); isFn {
			v = f()
		}
	} else {
		switch marker {
		case "probe":
			pr := pg.probe
			if pr.URL == "" {
				pr.URL = pg.nav.URL
			}
			v = pr
		case "detail":
			v = pg.detail
		case "page-text":
			v = pg.text
		case "click":
			v = pg.click
		case "overlay":
			v = pg.overlay
		case "scroll", "scroll-end":
			v = map[string]any{"y": 0, "h": 800, "total": 4000}
		default:
			ex := pg.extract
			if ex.URL == "" {
				ex.URL = pg.nav.URL
			}
			v = ex
		}
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (ft *fakeTab) ClearChallenge(context.Context) (ChallengeClear, error) {
	ft.fb.mu.Lock()
	ft.fb.evals["clear-challenge"]++
	ft.fb.mu.Unlock()
	if ft.page.clear.Reason == "" {
		return ChallengeClear{Reason: "no-challenge"}, nil
	}
	return ft.page.clear, nil
}

func (ft *fakeTab) PopupURLs() []string {
	if !ft.clicked {
		return nil
	}
	return ft.page.popups
}

func (ft *fakeTab) Close() {
	if ft.closed {
		return
	}
	ft.closed = true
	ft.fb.mu.Lock()
	ft.fb.closed++
	ft.fb.open--
	ft.fb.mu.Unlock()
}

// ---- builders ----

func rawRows(cards ...rawCard) []json.RawMessage {
	out := make([]json.RawMessage, len(cards))
	for i, c := range cards {
		out[i], _ = json.Marshal(c)
	}
	return out
}

// cardsPage is a results page of the given cards.
func cardsPage(cards ...rawCard) fakePage {
	return fakePage{extract: extractResult{Cards: len(cards), Rows: rawRows(cards...)}}
}

// jobCard is a card of a numbered job; the day count is its printed age.
func jobCard(id int, title, company string) rawCard {
	return rawCard{ID: fmt.Sprint(id), Title: title, Company: company, Location: "Austin, TX", Age: "1 day ago",
		Href: fmt.Sprintf("https://www.example.test/c/%d", id)}
}

func manyCards(from, n int, company string) []rawCard {
	var out []rawCard
	for i := 0; i < n; i++ {
		out = append(out, jobCard(from+i, fmt.Sprintf("Backend Engineer %d", from+i), fmt.Sprintf("%s %d", company, from+i)))
	}
	return out
}

func postingText() string {
	s := "Build and run backend services for the product. "
	for len(s) < 260 {
		s += "You will write Go, review code and ship changes with the team. "
	}
	return s
}

// browserEnv is an env whose browser sources run on a fake window.
func browserEnv(t *testing.T, fb *fakeBrowser) *env {
	t.Helper()
	e := newEnv(t)
	e.sc.Browser = &BrowserEnv{Window: fb,
		Host: func(context.Context) HostSample { return HostSample{Verdict: PressureNormal, FreeMiB: 8000, CPUs: 8} },
		Rand: fb.rand, Sleep: fb.sleep, ContentWait: time.Millisecond, Poll: time.Microsecond,
		DetailNav: 40 * time.Millisecond, DetailEval: 20 * time.Millisecond, DetailRetry: 60 * time.Millisecond}
	return e
}

// googleRoute answers a Google Jobs results page with n cards, each with a
// share URL, and every posting page with a description and an employer link.
func googleRoute(n int, extra func(rawURL string, p fakePage) fakePage) func(string) fakePage {
	return func(raw string) fakePage {
		var cards []rawCard
		for i := 1; i <= n; i++ {
			c := jobCard(i, fmt.Sprintf("Backend Engineer %d", i), fmt.Sprintf("Company %d", i))
			c.Href = fmt.Sprintf("https://www.google.com/search?q=engineer&htidocid=doc%d", i)
			c.ID = c.Href
			cards = append(cards, c)
		}
		p := cardsPage(cards...)
		p.detail = detailRead{Desc: postingText(), Apply: &applyControl{Href: "https://careers.example.com/jobs/" + url.QueryEscape(raw[strings.LastIndex(raw, "=")+1:]), Text: "Apply"}}
		if extra != nil {
			p = extra(raw, p)
		}
		return p
	}
}
