package apply

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/discovery"
	"applysmith/internal/gates"
)

func TestApply_Resolve_AggregatorHostListAndEduShape(t *testing.T) {
	for _, u := range []string{
		"https://www.ziprecruiter.com/jobs/example-corp-1a2b", "https://www.indeed.com/viewjob?jk=abc",
		"https://careers.sdstate.edu/jobs/meadow-ai-backend-engineer", "https://capd.mit.edu/jobs/example-corp-platform-engineer/",
	} {
		if !IsAggregatorURL(u) {
			t.Errorf("%s is a mirror", u)
		}
	}
	for _, u := range []string{
		"https://smartapply.indeed.com/beta/indeedapply/form/x", "https://apply.indeed.com/x", "https://www.indeed.com/jobs?q=go",
		"https://careers.sdstate.edu/about", "https://careers.sdstate.edu/jobs/search", "https://example.com/jobs/platform-engineer",
		"https://boards.greenhouse.io/example-corp/jobs/1", "https://notziprecruiter.com/jobs/1",
	} {
		if IsAggregatorURL(u) {
			t.Errorf("%s is not a mirror", u)
		}
	}
}

func TestApply_Resolve_ScriptRedirectBoardIsNotMirror(t *testing.T) {
	// A board whose Apply is a script redirect plain HTTP cannot read is walked,
	// not resolved: the walk through it works.
	u := "https://builtin.com/job/platform-engineer/7"
	if IsAggregatorURL(u) {
		t.Fatalf("%s is walked, not resolved", u)
	}
	g := newRig(t, nil)
	var opened string
	g.r.d.Drive = func(_ context.Context, e *Exec) *stop { opened = e.Cls.ApplyURL; return nil }
	run := g.r.RunOne(context.Background(), Input{URL: u, Caller: gates.CallerCLI})
	if run.Outcome.Kind != OutcomeFilled || opened != u || run.ATS != ATSGeneric {
		t.Fatalf("outcome %+v opened %q ats %s", run.Outcome, opened, run.ATS)
	}
}

const (
	zipURL    = "https://www.ziprecruiter.com/c/Example-Corp/Job/Platform-Engineer/-in-San-Francisco,CA?jid=abc"
	mirrorHTM = `<html><body><a href="/about">About</a><a href="https://jobs.lever.co/example-corp/00000000-0000-0000-0000-000000000001">Apply on company site</a></body></html>`
)

// mirrorRig counts every source call, in the order they were made.
type mirrorRig struct {
	calls  []string
	html   string
	status int
	boards map[string][]discovery.Posting // by board key
	reg    []discovery.Board
	names  map[string]string
	web    []WebHit
	atsWeb []WebHit
	// wdSites are the sites the web search handed the Workday search;
	// wdBySite is what that search finds.
	wdSites  []string
	wdBySite []BoardMatch
	wd       []BoardMatch
	m        *MirrorSources
}

func newMirrorRig() *mirrorRig {
	r := &mirrorRig{status: 200, boards: map[string][]discovery.Posting{}, names: map[string]string{}}
	r.m = &MirrorSources{
		Page: func(context.Context, string) (string, int) {
			r.calls = append(r.calls, "page")
			return r.html, r.status
		},
		Registry: func(_ context.Context, c string) []discovery.Board {
			r.calls = append(r.calls, "registry")
			return r.reg
		},
		Board: func(_ context.Context, b discovery.Board) ([]discovery.Posting, error) {
			r.calls = append(r.calls, "board:"+b.Key())
			if p, ok := r.boards[b.Key()]; ok {
				return p, nil
			}
			return nil, fmt.Errorf("404")
		},
		BoardName: func(_ context.Context, slug string) string { return r.names[slug] },
		Workday: func(_ context.Context, _, _ string, _, sites []string) []BoardMatch {
			r.calls = append(r.calls, "workday")
			if len(sites) > 0 {
				r.wdSites = append(r.wdSites, sites...)
				return r.wdBySite
			}
			return r.wd
		},
		Search: func(_ context.Context, q string, include, exclude []string) ([]WebHit, error) {
			r.calls = append(r.calls, "search")
			if len(include) == 0 && len(exclude) == 0 {
				return nil, fmt.Errorf("mirrors not excluded")
			}
			if len(include) > 0 {
				return r.atsWeb, nil
			}
			return r.web, nil
		},
	}
	return r
}

