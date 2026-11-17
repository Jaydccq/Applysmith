// Clean-room: docs/provenance.md#CR-EXTRACT

package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// detailSpec says how a board's posting page is read.
type detailSpec struct {
	sel detailSel
	// pageURL is the page of a posting; the row URL by default.
	pageURL func(p Posting) string
	// gapLo and gapHi separate detail reads when set.
	gapLo, gapHi time.Duration
	// boardHost reports a host of the board itself, never an employer.
	boardHost func(host string) bool
	// read replaces the generic posting-page read for a board with its own.
	read func(ctx context.Context, b *bscan, tab Tab, p *Posting) error
	// otherKey reports a destination that names another posting on the board.
	otherKey func(landing, own string) bool
}

var errDetailBlocked = errors.New("a challenge answered the detail read")

var (
	assetExtRE   = lazyregexp.New(`(?i)\.(js|css|png|jpe?g|gif|svg|ico|woff2?|ttf|map|json|xml|webp)(\?|$)`)
	trackingRE   = lazyregexp.New(`(?i)(doubleclick|googletagmanager|google-analytics|googleadservices|googlesyndication|adsrvr|bat\.bing|facebook\.com/tr|px\.ads|analytics|pixel|beacon|/collect\b|/track(ing)?\b|/ads?/)`)
	redirectKeys = []string{"url", "u", "redirect", "redirect_url", "dest", "destination", "target"}
)

// unwrapRedirect reads the destination a board's outbound link carries in a
// query parameter ("safety/go?url=...").
func unwrapRedirect(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	for _, k := range redirectKeys {
		if v := u.Query().Get(k); strings.HasPrefix(v, "http") {
			return v
		}
	}
	return raw
}

// usefulApplyURL screens an employer URL: an http(s) page, not a static
// asset and not a tracking or advertising address.
func usefulApplyURL(raw string, boardHost func(string) bool) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	if boardHost != nil && boardHost(strings.ToLower(u.Hostname())) {
		return false
	}
	return !assetExtRE.MatchString(u.Path) && !trackingRE.MatchString(u.Host+u.Path)
}

// listingBoards are the job boards whose posting pages carry an employer's
// posting. An evaluation reads such a page as the board's scan reads it.
var listingBoards = []*detailSpec{linkedInDetail, glassdoorSpec.detail, indeedSpec.detail, zipSpec.detail}

func listingBoard(rawURL string) *detailSpec {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	h := strings.ToLower(u.Hostname())
	for _, ds := range listingBoards {
		if ds.boardHost(h) {
			return ds
		}
	}
	return nil
}

// ListingDescription is where a job board's posting page at rawURL keeps the
// posting's description, as the board's scan reads it (alternative selectors
// divided by "|"); "" for a page of no job board.
func ListingDescription(rawURL string) string {
	if ds := listingBoard(rawURL); ds != nil {
		return ds.sel.Desc
	}
	return ""
}

// OnListingBoard reports whether rawURL is a page of a job board whose
// postings mirror an employer's.
func OnListingBoard(rawURL string) bool { return listingBoard(rawURL) != nil }

// ApplyTarget is the page an apply link on a job board's posting page names
// off the board: the link itself, or the address the board's redirect link
// carries in its url parameter. A link that stays on the board is the board's
// own apply flow, which is never opened.
func ApplyTarget(listing, href string) (string, bool) {
	ds := listingBoard(listing)
	if ds == nil {
		return "", false
	}
	target := unwrapRedirect(href)
	if !usefulApplyURL(target, ds.boardHost) {
		return "", false
	}
	return target, true
}

