package discovery

import (
	"context"
	"errors"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// A Jobright card holds two links to the same posting (the card body and the
// match-score panel), so the rows are reduced to one per id before they are
// counted against a limit or opened. Signed out, the feed address serves the
// landing page, whose marquee also links to postings; those links are not
// cards, so the page reads as empty and its sign-in words decide.
var jobrightListJS = script("jobright-list", `const as=OUTER([...document.querySelectorAll('a[href^="/jobs/info/"],a[href*="jobright.ai/jobs/info/"]')].filter(a=>!a.closest('[class*="landing-page"],[class*="marquee"]')));
const seen=new Set();
const rows=as.map(a=>{const c=a.closest('[class*="job-card"],[data-testid="job-card"],li,article')||a;const m=(a.getAttribute('href')||'').match(/\/jobs\/info\/([0-9a-zA-Z]+)/);
return {id:m?m[1]:'',title:QT(c,'[class*="job-title"],[data-testid="job-title"],h2,h3')||T(a),company:QT(c,'[class*="company-name"],[data-testid="company-name"]'),location:QT(c,'[class*="primary-location"],[class*="job-location"],[data-testid="job-location"]'),href:ABS(a.getAttribute('href')||''),age:QT(c,'[class*="publish-time"],[data-testid="posted"],time'),salary:QT(c,'[class*="salary"]')}}).filter(r=>r.id&&!seen.has(r.id)&&seen.add(r.id));
return {url:location.href,cards:as.length,rows};`)

// overlayJS reports a blocking dialog and the selector of its own close
// control, if it has one.
var overlayJS = script("overlay", `const d=document.querySelector('[role="dialog"],[aria-modal="true"]');if(!d||!VIS(d))return {open:false};
const c=Q(d,'[aria-label="Close"]|[aria-label="close"]|button[class*="close"]|[data-testid="close"]');if(!c)return {open:true,close:''};
c.setAttribute('data-aj-close','1');return {open:true,close:'[data-aj-close]'};`)

type overlayState struct {
	Open  bool   `json:"open"`
	Close string `json:"close"`
}

var jobrightSignedOut = lazyregexp.New(`(?i)\b(sign up|join now|log in|sign in)\b`)

// readJobrightRecommend reads the signed-in recommendation feed.
func readJobrightRecommend(ctx context.Context, r *run) error {
	b, err := r.newBscan()
	if err != nil {
		return err
	}
	defer b.close()
	base := r.base("jobright", jobrightBase)
	target := strings.TrimRight(base, "/") + "/jobs/recommend"
	r.recordSearch("recommend", target, 0, false)
	r.queryRan()
	ps := pageSpec{url: target, extract: jobrightListJS, signInText: jobrightSignedOut, stayOn: "/jobs/recommend", scrollCards: "a[href*=\"/jobs/info/\"]"}
	lr, err := b.readList(ctx, ps)
	if err != nil {
		return err
	}
	if end := b.pageEnds(lr, target); end != nil {
		return end
	}
	if lr, err = b.clearOverlay(ctx, ps, lr); err != nil {
		return err
	}
	if lr.Cards == 0 {
		r.layoutChange(ReasonNoCards, "recommend: the feed showed no cards")
		return nil
	}
	now := r.repo.now()
	var rows []Posting
	for _, c := range lr.cards() {
		if p, ok := jobrightPosting(base, c, now); ok {
			rows = append(rows, p)
		}
	}
	if len(rows) == 0 {
		r.layoutChange(ReasonEmptyExtract, "recommend")
		return nil
	}
	if limit := r.rowLimit(0); limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	if rows, err = r.listingRejects(ctx, rows, true); err != nil {
		return err
	}
	return b.finishRows(ctx, jobrightDetail, "recommend", rows)
}

func jobrightPosting(base string, c rawCard, now time.Time) (Posting, bool) {
	if c.ID == "" || strings.TrimSpace(c.Title) == "" {
		return Posting{}, false
	}
	p := Posting{Title: c.Title, Company: c.Company, Location: c.Location, JobID: c.ID, Compensation: c.Salary,
		URL: strings.TrimRight(base, "/") + "/jobs/info/" + c.ID, PostedAt: cardTime(c, now)}
	p.addFlag(FlagMirror)
	return p, true
}

// clearOverlay closes an upgrade or promotion dialog through its own close
// control and reads the feed again. It never clicks outside the dialog and
// never presses a key; a dialog with no close control ends the run `blocked`.
func (b *bscan) clearOverlay(ctx context.Context, ps pageSpec, lr listRead) (listRead, error) {
	for i := 0; i < 3; i++ {
		var ov overlayState
		if err := b.tab.Eval(ctx, overlayJS, &ov); err != nil {
			return lr, err
		}
		if !ov.Open {
			return lr, nil
		}
		if ov.Close == "" {
			return lr, &endRun{status: RunBlocked, reason: "overlay", err: errors.New("a dialog covers the feed and has no close control")}
		}
		var cr clickResult
		if err := b.tab.Eval(ctx, clickJS(clickOverlay, ov.Close), &cr); err != nil {
			return lr, err
		}
		if !cr.Clicked {
			return lr, &endRun{status: RunBlocked, reason: "overlay", err: errors.New("the dialog's close control could not be pressed: " + cr.Reason)}
		}
		if err := b.env.sleep(ctx, b.env.poll()); err != nil {
			return lr, err
		}
		var ex extractResult
		if err := b.tab.Eval(ctx, ps.extract, &ex); err != nil {
			return lr, err
		}
		lr.Cards, lr.Rows = ex.Cards, ex.Rows
	}
	return lr, nil
}
