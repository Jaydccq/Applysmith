package answerbank

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

const bankFile = workspace.FileAnswerBank

func TestSeed_FreshWorkspaceWritesDefault(t *testing.T) {
	s, ws := newService(t)
	created, err := s.Seed()
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if got := readFile(t, ws, bankFile); got != string(DefaultBytes()) {
		t.Fatal("the seeded file must equal the embedded default bank")
	}
	if info, _ := os.Stat(ws.Path(bankFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if entries, _ := filepath.Glob(filepath.Join(ws.Root, ".answer-bank.yml.*")); len(entries) != 0 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestSeed_NeverOverwritesExisting(t *testing.T) {
	for name, content := range map[string]string{
		"three custom entries": bankText(fixedYAML("a", "1"), fixedYAML("b", "2"), fixedYAML("c", "3")),
		"empty file":           "",
		"previous default":     string(DefaultBytes()),
	} {
		t.Run(name, func(t *testing.T) {
			s, ws := newService(t)
			writeFile(t, ws, bankFile, content)
			created, err := s.Seed()
			if err != nil || created || readFile(t, ws, bankFile) != content {
				t.Fatalf("created = %v, err = %v, file changed = %v", created, err, readFile(t, ws, bankFile) != content)
			}
			// Seeding runs on every server start: with a bank present it writes
			// nothing, so a full or read-only disk does not stop the start.
			if err := os.Chmod(ws.Root, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(ws.Root, 0o700) })
			if created, err := s.Seed(); err != nil || created {
				t.Fatalf("unwritable folder: created = %v, err = %v", created, err)
			}
		})
	}
}

func TestLoad_MissingFileIsEmptyBank(t *testing.T) {
	s, ws := newService(t)
	l, err := s.File()
	if err != nil || l.Exists || len(l.Bank.Entries) != 0 || len(l.Warnings) != 0 {
		t.Fatalf("%+v, %v", l, err)
	}
	if _, err := os.Stat(ws.Path(bankFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a read created the file")
	}
}

// A bank file deleted while the server runs is an empty bank with no file; only
// start or setup seeds, so a read never re-creates it.
func TestLoad_DeletedFileIsEmptyBankNotReseeded(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, bankText(fixedYAML("a", "1")))
	if l, err := s.File(); err != nil || !l.Exists || l.Bank.Entries[0].ID != "a" {
		t.Fatalf("%+v, %v", l, err)
	}
	if err := os.Remove(ws.Path(bankFile)); err != nil {
		t.Fatal(err)
	}
	l, err := s.File()
	if err != nil || l.Exists || len(l.Bank.Entries) != 0 || len(l.Warnings) != 0 {
		t.Fatalf("%+v, %v", l, err)
	}
	if _, err := os.Stat(ws.Path(bankFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a read re-seeded the deleted file")
	}
}

func TestLoad_FailedReloadKeepsPreviousBank(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, bankText(fixedYAML("a", "kept")))
	if l, err := s.File(); err != nil || l.Bank.Entries[0].Answer != "kept" {
		t.Fatalf("%+v %v", l, err)
	}
	for name, broken := range map[string]string{
		"syntax":        "version: 1\nentries: [\n",
		"version 2":     "version: 2\n",
		"duplicate key": bankText("  - id: a\n    kind: fixed\n    answer: x\n    answer: y\n"),
	} {
		writeFile(t, ws, bankFile, broken)
		l, err := s.File()
		var le *LoadError
		if !errors.As(err, &le) || l.Bank.Entries[0].Answer != "kept" {
			t.Fatalf("%s: err = %v, kept = %+v", name, err, l.Bank.Entries)
		}
	}
}

func TestSave_RoundTripOfEveryKind(t *testing.T) {
	no := false
	b := match.Bank{Version: 1, Employers: []match.Employer{{Name: "Example Corp", Aliases: []string{"Example"}}}, Entries: []match.Entry{
		{ID: "f", Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: "x"}},
		{ID: "p", Description: "d", Kind: match.KindPickOption, Body: match.Body{Preferred: []string{"A", "B"}, FallbackText: "A", FallbackAnyOption: true}},
		{ID: "c", Description: "d", Kind: match.KindCompensationRange, Body: match.Body{Minimum: "1", Maximum: "2", DefaultAnswer: "1", Currency: "USD"}},
		{ID: "l", Description: "d", Kind: match.KindLanguageProficiency, Body: match.Body{Language: "English", Speaking: "Fluent", Listening: "Fluent", Reading: "Fluent", Writing: "Fluent", ProficiencyOptions: []string{"Fluent"}}},
		{ID: "e", Description: "d", Kind: match.KindEmployerHistory, Body: match.Body{AnswerIfMatch: "Yes", AnswerIfNoMatch: "No", AbstainIfAmbiguous: &no}},
		{ID: "a", Description: "d", Kind: match.KindAutoAcknowledge},
		{ID: "m", Description: "d", Kind: match.KindMultiSelect, Body: match.Body{Selected: []string{}}},
		{ID: "m2", Description: "d", Kind: match.KindMultiSelect, Meta: match.Meta{Placeholder: true}},
	}}
	s, ws := newService(t)
	if _, err := s.Save(b, ""); err != nil {
		t.Fatal(err)
	}
	l := mustParse(t, readFile(t, ws, bankFile))
	if len(l.Warnings) != 0 || len(l.Bank.Entries) != len(b.Entries) {
		t.Fatalf("%v %d", l.Warnings, len(l.Bank.Entries))
	}
	for i, want := range b.Entries {
		got := l.Bank.Entries[i]
		got.Runtime = match.Runtime{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry %s:\n got %+v\nwant %+v", want.ID, got, want)
		}
	}
	if len(l.Bank.Employers) != 1 || l.Bank.Employers[0].Aliases[0] != "Example" {
		t.Fatalf("employers = %+v", l.Bank.Employers)
	}
	if strings.Contains(readFile(t, ws, bankFile), "selected: null") {
		t.Fatal("selected must not be written as null")
	}
}

func TestSave_DoesNotMaterializeEmptySelected(t *testing.T) {
	s, ws := newService(t)
	b := match.Bank{Version: 1, Entries: []match.Entry{{ID: "m", Description: "d", Kind: match.KindMultiSelect, Meta: match.Meta{Placeholder: true}}}}
	if _, err := s.Save(b, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, ws, bankFile), "selected") {
		t.Fatal("an unanswered multi-select gained a selected key")
	}
}

