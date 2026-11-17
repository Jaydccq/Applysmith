package answerbank

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

// Rows of the bank-store audit (ST) that the load, save and profile tests do
// not reach on their own.

func TestLoad_PathIsWorkspaceAnchored(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, bankText(fixedYAML("anchored", "yes")))
	elsewhere := t.TempDir()
	for _, p := range []string{filepath.Join(elsewhere, bankFile.Name()), filepath.Join(elsewhere, "config", bankFile.Name())} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(bankText(fixedYAML("elsewhere", "no"))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(elsewhere)
	l, err := s.File()
	if err != nil || l.Bank.Entries[0].ID != "anchored" || slices.ContainsFunc(l.Bank.Entries, func(e match.Entry) bool { return e.ID == "elsewhere" }) {
		t.Fatalf("%+v %v", l.Bank.Entries, err)
	}
	fresh, freshWS := newService(t)
	if created, err := fresh.Seed(); err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	if _, err := os.Stat(freshWS.Path(bankFile)); err != nil {
		t.Fatalf("the seed is not in the workspace: %v", err)
	}
	if left, _ := os.ReadDir(elsewhere); len(left) != 2 {
		t.Fatalf("the seed wrote into the working directory: %v", left)
	}
}

func TestSave_MalformedExistingFileUntouched(t *testing.T) {
	good := match.Bank{Version: 1, Entries: []match.Entry{{ID: "a", Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: "x"}}}}
	for name, broken := range map[string]string{
		"syntax error":    "version: 1\nentries: [\n",
		"not a mapping":   "- a\n- b\n",
		"two documents":   "version: 1\n---\nversion: 1\n",
		"alias":           "version: 1\nentries: &x []\noverrides: *x\n",
		"scalar document": "just text\n",
	} {
		t.Run(name, func(t *testing.T) {
			s, ws := newService(t)
			writeFile(t, ws, bankFile, broken)
			_, err := s.Save(good, "")
			var le *LoadError
			if !errors.As(err, &le) {
				t.Fatalf("err = %v", err)
			}
			if got := readFile(t, ws, bankFile); got != broken {
				t.Fatalf("the file was rewritten:\n%s", got)
			}
		})
	}
	t.Run("a comment-only file is an empty bank", func(t *testing.T) {
		s, ws := newService(t)
		writeFile(t, ws, bankFile, "# nothing yet\n")
		if _, err := s.Save(good, ""); err != nil {
			t.Fatal(err)
		}
		if l := mustParse(t, readFile(t, ws, bankFile)); len(l.Bank.Entries) != 1 {
			t.Fatalf("%+v", l.Bank.Entries)
		}
	})
}

func TestSave_UnchangedBankByteIdentical(t *testing.T) {
	text := "# my bank\nversion: 1\nemployers:\n  - name: Old Corp\n    aliases: [OC]\nentries:\n  # keep this note\n  - id: a\n    description: d   # why\n" +
		"    kind: fixed\n    answer:  'one'\n  - {id: b, description: d, kind: fixed, answer: two}\noverrides: {}\n"
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, ws, bankFile); got != text {
		t.Fatalf("an unchanged save rewrote the file:\n%s", got)
	}
}

func TestSave_InteriorCommentSurvivesAFieldEdit(t *testing.T) {
	text := "version: 1\nemployers: []\nentries:\n  - id: a\n    description: d\n    # why this answer\n    kind: fixed\n" +
		"    answer: one   # the old value\n    category: other   # grouping\n  - id: b\n    description: d\n    kind: fixed\n    answer: two\noverrides: {}\n"
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	l.Bank.Entries[0].Answer = "changed"
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	for _, want := range []string{"# why this answer", "# the old value", "# grouping", "answer: changed"} {
		if !strings.Contains(saved, want) {
			t.Errorf("the save lost %q:\n%s", want, saved)
		}
	}
	if strings.Contains(saved, "answer: one") {
		t.Fatalf("the old value stayed:\n%s", saved)
	}
	if again, err := s.File(); err != nil || again.Bank.Entries[0].Answer != "changed" || again.Bank.Entries[1].Answer != "two" {
		t.Fatalf("%+v %v", again.Bank.Entries, err)
	}
}

