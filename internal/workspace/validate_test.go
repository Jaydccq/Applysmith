package workspace

import (
	"strings"
	"testing"
	"time"
)

// at renders "path code" pairs for compact table assertions.
func at(rep Report) []string {
	var out []string
	for _, i := range rep.Issues {
		out = append(out, i.Path+" "+i.Code)
	}
	return out
}

func wantIssues(t *testing.T, rep Report, want ...string) {
	t.Helper()
	got := at(rep)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("issues\n got: %q\nwant: %q\n%s", got, want, rep)
	}
}

func TestSeededTemplatesAreValid(t *testing.T) {
	for _, f := range Files {
		data := templateBytes(f)
		if data == nil {
			continue
		}
		if rep := Validate(f, data); !rep.OK() {
			t.Errorf("%s: %s", f.Name(), rep)
		}
	}
}

// fakeProviderKey is assembled at run time so the repository guard never sees a
// credential-shaped literal in source.
var fakeProviderKey = "sk" + "-" + strings.Repeat("x", 26)

func TestProfileValidation(t *testing.T) {
	// The computer's clock is early on a local day east of UTC, where the UTC
	// date is still the day before: date-past compares calendar dates on this
	// computer's clock, the zone the dashboard writes today's date in.
	restore := clock
	clock = func() time.Time { return time.Date(2026, 10, 22, 3, 0, 0, 0, time.FixedZone("UTC+9", 9*3600)) }
	t.Cleanup(func() { clock = restore })
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"minimal", "version: 1\n", nil},
		{"missing version", "candidate: {}\n", []string{"version required"}},
		{"role priority enum", "version: 1\ntargets:\n  roles:\n    - title: A\n    - title: B\n    - title: C\n      priority: urgent\n", []string{"targets.roles[2].priority not_allowed"}},
		{"role priority default", "version: 1\ntargets:\n  roles:\n    - title: A\n", nil},
		{"misspelled key", "version: 1\ntargets:\n  compensaton: {}\n", []string{"targets.compensaton unknown_key"}},
		{"skill level", "version: 1\nskills:\n  - name: Go\n    level: guru\n", []string{"skills[0].level not_allowed"}},
		{"dob shape", "version: 1\ncandidate:\n  date_of_birth: 15/06/2005\n", []string{"candidate.date_of_birth invalid_format"}},
		{"dob future", "version: 1\ncandidate:\n  date_of_birth: 2999-01-01\n", []string{"candidate.date_of_birth invalid_format"}},
		{"dob ok", "version: 1\ncandidate:\n  date_of_birth: 2005-06-15\n", nil},
		{"compensation order", "version: 1\ntargets:\n  compensation:\n    minimum: 100\n    target: 50\n", []string{"targets.compensation.minimum constraint"}},
		{"compensation negative", "version: 1\ntargets:\n  compensation:\n    minimum: -1\n", []string{"targets.compensation.minimum out_of_range"}},
		{"link without scheme", "version: 1\nlinks:\n  github: github.com/example\n", []string{"links.github invalid_format"}},
		{"link ok", "version: 1\nlinks:\n  github: https://github.com/example\n  other:\n    - https://example.org\n", nil},
		{"scan evaluate_limit", "version: 1\nscan:\n  defaults:\n    linkedin-feed:\n      evaluate_limit: -1\n", []string{"scan.defaults.linkedin-feed.evaluate_limit out_of_range"}},
		{"scan blocked poster pattern", "version: 1\nscan:\n  defaults:\n    linkedin-feed:\n      blocked_posters: [\"Acme\", \"re:(recruit\"]\n", []string{"scan.defaults.linkedin-feed.blocked_posters[1] constraint"}},
		{"scan unknown source", "version: 1\nscan:\n  defaults:\n    myspace: {}\n", []string{"scan.defaults.myspace not_allowed"}},
		{"tier delta range", "version: 1\neligibility_gate:\n  employer_tiers:\n    staffing:\n      policy: penalize\n      delta: 7\n", []string{"eligibility_gate.employer_tiers.staffing.delta out_of_range"}},
		{"tier penalize needs delta", "version: 1\neligibility_gate:\n  employer_tiers:\n    staffing:\n      policy: penalize\n", []string{"eligibility_gate.employer_tiers.staffing.delta required"}},
		{"tier ok", "version: 1\neligibility_gate:\n  employer_tiers:\n    staffing:\n      policy: penalize\n      delta: 2.5\n", nil},
		{"history order", "version: 1\nhistory:\n  employment:\n    - employer: Example Corp\n      start: 2022-06\n      end: 2021-01\n", []string{"history.employment[0].end constraint"}},
		{"history present", "version: 1\nhistory:\n  employment:\n    - employer: Example Corp\n      start: 2022-06\n      end: present\n", nil},
		{"history blank employer", "version: 1\nhistory:\n  employment:\n    - title: X\n", []string{"history.employment[0].employer required"}},
		{"relationship ok", "version: 1\ncompany_relationships:\n  relationships:\n    - family: relative\n      company: Example Corp\n      person_name: Sam Example\n      recorded: 2026-09-01\n  complete:\n    relative: 2026-09-02\n", nil},
		{"relationship recorded today east of UTC", "version: 1\ncompany_relationships:\n  relationships:\n    - family: employment\n      company: Example Corp\n      recorded: 2026-10-22\n  complete:\n    employment: 2026-10-22\n", nil},
		{"relationship family", "version: 1\ncompany_relationships:\n  relationships:\n    - family: cousin\n      company: Example Corp\n      recorded: 2026-09-01\n", []string{"company_relationships.relationships[0].family not_allowed"}},
		{"relationship needs a date", "version: 1\ncompany_relationships:\n  relationships:\n    - family: employment\n      company: Example Corp\n", []string{"company_relationships.relationships[0].recorded required"}},
		{"relationship complete family", "version: 1\ncompany_relationships:\n  complete:\n    neighbour: 2026-09-01\n", []string{"company_relationships.complete.neighbour not_allowed"}},
		{"relationship complete date", "version: 1\ncompany_relationships:\n  complete:\n    employment: soon\n", []string{"company_relationships.complete.employment invalid_format"}},
		{"sponsorship tri-state", "version: 1\nwork_authorization:\n  requires_sponsorship_now: false\n  requires_sponsorship_future:\n", nil},
		{"sponsorship wrong type", "version: 1\nwork_authorization:\n  requires_sponsorship_now: maybe\n", []string{"work_authorization.requires_sponsorship_now type_mismatch"}},
		{"ask-me marker", "version: 1\ncandidate:\n  address:\n    line1: \"@ask-me\"\n    country: GB\n", nil},
		{"bad country", "version: 1\ncandidate:\n  address:\n    country: Britain\n", []string{"candidate.address.country invalid_format"}},
		{"quiet hours", "version: 1\nnotifications:\n  quiet_hours:\n    enabled: true\n    start: \"22:00\"\n    end: \"07:30\"\n    timezone: Europe/London\n", nil},
		{"quiet hours bad", "version: 1\nnotifications:\n  quiet_hours:\n    start: \"25:00\"\n    timezone: Mars/Base\n", []string{"notifications.quiet_hours.start invalid_format", "notifications.quiet_hours.timezone invalid_format"}},
		{"roster two defaults", "version: 1\nresumes:\n  - id: a\n    file: resumes/a.pdf\n    default: true\n  - id: b\n    file: resumes/b.pdf\n    default: true\n", []string{"resumes constraint"}},
		{"roster no default", "version: 1\nresumes:\n  - id: a\n    file: resumes/a.pdf\n", []string{"resumes constraint"}},
		{"roster duplicate id", "version: 1\nresumes:\n  - id: a\n    file: resumes/a.pdf\n    default: true\n  - id: a\n    file: resumes/b.pdf\n", []string{"resumes[1].id constraint"}},
		{"roster id shape", "version: 1\nresumes:\n  - id: Backend CV\n    file: resumes/a.pdf\n    default: true\n", []string{"resumes[0].id invalid_format"}},
		{"roster file traversal", "version: 1\nresumes:\n  - id: a\n    file: ../secrets.pdf\n    default: true\n", []string{"resumes[0].file path_outside_workspace"}},
		{"roster file absolute", "version: 1\nresumes:\n  - id: a\n    file: /etc/x.pdf\n    default: true\n", []string{"resumes[0].file path_outside_workspace"}},
		{"roster file outside resumes", "version: 1\nresumes:\n  - id: a\n    file: knowledge/x.pdf\n    default: true\n", []string{"resumes[0].file constraint"}},
		{"roster file in a subfolder", "version: 1\nresumes:\n  - id: a\n    file: resumes/2026/x.pdf\n    default: true\n", []string{"resumes[0].file constraint"}},
		{"roster file not pdf", "version: 1\nresumes:\n  - id: a\n    file: resumes/x.txt\n    default: true\n", []string{"resumes[0].file invalid_format"}},
		{"roster blank entry", "version: 1\nresumes:\n  - id: \"\"\n    file: \"\"\n    default: true\n", []string{"resumes[0].id required", "resumes[0].file required"}},
		{"type mismatch names both", "version: 1\ntargets: [a]\n", []string{"targets type_mismatch"}},
		{"duplicate key", "version: 1\nversion: 1\n", []string{"version duplicate_key"}},
		{"secret key", "version: 1\nllm_api_key: abc\n", []string{"llm_api_key secret_in_config"}},
		{"secret nested key", "version: 1\ncandidate:\n  site_password: x\n", []string{"candidate.site_password secret_in_config"}},
		{"secret value", "version: 1\ncandidate:\n  preferred_name: " + fakeProviderKey + "\n", []string{"candidate.preferred_name secret_in_config"}},
		{"word with sk prefix is fine", "version: 1\nskills:\n  - name: sk-learn\n", nil},
		{"invalid yaml", "version: 1\n  bad: [\n", []string{" invalid_yaml"}},
		{"empty file", "", []string{" type_mismatch"}},
		{"not a mapping", "- a\n", []string{" type_mismatch"}},
		{"null document", "~\n", []string{" type_mismatch"}},
		{"bare document marker", "---\n", []string{" type_mismatch"}},
		{"null list item", "version: 1\ntargets:\n  locations:\n    - ~\n    - London\n", []string{"targets.locations[0] type_mismatch"}},
		{"blank list item of mappings", "version: 1\ntargets:\n  roles:\n    -\n    - title: A\n", []string{"targets.roles[0] type_mismatch"}},
		{"quiet hours one-digit hour", "version: 1\nnotifications:\n  quiet_hours:\n    start: \"7:30\"\n", []string{"notifications.quiet_hours.start invalid_format"}},
		{"compensation zero", "version: 1\ntargets:\n  compensation:\n    minimum: 0\n    target: 0\n", []string{"targets.compensation.minimum out_of_range", "targets.compensation.target out_of_range"}},
		{"skill years nan", "version: 1\nskills:\n  - name: Go\n    years: .nan\n", []string{"skills[0].years out_of_range"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rep := ParseProfile([]byte(tc.yaml))
			wantIssues(t, rep, tc.want...)
		})
	}
}

