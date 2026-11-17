package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Bounds of the LinkedIn feed scan.
const (
	feedRawPosts      = 100
	feedScrollSteps   = 25
	feedQueries       = 6
	feedThreads       = 8
	feedClassifyMax   = 40
	feedDefaultEval   = 10
	feedPostTextMax   = 3000
	feedNudgeChance   = 18
	feedPauseChance   = 25
	feedFirstViewport = 800
)

// Filter reasons of the feed scan.
const (
	FilterBlockedPoster = "blocked-poster"
	FilterFreeMailOnly  = "free-mail-only"
	FilterNoHiringWord  = "no-hiring-word"
	FilterNotHiring     = "not-hiring"
	FilterParseFailed   = "parse-failed"
	FilterClassifyLimit = "classify-limit"
)

var (
	hiringWordRE  = lazyregexp.New(`(?i)\b(hiring|hire|opening|openings|position|positions|role|roles|apply|join (us|our|the)|recruiting|recruit|looking for|we're growing|vacanc\w*|job|jobs)\b`)
	commentHintRE = lazyregexp.New(`(?i)(link|apply|details|jd|application|info)\s+(is |are |can be found |below )?(in|on|at)\s+(the\s+)?comments?|in the comments|comments? below|\x{1F447}|\x{2B07}`)
	freeMailHosts = []string{"gmail.com", "googlemail.com", "yahoo.com", "outlook.com", "hotmail.com", "live.com", "icloud.com", "me.com", "aol.com", "proton.me", "protonmail.com"}
	feedSkipPath  = lazyregexp.New(`(?i)^/(in|company|school|feed|hashtag|search|pub|showcase)/`)
)

type feedPost struct {
	URN       string `json:"urn"`
	Permalink string `json:"permalink"`
	Author    string `json:"author"`
	// Profile is the author's profile address, without its query, on the
	// server-driven layout.
	Profile  string   `json:"profile"`
	Headline string   `json:"headline"`
	Text     string   `json:"text"`
	Links    []string `json:"links"`
	Mails    []string `json:"mails"`
}

// The server-driven feed renders a post as a componentkey
// "expanded<key>FeedType_..." with hashed classes and no urn or permalink:
// its key stands for the urn, the body is the expandable text box, and the
// author's name, connection degree, headline and age are the paragraphs
// beside the first profile link (the degree and the age are not the
// headline). A card with no body (suggestions) is not a post.
var feedPostsJS = script("linkedin-feed-posts", `const ps=OUTER([...document.querySelectorAll('div.feed-shared-update-v2[data-urn],div[data-urn^="urn:li:activity"],article[data-urn],[componentkey^="expanded"][componentkey*="FeedType_"]')]);
const sdui=p=>{const box=p.querySelector('[data-testid="expandable-text-box"]');if(!box)return null;
const a=[...p.querySelectorAll('a[href*="/in/"],a[href*="/company/"],a[href*="/school/"],a[href*="/showcase/"]')].find(x=>T(x));
let h=a;for(let i=0;h&&i<4;i++){if(h.parentElement&&h.parentElement.querySelectorAll('p').length>=3){h=h.parentElement;break}h=h.parentElement}
const lines=h?[...h.querySelectorAll('p')].map(T).filter(t=>t&&!/^\u2022/.test(t)&&!/^(\d+(s|m|h|d|w|mo|yr)|[A-Z][a-z]{2} \d{1,2}(, \d{4})?|Promoted)$/.test(t)):[];const perm=QA(p,'a[href*="/feed/update/"]','href');
return {urn:(p.getAttribute('componentkey')||'').replace(/^expanded/,'').replace(/FeedType_.*$/,''),permalink:perm?ABS(perm):'',author:lines[0]||(a?T(a):''),profile:a?ABS(a.getAttribute('href')||'').split('?')[0]:'',headline:lines[1]||'',text:T(box),
links:[...box.querySelectorAll('a[href]')].map(x=>ABS(x.getAttribute('href')||'')).filter(h=>/^https?:/.test(h)),mails:[...p.querySelectorAll('a[href^="mailto:"]')].map(x=>(x.getAttribute('href')||'').slice(7).split('?')[0])}};
const rows=ps.map(p=>{if(p.hasAttribute('componentkey'))return sdui(p);const urn=p.getAttribute('data-urn')||'';const perm=QA(p,'a[href*="/feed/update/"]','href')||(urn?'/feed/update/'+urn+'/':'');
return {urn,permalink:ABS(perm),author:QT(p,'.update-components-actor__title|.update-components-actor__name'),headline:QT(p,'.update-components-actor__description'),text:QT(p,'.update-components-text|.feed-shared-text|.break-words'),
links:[...p.querySelectorAll('.update-components-text a[href],.feed-shared-text a[href],.update-components-article a[href],a.app-aware-link[href]')].map(a=>ABS(a.getAttribute('href')||'')).filter(h=>/^https?:/.test(h)),
mails:[...p.querySelectorAll('a[href^="mailto:"]')].map(a=>(a.getAttribute('href')||'').slice(7).split('?')[0])}}).filter(Boolean);
return {url:location.href,cards:ps.length,rows};`)

