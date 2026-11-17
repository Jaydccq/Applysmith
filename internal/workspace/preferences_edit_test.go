package workspace

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const preferencesProfile = `version: 1

# where I would live
preferences:
  max_days_in_office: 2   # hybrid at most
  relocation:
    default: closed
    places: [Lisbon, Oslo]

candidate:
  full_name: Alex Example
`

func writeProfile(t *testing.T, w *Workspace, body string) Content {
	t.Helper()
	saved, err := w.Write(FileProfile, []byte(body), mustRead(t, w, FileProfile).Revision)
	if err != nil {
		t.Fatal(err)
	}
	return Content{Revision: saved.Revision}
}

// The preferences the answer bank reads are edited through the same revisioned,
// comment-preserving path as every other profile key.
func TestProfilePreferencesEdit_CommentsAndOrderSurvive(t *testing.T) {
	w := newWorkspace(t)
	rev := writeProfile(t, w, preferencesProfile).Revision
	saved, err := w.Edit(FileProfile, []Op{
		{Kind: OpAppend, Path: "preferences.relocation.places", Value: "Berlin"},
		{Kind: OpSet, Path: "preferences.max_days_in_office", Value: "3"},
	}, rev)
	if err != nil || !saved.Changed {
		t.Fatalf("%v %+v", err, saved)
	}
	want := strings.NewReplacer("max_days_in_office: 2", "max_days_in_office: 3", "places: [Lisbon, Oslo]", "places: [Lisbon, Oslo, Berlin]").Replace(preferencesProfile)
	if got := string(mustRead(t, w, FileProfile).Data); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if backups, _ := backupStamps(w.BackupsDir(), "profile.yml"); len(backups) == 0 {
		t.Fatal("the previous content was not backed up")
	}
}

func TestProfilePreferencesEdit_InvalidValuesRefusedAndFileUnchanged(t *testing.T) {
	cases := map[string]struct {
		op   Op
		path string
	}{
		"days above seven": {Op{Kind: OpSet, Path: "preferences.max_days_in_office", Value: "9"}, "preferences.max_days_in_office"},
		"days not integer": {Op{Kind: OpSet, Path: "preferences.max_days_in_office", Value: "2.5"}, "preferences.max_days_in_office"},
		"place not string": {Op{Kind: OpSet, Path: "preferences.relocation.places", Value: "[Lisbon, {a: b}]"}, "preferences.relocation.places"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorkspace(t)
			rev := writeProfile(t, w, preferencesProfile).Revision
			_, err := w.Edit(FileProfile, []Op{c.op}, rev)
			var ie *InvalidError
			if !errors.As(err, &ie) || !strings.HasPrefix(ie.Report.Issues[0].Path, c.path) {
				t.Fatalf("err = %v", err)
			}
			if string(mustRead(t, w, FileProfile).Data) != preferencesProfile {
				t.Fatal("a refused edit changed the file")
			}
		})
	}
}

func TestProfilePreferencesEdit_NullClearsAndMalformedFileIsNotClobbered(t *testing.T) {
	w := newWorkspace(t)
	rev := writeProfile(t, w, preferencesProfile).Revision
	if _, err := w.Edit(FileProfile, []Op{{Kind: OpSet, Path: "preferences.max_days_in_office", Value: ""}}, rev); err != nil {
		t.Fatal(err)
	}
	p, rep := ParseProfile(mustRead(t, w, FileProfile).Data)
	if !rep.OK() || p.Preferences.MaxDaysInOffice != nil {
		t.Fatalf("the ceiling stayed: %v %s", p.Preferences.MaxDaysInOffice, rep)
	}

	broken := "version: 1\npreferences: [\n"
	writeProfile(t, w, preferencesProfile)
	// A file edited by hand into a broken state, behind the workspace's back.
	if err := os.WriteFile(w.Path(FileProfile), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Edit(FileProfile, []Op{{Kind: OpSet, Path: "preferences.max_days_in_office", Value: "4"}}, mustRead(t, w, FileProfile).Revision); err == nil {
		t.Fatal("an edit over a malformed file must fail")
	}
	if got := string(mustRead(t, w, FileProfile).Data); got != broken {
		t.Fatalf("the malformed file was rewritten: %q", got)
	}
}