func TestSave_LosesNothing(t *testing.T) {
	text := bankText(fixedYAML("office-attendance-ok", "'Yes'",
		"question_negation_terms: ['\\bremote only\\b']",
		"numeric_threshold: {extractor: days_per_week_onsite, max_value_profile_key: preferences.max_days_in_office, above_answer: 'No', on_parse_miss: abstain}",
		"location_policy: {predicate: willing_to_work_at, if_true: 'Yes', if_false: 'No'}"),
		"  - id: pick\n    description: d\n    kind: pick-option\n    preferred: [A]\n    fallback_text: ''\n    fallback_any_option: true\n")
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(l.Bank, ""); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	for _, want := range []string{"question_negation_terms", "numeric_threshold", "on_parse_miss: abstain", "location_policy", "fallback_any_option: true"} {
		if !strings.Contains(saved, want) {
			t.Errorf("the saved file lost %q:\n%s", want, saved)
		}
	}
}

func TestSave_NeverPersistsRuntimeStamps(t *testing.T) {
	s, ws := newService(t)
	profile := "version: 1\npreferences:\n  max_days_in_office: 3\n  max_travel_percent: 25\n  relocation:\n    default: closed\ntargets:\n  roles:\n    - {title: Intern, seniority: intern}\n"
	writeFile(t, ws, workspace.FileProfile, profile)
	file := bankText(
		fixedYAML(match.IDOfficeAttendanceOK, "'Yes'", "numeric_threshold: {extractor: days_per_week_onsite}", "location_policy: {predicate: willing_to_work_at}"),
		"  - id: willing-to-travel\n    description: d\n    kind: pick-option\n    preferred: []\n    fallback_text: ''\n    placeholder: true\n")
	writeFile(t, ws, bankFile, file)
	stamped, err := s.Bank()
	if err != nil {
		t.Fatal(err)
	}
	office, _ := stamped.Bank.Find(match.IDOfficeAttendanceOK)
	travel, _ := stamped.Bank.Find(match.IDWillingToTravel)
	career, hasCareer := stamped.Bank.Find(match.IDDesiredCareerLevel)
	if *office.NumericThreshold.MaxValue != 3 || office.LocationPolicy.Resolved == nil || travel.Preferred[0] != "Up to 25%" || !hasCareer || career.Preferred[0] != "Entry Level" {
		t.Fatalf("the load did not stamp: %+v %+v", office.Meta, travel.Body)
	}
	// The dashboard saves back what it read.
	if _, err := s.Save(stamped.Bank, ""); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	for _, forbidden := range []string{"max_value:", "resolved", "Up to 25%", "Entry Level", "desired-career-level"} {
		if strings.Contains(saved, forbidden) {
			t.Errorf("the saved file holds %q:\n%s", forbidden, saved)
		}
	}
	if !strings.Contains(saved, "willing-to-travel") {
		t.Fatal("the user's own entry must stay")
	}
}

