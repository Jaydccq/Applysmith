package identity

import "testing"

func TestCanonicalURL_PerHostRules(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"tracking params", "http://Example.com/jobs/42/?utm_source=x&gh_src=y&ref=z&source=a&lever-source=b&jr_id=1&cmpid=2&ss=3&id=7#frag", "https://example.com/jobs/42?id=7"},
		{"utm prefix any case", "https://example.com/jobs/42?UTM_Campaign=x", "https://example.com/jobs/42"},
		{"repeated slashes", "https://example.com//jobs///42//", "https://example.com/jobs/42"},
		{"query order stable", "https://example.com/j?b=2&a=1", "https://example.com/j?a=1&b=2"},
		{"application step apply", "https://example.com/jobs/42/apply", "https://example.com/jobs/42"},
		{"application step apply-manually", "https://example.com/jobs/42/apply-manually", "https://example.com/jobs/42"},
		{"application step autofill", "https://example.com/jobs/42/autofill-with-resume", "https://example.com/jobs/42"},
		{"application step login", "https://example.com/jobs/42/login", "https://example.com/jobs/42"},
		{"application step sign-in", "https://example.com/jobs/42/Sign-In", "https://example.com/jobs/42"},
		{"step only segment is kept", "https://example.com/apply", "https://example.com/apply"},
		{"indeed keeps jk", "https://www.indeed.com/viewjob?jk=abc123&from=serp&vjs=3", "https://www.indeed.com/viewjob?jk=abc123"},
		{"indeed country domain keeps jk", "https://de.indeed.com/viewjob?jk=abc123&from=serp", "https://de.indeed.com/viewjob?jk=abc123"},
		{"indeed co.uk keeps jk", "https://www.indeed.co.uk/viewjob?jk=abc123&from=serp", "https://www.indeed.co.uk/viewjob?jk=abc123"},
		{"indeed look-alike keeps its query", "https://notindeed.com/viewjob?jk=abc123&id=7", "https://notindeed.com/viewjob?id=7&jk=abc123"},
		{"indeed subdomain of another site keeps its query", "https://indeed.example.com/viewjob?jk=abc123&id=7", "https://indeed.example.com/viewjob?id=7&jk=abc123"},
		{"greenhouse embed keeps for and token", "https://boards.greenhouse.io/embed/job_app?for=example-co&token=1001&utm_source=x&gh_src=q", "https://boards.greenhouse.io/embed/job_app?for=example-co&token=1001"},
		{"greenhouse board drops query", "https://boards.greenhouse.io/example-co/jobs/1001?gh_src=abc", "https://boards.greenhouse.io/example-co/jobs/1001"},
		{"greenhouse board index keeps gh_jid", "https://boards.greenhouse.io/example-co?gh_jid=1001&gh_src=x", "https://boards.greenhouse.io/example-co?gh_jid=1001"},
		{"greenhouse company host keeps gh_jid", "https://example-co.greenhouse.io/?gh_jid=1001", "https://example-co.greenhouse.io?gh_jid=1001"},
		{"greenhouse job path drops gh_jid", "https://job-boards.greenhouse.io/example-co/jobs/1001?gh_jid=1001", "https://job-boards.greenhouse.io/example-co/jobs/1001"},
		{"lever drops query and apply", "https://jobs.lever.co/example-co/0a1b2c3d-1111-2222-3333-444455556666/apply?lever-source=x&foo=1", "https://jobs.lever.co/example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"ashby drops query and application", "https://jobs.ashbyhq.com/example-co/0a1b2c3d-1111-2222-3333-444455556666/application?utm_source=x", "https://jobs.ashbyhq.com/example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"smartrecruiters drops query", "https://jobs.smartrecruiters.com/ExampleCo/743999000000001-software-engineer?trid=9", "https://jobs.smartrecruiters.com/ExampleCo/743999000000001-software-engineer"},
		{"workday apply suffix", "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Exampleton/Engineer_R100/apply/applyManually?source=x", "https://example.wd5.myworkdayjobs.com/careers/job/Engineer_R100"},
		{"workday job path keeps site and title", "https://example.wd5.myworkdayjobs.com/careers/job/Engineer_R100", "https://example.wd5.myworkdayjobs.com/careers/job/Engineer_R100"},
		{"workday site path without a job stays", "https://example.wd5.myworkdayjobs.com/en-US/Careers?q=engineer", "https://example.wd5.myworkdayjobs.com/en-US/Careers?q=engineer"},
		{"workday site named like a locale is kept", "https://example.wd5.myworkdayjobs.com/US/job/Engineer_R100", "https://example.wd5.myworkdayjobs.com/us/job/Engineer_R100"},
		{"workday recruiting site", "https://wd5.myworkdaysite.com/en-US/recruiting/Example/External/job/AZ---Chandler/Engineer_R100", "https://wd5.myworkdaysite.com/recruiting/example/external/job/Engineer_R100"},
		{"amazon locale prefix", "https://www.amazon.jobs/en-gb/jobs/2999001/software-dev-engineer?cmpid=x", "https://www.amazon.jobs/jobs/2999001"},
		{"amazon no locale", "https://amazon.jobs/jobs/2999001", "https://www.amazon.jobs/jobs/2999001"},
		{"linkedin slug", "https://www.linkedin.com/jobs/view/software-engineer-at-example-3912345678?trk=x", "https://www.linkedin.com/jobs/view/3912345678"},
		{"linkedin bare id", "https://uk.linkedin.com/jobs/view/3912345678/", "https://www.linkedin.com/jobs/view/3912345678"},
		{"linkedin currentJobId", "https://www.linkedin.com/jobs/collections/recommended/?currentJobId=3912345678&q=x", "https://www.linkedin.com/jobs/view/3912345678"},
		{"empty path", "https://Example.com/", "https://example.com"},
		{"non-default port kept", "https://example.com:8443/jobs/1", "https://example.com:8443/jobs/1"},
		{"query glued onto the path with an ampersand", "http://boards.greenhouse.io/example-co/jobs/4012345&gh_src=abc1+?gh_src=abc1+", "https://boards.greenhouse.io/example-co/jobs/4012345"},
		{"glued query kept as a query elsewhere", "https://example.com/jobs/42&id=7&utm_source=x", "https://example.com/jobs/42?id=7"},
		{"ampersand in a path word stays", "https://example.com/teams/r&d/42", "https://example.com/teams/r&d/42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalURL(tt.in)
			if err != nil || got != tt.want {
				t.Fatalf("CanonicalURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

// One Workday requisition is one key whichever form Workday or a board shares
// it in: with or without a locale, in any site case, with or without a
// location segment or a share parameter (GM's JR-202620428 was two keys,
// Visa's share_id and KLA's bid made more).
func TestIdentity_Workday_AllShapesOneKey(t *testing.T) {
	want := "https://generalmotors.wd5.myworkdayjobs.com/careers_gm/job/Data-Engineer_JR-202620428"
	for _, in := range []string{
		"https://generalmotors.wd5.myworkdayjobs.com/en-US/Careers_GM/job/Data-Engineer_JR-202620428",
		"https://generalmotors.wd5.myworkdayjobs.com/careers_gm/job/Austin-Texas-United-States-of-America/Data-Engineer_JR-202620428",
		"https://GeneralMotors.wd5.myworkdayjobs.com/Careers_GM/job/Warren-Michigan/Data-Engineer_JR-202620428?share_id=LinkedIn_corporate_page&bid=370",
		"https://generalmotors.wd5.myworkdayjobs.com/fr-CA/Careers_GM/job/Data-Engineer_JR-202620428/apply/autofillWithResume",
	} {
		if got, err := URLKey(in); err != nil || got != want {
			t.Errorf("URLKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	other, _ := URLKey("https://generalmotors.wd5.myworkdayjobs.com/en-US/Careers_GM/job/Data-Engineer_JR-202620429")
	if other == want {
		t.Errorf("another requisition shares the key %q", other)
	}
}

func TestCanonicalURL_Rejects(t *testing.T) {
	for _, in := range []string{"", "not a url", "ftp://example.com/x", "/relative/path", "https://"} {
		if got, err := CanonicalURL(in); err == nil {
			t.Errorf("CanonicalURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestIdentity_Greenhouse_AllShapesOneKey(t *testing.T) {
	const want = "greenhouse:example-co/1001"
	for _, in := range []string{
		"https://boards.greenhouse.io/example-co/jobs/1001",
		"https://boards.greenhouse.io/Example-Co/jobs/1001/apply?gh_src=x",
		"https://job-boards.greenhouse.io/example-co/jobs/1001",
		"https://boards.eu.greenhouse.io/example-co/jobs/1001",
		"https://job-boards.eu.greenhouse.io/example-co/jobs/1001",
		"https://boards.greenhouse.io/embed/job_app?for=example-co&token=1001",
		"https://job-boards.greenhouse.io/embed/job_app?token=1001&for=Example-Co&utm_source=x",
		"https://boards.greenhouse.io/embed/job_board?for=example-co&gh_jid=1001",
		"https://boards.greenhouse.io/example-co?gh_jid=1001",
		"https://example-co.greenhouse.io/jobs/1001",
		"https://boards-api.greenhouse.io/v1/boards/example-co/jobs/1001",
		"http://boards.greenhouse.io/example-co/jobs/1001&gh_src=abc1+?gh_src=abc1+",
		"https://example.com/careers/job?1001=&board=example-co&gh_jid=1001",
		"https://example.com/careers?for=example-co&gh_jid=1001",
	} {
		t.Run(in, func(t *testing.T) {
			got, err := URLKey(in)
			if err != nil || got != want {
				t.Fatalf("URLKey = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestIdentity_OtherATSShapes(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"lever", "https://jobs.lever.co/Example-Co/0A1B2C3D-1111-2222-3333-444455556666", "lever:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"lever apply", "https://jobs.lever.co/example-co/0a1b2c3d-1111-2222-3333-444455556666/apply?lever-source=x", "lever:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"lever eu", "https://jobs.eu.lever.co/example-co/0a1b2c3d-1111-2222-3333-444455556666", "lever:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"lever api", "https://api.lever.co/v0/postings/example-co/0a1b2c3d-1111-2222-3333-444455556666", "lever:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"ashby", "https://jobs.ashbyhq.com/example-co/0a1b2c3d-1111-2222-3333-444455556666", "ashby:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"ashby application", "https://jobs.ashbyhq.com/example-co/0a1b2c3d-1111-2222-3333-444455556666/application", "ashby:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"ashby jid param", "https://jobs.ashbyhq.com/example-co?ashby_jid=0a1b2c3d-1111-2222-3333-444455556666", "ashby:example-co/0a1b2c3d-1111-2222-3333-444455556666"},
		{"smartrecruiters jobs", "https://jobs.smartrecruiters.com/ExampleCo/743999000000001-software-engineer", "smartrecruiters:exampleco/743999000000001"},
		{"smartrecruiters careers", "https://careers.smartrecruiters.com/ExampleCo/743999000000001", "smartrecruiters:exampleco/743999000000001"},
		{"smartrecruiters oneclick", "https://jobs.smartrecruiters.com/oneclick-ui/company/ExampleCo/publication/0a1b2c3d-1111-2222-3333-444455556666", "smartrecruiters:exampleco/0a1b2c3d-1111-2222-3333-444455556666"},
		{"smartrecruiters api", "https://api.smartrecruiters.com/v1/companies/ExampleCo/postings/743999000000001", "smartrecruiters:exampleco/743999000000001"},
		{"workable apply host", "https://apply.workable.com/example-co/j/AB12CD34EF/", "workable:example-co/ab12cd34ef"},
		{"workable apply step", "https://apply.workable.com/example-co/j/AB12CD34EF/apply/", "workable:example-co/ab12cd34ef"},
		{"workable subdomain", "https://example-co.workable.com/jobs/123456", "workable:example-co/123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := URLKey(tt.in)
			if err != nil || got != tt.want {
				t.Fatalf("URLKey(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
			if !IsATSNative(got) {
				t.Errorf("%q should be ATS-native", got)
			}
		})
	}
}

func TestIdentity_NoATSKey(t *testing.T) {
	for _, in := range []string{
		"https://apply.workable.com/j/AB12CD34",                  // short link: no board
		"https://www.greenhouse.io/jobs/1001",                    // infrastructure host
		"https://boards.greenhouse.io/example-co",                // board page, no posting
		"https://boards.greenhouse.io/embed/job_board?for=x",     // board embed, no posting
		"https://jobs.lever.co/example-co",                       // company page
		"https://jobs.smartrecruiters.com/ExampleCo",             // company page
		"https://example.com/careers?gh_jid=1001",                // employer host: board unknown
		"https://api.greenhouse.io/v1/boards/example-co/jobs/1",  // not a board host
		"https://www.linkedin.com/jobs/view/3912345678",          // not an ATS
		"https://jobs.lever.co/example-co/not-a-posting-id-xx!!", // malformed id
	} {
		if k, ok := ATSKey(in); ok {
			t.Errorf("ATSKey(%q) = %q, want none", in, k)
		}
	}
}

func TestIdentity_KeyPreferenceOrder(t *testing.T) {
	p := Posting{
		URL: "https://boards.greenhouse.io/example-co/jobs/1001", Source: "Feed", SourceJobID: "77",
		Company: "Example Corp", Role: "Engineer", Text: "body",
	}
	steps := []struct {
		name string
		mut  func(*Posting)
		want string
	}{
		{"ats native", func(*Posting) {}, "greenhouse:example-co/1001"},
		{"canonical url", func(p *Posting) { p.URL = "https://Example.com/careers/1/?utm_source=x" }, "https://example.com/careers/1"},
		{"source and id", func(p *Posting) { p.URL = "" }, "feed:77"},
		{"company and role", func(p *Posting) { p.Source = "" }, "example|engineer"},
		{"company role place", func(p *Posting) { p.Location = "Exampleton, EX" }, "example|engineer|ex-exampleton"},
		{"content hash", func(p *Posting) { p.Company = "Unknown" }, ""},
	}
	for _, s := range steps {
		s.mut(&p)
		got := p.IdentityKey()
		if s.name == "content hash" {
			if len(got) < 10 || got[:8] != "content:" {
				t.Fatalf("%s: got %q", s.name, got)
			}
			continue
		}
		if got != s.want {
			t.Fatalf("%s: got %q want %q", s.name, got, s.want)
		}
	}
	if k := (Posting{}).IdentityKey(); k != "" {
		t.Errorf("empty posting key = %q", k)
	}
	if k := (Posting{Key: "stored:1", URL: "https://example.com/a"}).IdentityKey(); k != "stored:1" {
		t.Errorf("a stored key must win, got %q", k)
	}
}

func TestNativeKey_AgreesWithATSKey(t *testing.T) {
	for _, tc := range []struct {
		ats, slug, id string
		want          string
		ok            bool
	}{
		{Greenhouse, "Example-Co", "1001", "greenhouse:example-co/1001", true},
		{Lever, "example-co", "0A1B2C3D-1111-2222-3333-444455556666", "lever:example-co/0a1b2c3d-1111-2222-3333-444455556666", true},
		{Workable, "example-co", "AB12CD34", "workable:example-co/ab12cd34", true},
		{Greenhouse, "", "1001", "", false},
		{Greenhouse, "example-co", "", "", false},
		{Greenhouse, "boards", "1001", "", false},
		{Lever, "example-co", "short", "", false},
		{"taleo", "example-co", "1001", "", false},
		{"", "", "", "", false},
	} {
		got, ok := NativeKey(tc.ats, tc.slug, tc.id)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NativeKey(%q, %q, %q) = %q, %v", tc.ats, tc.slug, tc.id, got, ok)
		}
	}
}

func TestIdentity_Canonical_AmazonNumericIDOnly(t *testing.T) {
	a, err := CanonicalURL("https://www.amazon.jobs/en/jobs/1234567/example-title")
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalURL("https://www.amazon.jobs/jobs/1234567/example-title-")
	if err != nil {
		t.Fatal(err)
	}
	if a != "https://www.amazon.jobs/jobs/1234567" || a != b {
		t.Fatalf("canonical forms = %q and %q", a, b)
	}
	ka := Posting{URL: a}.IdentityKey()
	kb := Posting{URL: b}.IdentityKey()
	if ka == "" || ka != kb {
		t.Fatalf("identity keys = %q and %q", ka, kb)
	}
}