// enrichRows reads the detail page of every survivor. Reads run in parallel up
// to the host-aware limit, at most four per company; a row whose read fails
// stays on its list text. A challenge stops the remaining reads. When the
// stage itself fails after some rows were enriched, those are kept.
func (b *bscan) enrichRows(ctx context.Context, ds *detailSpec, rows []Posting) ([]Posting, error) {
	r := b.r
	type result struct {
		state string // enriched, degraded, failed, skipped
		post  Posting
	}
	res := make([]result, len(rows))
	for i := range rows {
		res[i] = result{state: "skipped", post: rows[i]}
	}
	sem := make(chan struct{}, b.detailN)
	var wg sync.WaitGroup
	var gapMu sync.Mutex
	var stage error
	stop := make(chan struct{})
	var stopOnce sync.Once
	halt := func(err error) {
		b.mu.Lock()
		if stage == nil {
			stage = err
		}
		b.mu.Unlock()
		stopOnce.Do(func() { close(stop) })
	}
	for i := range rows {
		key := strings.ToLower(strings.TrimSpace(rows[i].Company))
		b.mu.Lock()
		over := key != "" && b.perCompany[key] >= maxDetailPerCompany
		if !over {
			b.perCompany[key]++
		}
		b.mu.Unlock()
		if over {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
			select {
			case <-stop:
				return
			default:
			}
			if ds.gapHi > 0 {
				gapMu.Lock()
				err := b.env.sleep(ctx, b.env.rand(ds.gapLo, ds.gapHi))
				gapMu.Unlock()
				if err != nil {
					return
				}
			}
			p := rows[i]
			err := b.readDetail(ctx, ds, &p)
			switch {
			case err == nil:
				res[i] = result{"enriched", p}
			case errors.Is(err, errDetailBlocked):
				b.mu.Lock()
				if b.end == nil {
					b.end = &endRun{status: RunBlocked, reason: blockChallenge, err: errors.New(hostOf(p.URL) + " answered a detail read with a challenge; the remaining reads were skipped")}
				}
				b.mu.Unlock()
				halt(err)
			case ctx.Err() != nil:
			case isStageFailure(err):
				res[i] = result{"failed", p}
				halt(err)
			default:
				p.addFlag(FlagDegraded)
				res[i] = result{"degraded", p}
				b.r.noteError("detail %s: %s", hostOf(p.URL), firstLine(err))
			}
		}()
	}
	wg.Wait()
	var out []Posting
	var enriched, degraded, failed, skipped int
	for _, x := range res {
		switch x.state {
		case "enriched":
			enriched++
		case "degraded":
			degraded++
		case "failed":
			failed++
			x.post.addFlag(FlagEnrichFailed)
		default:
			skipped++
			if stage != nil && !errors.Is(stage, errDetailBlocked) {
				failed++
				skipped--
				x.post.addFlag(FlagEnrichFailed)
			}
		}
		out = append(out, x.post)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stage != nil && !errors.Is(stage, errDetailBlocked) && enriched == 0 {
		return nil, fmt.Errorf("reading posting pages failed before any was read: %w", stage)
	}
	r.mu.Lock()
	r.rec.Counts.Enriched += enriched
	r.rec.Counts.EnrichmentDegraded += degraded
	r.rec.Counts.EnrichmentFailed += failed
	r.rec.Counts.EnrichmentSkipped += skipped
	r.mu.Unlock()
	return out, nil
}

// isStageFailure is an error that says the browser, not one page, failed.
func isStageFailure(err error) bool {
	var te *tabOpenError
	return errors.As(err, &te)
}

type tabOpenError struct{ err error }

func (e *tabOpenError) Error() string { return "no tab could be opened: " + e.err.Error() }
func (e *tabOpenError) Unwrap() error { return e.err }

// readDetail opens a tab, reads the posting page and merges it into the row.
// A navigation or script failure is retried once.
func (b *bscan) readDetail(ctx context.Context, ds *detailSpec, p *Posting) error {
	tab, err := b.win.OpenTab(ctx, "detail-"+b.r.req.Source, b.host())
	if err != nil {
		return &tabOpenError{err}
	}
	defer tab.Close()
	if ds.read != nil {
		return ds.read(ctx, b, tab, p)
	}
	var d detailRead
	for attempt := 0; ; attempt++ {
		b.r.addDetail("detail-opens", 1)
		d, err = b.readPosting(ctx, tab, ds, p)
		if err == nil || ctx.Err() != nil || errors.Is(err, errDetailBlocked) || attempt > 0 {
			break
		}
		b.r.addDetail("retries", 1)
	}
	if err != nil {
		return err
	}
	return b.applyDetail(ctx, tab, ds, p, d)
}

func (b *bscan) readPosting(ctx context.Context, tab Tab, ds *detailSpec, p *Posting) (detailRead, error) {
	target := p.fetchURL
	if target == "" {
		target = p.URL
	}
	if ds.pageURL != nil {
		target = ds.pageURL(*p)
	}
	nav, err := tab.Goto(ctx, target)
	if err != nil {
		return detailRead{}, err
	}
	if nav.Failed != "" {
		return detailRead{}, fmt.Errorf("the page did not load: %s", nav.Failed)
	}
	script := detailJS(ds.sel)
	deadline := time.Now().Add(b.env.contentWait())
	var d detailRead
	for {
		if err := tab.Eval(ctx, script, &d); err != nil {
			return d, err
		}
		if len([]rune(d.Desc)) >= 200 || d.Apply != nil || time.Now().After(deadline) {
			break
		}
		if err := b.env.sleep(ctx, b.env.poll()); err != nil {
			return d, err
		}
	}
	if len([]rune(d.Desc)) < 200 {
		d.Desc = ""
		if challengePageRE.MatchString(d.Head) {
			return d, errDetailBlocked
		}
		if d.Apply == nil && !d.Easy {
			return d, errors.New("the posting page showed no description and no apply control")
		}
	}
	return d, nil
}

// applyDetail merges a posting page into the row and finds the employer URL.
func (b *bscan) applyDetail(ctx context.Context, tab Tab, ds *detailSpec, p *Posting, d detailRead) error {
	if d.Desc != "" {
		p.Description = truncateRunes(d.Desc, maxDescription)
	}
	if d.Company != "" && p.Company == "" {
		p.Company = d.Company
	}
	if d.Location != "" && p.Location == "" {
		p.Location = d.Location
	}
	if p.PostedAt.IsZero() && d.DatePosted != "" {
		// A board whose cards show no age may still date the posting page.
		if t := parseTime(d.DatePosted); !t.IsZero() && !t.After(b.r.repo.now().Add(24*time.Hour)) {
			p.PostedAt = t
		}
	}
	if d.Easy {
		p.addFlag(FlagEasyApply)
	}
	if d.Applicants != "" {
		p.addFlag("applicants=" + truncateRunes(d.Applicants, 60))
	}
	own := p.URL
	employer, text := "", ""
	apply, mark := d.Apply, "[data-aj-apply]"
	if i := employerApply(d.Applies, p.Company); i > 0 {
		apply, mark = &d.Applies[i], fmt.Sprintf(`[data-aj-apply="%d"]`, i)
	}
	if apply != nil {
		if href := unwrapRedirect(apply.Href); usefulApplyURL(href, ds.boardHost) && !(ds.otherKey != nil && ds.otherKey(href, own)) {
			employer = href
		} else if apply.Href == "" || apply.Href == "#" || strings.HasPrefix(apply.Href, "javascript") || usefulBoardDest(href, ds) {
			employer, text = b.followApply(ctx, tab, ds, own, p.Title, apply, mark)
			if employer == "" && b.clicksLeft() == 0 {
				p.addFlag(FlagEmployerURLGap)
			}
		}
	}
	if employer != "" {
		b.useEmployer(ctx, p, employer, text)
	}
	return nil
}

// applySiteRE reads the site an "Apply on X" control names, and siteWordsRE
// the words that say it is the company's own job site.
var (
	applySiteRE = lazyregexp.New(`(?i)^apply (?:directly )?(?:on|at) (.+)$`)
	siteWordsRE = lazyregexp.New(`(?i)\b(?:careers?(?: site| page)?|jobs?(?: site| page)?|job board)\s*$`)
)

// employerApply is the index of the apply control that names the posting's
// company ("Apply on Delta Careers" for Delta Systems), or -1. A board lists
// one control per site that carries the posting, and an aggregator is often
// first; the company's own site is the one to follow.
func employerApply(applies []applyControl, company string) int {
	own := identity.CompanyKeys(company)
	if len(own) == 0 {
		return -1
	}
	for i, a := range applies {
		m := applySiteRE.FindStringSubmatch(strings.TrimSpace(a.Text))
		if m == nil {
			continue
		}
		// "Truist Careers - Truist Bank" names the site, then its owner.
		for _, part := range strings.Split(m[1], " - ") {
			site := strings.TrimSpace(siteWordsRE.ReplaceAllString(part, ""))
			for _, k := range identity.CompanyKeys(site) {
				for _, o := range own {
					if leadingWords(k, o) || leadingWords(o, k) {
						return i
					}
				}
			}
		}
	}
	return -1
}

// leadingWords reports that short is long, or long's first words.
func leadingWords(short, long string) bool {
	return short == long || strings.HasPrefix(long, short+" ")
}

// usefulBoardDest is a link that stays on the board (its own redirect), so
// the destination is learned by the bounded click.
func usefulBoardDest(href string, ds *detailSpec) bool {
	u, err := url.Parse(href)
	return err == nil && ds.boardHost != nil && ds.boardHost(strings.ToLower(u.Hostname()))
}

func (b *bscan) clicksLeft() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maxFollowClicks - b.clicks
}