func TestSave_EmptyBodyNeedsPlaceholder(t *testing.T) {
	s, _ := newService(t)
	mk := func(placeholder bool) match.Bank {
		return match.Bank{Version: 1, Entries: []match.Entry{{ID: "a", Description: "d", Kind: match.KindFixed, Meta: match.Meta{Placeholder: placeholder}}}}
	}
	_, err := s.Save(mk(false), "")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Issues[0].EntryID != "a" {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Save(mk(true), ""); err != nil {
		t.Fatal(err)
	}
}

// An entry that has an answer is saved as a real one whichever writer
// answered it; a blank placeholder, or one left to the user, keeps its flag.
func TestSave_AnAnsweredEntryIsNoLongerAPlaceholder(t *testing.T) {
	text := "version: 1\nemployers: []\nentries:\n  - id: a\n    description: d\n    kind: fixed\n    placeholder: true\n    answer: \"\"\n  - id: b\n    description: d\n    kind: fixed\n    placeholder: true\n    answer: \"\"\n  - id: c\n    description: d\n    kind: fixed\n    placeholder: true\n    answer: \"\"\noverrides: {}\n"
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, _ := s.File()
	l.Bank.Entries[0].Answer = "45"
	l.Bank.Entries[2].Answer = match.AskMe
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	got, _ := s.File()
	if a, _ := got.Bank.Find("a"); a.Placeholder || a.Answer != "45" {
		t.Fatalf("answered entry: %+v", a)
	}
	if b, _ := got.Bank.Find("b"); !b.Placeholder {
		t.Fatalf("blank entry lost its flag: %+v", b)
	}
	if c, _ := got.Bank.Find("c"); !c.Placeholder {
		t.Fatalf("an entry left to the user lost its flag: %+v", c)
	}
	if strings.Count(readFile(t, ws, bankFile), "placeholder: true") != 2 {
		t.Fatalf("saved:\n%s", readFile(t, ws, bankFile))
	}
}

func TestSave_UnknownCategoryRefused(t *testing.T) {
	s, _ := newService(t)
	b := match.Bank{Version: 1, Entries: []match.Entry{{ID: "a", Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: "x"}, Meta: match.Meta{Category: "hobbies"}}}}
	_, err := s.Save(b, "")
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "identity, contact") {
		t.Fatalf("err = %v", err)
	}
}

func TestSave_RefusesDuplicateIDsAndBadFields(t *testing.T) {
	s, _ := newService(t)
	bad := 2.0
	b := match.Bank{Version: 1, Entries: []match.Entry{
		{ID: "a", Kind: match.KindFixed, Body: match.Body{Answer: "x"}},
		{ID: "a", Kind: match.KindFixed, Body: match.Body{Answer: "y"}},
		{ID: "b", Kind: match.KindFixed, Body: match.Body{Answer: "y"}, Meta: match.Meta{InputTypes: []string{"hologram"}, State: "inactive", Confidence: &bad}},
	}}
	var ve *ValidationError
	if _, err := s.Save(b, ""); !errors.As(err, &ve) || len(ve.Issues) != 4 {
		t.Fatalf("err = %v", err)
	}
}