func TestIssueCarriesFileKeyPathLineAndAllowedValues(t *testing.T) {
	_, rep := ParseProfile([]byte("version: 1\ntargets:\n  roles:\n    - title: A\n      priority: urgent\n"))
	if len(rep.Issues) != 1 {
		t.Fatal(rep)
	}
	i := rep.Issues[0]
	if i.File != "profile.yml" || i.Path != "targets.roles[0].priority" || i.Line != 5 || i.Code != CodeNotAllowed {
		t.Fatalf("issue = %+v", i)
	}
	if strings.Join(i.Allowed, ",") != "primary,secondary" {
		t.Fatalf("allowed = %v", i.Allowed)
	}
}

func TestSeededProfileInventsNoAnswers(t *testing.T) {
	p, _ := ParseProfile(templateBytes(FileProfile))
	wa := p.WorkAuthorization
	if wa.RequiresSponsorshipNow != nil || wa.RequiresSponsorshipFuture != nil || wa.VisaStatus != "" || wa.SecurityClearance != "" || len(wa.AuthorizedCountries) != 0 {
		t.Fatalf("work authorization = %+v", wa)
	}
	if p.Candidate != (Candidate{}) || len(p.History.Employment) != 0 || len(p.Skills) != 0 || p.Targets.Compensation != (Compensation{}) {
		t.Fatalf("seed holds values: %+v", p)
	}
}

