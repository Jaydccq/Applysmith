//go:build browser

package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/gates"
)

const oracleApplicant = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 555 0100"},
"history":{"employment":[
{"employer":"Example Labs","title":"Staff Engineer","summary":"Built the platform.","startDate":"2021-03","current":true},
{"employer":"Sample Works","title":"Engineer","startDate":"2017-06","endDate":"2021-02","current":false},
{"employer":"","title":"Intern","startDate":"2016-06","endDate":"2016-08","current":false}],
"education":[{"school":"Example University","degree":"Bachelor of Science","field":"Computer Science","startDate":"2013-09","endDate":"2017-05","inProgress":false}]}}`

func TestE2E_Oracle_WorkEducationRowsAddedOnceAndSaved(t *testing.T) {
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	b := st.add(&board{kind: "oracle", marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))})
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	g.runner.d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(oracleApplicant), nil }
	url := st.postingURL(b)
	t.Cleanup(func() {
		for _, ti := range lane.Tabs() {
			if ti.ID != shared.anchor {
				_ = lane.CloseTab(context.Background(), ti.ID)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	run := g.runner.RunOne(ctx, Input{URL: url, Caller: gates.CallerCLI})
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v", run.Outcome)
	}
	events := strings.Join(b.eventList(), "\n")
	// Exactly one experience row (the one not already on the timeline, and
	// not the one with no employer) and one education row, each saved once.
	if n := strings.Count(events, "save:exp:"); n != 1 || !strings.Contains(events, "save:exp:Example Labs:Staff Engineer:March 2021:current:Built the platform.") {
		t.Fatalf("experience saves %d\n%s", n, events)
	}
	if n := strings.Count(events, "save:edu:"); n != 1 || !strings.Contains(events, "save:edu:Example University:Computer Science:Computer Science:September 2013-May 2017") {
		t.Fatalf("education saves %d\n%s", n, events)
	}
	if strings.Contains(events, "Sample Works") {
		t.Fatalf("a row already on the timeline was added again\n%s", events)
	}
	// The External pill by keyword; the Degree pill is the last choice before Save.
	list := b.eventList()
	eduSave, degree, school := -1, -1, -1
	for i, e := range list {
		switch {
		case strings.HasPrefix(e, "save:edu"):
			eduSave = i
		case e == "pill:Bachelor's Degree":
			degree = i
		case strings.HasPrefix(e, "school:"):
			school = i
		}
	}
	if !strings.Contains(events, "pill:External (Outside Example Co)") || degree < 0 || school < 0 || !(school < degree && degree < eduSave) {
		t.Fatalf("order school=%d degree=%d save=%d\n%s", school, degree, eduSave, events)
	}
	if g.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'control_press' AND run_id = ? AND code = 'ok'`, run.ID) < 4 {
		t.Fatal("the add buttons and the Save presses were not audited through the gate")
	}
	if !strings.Contains(strings.Join(run.Pages[0].Notes, " "), "no employer was skipped") {
		t.Fatalf("notes %v", run.Pages[0].Notes)
	}
}