func (r *mirrorRig) resolve(company, role string) MirrorResolution {
	return r.m.Resolve(context.Background(), zipURL, MirrorHint{Company: company, Role: role})
}

func boardPost(ats, slug, company, title, url, loc, desc string) discovery.Posting {
	return discovery.Posting{ATS: ats, Board: slug, Company: company, Title: title, URL: url, Location: loc, Description: desc}
}

func TestApply_Resolve_MirrorOrderAndAcceptanceRules(t *testing.T) {
	const emp = "https://jobs.example.test/p/1"
	t.Run("the page's own outbound link to a recognized system comes first", func(t *testing.T) {
		r := newMirrorRig()
		r.html = mirrorHTM
		got := r.resolve("Example Corp", "Platform Engineer")
		if got.Via != "outbound-link" || !strings.HasPrefix(got.URL, "https://jobs.lever.co/example-corp/") || strings.Join(r.calls, ",") != "page" {
			t.Fatalf("%+v calls %v", got, r.calls)
		}
	})
	t.Run("a link to another mirror or a marketing page is no resolution", func(t *testing.T) {
		r := newMirrorRig()
		r.html = `<a href="https://www.glassdoor.com/job/1">x</a><a href="https://example.test/careers">y</a><a href="mailto:a@example.test">z</a>`
		if got := r.resolve("", ""); got.Via != "" || got.Reason == "" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the registry then the Workday sites then guessed boards, then the web", func(t *testing.T) {
		r := newMirrorRig()
		r.resolve("Example Corp", "Platform Engineer")
		got := strings.Join(r.calls, ",")
		if !strings.HasPrefix(got, "page,registry,workday,board:greenhouse:example") || !strings.HasSuffix(got, "search") {
			t.Fatalf("calls %s", got)
		}
	})
	t.Run("the web is searched only when no board found anything", func(t *testing.T) {
		r := newMirrorRig()
		r.reg = []discovery.Board{{ATS: "lever", Slug: "example"}}
		r.boards["lever:example"] = []discovery.Posting{boardPost("lever", "example", "", "Platform Engineer", emp, "", "")}
		got := r.resolve("Example Corp", "Platform Engineer")
		if got.URL != emp || got.Via != "registry-search" || strings.Contains(strings.Join(r.calls, ","), "search") {
			t.Fatalf("%+v calls %v", got, r.calls)
		}
	})
	t.Run("the title is the same words in order; a level or a qualifier is another job", func(t *testing.T) {
		for title, want := range map[string]bool{
			"Platform Engineer": true, "platform  engineer": true, "Platform Engineer - Example": true,
			"Senior Platform Engineer": false, "Platform Engineer II": false, "Platform Engineers": false, "Engineer": false,
		} {
			r := newMirrorRig()
			r.reg = []discovery.Board{{ATS: "lever", Slug: "example"}}
			r.boards["lever:example"] = []discovery.Posting{boardPost("lever", "example", "", title, emp, "", "")}
			if got := r.resolve("Example Corp", "Platform Engineer").URL == emp; got != want {
				t.Errorf("%q: resolved=%v want %v", title, got, want)
			}
		}
	})
	t.Run("exactly one posting; several are narrowed by the city the mirror names, else not guessed", func(t *testing.T) {
		r := newMirrorRig()
		r.reg = []discovery.Board{{ATS: "lever", Slug: "example"}}
		r.boards["lever:example"] = []discovery.Posting{
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/ny", "New York, NY", ""),
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/sf", "San Francisco, California", ""),
		}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.URL != "https://jobs.example.test/p/sf" {
			t.Fatalf("the city in the mirror's path: %+v", got)
		}
		r.boards["lever:example"] = []discovery.Posting{
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/a", "Austin, TX", ""),
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/b", "Denver, CO", ""),
		}
		got := r.resolve("Example Corp", "Platform Engineer")
		if got.URL != "" || !strings.Contains(got.Reason, "2 employer postings share its title") {
			t.Fatalf("%+v", got)
		}
		if locationHasCity("Sanfrancisco, CA", "San Francisco") || !locationHasCity("San Francisco, California", "San Francisco") {
			t.Fatal("a city counts as whole words in order")
		}
	})
	t.Run("a board named by the company's first word counts when its posting names the whole company", func(t *testing.T) {
		// Palantir Technologies posts on Lever as "palantir".
		for desc, want := range map[string]bool{"Example Technologies builds platforms.": true, "Example builds platforms.": false} {
			r := newMirrorRig()
			r.boards["lever:example"] = []discovery.Posting{boardPost("lever", "example", "", "Platform Engineer", emp, "", desc)}
			if got := r.resolve("Example Technologies", "Platform Engineer"); (got.URL == emp) != want || want && got.Via != "board-probe" {
				t.Errorf("%q: %+v", desc, got)
			}
		}
	})
	t.Run("several same-titled postings are narrowed by the city the scans recorded", func(t *testing.T) {
		// Indeed's address names no city; the scan recorded Palo Alto.
		r := newMirrorRig()
		r.status, r.html = 403, ""
		r.reg = []discovery.Board{{ATS: "lever", Slug: "example"}}
		r.boards["lever:example"] = []discovery.Posting{
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/ny", "New York, NY", ""),
			boardPost("lever", "example", "", "Platform Engineer", "https://jobs.example.test/p/pa", "Palo Alto, CA", ""),
		}
		indeed := "https://www.indeed.com/viewjob?jk=0123456789abcdef"
		r.m.Location = func(_ context.Context, raw string) string {
			if raw == indeed {
				return "Palo Alto, CA"
			}
			return ""
		}
		if got := r.m.Resolve(context.Background(), indeed, MirrorHint{Company: "Example Corp", Role: "Platform Engineer"}); got.URL != "https://jobs.example.test/p/pa" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a guessed board counts only when it proves it is that company's", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(r *mirrorRig)
			want  bool
		}{
			{"workable board names the company", func(r *mirrorRig) {
				r.boards["workable:example"] = []discovery.Posting{boardPost("workable", "example", "Example Corp", "Platform Engineer", emp, "", "")}
			}, true},
			{"workable board of another company", func(r *mirrorRig) {
				r.boards["workable:example"] = []discovery.Posting{boardPost("workable", "example", "Other Holdings", "Platform Engineer", emp, "", "")}
			}, false},
			{"greenhouse board named for the company", func(r *mirrorRig) {
				r.boards["greenhouse:example"] = []discovery.Posting{boardPost("greenhouse", "example", "", "Platform Engineer", emp, "", "")}
				r.names["example"] = "Example Corp, Inc."
			}, true},
			{"greenhouse board of another company", func(r *mirrorRig) {
				r.boards["greenhouse:example"] = []discovery.Posting{boardPost("greenhouse", "example", "", "Platform Engineer", emp, "", "")}
				r.names["example"] = "Example Corporation of Elsewhere"
			}, false},
			{"ashby posting text says the company", func(r *mirrorRig) {
				r.boards["ashby:example"] = []discovery.Posting{boardPost("ashby", "example", "", "Platform Engineer", emp, "", "Join Example Corp to build tools")}
			}, true},
			{"ashby posting text names nobody", func(r *mirrorRig) {
				r.boards["ashby:example"] = []discovery.Posting{boardPost("ashby", "example", "", "Platform Engineer", emp, "", "Join us to build tools")}
			}, false},
		}
		for _, c := range cases {
			r := newMirrorRig()
			c.setup(r)
			if got := r.resolve("Example Corp", "Platform Engineer"); (got.URL == emp) != c.want {
				t.Errorf("%s: %+v", c.name, got)
			}
		}
	})
	t.Run("a greenhouse board keyed by the whole name may go by its leading words; the first-word guess may not", func(t *testing.T) {
		r := newMirrorRig()
		r.boards["greenhouse:examplesecurity"] = []discovery.Posting{boardPost("greenhouse", "examplesecurity", "", "Platform Engineer", emp, "", "")}
		r.names["examplesecurity"] = "Example"
		if got := r.resolve("Example Security", "Platform Engineer"); got.URL != emp {
			t.Errorf("whole-name board: %+v", got)
		}
		r = newMirrorRig()
		r.boards["greenhouse:example"] = []discovery.Posting{boardPost("greenhouse", "example", "", "Platform Engineer", emp, "", "")}
		r.names["example"] = "Example"
		if got := r.resolve("Example Security", "Platform Engineer"); got.URL == emp {
			t.Errorf("first-word board: %+v", got)
		}
	})
	t.Run("a web hit needs the same title, a company-owned or recognized host, and not another mirror", func(t *testing.T) {
		cases := []struct {
			name string
			hit  WebHit
			want bool
		}{
			{"trailing company part", WebHit{"Platform Engineer at Example Corp", "https://www.example.test/careers/jobs/1"}, true},
			{"careers tail", WebHit{"Platform Engineer - Careers | example.test", "https://careers.example.test/jobs/1"}, true},
			{"tail that does not name the company", WebHit{"Platform Engineer in Test", "https://www.example.test/careers/jobs/1"}, false},
			{"someone else's host", WebHit{"Platform Engineer at Example Corp", "https://jobs.otherhost.test/1"}, false},
			{"a recognized system whose title names the company", WebHit{"Platform Engineer at Example Corp", "https://jobs.lever.co/zzz/1"}, true},
			{"a recognized system whose title does not", WebHit{"Platform Engineer", "https://jobs.lever.co/zzz/1"}, false},
			{"linkedin", WebHit{"Platform Engineer at Example Corp", "https://www.linkedin.com/jobs/view/1"}, false},
			{"another mirror", WebHit{"Platform Engineer at Example Corp", "https://www.ziprecruiter.com/jobs/1"}, false},
		}
		for _, c := range cases {
			r := newMirrorRig()
			r.web = []WebHit{c.hit}
			if got := r.resolve("Example Corp", "Platform Engineer"); (got.URL == c.hit.URL) != c.want {
				t.Errorf("%s: %+v", c.name, got)
			}
		}
		// The employer's own posting the open web ranks below its copies is found
		// by the same search over the recognized systems alone (Caris).
		r := newMirrorRig()
		r.atsWeb = []WebHit{{"Platform Engineer at Examplecorp", "https://examplecorp.wd12.myworkdayjobs.com/en-US/ext/job/Austin-TX/Platform-Engineer_JR1"}}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.Via != "web-search" || got.URL != r.atsWeb[0].URL {
			t.Errorf("the search over the recognized systems: %+v", got)
		}
		// A hit of another posting on the company's own Workday site hands that
		// site to the Workday search, which finds the posting; a hit on someone
		// else's Workday site does not.
		r = newMirrorRig()
		other := WebHit{"Staff DevOps Engineer", "https://example.wd12.myworkdayjobs.com/en-US/EXT/job/Staff-DevOps-Engineer_JR2"}
		r.web = []WebHit{other, {"Platform Engineer at Amgen", "https://amgen.wd1.myworkdayjobs.com/en-US/Careers/job/Platform-Engineer_R1"}}
		r.wdBySite = []BoardMatch{{URL: "https://example.wd12.myworkdayjobs.com/EXT/job/Platform-Engineer_JR1", Key: "example.wd12/job/JR1", Via: "workday-search"}}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.Via != "workday-search" || got.URL != r.wdBySite[0].URL || strings.Join(r.wdSites, ",") != other.URL {
			t.Errorf("the company's Workday site from the web: %+v, sites %v", got, r.wdSites)
		}
		// One posting reached by two addresses is one match.
		r = newMirrorRig()
		r.web = []WebHit{
			{"Platform Engineer at Example Corp", "https://examplecorp.wd12.myworkdayjobs.com/en-US/ext/job/Austin-TX/Platform-Engineer_JR1"},
			{"Platform Engineer at Example Corp", "https://examplecorp.wd12.myworkdayjobs.com/en-US/EXT/job/Platform-Engineer_JR1"},
		}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.Via != "web-search" || got.URL == "" {
			t.Errorf("two addresses of one posting: %+v", got)
		}
		// Every query failing is said, with the search's own error.
		r = newMirrorRig()
		r.m.Search = func(context.Context, string, []string, []string) ([]WebHit, error) {
			return nil, errors.New("api.exa.ai: HTTP 402")
		}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.URL != "" || !strings.Contains(got.Reason, "the web search failed: api.exa.ai: HTTP 402") {
			t.Errorf("a failed search: %+v", got)
		}
		// No key: the step is skipped, never an error, and the stop says the key would have searched the web.
		for name, search := range map[string]func(context.Context, string, []string, []string) ([]WebHit, error){
			"no source": nil,
			"no key":    func(context.Context, string, []string, []string) ([]WebHit, error) { return nil, ErrNoWebSearch },
		} {
			r := newMirrorRig()
			r.m.Search = search
			if got := r.resolve("Example Corp", "Platform Engineer"); got.URL != "" || !strings.Contains(got.Reason, "the open web was not searched: no web search key is stored") {
				t.Errorf("%s: %+v", name, got)
			}
		}
	})
	t.Run("two boards listing one posting count once", func(t *testing.T) {
		r := newMirrorRig()
		r.wd = []BoardMatch{{URL: "https://x.wd5.myworkdayjobs.com/SiteA/job/p", Key: "x.wd5/job/p", Via: "workday-search"}, {URL: "https://x.wd5.myworkdayjobs.com/SiteB/job/p", Key: "x.wd5/job/p", Via: "workday-search"}}
		if got := r.resolve("Example Corp", "Platform Engineer"); got.Via != "workday-search" || got.URL == "" {
			t.Fatalf("%+v", got)
		}
	})
}

