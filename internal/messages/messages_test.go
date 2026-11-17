// Clean-room: docs/provenance.md#CR-MESSAGES

package messages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func outreachFixture(reply string) llm.Fixture {
	return llm.Fixture{Task: Task, Contains: "Task: outreach", Steps: []llm.Step{{Text: reply}}}
}

func followupFixture(reply string) llm.Fixture {
	return llm.Fixture{Task: Task, Contains: "Task: followup", Steps: []llm.Step{{Text: reply}}}
}

func TestOutreach_GoldenWithFakeProvider(t *testing.T) {
	reply := replyJSON(
		"Hi Sam, I saw your team is hiring a Backend Engineer in Sampleville. I built a Go scheduling service that handles 40 clinics. Happy to send my CV if it maps to the role.",
		[]string{
			"Hi Sam, your post on on-call load stuck with me. I mentored three engineers through their first on-call rotation. Would you share how your team splits the pager?",
			"Hi Sam, I cut a nightly batch from 6 hours to 50 minutes at Example Systems. Your team runs similar pipelines. Is the Backend Engineer role still open?",
		},
		map[string][]string{
			"built a Go scheduling service that handles 40 clinics": {"F1"},
			"mentored three engineers through on-call":              {"F3"},
			"Sam wrote a post on on-call load":                      {"U1"},
			"Sam's team runs similar pipelines":                     {},
			"is open to Sampleville":                                {"F7", "X9"},
		},
		"That Sam is the recruiter for this role.")
	f := newFixture(t, "Plain words. Never say \"leverage\".", outreachFixture(reply))
	id := f.row(tracker.Applied, "2026-09-30")
	f.linkReport(id)
	d, err := f.svc.Outreach(f.ctx, OutreachRequest{Candidacy: id, Contact: Contact{Kind: ContactRecruiter, Name: "Sam Sample", Note: "Sam wrote a post on on-call load."}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.rec.reqs) != 1 || f.rec.reqs[0].Task != Task {
		t.Fatalf("one call under %s, got %d", Task, len(f.rec.reqs))
	}
	prompt := f.rec.reqs[0].Prompt
	for _, never := range []string{"alex.example@example.com", "555", "https://www.example.com", "999999", "Walk-away"} {
		if strings.Contains(prompt, never) {
			t.Errorf("the prompt holds %q; contact details and pay sections never reach a message", never)
		}
	}
	golden(t, "outreach_prompt.golden.txt", prompt)
	golden(t, "outreach.golden.txt", d.Text())
}

func TestOutreach_ChecksDropUngroundedMessages(t *testing.T) {
	reply := replyJSON(
		"Hi Sam, I grew revenue 300% at Example Systems. Happy to send my CV.",
		[]string{
			"Hi Sam, call me on +1 202 555 0142 about the Backend Engineer role. Happy to talk.",
			"Hi Sam, I am passionate about scheduling software. Happy to send my CV.",
		}, nil)
	f := newFixture(t, "", outreachFixture(reply))
	_, err := f.svc.Outreach(f.ctx, OutreachRequest{Company: "Example Corp", Role: "Backend Engineer", Contact: Contact{Kind: ContactRecruiter}})
	if !errors.Is(err, ErrNoDraft) {
		t.Fatalf("err = %v", err)
	}
	for _, why := range []string{"the number 300", "phone number", "stock phrase"} {
		if !strings.Contains(err.Error(), why) {
			t.Errorf("the refusal does not name %q: %v", why, err)
		}
	}
}

func TestOutreach_PrimaryDroppedPromotesAnAlternative(t *testing.T) {
	long := "Hi Sam, " + strings.Repeat("I build backend services in Go. ", 12)
	reply := replyJSON(long, []string{
		"Hi Sam, I built a Go scheduling service that handles 40 clinics. Happy to send my CV if it fits.",
		"Hi Sam, I mentored three engineers through on-call. How does your team run its rotation?",
	}, nil)
	f := newFixture(t, "", outreachFixture(reply))
	d, err := f.svc.Outreach(f.ctx, OutreachRequest{Company: "Example Corp", Contact: Contact{Kind: ContactHiringManager, Name: "Sam"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Primary.Message, "40 clinics") || len(d.Alternatives) != 1 || len(d.Dropped) != 1 || !strings.Contains(d.Dropped[0], "over the limit of 300") {
		t.Errorf("draft %+v", d)
	}
}

func TestOutreach_RefusesUnknownKindAndMissingCompany(t *testing.T) {
	f := newFixture(t, "")
	if _, err := f.svc.Outreach(f.ctx, OutreachRequest{Company: "Example Corp", Contact: Contact{Kind: "friend"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("kind: %v", err)
	}
	if _, err := f.svc.Outreach(f.ctx, OutreachRequest{Contact: Contact{Kind: ContactPeer}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("company: %v", err)
	}
	if _, err := f.svc.Outreach(f.ctx, OutreachRequest{Candidacy: 99, Contact: Contact{Kind: ContactPeer}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("row: %v", err)
	}
	if len(f.rec.reqs) != 0 {
		t.Errorf("a refused request calls no model: %d", len(f.rec.reqs))
	}
}

const pingReply = `Hi Sam, I applied for the Backend Engineer role on September 30. Since then I mentored three engineers through their first on-call rotation. Is there a window this week for a short call?`

func TestFollowup_GoldenRecruiterPing(t *testing.T) {
	reply := replyJSON(pingReply, []string{"Hi Sam, a short note on my Backend Engineer application from September 30. Would a 15 minute call this week work?"},
		map[string][]string{"applied on September 30": {"T1"}, "mentored three engineers": {"F3"}})
	f := newFixture(t, "", followupFixture(reply))
	id := f.row(tracker.Applied, "2026-09-30")
	d, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: id, Contact: "Sam"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "followup.golden.txt", d.Text())
	if !strings.Contains(f.rec.reqs[0].Prompt, "Kind of follow-up: recruiter_ping") {
		t.Error("an applied row gets a recruiter ping")
	}
}

func TestFollowup_KindFollowsTheRowStatus(t *testing.T) {
	for status, kind := range map[tracker.Status]string{tracker.Responded: FollowupStatus, tracker.Interview: FollowupThanks} {
		reply := replyJSON("Thanks again for the update on the Backend Engineer role. What is the next step?", nil, nil)
		f := newFixture(t, "", followupFixture(reply))
		id := f.row(status, "2026-09-30")
		d, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: id})
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != kind || !strings.Contains(f.rec.reqs[0].Prompt, "Kind of follow-up: "+kind) {
			t.Errorf("%s: kind %s", status, d.Kind)
		}
		if strings.Contains(d.Text(), "applysmith tracker followup") {
			t.Errorf("%s: only an applied row's follow-up is recorded in the cadence", status)
		}
	}
}

func TestFollowup_CadenceExhaustedIsRefusedUnlessAskedByName(t *testing.T) {
	reply := replyJSON(pingReply, nil, nil)
	f := newFixture(t, "", followupFixture(reply))
	id := f.row(tracker.Applied, "2026-09-30")
	pol := f.svc.d.Policy()
	for range 2 {
		if _, err := f.tr.RecordFollowup(f.ctx, id, tracker.ActorUser, 0, pol); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: id}); !errors.Is(err, ErrCadence) {
		t.Fatalf("err = %v", err)
	}
	d, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: id, Kind: FollowupPing})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.Notes, " "), "your call") {
		t.Errorf("notes %v", d.Notes)
	}
}