// feedCommentsJS reads the links in the comments of one post, with who wrote
// each comment. On the server-driven layout the post is the componentkey
// "expanded<urn>..." and a comment is a CommentComponentReference_<urn>: its
// profile link carries the name as the first text of an aria-hidden span
// (the visible one adds badges such as "Premium Profile"), the link is the
// author's profile, and its body is an expandable text box, whose links are
// the comment's own.
func feedCommentsJS(urn string) string {
	return script("linkedin-feed-comments", `const u=`+jsString(urn)+`;const p=document.querySelector('[data-urn='+JSON.stringify(u)+']')||document.querySelector('[componentkey^='+JSON.stringify('expanded'+u)+']');if(!p)return {comments:[]};
const hrefs=n=>n?[...n.querySelectorAll('a[href]')].map(a=>ABS(a.getAttribute('href')||'')).filter(h=>/^https?:/.test(h)):[];
const old=[...p.querySelectorAll('.comments-comment-item,.comments-comment-entity')].map(c=>({author:QT(c,'.comments-post-meta__name-text|.comments-comment-meta__description-title'),links:hrefs(c)}));
const sd=[...p.querySelectorAll('[componentkey^="CommentComponentReference_"]')].map(c=>{const a=[...c.querySelectorAll('a[href*="/in/"],a[href*="/company/"]')].find(x=>T(x));const h=a&&a.querySelector('[aria-hidden="true"]');
return {author:((h&&h.firstChild?h.firstChild.textContent:(a?T(a):''))||'').split('\u2022')[0].trim(),profile:a?ABS(a.getAttribute('href')||'').split('?')[0]:'',links:hrefs(c.querySelector('[data-testid="expandable-text-box"]'))}});
return {comments:old.concat(sd)};`)
}

type feedComments struct {
	Comments []struct {
		Author  string   `json:"author"`
		Profile string   `json:"profile"`
		Links   []string `json:"links"`
	} `json:"comments"`
}

// commentToggle selects the control that opens a post's thread: the comment
// count on the urn layout, the action bar's "Comment" button (its text is
// the count) on the server-driven one. Pressing it opens the thread and an
// empty reply box; nothing is typed or sent.
func commentToggle(urn string) string {
	q, sd := jsString(urn), jsString("expanded"+urn)
	return fmt.Sprintf("[data-urn=%s] button.social-details-social-counts__comments, [data-urn=%s] button[aria-label*=\"comments\" i], [componentkey^=%s] button[aria-label=\"Comment\" i]", q, q, sd)
}

// fraction is a random number in [lo, hi] drawn through the injectable source.
func (e *BrowserEnv) fraction(lo, hi float64) float64 {
	ms := func(f float64) time.Duration { return time.Duration(f*1000) * time.Millisecond }
	return float64(e.rand(ms(lo), ms(hi))/time.Millisecond) / 1000
}

func (e *BrowserEnv) chance(pct int) bool {
	return int(e.rand(0, 99*time.Millisecond)/time.Millisecond) < pct
}