func TestUnansweredSponsorshipIsNotFalse(t *testing.T) {
	p, rep := ParseProfile([]byte("version: 1\nwork_authorization:\n  requires_sponsorship_now: false\n  requires_sponsorship_future:\n"))
	if !rep.OK() {
		t.Fatal(rep)
	}
	if p.WorkAuthorization.RequiresSponsorshipNow == nil || *p.WorkAuthorization.RequiresSponsorshipNow {
		t.Fatal("explicit false lost")
	}
	if p.WorkAuthorization.RequiresSponsorshipFuture != nil {
		t.Fatal("blank read as answered")
	}
}

func TestBatchSkipListDefault(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"key absent", "version: 1\n", "Apple,Microsoft,Alphabet,Amazon,Meta,NVIDIA,Tesla"},
		{"empty list", "version: 1\nbatch_skip_companies: []\n", ""},
		{"null value", "version: 1\nbatch_skip_companies:\n", ""},
		{"present key replaces default", "version: 1\nbatch_skip_companies:\n  - Tesla\n", "Tesla"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, rep := ParseProfile([]byte(tc.yaml))
			if !rep.OK() {
				t.Fatal(rep)
			}
			if got := strings.Join(p.EffectiveSkipCompanies(), ","); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestPortalsValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"ats entry", "version: 1\ncompanies:\n  - name: Example Corp\n    ats: greenhouse\n    slug: example-corp\n", nil},
		{"careers url entry", "version: 1\ncompanies:\n  - name: Example Corp\n    careers_url: https://example.com/careers\n", nil},
		{"neither source form", "version: 1\ncompanies:\n  - name: Example Corp\n", []string{"companies[0] constraint"}},
		{"slug without ats", "version: 1\ncompanies:\n  - name: Example Corp\n    slug: example-corp\n", []string{"companies[0] constraint"}},
		{"unknown ats", "version: 1\ncompanies:\n  - name: Example Corp\n    ats: unknownboard\n    slug: x\n", []string{"companies[0].ats not_allowed"}},
		{"careers url http", "version: 1\ncompanies:\n  - name: Example Corp\n    careers_url: http://example.com\n", []string{"companies[0].careers_url invalid_format"}},
		{"discovery source", "version: 1\nsources:\n  linkedin-feed:\n    enabled: true\n    queries:\n      - go engineer\n", nil},
		{"alert feed https", "version: 1\nsources:\n  signals-alerts:\n    enabled: true\n    queries:\n      - url: https://www.google.com/alerts/feeds/12/34\n", nil},
		{"alert feed http", "version: 1\nsources:\n  signals-alerts:\n    queries:\n      - url: http://alerts.example.com/f\n", []string{"sources.signals-alerts.queries[0] constraint"}},
		{"alert management page", "version: 1\nsources:\n  signals-alerts:\n    queries:\n      - https://www.google.com/alerts\n", []string{"sources.signals-alerts.queries[0] constraint"}},
		{"unknown source", "version: 1\nsources:\n  signals-rss:\n    enabled: true\n", []string{"sources.signals-rss not_allowed"}},
		{"concurrency too high", "version: 1\nconcurrency:\n  detail: 40\n", []string{"concurrency.detail out_of_range"}},
		{"concurrency bounds", "version: 1\nconcurrency:\n  list: 1\n  detail: 16\n", nil},
		{"company title override", "version: 1\ncompanies:\n  - name: Example Corp\n    careers_url: https://example.com\n    filters:\n      title:\n        positive: [staff]\n", nil},
		{"blank filter terms", "version: 1\nfilters:\n  locations:\n    include: [Remote, ' ']\n  skip_companies: ['']\n", []string{"filters.locations.include[1] required", "filters.skip_companies[0] required"}},
		{"boost range", "version: 1\nfilters:\n  title:\n    seniority_boost:\n      - term: senior\n        weight: 11\n", []string{"filters.title.seniority_boost[0].weight out_of_range"}},
		{"other shape rejected", "version: 1\ntitle_filter:\n  positive: [a]\n", []string{"title_filter unknown_key"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rep := ParsePortals([]byte(tc.yaml))
			wantIssues(t, rep, tc.want...)
		})
	}
}

