//go:build browser

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const (
	replayRunID = "run-replay-1"
	// shellRunID stopped on a page that only frames its application, as an
	// embedded board does; the frame is refused like any other request.
	shellRunID = "run-replay-2"
	teamLabel  = "Which team size do you prefer?"
	tideLabel  = "Which tide table publisher do you trust most?"
	// A Yes/No question in a react-select list, and one asked by a pair of
	// scripted Yes/No buttons: only the site's scripts open the list and
	// press the buttons.
	licenceLabel = "Is your lighthouse keeper licence endorsed for foghorn duty?"
	pilotLabel   = "Do you hold a current harbour pilot certificate?"
)

const replayProfile = `version: 1
candidate:
  full_name: Alex Example
  address:
    city: Durham
    region: NC
    country: US
`

// replayBank answers the team-size question and nothing about tide tables.
const replayBank = `version: 1
entries:
  - {id: team-size, description: "The team size the applicant prefers", kind: pick-option, category: other, input_types: [single_select], examples: ["Which team size do you prefer?"], preferred: ["Small team"], fallback_text: "Small team"}
  - {id: keeper-licence, description: "Whether the applicant needs a keeper licence", kind: fixed, category: other, input_types: [yes_no], examples: ["Is your lighthouse keeper licence endorsed for foghorn duty?"], answer: "No"}
  - {id: pilot-certificate, description: "Whether the applicant holds a pilot certificate", kind: fixed, category: other, input_types: [yes_no], examples: ["Do you hold a current harbour pilot certificate?"], answer: "No"}
`

// replaySnapshot is a stop snapshot as the apply chain writes it: the stopped
// run left a value in the tide box and pressed No on the pilot question, the
// page links a stylesheet, carries a script, an image at a path of its own
// site and a beacon on another host.
const replaySnapshot = `<!doctype html><html><head><title>Apply</title>
<script>document.title = "site code ran"</script>
<link rel="stylesheet" href="https://cdn.example.test/site.css">
</head><body onload="document.body.dataset.ran = '1'">
<form action="/jobs/42/submit" method="post">
<label for="fn">First Name</label><input id="fn" name="fn" required>
<label for="ln">Last Name</label><input id="ln" name="ln" required>
<label for="team">Which team size do you prefer?</label>
<select id="team" name="team" required><option value="">Select...</option><option>Small team</option><option>Large team</option></select>
<label for="tide">Which tide table publisher do you trust most?</label><input id="tide" name="tide" required value="Harbour Almanac Example">
<div class="select__container"><label id="lic-label" for="lic">Is your lighthouse keeper licence endorsed for foghorn duty?</label>
<div class="select-shell"><div class="select__control"><div class="select__value-container"><div class="select__placeholder">Select...</div>
<div class="select__input-container"><input class="select__input" id="lic" type="text" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-haspopup="true" aria-labelledby="lic-label" aria-required="true"></div></div>
<div class="select__indicators"><button type="button" aria-label="Toggle flyout" tabindex="-1"></button></div></div></div></div>
<img src="/pixel.png" alt=""><img src="%s/beacon" alt="">
<button type="submit">Submit application</button>
</form>
<div class="entry"><label class="_heading_q1w2e_52 _required_q1w2e_91" for="pilot">Do you hold a current harbour pilot certificate?</label>
<div class="_yesno_r4t5y_148"><button class="_option_z9x8c_32" aria-pressed="false">Yes</button><button class="_option_z9x8c_32 _active_z9x8c_57" aria-pressed="true">No</button>
<input type="checkbox" tabindex="-1" name="pilot"></div></div>
</body></html>`

// shellSnapshot is a company page whose application sits in a frame.
const shellSnapshot = `<!doctype html><html><head><title>Careers</title></head><body>
<h1>Platform Engineer</h1><iframe src="/embed/job_app?for=example" width="600" height="800"></iframe>
</body></html>`