// scrollStep scrolls one human-paced step: 0.6 to 1.05 of the viewport (500
// pixels at least), a dwell of 0.9 to 2.2 seconds, a one in four extra pause
// of 1.6 to 3.3 seconds, and now and then a short nudge back up.
func (b *bscan) scrollStep(ctx context.Context, viewport int) (int, error) {
	dy := max(int(b.env.fraction(0.6, 1.05)*float64(viewport)), 500)
	var pos struct {
		H int `json:"h"`
	}
	if err := b.tab.Eval(ctx, scrollJS(dy), &pos); err != nil {
		return viewport, err
	}
	if pos.H > 0 {
		viewport = pos.H
	}
	dwell := b.env.rand(900*time.Millisecond, 2200*time.Millisecond)
	if b.env.chance(feedPauseChance) {
		dwell += b.env.rand(1600*time.Millisecond, 3300*time.Millisecond)
	}
	if err := b.env.sleep(ctx, dwell); err != nil {
		return viewport, err
	}
	if b.env.chance(feedNudgeChance) {
		up := int(b.env.fraction(0.1, 0.25) * float64(viewport))
		if err := b.tab.Eval(ctx, scrollJS(-up), nil); err != nil {
			return viewport, err
		}
	}
	return viewport, nil
}

func feedLinkKeep(raw string) bool {
	raw = unwrapRedirect(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	if linkedInHost(strings.ToLower(u.Hostname())) {
		return !feedSkipPath.MatchString(u.Path)
	}
	return true
}

// posterPattern is one blocked_posters entry: text a poster's name or
// headline holds, or after "re:" an expression it matches, case ignored.
type posterPattern struct {
	text string
	re   *regexp.Regexp
}

// posterPatterns reads the entries once for a run; an empty entry or an
// expression that does not compile blocks no one.
func posterPatterns(entries []string) []posterPattern {
	var out []posterPattern
	for _, pat := range entries {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if re, ok := strings.CutPrefix(pat, "re:"); ok {
			if rx, err := regexp.Compile("(?i)" + re); err == nil {
				out = append(out, posterPattern{re: rx})
			}
			continue
		}
		out = append(out, posterPattern{text: strings.ToLower(pat)})
	}
	return out
}

func blockedPoster(patterns []posterPattern, fields ...string) bool {
	hay := strings.ToLower(strings.Join(fields, " "))
	for _, pat := range patterns {
		if pat.re != nil && pat.re.MatchString(hay) || pat.re == nil && strings.Contains(hay, pat.text) {
			return true
		}
	}
	return false
}

var httpURLRE = lazyregexp.New(`https?://`)

// freeMailOnly reports a post whose only contact is a personal address and
// that offers no web link.
func freeMailOnly(emails, links []string, text string) bool {
	if len(emails) == 0 {
		return false
	}
	for _, l := range links {
		if strings.HasPrefix(l, "http") {
			return false
		}
	}
	if httpURLRE.MatchString(text) {
		return false
	}
	for _, e := range emails {
		at := strings.LastIndex(e, "@")
		if at < 0 || !hostMatches(e[at+1:], freeMailHosts) {
			return false
		}
	}
	return true
}

const feedSystem = `You classify one LinkedIn post. Answer with a single JSON object and nothing else:
{"is_hiring":bool,"poster_type":"person|company","company":string,"roles":[string],"locations":[string],"work_model":string,"compensation":string,"apply_links":[string],"contact_method":"link|email|dm|comment|tag","sponsorship":"yes|no|unknown","confidence":number between 0 and 1}.
is_hiring is true only when a company offers a role someone can apply to. A job seeker, a recruiter's service pitch, vendor promotion and commentary are not hiring. A recruiter or staffing post that names a concrete role with a way to apply is hiring.`

type feedClassification struct {
	IsHiring      *bool    `json:"is_hiring"`
	PosterType    string   `json:"poster_type"`
	Company       string   `json:"company"`
	Roles         []string `json:"roles"`
	Locations     []string `json:"locations"`
	WorkModel     string   `json:"work_model"`
	Compensation  string   `json:"compensation"`
	ApplyLinks    []string `json:"apply_links"`
	ContactMethod string   `json:"contact_method"`
	Sponsorship   string   `json:"sponsorship"`
	Confidence    float64  `json:"confidence"`
}

// parseClassification reads the model's answer; anything that is not the
// object above is a parse failure.
func parseClassification(out string) (feedClassification, bool) {
	out = strings.TrimSpace(out)
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		out = out[i : j+1]
	}
	var c feedClassification
	if json.Unmarshal([]byte(out), &c) != nil || c.IsHiring == nil || c.Confidence < 0 || c.Confidence > 1 {
		return c, false
	}
	return c, true
}

func feedPrompt(p feedPost) string {
	b, _ := json.Marshal(map[string]any{"author": p.Author, "headline": p.Headline, "text": truncateRunes(p.Text, feedPostTextMax), "links": p.Links})
	return string(b)
}

