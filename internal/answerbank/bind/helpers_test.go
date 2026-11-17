package bind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/kb"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// alexProfile is the synthetic applicant of the specification.
const alexProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  phone: "+1 555 0100"
  date_of_birth: "1990-04-02"
  address:
    line1: 1 Example Street
    city: Durham
    county: Durham County
    region: NC
    postal_code: "27701"
    country: US
work_authorization:
  authorized_countries: [US]
  requires_sponsorship_now: false
  visa_status: Citizen
history:
  employment:
    - employer: Example Labs
      title: Staff Engineer
      start: "2021-03"
      end: present
      location: Durham, NC
    - employer: Sample Works
      title: Engineer
      start: "2017-06"
      end: "2021-02"
  education:
    - school: Example University
      degree: BS
      field: Computer Science
      start: "2013-09"
      end: "2017-05"
      grade_average: "3.8"
skills:
  - name: Go
    years: 6
    level: expert
  - name: Rust
`

// alexSponsored also answers the future-sponsorship question, which the base
// profile leaves unset.
var alexSponsored = strings.Replace(alexProfile, "  requires_sponsorship_now: false\n", "  requires_sponsorship_now: false\n  requires_sponsorship_future: false\n", 1)

type fixture struct {
	ws   *workspace.Workspace
	st   *store.Store
	bank *answerbank.Service
	now  time.Time
}

func (f *fixture) clock() time.Time { return f.now }

func newFixture(t *testing.T, profile string) *fixture {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{ws: ws, st: st, bank: answerbank.New(ws, st), now: time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)}
	if _, err := f.bank.Seed(); err != nil {
		t.Fatal(err)
	}
	if profile != "" {
		f.write(t, workspace.FileProfile, profile)
	}
	return f
}

func (f *fixture) write(t *testing.T, file workspace.File, text string) {
	t.Helper()
	if err := os.WriteFile(f.ws.Path(file), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) binding(mod func(*Deps)) *Binding {
	d := Deps{Bank: f.bank, Workspace: f.ws, Now: f.clock}
	if mod != nil {
		mod(&d)
	}
	return New(d)
}

// recorder keeps the telemetry and the system writes it is given.
type recorder struct {
	mu     sync.Mutex
	events []learn.Event
	writes [][3]string
}

func (r *recorder) Record(_ context.Context, evs ...learn.Event) {
	r.mu.Lock()
	r.events = append(r.events, evs...)
	r.mu.Unlock()
}

func (r *recorder) RecordSystemWrites(_ context.Context, runID string, writes ...learn.SystemWrite) error {
	r.mu.Lock()
	for _, w := range writes {
		r.writes = append(r.writes, [3]string{runID, w.Selector, w.Value})
	}
	r.mu.Unlock()
	return nil
}

type fakeJobs map[string]string

func (j fakeJobs) Location(_ context.Context, url string) string  { return j[url] }
func (fakeJobs) History(context.Context) []match.RelationshipFact { return nil }

// fakeEvidence answers every search with one snippet, and reads sections as
// the user's own documents, or fails with err.
type fakeEvidence struct {
	queries  []string
	sections []kb.OwnSection
	err      error
	// facts, when set, are what every search finds.
	facts []kb.Fact
}

func (e *fakeEvidence) Search(_ context.Context, q kb.Query) (kb.Result, error) {
	e.queries = append(e.queries, q.Query)
	if e.facts != nil {
		return kb.Result{Facts: e.facts}, nil
	}
	return kb.Result{Facts: []kb.Fact{{Text: "Led a Go migration of the billing services", SourceKind: kb.KindSnippet, SourceRef: "s1", Stage: kb.StageLexical}}}, nil
}

func (e *fakeEvidence) OwnSections(context.Context) ([]kb.OwnSection, error) {
	return e.sections, e.err
}

func answerbankPatch(autoAccept *bool) answerbank.SettingsPatch {
	return answerbank.SettingsPatch{AutoAcceptLegalDeclarations: autoAccept}
}

func removeIfPresent(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
