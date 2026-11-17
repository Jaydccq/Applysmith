package apply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/engine"
)

// ProbeResult is what a liveness probe concluded about a posting page.
type ProbeResult string

const (
	// ProbeLive: the page reads as the posting.
	ProbeLive ProbeResult = "live"
	// ProbeExpired: the posting is closed or gone.
	ProbeExpired ProbeResult = "expired"
	// ProbeWrongJob: the page names another job than the row.
	ProbeWrongJob ProbeResult = "wrong-job"
	// ProbeChallenge: a bot check stands where the page should be.
	ProbeChallenge ProbeResult = "challenge"
	// ProbeUnknown: the page could not be read or timed out; the chain
	// diagnoses it.
	ProbeUnknown ProbeResult = "unknown"
)

// Probe sizes.
const (
	probePages       = 3
	probePageTimeout = 25 * time.Second
	probeSettleReads = 4
)

// ProbeTarget is a posting page to look at, with the job it should name.
type ProbeTarget struct {
	URL, Company, Role string
}

// ProbeVerdict is the result for the target at the same index.
type ProbeVerdict struct {
	Result ProbeResult
	Reason string
}

// Prober looks at the pages of the selected rows.
type Prober interface {
	Probe(ctx context.Context, targets []ProbeTarget) ([]ProbeVerdict, error)
}

// LivenessProber probes through one browser window, a few pages at a time.
type LivenessProber struct {
	Window Window
	// Pages is how many pages load at once; zero reads as 3.
	Pages int
	// Timeout bounds one page; zero reads as 25 seconds.
	Timeout time.Duration
	// Settle is the wait between reads of a page still loading; zero reads as
	// half a second, negative waits not at all.
	Settle time.Duration
}

// Probe loads each target and reads what the page says about the posting. A
// page that cannot be loaded or read is unknown, never a stop: the chain
// diagnoses it when its row runs.
func (p LivenessProber) Probe(ctx context.Context, targets []ProbeTarget) ([]ProbeVerdict, error) {
	out := make([]ProbeVerdict, len(targets))
	sem := make(chan struct{}, orInt(p.Pages, probePages))
	var wg sync.WaitGroup
	for i, t := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return out, ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = p.probeOne(ctx, t)
		}()
	}
	wg.Wait()
	return out, ctx.Err()
}

func orInt(n, d int) int {
	if n > 0 {
		return n
	}
	return d
}

func (p LivenessProber) probeOne(ctx context.Context, t ProbeTarget) ProbeVerdict {
	unknown := func(format string, a ...any) ProbeVerdict {
		return ProbeVerdict{Result: ProbeUnknown, Reason: fmt.Sprintf(format, a...)}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = probePageTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tab, err := p.Window.OpenTab(pctx, "list-probe", engine.Host{})
	if err != nil {
		return unknown("no page to load: %v", err)
	}
	defer tab.Close(context.WithoutCancel(ctx))
	nav, err := tab.NavigateDOM(pctx, t.URL)
	if embed := discovery.AshbyEmbedURL(t.URL); embed != "" && err == nil && (nav.Status == 404 || nav.Status == 410) {
		if en, eerr := tab.NavigateDOM(pctx, embed); eerr == nil && en.Status > 0 && en.Status < 400 {
			nav = en
		}
	}
	switch {
	case err != nil:
		return unknown("the page did not load: %v", err)
	case nav.Status == 404 || nav.Status == 410:
		return ProbeVerdict{Result: ProbeExpired, Reason: fmt.Sprintf("HTTP %d", nav.Status)}
	case nav.Status >= 500 || nav.Error != "":
		return unknown("the site is not answering (HTTP %d %s)", nav.Status, nav.Error)
	}
	var st *PageState
	for i := 0; i < probeSettleReads; i++ {
		if st, err = Probe(pctx, tab); err != nil {
			return unknown("the page could not be read: %v", err)
		}
		if !st.Loading {
			break
		}
		if wait := p.settle(); wait > 0 {
			select {
			case <-time.After(wait):
			case <-pctx.Done():
				return unknown("the page did not finish loading: %v", pctx.Err())
			}
		}
	}
	return classifyPage(nav, st, t)
}

func (p LivenessProber) settle() time.Duration {
	switch {
	case p.Settle < 0:
		return 0
	case p.Settle == 0:
		return 500 * time.Millisecond
	}
	return p.Settle
}

// classifyPage reads a loaded page. Only the page's own copy counts: an
// interstitial's wording, a closed-posting statement, or a heading naming a
// different job.
func classifyPage(nav browser.NavResult, st *PageState, t ProbeTarget) ProbeVerdict {
	if note := BotInterstitial(st.Title, st.Lines); note != "" {
		return ProbeVerdict{Result: ProbeChallenge, Reason: clip(note, 160)}
	}
	if note := ClosedNotice(st.Lines); note != "" {
		return ProbeVerdict{Result: ProbeExpired, Reason: clip(note, 160)}
	}
	if role := PageRole(st.Title, st.Headings, ""); StaleLink(role, t.Role, t.Company) {
		return ProbeVerdict{Result: ProbeWrongJob, Reason: clip(fmt.Sprintf("the page names %q, the row is %q", role, t.Role), 160)}
	}
	return ProbeVerdict{Result: ProbeLive}
}

// probeRows probes the rows that survived the rules, through one browser, and
// turns expiry, wrong-job evidence and a challenge into precheck stops written
// in list order. An unreadable or timed-out page stays unknown and the row
// runs.
func (s *Selector) probeRows(ctx context.Context, res *SelectResult, opts SelectOptions) {
	if s.Probe == nil || len(res.Rows) == 0 {
		return
	}
	targets := make([]ProbeTarget, len(res.Rows))
	for i, r := range res.Rows {
		targets[i] = ProbeTarget{URL: firstNonEmpty(r.EmployerURL, r.URL), Company: r.Company, Role: r.Role}
	}
	verdicts, err := s.Probe.Probe(ctx, targets)
	if err != nil && len(verdicts) != len(targets) {
		return
	}
	kept := res.Rows[:0]
	for i, r := range res.Rows {
		if i >= len(verdicts) {
			kept = append(kept, r)
			continue
		}
		reason, class, owner := "", ClassPosting, ownerPost
		switch v := verdicts[i]; v.Result {
		case ProbeExpired:
			reason = "liveness expired: " + v.Reason
		case ProbeWrongJob:
			reason = "liveness mismatch: " + v.Reason
		case ProbeChallenge:
			reason, class, owner = "bot-challenge: "+v.Reason+"; complete verification yourself", ClassDefect, ownerUser
		default:
			kept = append(kept, r)
			continue
		}
		c := Candidate{URL: r.URL, Company: r.Company, Role: r.Role}
		s.recordStop(ctx, res, c, reason, class, opts)
		res.Skipped = append(res.Skipped, Skipped{Company: r.Company, Role: r.Role, URL: r.URL, Rule: RulePrecheck, Reason: strings.TrimSpace(reason), Owner: owner})
	}
	res.Rows = kept
}