// feedSources are the pages a run reads: the home feed unless a saved search
// says `home: off`, and up to six keyword searches.
func (r *run) feedSources(base string) ([]string, error) {
	qs, err := r.queries()
	if err != nil {
		return nil, err
	}
	var out []string
	home := true
	var terms []string
	for _, q := range qs {
		if strings.EqualFold(q.Get("home"), "off") {
			home = false
		}
		if q.Search != "" {
			terms = append(terms, q.Search)
		}
	}
	if len(qs) == 0 {
		for _, role := range r.req.Profile.Targets.Roles {
			if t := strings.TrimSpace(role.Title); t != "" {
				terms = append(terms, "hiring "+t)
			}
		}
	}
	if home {
		out = append(out, strings.TrimRight(base, "/")+"/feed/")
	}
	for i, t := range terms {
		if i >= feedQueries {
			break
		}
		v := url.Values{"keywords": {t}, "datePosted": {`"past-24h"`}, "sortBy": {`"date_posted"`}}
		out = append(out, strings.TrimRight(base, "/")+"/search/results/content/?"+v.Encode())
	}
	if len(out) == 0 {
		return nil, skipRun("nothing to read: the home feed is off and there are no keyword searches")
	}
	return out, nil
}

// readLinkedInFeed reads the signed-in feed and the keyword searches, drops
// what needs no model, classifies the rest and files the hiring posts as
// leads. It never likes, comments, messages or applies.
func readLinkedInFeed(ctx context.Context, r *run) error {
	if r.sc.FeedModel == nil {
		return errors.New("no model is configured for the feed-classify task")
	}
	b, err := r.newBscan()
	if err != nil {
		return err
	}
	defer b.close()
	base := r.base("linkedin-feed", linkedInBase)
	pages, err := r.feedSources(base)
	if err != nil {
		return err
	}
	var posts []feedPost
	seen := map[string]bool{}
	for _, page := range pages {
		if len(seen) >= feedRawPosts {
			break
		}
		r.queryRan()
		got, err := b.readFeedPage(ctx, page, feedRawPosts-len(seen), seen)
		if err != nil {
			var end *endRun
			if errors.As(err, &end) || ctx.Err() != nil || errors.Is(err, ErrOffline) {
				return err
			}
			// A scroll that fails part way returns the posts read before it.
			if len(got) == 0 {
				r.queryFailed(hostOf(page), err)
				continue
			}
			r.partialRead(hostOf(page), err)
		}
		r.addDetail("posts-raw", len(got))
		got = r.feedPrefilter(got)
		b.expandThreads(ctx, got)
		posts = append(posts, got...)
	}
	return r.classifyAndFile(ctx, posts)
}

// readFeedPage scrolls one page and collects its posts, up to room of them.
func (b *bscan) readFeedPage(ctx context.Context, page string, room int, seen map[string]bool) ([]feedPost, error) {
	pause := b.pauseBetween
	if err := pause(ctx); err != nil {
		return nil, err
	}
	ps := pageSpec{url: page, extract: feedPostsJS, stayOn: ""}
	lr, err := b.readList(ctx, ps)
	if err != nil {
		return nil, err
	}
	if end := b.pageEnds(lr, page); end != nil {
		return nil, end
	}
	var out []feedPost
	viewport, idle := feedFirstViewport, 0
	for step := 0; ; step++ {
		var ex extractResult
		if err := b.tab.Eval(ctx, feedPostsJS, &ex); err != nil {
			return out, err
		}
		fresh := 0
		for _, raw := range ex.Rows {
			var p feedPost
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			key := p.URN
			if key == "" {
				key = p.Permalink
			}
			if key == "" || seen[key] || len(out) >= room {
				continue
			}
			seen[key] = true
			out = append(out, p)
			fresh++
		}
		if fresh == 0 {
			idle++
		} else {
			idle = 0
		}
		if len(out) >= room || step+1 >= feedScrollSteps || idle >= 4 {
			return out, nil
		}
		if viewport, err = b.scrollStep(ctx, viewport); err != nil {
			return out, err
		}
	}
}