// Two stopped runs replayed. The first: the page is served at its own address and no
// request reaches the network, neither the site nor another host (both are
// loopback servers here, which Chrome lets a loopback page reach); the bank's
// answer counts and the missing one is left even though the stopped run's
// copy holds a value for it; the list and the buttons that only the site's
// scripts drive need the live page, the pressed button the copy holds
// included; a baseline that had the missing one answered fails the
// comparison. The second, a page that frames its application, is replayed to
// the end like any other: its frames are read through the adopted tab.
func TestReplay_StopSnapshotAgainstBank(t *testing.T) {
	// Only a missing Chrome is a skip; any other failure of the replay fails.
	if _, err := browser.DiscoverChrome(context.Background(), os.Getenv); err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	var hits atomic.Int32
	count := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	site, third := httptest.NewServer(count), httptest.NewServer(count)
	defer site.Close()
	defer third.Close()
	root := replayWorkspace(t,
		stopRun{replayRunID, site.URL + "/jobs/42/apply", fmt.Sprintf(replaySnapshot, third.URL), fmt.Sprintf("check: page 1 %q: required and not answered; page 1 %q: required and not answered", teamLabel, tideLabel)},
		stopRun{shellRunID, site.URL + "/careers/42", shellSnapshot, "fill: no application form"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := replay(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 2 || res.Runs[0].Error != "" || res.Runs[1].RunID != shellRunID || res.Runs[1].Error == "panic" {
		t.Fatalf("runs %+v", res.Runs)
	}
	run := res.Runs[0]
	if n := hits.Load(); n != 0 {
		t.Fatalf("the network was reached %d time(s)", n)
	}
	// A path of the page's own site resolves against the original address.
	if !slices.Contains(run.refused, site.URL+"/pixel.png") || !slices.Contains(run.refused, third.URL+"/beacon") {
		t.Fatalf("refused %v", run.refused)
	}
	status := map[string]string{}
	for _, f := range run.Fields {
		status[f.Label] = f.Status
	}
	if status[teamLabel] != statusAnswered || !strings.HasPrefix(status[tideLabel], statusLeft) ||
		status[licenceLabel] != statusNeedsLive || status[pilotLabel] != statusNeedsLive {
		t.Fatalf("fields %+v", run.Fields)
	}
	if !slices.Equal(run.NowAnswered, []string{teamLabel}) {
		t.Fatalf("now answered %v, want the team question the run left", run.NowAnswered)
	}

	base := res
	base.Runs = []RunResult{run, res.Runs[1]}
	base.Runs[0].Fields = slices.Clone(run.Fields)
	for i, f := range base.Runs[0].Fields {
		if f.Label == tideLabel {
			base.Runs[0].Fields[i].Status = statusAnswered
		}
	}
	dir := t.TempDir()
	if _, err := finish(&bytes.Buffer{}, filepath.Join(dir, "base"), base, ""); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := finish(&out, filepath.Join(dir, "now"), res, filepath.Join(dir, "base", "results.json"))
	if err != nil || code != 1 || !strings.Contains(out.String(), "regression "+replayRunID+" \""+tideLabel+"\"") {
		t.Fatalf("code %d err %v output:\n%s", code, err, out.String())
	}
}

// A text box that bears an upload's label is the upload widget's state, which
// only the site's stylesheet hides: it needs the live page like the file
// control itself. A text box of its own the bank cannot answer stays left.
func TestClassify_UploadCompanionNeedsLivePage(t *testing.T) {
	const upload, link = "Upload resume *", "Portfolio link *"
	rows := []apply.CheckedRow{
		{Label: upload, Kind: "file", Required: true, Outcome: "skipped"},
		{Label: upload, Kind: "text", Required: true, Outcome: "abstained", Why: "abstained:no-entry"},
		{Label: link, Kind: "text", Required: true, Outcome: "abstained", Why: "abstained:no-entry"},
	}
	var v gates.ReadinessResult
	for _, l := range []string{upload, upload, link} {
		v.Failures = append(v.Failures, gates.Failure{Code: gates.CodeRequiredUnanswered, Page: 1, Label: l})
	}
	got := classify(rows, v)
	want := []Field{
		{Label: upload, Kind: "file", Status: statusNeedsLive},
		{Label: upload, Kind: "text", Status: statusNeedsLive},
		{Label: link, Kind: "text", Status: statusLeft + "abstained:no-entry"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("classify = %+v, want %+v", got, want)
	}
}

// stopRun is a stopped run's row and its stop snapshot.
type stopRun struct{ id, page, snapshot, reason string }

// replayWorkspace is a fictional workspace with stopped runs and their stop
// snapshots.
func replayWorkspace(t *testing.T, runs ...stopRun) string {
	t.Helper()
	root := t.TempDir()
	ws := workspace.Open(root)
	snaps := filepath.Join(ws.StateDir(), "apply", "snapshots")
	if err := os.MkdirAll(snaps, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{ws.Path(workspace.FileProfile): replayProfile, ws.Path(workspace.FileAnswerBank): replayBank}
	for _, r := range runs {
		files[filepath.Join(snaps, r.id+"-stop.html")] = r.snapshot
	}
	for f, body := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.Write(ctx, func(tx *sql.Tx) error {
		for _, r := range runs {
			_, err := tx.ExecContext(ctx, `INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key, ats, outcome_kind, outcome_class, outcome_reason, pages_json)
				VALUES (?, '2031-03-04T10:00:00.000Z', 'dashboard', ?, ?, 'generic', 'stopped', 'answer_gap', ?, ?)`,
				r.id, r.page, "url:"+r.id, r.reason, `[{"url":"`+r.page+`","step":1}]`)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}