func TestPortalsDefaultsAndOverride(t *testing.T) {
	p, rep := ParsePortals([]byte("version: 1\ncompanies:\n  - name: A\n    careers_url: https://example.com\n    filters:\n      title:\n        positive: []\n  - name: B\n    careers_url: https://example.org\n    enabled: false\n"))
	if !rep.OK() {
		t.Fatal(rep)
	}
	if p.Concurrency.EffectiveList() != 4 || p.Concurrency.EffectiveDetail() != 6 || !p.Registry.IsEnabled() {
		t.Fatal("defaults wrong")
	}
	if p.Companies[0].Filters.Title == nil || p.Companies[1].Filters.Title != nil {
		t.Fatal("override presence lost")
	}
	if !p.Companies[0].IsEnabled() || p.Companies[1].IsEnabled() {
		t.Fatal("enabled default wrong")
	}
}

func TestPolicyValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"seed", string(templateBytes(FilePolicy)), nil},
		{"version only", "version: 1\n", nil},
		{"cooldown out of range", "version: 1\ncooldowns:\n  captcha: 2161\n", []string{"cooldowns.captcha out_of_range"}},
		{"cooldown ceiling", "version: 1\ncooldowns:\n  captcha: 2160\n", nil},
		{"cooldown null", "version: 1\ncooldowns:\n  captcha:\n", []string{"cooldowns.captcha type_mismatch"}},
		{"removed signup_quota", "version: 1\nsignup_quota:\n  total_per_week: 5\n", []string{"signup_quota unknown_key"}},
		{"captcha maybe", "version: 1\ncaptcha_solver: maybe\n", []string{"captcha_solver not_allowed"}},
		{"captcha off", "version: 1\ncaptcha_solver: off\n", nil},
		{"followup zero", "version: 1\ntracker:\n  followup:\n    first_after_days: 0\n", []string{"tracker.followup.first_after_days out_of_range"}},
		{"followup disabled", "version: 1\ntracker:\n  followup:\n    max_followups: 0\n", nil},
		{"remote mode", "version: 1\nremote_access:\n  mode: wan\n", []string{"remote_access.mode not_allowed"}},
		{"remote port", "version: 1\nremote_access:\n  tunnel_port: 80\n", []string{"remote_access.tunnel_port out_of_range"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rep := ParsePolicy([]byte(tc.yaml))
			wantIssues(t, rep, tc.want...)
		})
	}
}