// feedPrefilter drops, before any model call, the posts of blocked posters,
// the posts whose only contact is a personal address, and the posts with no
// hiring word at all. Each is counted.
func (r *run) feedPrefilter(posts []feedPost) []feedPost {
	patterns := posterPatterns(r.req.Profile.Scan.Defaults["linkedin-feed"].BlockedPosters)
	var keep []feedPost
	blocked, free, noWord := 0, 0, 0
	for _, p := range posts {
		emails := append(ExtractEmails(p.Text), p.Mails...)
		switch {
		case blockedPoster(patterns, p.Author, p.Headline):
			blocked++
		case freeMailOnly(emails, p.Links, p.Text):
			free++
		case !hiringWordRE.MatchString(p.Text):
			noWord++
		default:
			keep = append(keep, p)
		}
	}
	r.dropRows(FilterBlockedPoster, blocked)
	r.dropRows(FilterFreeMailOnly, free)
	r.dropRows(FilterNoHiringWord, noWord)
	return keep
}

// expandThreads opens the comment thread of up to eight posts whose text
// says the link is in the comments, and adds the links the author left there.
func (b *bscan) expandThreads(ctx context.Context, posts []feedPost) {
	for i := range posts {
		if b.threads >= feedThreads || ctx.Err() != nil {
			return
		}
		p := &posts[i]
		if p.URN == "" || !commentHintRE.MatchString(p.Text) {
			continue
		}
		var cr clickResult
		if err := b.tab.Eval(ctx, clickJS(clickComments, commentToggle(p.URN)), &cr); err != nil || !cr.Clicked {
			continue
		}
		b.threads++
		b.r.addDetail("threads-opened", 1)
		// The server-driven thread loads after the press; a few polls wait
		// for its first comment.
		var fc feedComments
		for try := 0; try < 6 && len(fc.Comments) == 0; try++ {
			if err := b.env.sleep(ctx, b.env.poll()); err != nil {
				return
			}
			if err := b.tab.Eval(ctx, feedCommentsJS(p.URN), &fc); err != nil {
				break
			}
		}
		for _, c := range fc.Comments {
			// The author's own comment: the same profile where both name one
			// (a badge such as "Author" can sit in the name), else the name.
			if sameProfile(c.Profile, p.Profile) || strings.EqualFold(strings.TrimSpace(c.Author), strings.TrimSpace(p.Author)) {
				p.Links = append(p.Links, c.Links...)
			}
		}
	}
}

// sameProfile reports two profile addresses naming the same member or page.
func sameProfile(a, b string) bool {
	norm := func(v string) string {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Path == "" {
			return ""
		}
		return strings.ToLower(strings.TrimRight(u.Path, "/"))
	}
	na := norm(a)
	return na != "" && na == norm(b)
}

// classifyAndFile classifies at most 40 posts and files the hiring ones.
func (r *run) classifyAndFile(ctx context.Context, posts []feedPost) error {
	if r.req.Limit > 0 && len(posts) > r.req.Limit {
		r.dropRows(FilterOverLimit, len(posts)-r.req.Limit)
		posts = posts[:r.req.Limit]
	}
	if len(posts) > feedClassifyMax {
		r.dropRows(FilterClassifyLimit, len(posts)-feedClassifyMax)
		posts = posts[:feedClassifyMax]
	}
	for _, p := range posts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		exp := &Expander{HTTP: r.client.HTTP}
		out, err := r.sc.FeedModel(ctx, feedSystem, feedPrompt(p))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.dropRows(FilterParseFailed, 1)
			r.noteError("classifier: %v", err)
			continue
		}
		c, ok := parseClassification(out)
		if !ok {
			r.dropRows(FilterParseFailed, 1)
			continue
		}
		if !*c.IsHiring {
			r.dropRows(FilterNotHiring, 1)
			continue
		}
		var links []string
		for _, l := range append(append([]string(nil), c.ApplyLinks...), p.Links...) {
			l = unwrapRedirect(l)
			if !feedLinkKeep(l) {
				continue
			}
			if final, ok := exp.Expand(ctx, l); ok {
				l = final
			}
			links = append(links, l)
		}
		emails := append(ExtractEmails(p.Text), p.Mails...)
		in := FeedLeadInput{Permalink: p.Permalink, Author: p.Author, Body: p.Text, PosterType: c.PosterType, Company: c.Company, Roles: c.Roles,
			Locations: c.Locations, WorkModel: c.WorkModel, Compensation: c.Compensation, ApplyLinks: links, ContactEmails: emails,
			ContactMethod: c.ContactMethod, Sponsorship: c.Sponsorship, Confidence: c.Confidence}
		if _, _, err := r.addFeedLead(ctx, in); err != nil {
			return err
		}
	}
	return nil
}