// A value that reads like a later field's name ("description: answer") does
// not hide that field: its comments stay through an edit.
func TestSave_CommentSurvivesWhenAnEarlierValueIsAFieldName(t *testing.T) {
	text := "version: 1\nemployers: []\nentries:\n  - id: a\n    description: answer\n    kind: fixed\n" +
		"    # why this answer\n    answer: one   # the old value\n    category: other\noverrides: {}\n"
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	l.Bank.Entries[0].Answer = "changed"
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	for _, want := range []string{"# why this answer", "# the old value", "answer: changed"} {
		if !strings.Contains(saved, want) {
			t.Errorf("the save lost %q:\n%s", want, saved)
		}
	}
}

func TestLoad_CreatedByVocabulary(t *testing.T) {
	r := mustParse(t, bankText(
		fixedYAML("by-agent", "x", "created_by: agent", "pinned: true", "starter: true", "allowed_options_file: lists/fields.txt", "value_kind: text"),
		fixedYAML("by-user", "x", "created_by: user"),
		fixedYAML("by-other", "x", "created_by: robot"),
		fixedYAML("by-none", "x")))
	agent, _ := r.Bank.Find("by-agent")
	user, _ := r.Bank.Find("by-user")
	other, _ := r.Bank.Find("by-other")
	none, _ := r.Bank.Find("by-none")
	if agent.EffectiveCreatedBy() != match.CreatedByAgent || !agent.Pinned || user.EffectiveCreatedBy() != match.CreatedByUser ||
		other.EffectiveCreatedBy() != match.CreatedByUser || none.EffectiveCreatedBy() != match.CreatedByUser {
		t.Fatalf("%+v %+v %+v", agent.Meta, other.Meta, none.Meta)
	}
	if !hasWarning(r, `"by-other"`, "created_by") || !hasWarning(r, `"by-agent"`, "starter") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	// A hand-copied key of the old format is a warning and is ignored: no list
	// file is read and the entry still loads.
	if !hasWarning(r, `"by-agent"`, "allowed_options_file") || !hasWarning(r, `"by-agent"`, "value_kind") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

func TestMostRecentEmployment(t *testing.T) {
	row := func(employer, start, end string) workspace.Employment {
		return workspace.Employment{Employer: employer, Title: employer, Start: start, End: end}
	}
	tests := []struct {
		name string
		rows []workspace.Employment
		want string
	}{
		{"a current role beats a later-ending past role", []workspace.Employment{row("past", "2022-01", "2024-12"), row("current", "2019-01", "present")}, "current"},
		{"past roles compare by the later of end and start", []workspace.Employment{row("older", "2015-01", "2018-06"), row("newer", "2019-03", "2020-02")}, "newer"},
		{"the end month decides", []workspace.Employment{row("short", "2019-01", "2019-03"), row("long", "2017-01", "2019-09")}, "long"},
		{"the first of equals wins", []workspace.Employment{row("first", "2019-01", "2020-01"), row("second", "2019-01", "2020-01")}, "first"},
		{"a role without dates ranks last", []workspace.Employment{row("undated", "", ""), row("dated", "2001-01", "2002-01")}, "dated"},
		{"two current roles: the later start", []workspace.Employment{row("a", "2018-01", "present"), row("b", "2021-01", "present")}, "b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mostRecentEmployment(tc.rows)
			if !ok || got.Employer != tc.want {
				t.Fatalf("got %q, want %q", got.Employer, tc.want)
			}
		})
	}
	if _, ok := mostRecentEmployment(nil); ok {
		t.Fatal("no rows, no role")
	}
}

func TestPreferences_RelocationPolicyRead(t *testing.T) {
	read := func(block string) *match.RelocationPolicy {
		p, _ := workspace.ParseProfile([]byte("version: 1\ncandidate:\n  address:\n    city: Exampleville\n    county: Sample County\n    region: SC\n    country: US\n" + block))
		return preferencesFrom(p).Relocation
	}
	if read("") != nil {
		t.Fatal("an absent block is no policy")
	}
	got := read("preferences:\n  relocation:\n    default: closed\n    places: [' Lisbon ', Oslo, lisbon, '']\n    excluded: [Paris, '', paris, Rome]\n")
	if got == nil || got.Default != match.RelocationClosed || !slices.Equal(got.Places, []string{"Lisbon", "Oslo"}) ||
		!slices.Equal(got.Excluded, []string{"Paris", "Rome"}) {
		t.Fatalf("%+v", got)
	}
	if got.Home != (match.Home{City: "Exampleville", County: "Sample County", Region: "SC", Country: "US"}) {
		t.Fatalf("home = %+v", got.Home)
	}
	if d := read("preferences:\n  relocation:\n    places: [Oslo]\n").Default; d != match.RelocationOpen {
		t.Fatalf("an unstated default is open, got %q", d)
	}
}