func TestFollowup_DraftingSendsAndRecordsNothing(t *testing.T) {
	f := newFixture(t, "", followupFixture(replyJSON(pingReply, nil, nil)))
	id := f.row(tracker.Applied, "2026-09-30")
	before, err := f.tr.Get(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: id}); err != nil {
		t.Fatal(err)
	}
	after, err := f.tr.Get(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) || after.Entry.Version != before.Entry.Version {
		t.Errorf("drafting changed the row: %d events before, %d after", len(before.Events), len(after.Events))
	}
}

func TestFollowup_RefusesRowsNotInFlight(t *testing.T) {
	f := newFixture(t, "")
	e, err := f.tr.Add(f.ctx, tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: e.ID}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an evaluated row: %v", err)
	}
	if _, err := f.svc.Followup(f.ctx, FollowupRequest{Candidacy: e.ID, Kind: "nudge"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown kind: %v", err)
	}
}

// A tracker event is stamped in UTC; the fact says the day it happened on
// where the user is, the day the tracker's cadence counts from.
func TestFacts_TrackerEventsAreNamedByTheirLocalDay(t *testing.T) {
	a := application{entry: tracker.Entry{Company: "Example Corp", Role: "Backend Engineer", Status: tracker.Applied, AppliedAt: "2026-11-06"},
		events: []tracker.Event{
			{ID: 1, Kind: tracker.EventStatusChanged, NewStatus: tracker.Applied, At: "2026-11-07T03:30:00Z"},
			{ID: 2, Kind: tracker.EventFollowupRecorded, At: "2026-11-14T02:00:00Z"},
		}}
	b := &factBook{}
	b.trackerFacts(a, time.FixedZone("UTC-5", -5*60*60))
	var texts []string
	for _, f := range b.list {
		texts = append(texts, f.Text)
	}
	got := strings.Join(texts, "\n")
	for _, want := range []string{"on 2026-11-06", "Status: applied since 2026-11-06", "the last on 2026-11-13"} {
		if !strings.Contains(got, want) {
			t.Errorf("facts lack %q:\n%s", want, got)
		}
	}
}