// followApply opens the board redirect an Apply link names, or presses the
// Apply control mark selects once, to learn where it leads:
// at most 40 per run, each read on one throwaway page that is closed after
// its address and text are read. The destination is screened, and is the
// employer's page only when it shows the posting's title.
func (b *bscan) followApply(ctx context.Context, tab Tab, ds *detailSpec, own, title string, apply *applyControl, mark string) (string, string) {
	b.mu.Lock()
	if b.clicks >= maxFollowClicks {
		b.mu.Unlock()
		return "", ""
	}
	b.clicks++
	b.mu.Unlock()
	var dest string
	if href := unwrapRedirect(apply.Href); usefulBoardDest(href, ds) {
		// A link to the board's own redirect (Google's /goto?url=<token>, a
		// target=_blank anchor no popup stub sees) leads where opening it
		// leads, so the throwaway page opens it instead of a click.
		dest = href
	} else {
		before := len(tab.PopupURLs())
		var cr clickResult
		if err := tab.Eval(ctx, clickJS(clickApplyFlow, mark), &cr); err != nil || !cr.Clicked {
			return "", ""
		}
		for i := 0; i < 8 && dest == ""; i++ {
			if urls := tab.PopupURLs(); len(urls) > before {
				dest = urls[len(urls)-1]
				break
			}
			if err := b.env.sleep(ctx, 200*time.Millisecond); err != nil {
				return "", ""
			}
		}
		dest = unwrapRedirect(dest)
		if dest == "" || !usefulApplyURL(dest, ds.boardHost) {
			return "", ""
		}
	}
	b.r.addDetail("external-ats-navigations", 1)
	page, err := b.win.OpenTab(ctx, "employer-"+b.r.req.Source, b.host())
	if err != nil {
		return "", ""
	}
	defer page.Close()
	nav, err := page.Goto(ctx, dest)
	if err != nil || nav.Failed != "" {
		return "", ""
	}
	pt, ok := b.landing(ctx, page, title)
	if !ok {
		b.r.addDetail("apply-landing-other-posting", 1)
		return "", ""
	}
	final := firstNonEmpty(pt.URL, nav.URL, dest)
	if !usefulApplyURL(final, ds.boardHost) || (ds.otherKey != nil && ds.otherKey(final, own)) {
		return "", ""
	}
	return final, pt.Text
}