func TestPolicySeedIsRestrictive(t *testing.T) {
	p, rep := ParsePolicy(templateBytes(FilePolicy))
	if !rep.OK() {
		t.Fatal(rep)
	}
	if p.RemoteAccess.Mode != "off" || p.CaptchaSolver != "on" {
		t.Fatalf("seed policy = %+v", p)
	}
	if p.Cooldowns.AccountLocked != 720 || p.Tracker.Followup.CloseAfterDays != 35 {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestAnswerBankEnvelope(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"empty", "version: 1\nentries: []\n", nil},
		{"entries any item", "version: 1\nentries:\n  - anything: goes\n", nil},
		{"unknown top-level", "version: 1\nanswers: []\n", []string{"answers unknown_key"}},
		{"entries not a list", "version: 1\nentries: {}\n", []string{"entries type_mismatch"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantIssues(t, Validate(FileAnswerBank, []byte(tc.yaml)), tc.want...)
		})
	}
}

func TestSecretRejectedInEveryYAMLFile(t *testing.T) {
	for _, f := range []File{FileProfile, FilePortals, FilePolicy, FileAnswerBank} {
		rep := Validate(f, []byte("version: 1\nbot_token: abc\n"))
		found := false
		for _, i := range rep.Issues {
			found = found || (i.Path == "bot_token" && i.Code == CodeSecretInConfig)
		}
		if !found {
			t.Errorf("%s: %s", f.Name(), rep)
		}
	}
}

// Reports are served to the API, so a secret-shaped value in an ordinary
// field must never be repeated in a message or a key path.
func TestMessagesNeverEchoSecretShapedValues(t *testing.T) {
	key := fakeProviderKey
	tests := []struct{ name, yaml string }{
		{"not allowed", "version: 1\ntargets:\n  roles:\n    - priority: " + key + "\n"},
		{"bad format", "version: 1\nlinks:\n  github: " + key + "\n"},
		{"date", "version: 1\ncandidate:\n  date_of_birth: " + key + "\n"},
		{"unknown key name", "version: 1\n" + key + ": 1\n"},
		{"allowed map key", "version: 1\nscan:\n  defaults:\n    " + key + ": {}\n"},
		{"company name", "version: 1\ncompanies:\n  - name: " + key + "\n"},
		{"duplicate key", "version: 1\n" + key + ": 1\n" + key + ": 2\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.yaml)
			for _, rep := range []Report{Validate(FileProfile, src), Validate(FilePortals, src)} {
				if strings.Contains(rep.String(), key) {
					t.Fatalf("report echoes the value:\n%s", rep)
				}
				for _, i := range rep.Issues {
					if strings.Contains(i.Path+i.Message+strings.Join(i.Allowed, ","), key) {
						t.Fatalf("issue echoes the value: %+v", i)
					}
				}
			}
			if rep := Validate(FileProfile, src); !hasCode(rep, CodeSecretInConfig) {
				t.Fatalf("secret not flagged:\n%s", rep)
			}
		})
	}
}

func hasCode(rep Report, code string) bool {
	for _, i := range rep.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// A secret typed into the profile is refused with a pointer to the keychain, where secrets belong.
func TestSecretInConfigPointsToKeychain(t *testing.T) {
	_, rep := ParseProfile([]byte("version: 1\nllm_api_key: abc\n"))
	if m := rep.Issues[0].Message; !strings.Contains(m, "keychain") {
		t.Fatalf("message = %q", m)
	}
}