func TestApply_Resolve_AggregatorOnlyReasonPrefixAndWhy(t *testing.T) {
	cases := []struct {
		name, company, role string
		status              int
		want                []string
		notWant             string
	}{
		{"closed copy", "Example Corp", "Platform Engineer", 404, []string{"the mirror's own copy is closed, HTTP 404"}, "no employer is named"},
		{"gone", "Example Corp", "Platform Engineer", 410, []string{"HTTP 410"}, ""},
		{"bot wall", "Example Corp", "Platform Engineer", 403, []string{"behind a bot wall, HTTP 403"}, ""},
		{"too many", "Example Corp", "Platform Engineer", 429, []string{"HTTP 429"}, ""},
		{"no employer named", "Undisclosed", "Platform Engineer", 200, []string{"no employer is named"}, ""},
		{"a vague label", "Reputed Company", "Platform Engineer", 200, []string{"no employer is named"}, ""},
		{"the mirror's own name", "TalentHop", "Platform Engineer", 200, []string{"no employer is named"}, ""},
		{"nothing known", "Example Corp", "Platform Engineer", 200, nil, "("},
	}
	for _, c := range cases {
		r := newMirrorRig()
		r.status = c.status
		got := r.resolve(c.company, c.role)
		if got.URL != "" || !strings.HasPrefix(got.Reason, AggregatorOnlyPrefix) {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got.Reason, w) {
				t.Errorf("%s: %q lacks %q", c.name, got.Reason, w)
			}
		}
		if c.notWant != "" && strings.Contains(got.Reason, c.notWant) {
			t.Errorf("%s: %q has %q", c.name, got.Reason, c.notWant)
		}
		// A label that is not a company is never searched.
		if strings.HasPrefix(c.name, "no employer") || c.name == "a vague label" || c.name == "the mirror's own name" {
			for _, call := range r.calls {
				if call != "page" {
					t.Errorf("%s: searched (%s)", c.name, call)
				}
			}
		}
	}
	t.Run("the mirror's name with the employer in brackets names the employer", func(t *testing.T) {
		for in, want := range map[string]string{"TalentHop (Example Corp)": "Example Corp", "Example Group (via TalentHop)": "Example Group", "Example Corp (Remote)": "Example Corp"} {
			h, ok := employerNamed(MirrorHint{Company: in, Role: "Engineer"})
			if !ok || h.Company != want {
				t.Errorf("%q: %+v %v", in, h, ok)
			}
		}
	})
	t.Run("an unresolved mirror stops the run in resolve as a posting, before any browser work", func(t *testing.T) {
		g := newRig(t, nil)
		driven := false
		g.r.d.Drive = func(context.Context, *Exec) *stop { driven = true; return nil }
		run := g.r.RunOne(context.Background(), Input{URL: zipURL, Caller: gates.CallerCLI})
		mustStop(t, run, StageResolve, ClassPosting)
		if !strings.HasPrefix(run.Outcome.Reason, AggregatorOnlyPrefix) || driven {
			t.Fatalf("outcome %+v driven %v", run.Outcome, driven)
		}
	})
}

