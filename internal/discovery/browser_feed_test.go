package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/workspace"
)

func post(n int, text string) feedPost {
	return feedPost{URN: fmt.Sprintf("urn:li:activity:%d", n), Permalink: fmt.Sprintf("https://www.linkedin.com/feed/update/urn:li:activity:%d/", n),
		Author: fmt.Sprintf("Author %d", n), Headline: "Engineering lead", Text: text}
}

func feedRows(posts ...feedPost) extractResult {
	rows := make([]json.RawMessage, len(posts))
	for i, p := range posts {
		rows[i], _ = json.Marshal(p)
	}
	return extractResult{Cards: len(posts), Rows: rows}
}

// feedModel answers the classifier and counts its calls.
type feedModel struct {
	mu     sync.Mutex
	calls  int
	answer func(prompt string) string
}

func (m *feedModel) ask(_ context.Context, _, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.answer(prompt), nil
}

const notHiring = `{"is_hiring":false,"poster_type":"person","confidence":0.9}`

func feedEnv(t *testing.T, route func(string) fakePage, answer func(string) string) (*env, *fakeBrowser, *feedModel) {
	fb := newFakeBrowser(t, route)
	e := browserEnv(t, fb)
	m := &feedModel{answer: answer}
	e.sc.FeedModel = m.ask
	return e, fb, m
}

func feedPortals(t *testing.T, qs ...any) workspace.Portals {
	return portalsWith(t, eng(), "linkedin-feed", qs...)
}

// growing serves a feed that shows three new posts at every look.
func growing() func() any {
	n := 0
	return func() any {
		var ps []feedPost
		for i := 0; i < 3; i++ {
			n++
			ps = append(ps, post(n, fmt.Sprintf("We are hiring engineer number %d, apply today", n)))
		}
		all := feedRows(ps...)
		return all
	}
}

func TestLinkedInFeed_ReadsFeedAndQueriesWithinBounds(t *testing.T) {
	var qs []any
	for i := 0; i < 8; i++ {
		qs = append(qs, fmt.Sprintf("hiring engineer %d", i))
	}
	e, fb, m := feedEnv(t, func(raw string) fakePage {
		var ps []feedPost
		for i := 0; i < 5; i++ {
			ps = append(ps, post(len(raw)*1000+i, "We are hiring an engineer, apply now"))
		}
		return fakePage{extract: feedRows(ps...)}
	}, func(string) string { return notHiring })
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, qs...)})
	if rec.Status != RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Error)
	}
	pages := 0
	for _, u := range fb.visited() {
		if strings.Contains(u, "/feed/") || strings.Contains(u, "/search/results/content") {
			pages++
		}
	}
	if pages != 1+feedQueries {
		t.Fatalf("pages read = %d, want the home feed and %d searches", pages, feedQueries)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
	_ = m
}

func TestLinkedInFeed_RawPostCapAndClassifyCap(t *testing.T) {
	n := 0
	e, _, m := feedEnv(t, func(string) fakePage {
		return fakePage{extract: feedRows(), eval: map[string]any{"linkedin-feed-posts": func() any {
			var ps []feedPost
			for i := 0; i < 8; i++ {
				n++
				ps = append(ps, post(n, "We are hiring an engineer, apply now"))
			}
			return feedRows(ps...)
		}}}
	}, func(string) string { return notHiring })
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"})})
	if rec.Detail["posts-raw"] != feedRawPosts || m.calls != feedClassifyMax {
		t.Fatalf("raw posts %d (cap %d), classified %d (cap %d)", rec.Detail["posts-raw"], feedRawPosts, m.calls, feedClassifyMax)
	}
	if rec.Counts.Filtered[FilterClassifyLimit] != feedRawPosts-feedClassifyMax {
		t.Fatalf("filtered = %v", rec.Counts.Filtered)
	}
}

// A feed page whose read fails part way down keeps the posts read above the
// failure; the record carries the failure.
func TestLinkedInFeed_AReadThatFailsPartWayKeepsThePostsAlreadyRead(t *testing.T) {
	looks := 0
	e, _, m := feedEnv(t, func(string) fakePage {
		return fakePage{extract: feedRows(), eval: map[string]any{"linkedin-feed-posts": func() any {
			if looks++; looks > 2 {
				return make(chan int) // the script's answer cannot be read
			}
			return feedRows(post(1, "We are hiring an engineer, apply now"), post(2, "We are hiring a designer, apply now"))
		}}}
	}, func(string) string { return notHiring })
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"})})
	if rec.Status != RunCompleted || rec.Detail["posts-raw"] != 2 || m.calls != 2 || rec.Counts.BoardErrors != 1 || len(rec.Errors) != 1 {
		t.Fatalf("record = %+v, classified %d", rec, m.calls)
	}
}