func TestCheck_DatesAreNotPhoneNumbers(t *testing.T) {
	grounded := numbersIn([]Fact{{Text: "Applied on 2026-09-30"}}, nil)
	if why := check("I applied on 2026-09-30 and would like to talk.", 600, grounded); why != "" {
		t.Errorf("a date: %s", why)
	}
	if why := check("Reach me at 202 555 0142.", 600, numbersIn(nil, []string{"202 555 0142"})); !strings.Contains(why, "phone") {
		t.Errorf("a phone number: %q", why)
	}
}

var update = flag.Bool("update", false, "rewrite the golden files")

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

var testNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

const testProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex.example@example.com
  phone: "+1 555 0100"
targets:
  roles:
    - title: Backend Engineer
      seniority: senior
  locations: [Sampleville]
preferences:
  work_modes: [remote, hybrid]
history:
  employment:
    - employer: Example Systems
      title: Senior Engineer
      start: 2021-03
      end: present
  education:
    - school: Example University
      degree: BSc
      field: Mathematics
      start: 2011-09
      end: 2015-06
skills:
  - name: Go
  - name: SQL
`

const testCV = `# Alex Example

alex.example@example.com | +1 202 555 0142 | https://www.example.com/in/alex-example

## Experience

- Built the scheduling service in Go that handles 40 clinics.
- Cut the nightly batch from 6 hours to 50 minutes by moving it to streaming.
- Mentored three engineers through their first on-call rotation.
`

const testReport = "# Example Corp - Backend Engineer\n\n## Verdict\n\nScore withheld.\n\n## Fit\n\n| Group | Requirement | Hit | Evidence |\n|---|---|---|---|\n\n" +
	"```scorecard\n" + `{"items":{"must_have":[{"id":"m1","label":"backend services in Go","hit_level":1,"evidence":"CV: scheduling service in Go"},{"id":"m2","label":"Kubernetes","hit_level":0,"evidence":""}],` +
	`"nice_to_have":[],"hidden_signals":[{"id":"h1","label":"mentoring","hit_level":0.5,"evidence":"CV: mentored three engineers"}],"profile_alignment":[],"red_flags":[]}}` + "\n```\n\n## Pay\n\nWalk-away floor 999999.\n"

type fixture struct {
	t    *testing.T
	ctx  context.Context
	root string
	st   *store.Store
	tr   *tracker.Tracker
	svc  *Service
	rec  *recorder
}

// recorder keeps every request the service sends to the model.
type recorder struct {
	inner Model
	mu    sync.Mutex
	reqs  []llm.Request
}

func (r *recorder) Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return r.inner.Structured(ctx, req, schema, out)
}

