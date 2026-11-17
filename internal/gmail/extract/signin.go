package extract

import (
	"net/url"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// SigninMatch is a sign-in link offered for display. The product never fetches
// or navigates to it; the user opens it on their own click in a new tab.
type SigninMatch struct {
	URL     string
	Host    string
	Score   int
	Reasons []string
}

// SigninInput is a message as the sign-in extractor reads it: raw parts with
// markup intact, because an href does not survive tag stripping.
type SigninInput struct {
	HTML, Text, Subject string
}

// Scoring thresholds. A link needs two independent signals, because a call to
// action alone accepted account-notification mail and a URL shape alone
// accepted a plain login page.
const (
	signinMinScore    = 4
	signinMinMargin   = 2
	signinPointIntent = 2
	signinPenaltyESP  = 3
	contextChars      = 240
	// The rejection window looks before the link only: a mail introduces a
	// link before it prints it, so the text after a link belongs to the next one.
	rejectContextChars = 160
)

var (
	anchorRE  = lazyregexp.New(`(?is)<a\b[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	signinURL = lazyregexp.New(`(?i)https?://[^\s"'<>)\]]+`)
	urlHintRE = lazyregexp.New(`(?i)magic[-_]?link|authenticate|passwordless|one[-_]?time|/auth\b|\bauth/|sign[-_]?in|log[-_]?in|\blogin\b|verify|confirm|token=|\botl\b|\bsso\b`)
	ctaRE     = lazyregexp.New(`(?i)(?:sign|log)\s?-?\s?in\b|signin|access your account|secure link|(?:continue|return to|resume|view/edit|view or edit)\s+(?:your\s+)?application|complete your application|verify your email|confirm your email`)
	rejectRE  = lazyregexp.New(`(?i)unsubscribe|opt[-\s]?out|privacy|terms of|manage (?:your )?(?:email )?preferences|notification settings|forgot (?:your )?(?:password|user)|reset (?:your )?password|set (?:an?|your) (?:optional )?password|password as well|contact (?:us|support)|help cent|view online|view in browser|view this email|statement|invoice|receipt|mobile app|download the app`)
	espHostRE = lazyregexp.New(`(?i)(^|\.)awstrack\.me$|(^|\.)list-manage\.com$|(^|\.)sendgrid\.net$|(^|\.)mandrillapp\.com$|(^|\.)sparkpostmail\.com$|(^|\.)pstmrk\.it$|(^|\.)mailgun\.org$`)
	espPathRE = lazyregexp.New(`(?i)/ls/click|/wf/open|/L0/|/ss/c/`)
	trimURLRE = lazyregexp.New(`[.,;:!?'"]+$`)
)

type linkCandidate struct {
	url, host     string
	contexts      []string
	rejectContext []string
	inBodyText    bool
	fromAnchor    bool
}

func decodeOnce(u string) string {
	if d, err := url.QueryUnescape(strings.ReplaceAll(u, "+", "%2B")); err == nil {
		return d
	}
	return u
}

func slice(s string, from, to int) string {
	from, to = max(from, 0), min(to, len(s))
	if from > to {
		return ""
	}
	return s[from:to]
}

// isOpaque: letters and digits mixed, or long enough that it cannot be a word.
func isOpaque(v string) bool {
	var compact strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			compact.WriteRune(r)
		}
	}
	c := compact.String()
	if len(c) < 8 {
		return false
	}
	return hasLetterAndDigit(c) || len(c) >= 24
}

// carriesCredential is the first gate: the URL must hold something opaque, a
// query value, a fragment or a path segment.
func carriesCredential(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if len(u.Fragment) >= 11 {
		return true
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			if len(v) >= 10 && isOpaque(v) {
				return true
			}
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) >= 8 && isOpaque(seg) {
			return true
		}
	}
	return false
}

// SigninLink returns the one link that signs the user in, or nil. A link must
// clear three gates in order (an opaque credential; no password-reset,
// unsubscribe, preferences, support, view-online, statement, receipt, optional
// password or app-download reading in its URL, anchor text or the 160
// characters before it; at least 4 points) and the winner must lead the
// runner-up by 2 points. Returning the wrong link would open the wrong page in
// the user's signed-in browser, so a tie returns nothing.
func SigninLink(in SigninInput) *SigninMatch {
	cands := map[string]*linkCandidate{}
	var order []string
	add := func(raw, ctx, rejectCtx string, anchor, body bool) {
		u := trimURLRE.ReplaceAllString(strings.TrimSpace(DecodeEntities(raw)), "")
		if !strings.HasPrefix(strings.ToLower(u), "http://") && !strings.HasPrefix(strings.ToLower(u), "https://") {
			return
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Hostname() == "" {
			return
		}
		c := cands[u]
		if c == nil {
			c = &linkCandidate{url: u, host: strings.ToLower(parsed.Hostname())}
			cands[u] = c
			order = append(order, u)
		}
		c.contexts = append(c.contexts, ctx)
		c.rejectContext = append(c.rejectContext, rejectCtx)
		c.inBodyText = c.inBodyText || body
		c.fromAnchor = c.fromAnchor || anchor
	}
	html := in.HTML
	for _, m := range anchorRE.FindAllStringSubmatchIndex(html, -1) {
		href := html[m[2]:m[3]]
		anchorText := compact(StripHTML(html[m[4]:m[5]]))
		window := compact(StripHTML(slice(html, m[0]-contextChars, m[1]+contextChars)))
		before := compact(StripHTML(slice(html, m[0]-rejectContextChars, m[0])))
		add(href, anchorText+" "+window, anchorText+" "+before, true, false)
	}
	prose := StripHTML(html) + "\n" + in.Text + "\n" + in.Subject
	for _, loc := range signinURL.FindAllStringIndex(prose, -1) {
		window := compact(slice(prose, loc[0]-contextChars, loc[1]+contextChars))
		before := compact(slice(prose, loc[0]-rejectContextChars, loc[0]))
		add(prose[loc[0]:loc[1]], window, before, false, true)
	}

	type scored struct {
		c       *linkCandidate
		score   int
		reasons []string
	}
	var all []scored
	for _, key := range order {
		c := cands[key]
		if !carriesCredential(c.url) {
			continue
		}
		decoded := decodeOnce(c.url)
		rejected := rejectRE.MatchString(decoded)
		for _, r := range c.rejectContext {
			rejected = rejected || rejectRE.MatchString(r)
		}
		if rejected {
			continue
		}
		urlHint := urlHintRE.MatchString(decoded)
		cta := ctaRE.FindString(strings.Join(c.contexts, " "))
		if !urlHint && cta == "" {
			continue
		}
		s := scored{c: c}
		if urlHint {
			s.score += signinPointIntent
			s.reasons = append(s.reasons, "url-shape")
		}
		if cta != "" {
			s.score += signinPointIntent
			s.reasons = append(s.reasons, "cta:"+compact(cta))
		}
		if c.fromAnchor && c.inBodyText {
			s.score += signinPointIntent
			s.reasons = append(s.reasons, "copy-paste-confirmed")
		}
		if espHostRE.MatchString(c.host) || espPathRE.MatchString(c.url) {
			s.score -= signinPenaltyESP
			s.reasons = append(s.reasons, "esp-wrapper")
		}
		all = append(all, s)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) == 0 || all[0].score < signinMinScore {
		return nil
	}
	if len(all) > 1 && all[0].score-all[1].score < signinMinMargin {
		return nil
	}
	b := all[0]
	return &SigninMatch{URL: b.c.url, Host: b.c.host, Score: b.score, Reasons: b.reasons}
}