func TestLinkedInFeed_PrefilterBeforeModelCall(t *testing.T) {
	blocked := post(1, "We are hiring a backend engineer. Apply: https://careers.example.com/jobs/1")
	blocked.Author = "Spammy Talent Agency"
	free := post(2, "We are hiring an engineer, email me at someone@"+"gmail.com")
	noWord := post(3, "Great weekend at the lake with the family")
	good := post(4, "We are hiring a backend engineer. Apply: https://careers.example.com/jobs/4")
	e, _, m := feedEnv(t, func(string) fakePage {
		return fakePage{extract: feedRows(blocked, free, noWord, good)}
	}, func(string) string { return notHiring })
	req := ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"})}
	req.Profile.Scan.Defaults = map[string]workspace.ScanDefaults{"linkedin-feed": {BlockedPosters: []string{"spammy talent", "re:^zzz"}}}
	rec := e.run(req)
	if m.calls != 1 {
		t.Fatalf("model calls = %d, want 1", m.calls)
	}
	c := rec.Counts.Filtered
	if c[FilterBlockedPoster] != 1 || c[FilterFreeMailOnly] != 1 || c[FilterNoHiringWord] != 1 || c[FilterNotHiring] != 1 {
		t.Fatalf("filtered = %v", c)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedInFeed_Classifier_TaxonomyAndParseFailure(t *testing.T) {
	var posts []feedPost
	for i := 1; i <= 45; i++ {
		posts = append(posts, post(i, fmt.Sprintf("We are hiring engineer %d, apply", i)))
	}
	e, _, m := feedEnv(t, func(string) fakePage { return fakePage{extract: feedRows(posts...)} }, func(prompt string) string {
		switch {
		case strings.Contains(prompt, "engineer 1,"):
			return "I think this is a job post."
		case strings.Contains(prompt, "engineer 2,"):
			return `{"is_hiring":"maybe"}`
		}
		return notHiring
	})
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"})})
	if rec.Status != RunCompleted || m.calls != feedClassifyMax {
		t.Fatalf("status %s model calls %d", rec.Status, m.calls)
	}
	c := rec.Counts.Filtered
	if c[FilterParseFailed] != 2 || c[FilterClassifyLimit] != 5 || c[FilterNotHiring] != 38 {
		t.Fatalf("filtered = %v", c)
	}
	if _, ok := parseClassification(`{"is_hiring":true,"confidence":1.5}`); ok {
		t.Fatal("a confidence over 1 parses")
	}
	if c, ok := parseClassification("```json\n{\"is_hiring\":true,\"confidence\":0.7}\n```"); !ok || c.Confidence != 0.7 {
		t.Fatal("a fenced answer does not parse")
	}
}

func TestLinkedInFeed_CommentHintGateSelectsThreads(t *testing.T) {
	var posts []feedPost
	posts = append(posts, post(1, "We are hiring engineers. Link in the comments!"), post(2, "We are hiring engineers. Apply at https://careers.example.com/jobs/2"))
	for i := 3; i <= 12; i++ {
		posts = append(posts, post(i, fmt.Sprintf("We are hiring engineer %d, details in the comments", i)))
	}
	e, fb, _ := feedEnv(t, func(string) fakePage {
		p := fakePage{extract: feedRows(posts...), click: clickResult{Clicked: true}}
		return p
	}, func(string) string { return notHiring })
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"})})
	clicked := map[string]bool{}
	for _, s := range fb.scripts {
		if strings.Contains(s, "aj:click") {
			for i := 1; i <= 12; i++ {
				if strings.Contains(s, fmt.Sprintf("urn:li:activity:%d\\\"", i)) || strings.Contains(s, fmt.Sprintf("urn:li:activity:%d\"", i)) {
					clicked[fmt.Sprint(i)] = true
				}
			}
		}
	}
	if fb.count("click") != feedThreads || clicked["2"] || !clicked["1"] || rec.Detail["threads-opened"] != feedThreads {
		t.Fatalf("clicks %d clicked %v detail %v", fb.count("click"), clicked, rec.Detail)
	}
	if !commentHintRE.MatchString("see the \U0001F447 below") || commentHintRE.MatchString("we are hiring") {
		t.Fatal("the comment-hint list")
	}
}

func TestLinkedInFeed_AuthWallStopsTheRunWithNoPartialLeads(t *testing.T) {
	e, _, m := feedEnv(t, func(string) fakePage {
		return fakePage{nav: Nav{URL: "https://www.linkedin.com/authwall?trk=x"}}
	}, func(string) string { return notHiring })
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t)})
	if rec.Status != RunNeedsSignIn || m.calls != 0 || rec.Counts.Discovered != 0 {
		t.Fatalf("status %s calls %d", rec.Status, m.calls)
	}
}

func TestLinkedInFeed_NeverLikesConnectsOrMessages(t *testing.T) {
	// The only click a feed scan makes is the comment-thread toggle, and the
	// script refuses a control that names a write action.
	js := clickJS(clickComments, "button")
	for _, w := range []string{"like", "connect", "message", "follow", "send", "submit"} {
		if !strings.Contains(js, w) {
			t.Errorf("the click guard does not name %q", w)
		}
	}
}