func TestApply_Resolve_MirrorReadsPagesOnlyAndNeverRetriesAWall(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		r := newMirrorRig()
		r.status = status
		got := r.resolve("Example Corp", "Platform Engineer")
		pages := 0
		for _, c := range r.calls {
			if c == "page" {
				pages++
			}
		}
		if pages != 1 || got.URL != "" || !strings.Contains(got.Reason, "bot wall") {
			t.Errorf("status %d: page read %d times, %+v", status, pages, got)
		}
	}
	// Every lookup the resolver can make is a read; none can drive a browser,
	// sign in or submit.
	var fields []string
	typ := reflect.TypeOf(MirrorSources{})
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if got := strings.Join(fields, ","); got != "Page,Registry,Board,BoardName,Workday,Search,Location" {
		t.Fatalf("the resolver's lookups changed: %s", got)
	}
}

func TestApply_Resolve_EmployerURLFromListSkipsSecondResolution(t *testing.T) {
	var pages atomic.Int32
	const emp = "https://jobs.lever.co/example-corp/00000000-0000-0000-0000-000000000001"
	mk := func() *rig {
		return newRig(t, func(d *Deps) {
			d.Mirror = &MirrorSources{Page: func(context.Context, string) (string, int) { pages.Add(1); return mirrorHTM, 200 }}
			d.Drive = func(context.Context, *Exec) *stop { return nil }
		})
	}
	g := mk()
	run := g.r.RunOne(context.Background(), Input{URL: zipURL, EmployerURL: emp, Caller: gates.CallerCLI})
	if pages.Load() != 0 || run.ATS != ATSLever || run.Tenant != "example-corp" {
		t.Fatalf("pages read %d, ats %s tenant %s: the list's resolution is classified, not repeated", pages.Load(), run.ATS, run.Tenant)
	}
	// The run's identity stays the original URL.
	if want, _ := IdentityKey(zipURL); run.Identity != want {
		t.Fatalf("identity %q want %q", run.Identity, want)
	}
	// Without it the chain resolves in-chain, once.
	run = mk().r.RunOne(context.Background(), Input{URL: zipURL, Caller: gates.CallerCLI})
	if pages.Load() != 1 || run.ATS != ATSLever || !strings.Contains(stageOf(run, StageResolve).Detail, "outbound-link") {
		t.Fatalf("pages read %d, run %+v", pages.Load(), stageOf(run, StageResolve))
	}
}