func TestSave_LargeBankAccepted(t *testing.T) {
	s, ws := newService(t)
	b := match.Bank{Version: 1}
	for i := range 600 {
		ex := make([]string, 6)
		for j := range ex {
			ex[j] = fmt.Sprintf("Example phrasing number %d of entry %d asks about this topic in some detail", j, i)
		}
		b.Entries = append(b.Entries, match.Entry{ID: fmt.Sprintf("entry-%d", i), Description: "d", Kind: match.KindFixed,
			Body: match.Body{Answer: "x"}, Meta: match.Meta{Examples: ex}})
	}
	if _, err := s.Save(b, ""); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(ws.Path(bankFile)); info.Size() <= 256<<10 {
		t.Fatalf("the test bank is only %d bytes", info.Size())
	}
	huge := b
	huge.Entries = append([]match.Entry(nil), b.Entries[:1]...)
	huge.Entries[0].Examples = []string{strings.Repeat("x", MaxBankBytes)}
	var we *workspace.Error
	if _, err := s.Save(huge, ""); !errors.As(err, &we) || we.Code != workspace.CodePayloadTooLarge {
		t.Fatalf("err = %v", err)
	}
}

func TestSave_ConcurrentSavesNeverInterleave(t *testing.T) {
	s, ws := newService(t)
	mk := func(answer string) match.Bank {
		b := match.Bank{Version: 1}
		for i := range 40 {
			b.Entries = append(b.Entries, match.Entry{ID: fmt.Sprintf("e%d", i), Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: answer}})
		}
		return b
	}
	var wg sync.WaitGroup
	for _, answer := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Save(mk(answer), ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	l := mustParse(t, readFile(t, ws, bankFile))
	if len(l.Bank.Entries) != 40 {
		t.Fatalf("%d entries", len(l.Bank.Entries))
	}
	for _, e := range l.Bank.Entries {
		if e.Answer != l.Bank.Entries[0].Answer {
			t.Fatal("the file mixes two saves")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(ws.Root, ".answer-bank.yml.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestSave_StaleRevisionConflicts(t *testing.T) {
	s, _ := newService(t)
	b := match.Bank{Version: 1, Entries: []match.Entry{{ID: "a", Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: "x"}}}}
	if _, err := s.Save(b, ""); err != nil {
		t.Fatal(err)
	}
	b.Entries[0].Answer = "y"
	if _, err := s.Save(b, "not-the-revision"); !errors.Is(err, &workspace.Error{Code: workspace.CodeRevisionConflict}) {
		t.Fatalf("err = %v", err)
	}
}

func TestSave_CommentsSurviveAnEntryEdit(t *testing.T) {
	text := "# my bank\nversion: 1\nemployers: []\nentries:\n  # keep this note\n  - id: a\n    description: d\n    kind: fixed\n    answer: one\n  - id: b\n    description: d\n    kind: fixed\n    answer: two\noverrides: {}\n"
	s, ws := newService(t)
	writeFile(t, ws, bankFile, text)
	l, _ := s.File()
	l.Bank.Entries[1].Answer = "changed"
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	if !strings.Contains(saved, "# my bank") || !strings.Contains(saved, "# keep this note") || !strings.Contains(saved, "answer: changed") {
		t.Fatalf("saved:\n%s", saved)
	}
}

// olderDefaultsText is a bank file as an older release seeded it and the user
// kept it: a default it lacks, a list and two placeholders the release shipped
// older, and an answer and lists the user wrote.
func olderDefaultsText(t *testing.T) string {
	t.Helper()
	def, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	var mine []match.Entry
	for _, e := range def.Bank.Entries {
		switch e.ID {
		case "date-of-birth":
			continue
		case "preferred-name":
			e.Examples, e.Answer, e.Placeholder = []string{"Preferred first name"}, "Alex", false
		case "salutation":
			e.Examples, e.InputTypes = []string{"Salutation", "How should we address you"}, []string{"single_select", "long_text"}
		case "employer-history":
			e.AnswerIfMatch, e.AnswerIfNoMatch, e.Placeholder = "", "", true
		case "pronouns":
			e.Preferred, e.Placeholder, e.NeedsReview = nil, true, false
		case "currently-employed-by-this-company":
			e.AnswerIfMatch, e.AnswerIfNoMatch = "No", "No"
		}
		mine = append(mine, e)
	}
	data, err := Encode(match.Bank{Version: 1, Entries: mine})
	if err != nil {
		t.Fatal(err)
	}
	return "# my notes\n" + string(data)
}

// The shipped defaults apply at every load (flow round 11, D-1): the bank holds
// each default the file lacks, a list holding only the default's own items is
// the default's today, and a blank placeholder answers as its default ships,
// held for review when the default's is; what the user wrote stays, a shipped
// answer is not the user's own, and the file is never rewritten.
func TestFile_TheShippedDefaultsApplyAtEveryLoad(t *testing.T) {
	s, ws := newService(t)
	text := olderDefaultsText(t)
	writeFile(t, ws, bankFile, text)
	def, _ := DefaultBank()
	l, err := s.File()
	if err != nil || len(l.Warnings) != 0 || len(l.Bank.Entries) != len(def.Bank.Entries) {
		t.Fatalf("%v %v %d", err, l.Warnings, len(l.Bank.Entries))
	}
	for _, c := range []struct {
		id  string
		own bool
		ok  func(got, shipped match.Entry) bool
	}{
		{"date-of-birth", false, func(g, d match.Entry) bool {
			return reflect.DeepEqual(g.Body, d.Body) && slices.Equal(g.Examples, d.Examples)
		}},
		{"preferred-name", true, func(g, d match.Entry) bool { return slices.Equal(g.Examples, d.Examples) && g.Answer == "Alex" }},
		{"salutation", true, func(g, _ match.Entry) bool {
			return slices.Equal(g.Examples, []string{"Salutation", "How should we address you"}) && slices.Equal(g.InputTypes, []string{"single_select", "long_text"})
		}},
		{"employer-history", false, func(g, d match.Entry) bool {
			return !g.Placeholder && g.AnswerIfMatch == d.AnswerIfMatch && g.AnswerIfNoMatch == d.AnswerIfNoMatch && !g.NeedsReview
		}},
		{"pronouns", false, func(g, d match.Entry) bool {
			return !g.Placeholder && slices.Equal(g.Preferred, d.Preferred) && g.NeedsReview
		}},
		{"currently-employed-by-this-company", true, func(g, _ match.Entry) bool { return g.AnswerIfMatch == "No" }},
	} {
		t.Run(c.id, func(t *testing.T) {
			d, _ := def.Bank.Find(c.id)
			if g, ok := l.Bank.Find(c.id); !ok || !c.ok(g, d) || g.UserWritten() != c.own {
				t.Fatalf("%v %+v", ok, g)
			}
		})
	}
	if readFile(t, ws, bankFile) != text {
		t.Fatal("a load wrote the file")
	}
}

// A save writes what its writer changed (D-2): a site override on a default
// the file lacks is written while the default stays out, the bank a load gives
// saved back as it is leaves the file as it was, and an edit of a shipped
// default is the user's from then on while the defaults no edit touched stay
// out of the file.
func TestSave_WritesOnlyWhatItsWriterChanged(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, olderDefaultsText(t))
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	const site = "jobs.northwind.example"
	l.Bank.Overrides[site] = []match.Override{{ID: "date-of-birth", Fields: []string{"answer"}, Body: match.Body{Answer: "1990-04-02"}}}
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, ws, bankFile)
	if l, err = s.File(); err != nil {
		t.Fatal(err)
	}
	onSite, _ := match.Bank{Entries: l.Bank.EntriesForHost(site)}.Find("date-of-birth")
	_, held := mustParse(t, text).Bank.Find("date-of-birth")
	if _, err := s.Save(l.Bank, l.Revision); err != nil || readFile(t, ws, bankFile) != text || onSite.Answer != "1990-04-02" || held {
		t.Fatalf("the override on a default the file lacks: %v %+v\n%s", err, onSite, text)
	}
	after := l.Bank.Clone()
	for i := range after.Entries {
		switch e := &after.Entries[i]; e.ID {
		case "pronouns":
			e.NeedsReview = false
		case "date-of-birth":
			e.Answer = "1990-04-02"
		}
	}
	if _, err := s.Save(after, l.Revision); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, ws, bankFile)
	disk := mustParse(t, saved).Bank
	def, _ := DefaultBank()
	shipped, _ := def.Bank.Find("pronouns")
	pronouns, _ := disk.Find("pronouns")
	birth, _ := disk.Find("date-of-birth")
	history, _ := disk.Find("employer-history")
	name, _ := disk.Find("preferred-name")
	if !strings.HasPrefix(saved, "# my notes\n") || len(disk.Entries) != len(def.Bank.Entries) || !slices.Equal(pronouns.Preferred, shipped.Preferred) || pronouns.NeedsReview ||
		birth.Answer != "1990-04-02" || !history.Placeholder || history.AnswerIfMatch != "" || !slices.Equal(name.Examples, []string{"Preferred first name"}) {
		t.Fatalf("saved:\n%s", saved)
	}
	l, _ = s.File()
	for id, own := range map[string]bool{"pronouns": true, "date-of-birth": true, "employer-history": false} {
		if e, _ := l.Bank.Find(id); e.UserWritten() != own {
			t.Errorf("%s: the user's own = %v", id, e.UserWritten())
		}
	}
}