func TestLinkedInFeed_HiringPostWithPostingLinkBecomesLead(t *testing.T) {
	p := post(1, "We are hiring a backend engineer! Apply: https://jobs.lever.co/example-co/1234abcd-0000-0000-0000-000000000000")
	e, _, _ := feedEnv(t, func(string) fakePage { return fakePage{extract: feedRows(p)} }, func(string) string {
		return `{"is_hiring":true,"poster_type":"company","company":"Example Co","roles":["Backend Engineer"],"locations":["Austin, TX"],"contact_method":"link","sponsorship":"unknown","confidence":0.8,"apply_links":[]}`
	})
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "x"}), TopN: new(int)})
	if rec.Status != RunCompleted || rec.Counts.Discovered != 1 {
		t.Fatalf("status %s %s counts %+v", rec.Status, rec.Error, rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedInFeed_ShortenerAllowanceIsPerPost(t *testing.T) {
	var posts []feedPost
	for n := 1; n <= 7; n++ {
		p := post(n, fmt.Sprintf("We are hiring backend engineer number %d, apply today", n))
		p.Links = []string{fmt.Sprintf("https://bit.ly/job%d", n)}
		posts = append(posts, p)
	}
	e, _, _ := feedEnv(t, func(string) fakePage { return fakePage{extract: feedRows(posts...)} }, func(prompt string) string {
		var in struct {
			Author string `json:"author"`
		}
		if err := json.Unmarshal([]byte(prompt), &in); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(map[string]any{"is_hiring": true, "poster_type": "company", "company": in.Author, "roles": []string{"Backend Engineer"}, "locations": []string{"Remote - United States"}, "contact_method": "link", "sponsorship": "unknown", "confidence": 0.9})
		return string(out)
	})
	fs := newFeedServer(t)
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Header.Get("X-Original-Host") == "bit.ly" {
			http.Redirect(w, r, "https://www.example.org/careers"+r.URL.Path, http.StatusFound)
		}
	}
	target, _ := url.Parse(fs.URL)
	e.sc.Client.HTTP = &http.Client{Transport: &rewriteTransport{target: target}}
	rec := e.run(ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "engineer"}), TopN: new(int)})
	if rec.Status != RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Error)
	}
	asked := map[string]bool{}
	for _, r := range fs.requests() {
		if r.Host == "bit.ly" {
			asked[r.Path] = true
		}
	}
	if len(asked) != len(posts) {
		t.Fatalf("%d of %d posts had their shortener expanded: %v", len(asked), len(posts), asked)
	}
}

func TestLinkedInFeed_EffectiveEvaluationLimitHonorsTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		requested, common, specific *int
		want                        int
	}{
		{name: "feed default", want: 10},
		{name: "feed setting zero", specific: new(0), want: 0},
		{name: "common setting", common: new(3), want: 3},
		{name: "specific setting wins", common: new(3), specific: new(2), want: 2},
		{name: "explicit scan only", requested: new(0), common: new(3), specific: new(2), want: 0},
		{name: "explicit larger limit", requested: new(12), common: new(3), specific: new(2), want: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts []feedPost
			for n := 1; n <= 12; n++ {
				p := post(n, "We are hiring a backend engineer, apply today")
				p.Links = []string{fmt.Sprintf("https://www.linkedin.com/jobs/view/%d", 900000000+n)}
				posts = append(posts, p)
			}
			e, _, _ := feedEnv(t, func(string) fakePage { return fakePage{extract: feedRows(posts...)} }, func(prompt string) string {
				var in struct {
					Author string `json:"author"`
				}
				if err := json.Unmarshal([]byte(prompt), &in); err != nil {
					t.Fatal(err)
				}
				out, _ := json.Marshal(map[string]any{"is_hiring": true, "poster_type": "company", "company": in.Author, "roles": []string{"Backend Engineer"}, "locations": []string{"Remote - United States"}, "contact_method": "link", "sponsorship": "unknown", "confidence": 0.9})
				return string(out)
			})
			req := ScanRequest{Source: "linkedin-feed", Portals: feedPortals(t, map[string]any{"home": "off", "search": "engineer"}), TopN: tc.requested}
			req.Profile.Scan.Defaults = map[string]workspace.ScanDefaults{"linkedin-feed": {EvaluateTopN: tc.common, EvaluateLimit: tc.specific}}
			rec := e.run(req)
			e.enq.mu.Lock()
			queued := len(e.enq.reqs)
			e.enq.mu.Unlock()
			if rec.Status != RunCompleted || rec.Counts.Inserted != 12 || rec.Counts.Queued != tc.want || queued != tc.want {
				t.Fatalf("status=%s counts=%+v queued=%d want=%d error=%s", rec.Status, rec.Counts, queued, tc.want, rec.Error)
			}
			stored, err := e.rp.ScanGet(e.ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range []Record{rec, stored} {
				if fmt.Sprint(got.Params["evaluate_top_n"]) != fmt.Sprint(tc.want) {
					t.Fatalf("effective limit=%v want=%d", got.Params["evaluate_top_n"], tc.want)
				}
			}
		})
	}
}