// newFixture builds a workspace with a profile, a CV and voice notes, and a
// service whose model is the fake provider answering from the fixtures.
func newFixture(t *testing.T, voice string, fixtures ...llm.Fixture) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	for name, body := range map[string]string{"profile.yml": testProfile, "cv.md": testCV} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if voice != "" {
		if err := os.WriteFile(filepath.Join(root, "voice.md"), []byte(voice), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return testNow }
	st, err := store.Open(ctx, filepath.Join(root, store.StateDir, store.DBFile), store.Options{WorkspaceID: testWorkspaceID, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tr, err := tracker.New(st, tracker.Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(testWorkspaceID, vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	router := llm.New(st, v, llm.WithFake(llm.NewFake(fixtures...)), llm.WithOfflineSimulation(), llm.WithClock(clock))
	rec := &recorder{inner: router}
	svc, err := New(Deps{Store: st, Tracker: tr, Workspace: workspace.Open(root), Model: rec, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, root: root, st: st, tr: tr, svc: svc, rec: rec}
}

// row adds a tracker row in the given status, applied on appliedAt.
func (f *fixture) row(status tracker.Status, appliedAt string) int64 {
	f.t.Helper()
	e, err := f.tr.Add(f.ctx, tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.com/backend-1", Status: tracker.Applied, AppliedAt: appliedAt})
	if err != nil {
		f.t.Fatal(err)
	}
	if status != tracker.Applied {
		if _, err := f.tr.SetStatus(f.ctx, tracker.StatusChange{ID: e.ID, To: status, Actor: tracker.ActorUser}); err != nil {
			f.t.Fatal(err)
		}
	}
	return e.ID
}

// linkReport writes a final report for the row and links it.
func (f *fixture) linkReport(id int64) {
	f.t.Helper()
	path := "reports/0001-example.md"
	if err := os.MkdirAll(filepath.Join(f.root, "reports"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(testReport), 0o644); err != nil {
		f.t.Fatal(err)
	}
	err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO report_artifacts (kind, path, status, content_hash, created_at, updated_at) VALUES ('report', ?, 'ready', 'c', 'now', 'now')`, path); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
			VALUES (1, 'final', 'Example Corp', 'Backend Engineer', '', 'k', 'final', 'apply', 'credible', 'pass', ?, 'reports/0001-example.html', 'job', 'now')`, path); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE tracker_entries SET report_number = 1 WHERE id = ?`, id)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// goldenFile holds every golden output of this package, one section per name:
// a header line "=== <name> <byte length> ===", then exactly that many bytes
// and a newline.
const goldenFile = "testdata/messages.golden.txt"

func readGolden(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(goldenFile)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for rest := string(data); rest != ""; {
		var name string
		var n int
		head, tail, ok := strings.Cut(rest, "\n")
		if _, err := fmt.Sscanf(head, "=== %s %d ===", &name, &n); !ok || err != nil || len(tail) < n+1 {
			t.Fatalf("%s: malformed section header %q", goldenFile, head)
		}
		out[name], rest = tail[:n], tail[n+1:]
	}
	return out
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	all := readGolden(t)
	if *update {
		all[name] = got
		names := make([]string, 0, len(all))
		for k := range all {
			names = append(names, k)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, k := range names {
			fmt.Fprintf(&b, "=== %s %d ===\n%s\n", k, len(all[k]), all[k])
		}
		if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, ok := all[name]
	if !ok {
		t.Fatalf("%s has no section %q (run with -update to create it)", goldenFile, name)
	}
	if want != got {
		t.Errorf("%s differs from the golden file; run with -update after checking the change.\ngot:\n%s", name, got)
	}
}

// replyJSON is a scripted model reply.
func replyJSON(primary string, alternatives []string, claims map[string][]string, assumptions ...string) string {
	type variant struct {
		Angle   string `json:"angle"`
		Message string `json:"message"`
	}
	type claim struct {
		Text  string   `json:"text"`
		Facts []string `json:"facts"`
	}
	out := struct {
		Primary      variant   `json:"primary"`
		Alternatives []variant `json:"alternatives"`
		Claims       []claim   `json:"claims"`
		Assumptions  []string  `json:"assumptions"`
	}{Primary: variant{"fit", primary}, Alternatives: []variant{}, Claims: []claim{}, Assumptions: append([]string{}, assumptions...)}
	for i, a := range alternatives {
		out.Alternatives = append(out.Alternatives, variant{[]string{"proof", "question"}[i%2], a})
	}
	for _, text := range slices.Sorted(maps.Keys(claims)) {
		out.Claims = append(out.Claims, claim{text, claims[text]})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