func TestPreferences_ProfileEditTakesEffectOnTheNextRead(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, "version: 1\npreferences:\n  max_days_in_office: 2\n")
	writeFile(t, ws, bankFile, bankText(fixedYAML(match.IDOfficeAttendanceOK, "'Yes'", "numeric_threshold: {extractor: days_per_week_onsite}")))
	days := func() int {
		l, err := s.Bank()
		if err != nil {
			t.Fatal(err)
		}
		e, _ := l.Bank.Find(match.IDOfficeAttendanceOK)
		return *e.NumericThreshold.MaxValue
	}
	if got := days(); got != 2 {
		t.Fatalf("days = %d", got)
	}
	writeFile(t, ws, workspace.FileProfile, "version: 1\npreferences:\n  max_days_in_office: 4\n")
	if got := days(); got != 4 {
		t.Fatalf("a profile edit needed a restart: days = %d", got)
	}
}

func TestDefaultBank_StatedIDsExistWithTheirKind(t *testing.T) {
	b := defaultBank(t)
	full, rep := workspace.ParseProfile([]byte(`version: 1
candidate:
  full_name: Alex Example
  preferred_name: Alex
  email: alex@example.com
  phone: "+1 555 0100"
  address: {line1: 1 Example St, line2: Unit 2, city: Durham, county: Durham County, region: NC, postal_code: "27701", country: US}
links: {linkedin: "https://www.linkedin.com/in/alex", github: "https://github.com/alex", portfolio: "https://alex.example.com"}
work_authorization: {visa_status: "work_visa: H-1B", authorized_countries: [US], requires_sponsorship_now: true, requires_sponsorship_future: true, security_clearance: none}
targets:
  compensation: {currency: USD, period: year, minimum: 1, target: 2}
history:
  employment:
    - {employer: Example Corp, title: Engineer, start: "2021-07", end: present}
  education:
    - {school: Example University, degree: B.S., start: "2015-08", end: "2019-05", grade_average: "3.8"}
`))
	if !rep.OK() {
		t.Fatal(rep)
	}
	stated := statementsFrom(full, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	if len(stated) < 39 {
		t.Fatalf("only %d statements from a full profile", len(stated))
	}
	for _, st := range stated {
		if e, ok := b.Find(st.ID); !ok || e.Kind != st.Kind {
			t.Errorf("stated entry %q is missing or not %s", st.ID, st.Kind)
		}
	}
}

// The matcher refuses some entries by the shape of their id inside an
// education or work section, so the shipped ids must keep those shapes.
func TestDefaultBank_IDShapesTheMatcherKeysOn(t *testing.T) {
	today := match.Date{Year: 2031, Month: 1, Day: 1}
	cases := []struct{ id, question string }{
		{"address-street", "Street address"},
		{"email-address", "Email address"},
		{"phone-mobile", "Mobile phone number"},
		{"current-job-title", "Current job title"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			b := withEntry(t, c.id, func(e *match.Entry) { e.Placeholder, e.Answer = false, "Example value" })
			ask := func(path ...string) match.Result {
				return matchFacts(t, b, match.Facts{}, today, match.Question{Selector: "#q", Kind: match.KindText, InputType: match.InputText, Text: c.question, SectionPath: path})
			}
			if r := ask(); r.EntryID != c.id || r.Answer != "Example value" {
				t.Fatalf("outside a section: %+v", r)
			}
			repeated := []string{"Experience", "Employer #2"}
			if c.id != "current-job-title" {
				repeated = []string{"Education", "School #1"}
			}
			if r := ask(repeated...); r.Reason != match.ReasonSectionPolicy || !r.Terminal {
				t.Fatalf("in %v: %+v", repeated, r)
			}
		})
	}
	b := defaultBank(t)
	for _, e := range b.Entries {
		if strings.HasPrefix(e.ID, match.YearsExperiencePrefix) {
			t.Errorf("%s: the per-skill years entries belong to the user's bank", e.ID)
		}
	}
}