// landing reads a followed apply page until it shows the posting's title, at
// most the content wait. A click tracker moves on by script seconds after the
// load (appcast through diversityjobs' redirect page to the employer's Oracle
// posting took 8 s), so the page is read where it ended; an ad network can end
// on another posting (two ZipRecruiter rows, DTCC and Tillster, landed on one
// PTC internship), which is no employer page of this one.
func (b *bscan) landing(ctx context.Context, page Tab, title string) (pageText, bool) {
	deadline := time.Now().Add(b.env.contentWait())
	for {
		var pt pageText
		_ = page.Eval(ctx, pageTextJS, &pt)
		if showsTitle(pt, title) {
			return pt, true
		}
		if time.Now().After(deadline) {
			return pt, false
		}
		if err := b.env.sleep(ctx, b.env.poll()); err != nil {
			return pt, false
		}
	}
}

// showsTitle reports whether a page's title or text holds the posting's title
// word for word, with case and punctuation ignored.
func showsTitle(pt pageText, title string) bool {
	key := titleWords(title)
	if key == "" {
		return true
	}
	return strings.Contains(" "+titleWords(pt.Title)+" ", " "+key+" ") || strings.Contains(" "+titleWords(pt.Text)+" ", " "+key+" ")
}

var titleWordRE = lazyregexp.New(`[\p{L}\p{N}]+`)

func titleWords(s string) string {
	return strings.Join(titleWordRE.FindAllString(strings.ToLower(s), -1), " ")
}

// useEmployer makes the employer's URL the row's URL, keeps the board URL as
// an alias and takes the employer page's text when it is longer than the
// board's. text is empty when the page was not read yet.
func (b *bscan) useEmployer(ctx context.Context, p *Posting, employer, text string) {
	p.Aliases = append(p.Aliases, p.URL)
	p.URL = employer
	p.removeFlag(FlagMirror)
	if text == "" && len([]rune(p.Description)) < 200 {
		page, err := b.win.OpenTab(ctx, "employer-"+b.r.req.Source, b.host())
		if err != nil {
			return
		}
		defer page.Close()
		if nav, err := page.Goto(ctx, employer); err != nil || nav.Failed != "" {
			return
		}
		var pt pageText
		_ = page.Eval(ctx, pageTextJS, &pt)
		text = pt.Text
	}
	if len([]rune(text)) > len([]rune(p.Description)) {
		p.Description = truncateRunes(text, maxDescription)
	}
}