// Learning's stale rule counts the corrections of a shipped default as it does
// any entry's: one the file lacks is appended in its stale state, and the
// file's copy of one the load answered goes stale as the file holds it.
func TestEvolve_AShippedDefaultGoesStaleAsTheFileHoldsIt(t *testing.T) {
	s, ws := newService(t)
	s.now = func() time.Time { return time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC) }
	writeFile(t, ws, bankFile, olderDefaultsText(t))
	changed, err := s.Evolve(map[string]int{"date-of-birth": 3, "employer-history": 3})
	if err != nil || strings.Join(changed, ",") != "employer-history,date-of-birth" {
		t.Fatalf("%v %v", changed, err)
	}
	disk := mustParse(t, readFile(t, ws, bankFile)).Bank
	def, _ := DefaultBank()
	birth, _ := disk.Find("date-of-birth")
	history, _ := disk.Find("employer-history")
	if len(disk.Entries) != len(def.Bank.Entries) || birth.State != match.StateStale || history.State != match.StateStale || history.AnswerIfMatch != "" || !history.Placeholder {
		t.Fatalf("date-of-birth %+v, employer-history %+v", birth, history)
	}
}

func TestEvolve_ThirdCorrectionMakesTheEntryStale(t *testing.T) {
	s, ws := newService(t)
	s.now = func() time.Time { return time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC) }
	writeFile(t, ws, bankFile, "# note\n"+bankText(fixedYAML("a", "x"), fixedYAML("b", "y", "pinned: true"), fixedYAML("c", "z")))
	changed, err := s.Evolve(map[string]int{"a": 3, "b": 5, "c": 2})
	if err != nil || strings.Join(changed, ",") != "a" {
		t.Fatalf("%v %v", changed, err)
	}
	l, _ := s.File()
	a, _ := l.Bank.Find("a")
	b, _ := l.Bank.Find("b")
	if a.State != match.StateStale || a.StaleCorrectionCount != 3 || a.StaleAt != "2031-03-04T05:06:07Z" || b.EffectiveState() != match.StateActive {
		t.Fatalf("a = %+v, b = %+v", a.Meta, b.Meta)
	}
	if !strings.HasPrefix(readFile(t, ws, bankFile), "# note\n") {
		t.Fatal("a comment was lost")
	}
}

func TestUnanswered_UsesTheStampedBank(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, "version: 1\ntargets:\n  roles:\n    - {title: Intern, seniority: intern}\n")
	if _, err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Unanswered()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range r.Unanswered {
		if g.ID == match.IDDesiredCareerLevel {
			t.Fatal("the stamped career level is not a gap")
		}
	}
	if len(r.NeedsReview) == 0 {
		t.Fatal("the self-identification defaults need review")
	}
}
