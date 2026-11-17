package api

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/identity"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/tracker"
)

func TestCandidacyListFiltersSortsAndPages(t *testing.T) {
	h := newDomainHarness(t)
	h.addCandidacy("Acme Labs", "Platform Engineer", "https://jobs.example.com/acme/1")
	h.addCandidacy("Zeta Works", "Data Engineer", "https://jobs.example.com/zeta/1")
	h.addCandidacy("Example Corp", "Platform Engineer", "https://jobs.example.com/example/1")
	all := h.get("/api/candidacies")
	var env wire.Success[wire.CandidacyPage]
	strictInto(t, all.body, &env)
	if all.StatusCode != 200 || env.Data.Total != 3 || env.Data.Page != 1 || env.Data.PageSize != 25 || len(env.Data.Entries) != 3 {
		t.Fatalf("list: %s", all.body)
	}
	if env.Data.Entries[0].Company != "Example Corp" {
		t.Errorf("the default order is newest first, got %q first", env.Data.Entries[0].Company)
	}
	if r := h.get("/api/candidacies?company=zeta"); len(r.data()["entries"].([]any)) != 1 {
		t.Errorf("company filter: %s", r.body)
	}
}

func TestCandidacyListRejectsBadQueries(t *testing.T) {
	h := newDomainHarness(t)
	for name, path := range map[string]string{
		"page size bound": "/api/candidacies?pageSize=500",
		"bad status":      "/api/candidacies?status=hired",
	} {
		if r := h.get(path); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestSummaryCountsTheRowsByStatus(t *testing.T) {
	h := newDomainHarness(t)
	a := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	h.addCandidacy("Zeta Works", "Engineer", "https://jobs.example.com/zeta/1")
	h.patchRow("/api/candidacies/"+idOf(a), map[string]any{"status": "applied"})
	var sum wire.Success[wire.TrackerSummary]
	strictInto(t, h.get("/api/tracker/summary").body, &sum)
	if sum.Data.Counts[wire.StatusApplied] != 1 || sum.Data.Counts[wire.StatusEvaluated] != 1 || sum.Data.Total != 2 {
		t.Errorf("summary %+v", sum.Data)
	}
}

// The status constants of the wire are the tracker's table.
func TestWireStatusesMatchTheTrackerTable(t *testing.T) {
	want := []string{wire.StatusEvaluated, wire.StatusSkipped, wire.StatusApplied, wire.StatusResponded, wire.StatusInterview,
		wire.StatusOffer, wire.StatusRejected, wire.StatusDiscarded}
	var got []string
	for _, st := range tracker.Statuses() {
		got = append(got, string(st))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tracker %v, wire %v", got, want)
	}
}

// The actor is the credential, never the body.
func TestStatusChangeIsAttributedToTheCredential(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	forged := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied", "actor": "gmail"})
	if forged.StatusCode != 422 || forged.errCode() != "validation_failed" {
		t.Fatalf("a body naming an actor: %d %s", forged.StatusCode, forged.body)
	}
	done := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied", "note": "applied on the site", "on": "2026-01-01"})
	var env wire.Success[wire.StatusChangeResult]
	strictInto(t, done.body, &env)
	if done.StatusCode != 200 || !env.Data.Changed || env.Data.Correction || env.Data.Entry.Status != "applied" || env.Data.Entry.AppliedBy != "user" || env.Data.Entry.AppliedAt != "2026-01-01" {
		t.Fatalf("change: %s", done.body)
	}
	detail := h.get("/api/candidacies/" + idOf(row))
	var d wire.Success[wire.CandidacyDetail]
	strictInto(t, detail.body, &d)
	var sawChange bool
	for _, ev := range d.Data.Events {
		if ev.Kind == "status_changed" {
			sawChange = true
			if ev.Actor != "user" {
				t.Errorf("the event is attributed to %q", ev.Actor)
			}
		}
	}
	if !sawChange {
		t.Errorf("no status_changed event: %s", detail.body)
	}
}

func TestBackwardMoveIsAUserCorrectionAndTransitionsAreListed(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied"})
	back := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "evaluated"})
	if back.StatusCode != 200 || back.data()["correction"] != true {
		t.Fatalf("backward move: %d %s", back.StatusCode, back.body)
	}
	tr := h.get("/api/candidacies/" + idOf(row) + "/transitions")
	var env wire.Success[wire.CandidacyTransitions]
	strictInto(t, tr.body, &env)
	ordinary, correction := tracker.Targets(tracker.Evaluated)
	if tr.StatusCode != 200 || env.Data.Status != "evaluated" || len(env.Data.Ordinary) != len(ordinary) || len(env.Data.Correction) != len(correction) {
		t.Errorf("transitions: %s", tr.body)
	}
	if r := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "hired"}); r.StatusCode != 422 {
		t.Errorf("status hired: %d %s", r.StatusCode, r.body)
	}
	if r := h.patchRow("/api/candidacies/999", map[string]any{"status": "applied"}); r.StatusCode != 404 {
		t.Errorf("unknown row: %d %s", r.StatusCode, r.body)
	}
}

func TestStaleVersionIsARevisionConflict(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied"})
	stale := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "responded", "expectedVersion": 1})
	if stale.StatusCode != 409 || stale.errCode() != "revision_conflict" {
		t.Fatalf("%d %s", stale.StatusCode, stale.body)
	}
}

func TestAddThatMatchesAnExistingRowIsADuplicateConflict(t *testing.T) {
	h := newDomainHarness(t)
	first := h.addCandidacy("Acme Labs", "Platform Engineer", "https://jobs.example.com/acme/1")
	dup := h.post("/api/candidacies", map[string]any{"company": "Acme Labs", "role": "Platform Engineer", "url": "https://jobs.example.com/acme/1?utm_source=x"})
	if dup.StatusCode != 409 || dup.errCode() != "conflict" || dup.errReason() != wire.ReasonDuplicateCandidacy {
		t.Fatalf("%d %s", dup.StatusCode, dup.body)
	}
	details := dup.json()["error"].(map[string]any)["details"].(map[string]any)
	if details["matchId"] != first["id"] {
		t.Errorf("the conflict names row %v, want %v", details["matchId"], first["id"])
	}
	if n := h.get("/api/candidacies").data()["total"].(float64); n != 1 {
		t.Errorf("%v rows exist, want 1", n)
	}
}

func TestAddValidatesItsBody(t *testing.T) {
	h := newDomainHarness(t)
	for name, body := range map[string]map[string]any{
		"no company": {"role": "Engineer"},
		"bad url":    {"company": "A", "role": "B", "url": "javascript:alert(1)"},
	} {
		if r := h.post("/api/candidacies", body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	added := h.post("/api/candidacies", map[string]any{"company": "Acme Labs", "role": "Engineer", "status": "applied", "appliedAt": "2026-01-01"})
	if added.StatusCode != 201 || added.data()["source"] != "manual" || added.data()["appliedBy"] != "user" {
		t.Errorf("add: %d %s", added.StatusCode, added.body)
	}
	if loc := added.Header.Get("Location"); loc != "/api/candidacies/"+idOf(added.data()) {
		t.Errorf("location %q", loc)
	}
}

// A closed row leaves the default lists through the drawer's Archive button and
// comes back with Unarchive; a row in flight is refused.
func TestArchiveAndUnarchiveMoveOneRowOutOfTheDefaultList(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	path := "/api/candidacies/" + idOf(row)
	if r := h.post(path+"/archive", nil); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Fatalf("archiving a row in flight: %d %s", r.StatusCode, r.body)
	}
	if r := h.patchRow(path, map[string]any{"status": "discarded"}); r.StatusCode != 200 {
		t.Fatalf("close: %d %s", r.StatusCode, r.body)
	}
	if r := h.post(path+"/archive", nil); r.StatusCode != 200 || r.data()["archivedAt"] == "" {
		t.Fatalf("archive: %d %s", r.StatusCode, r.body)
	}
	if total := h.get("/api/candidacies").data()["total"]; total != float64(0) {
		t.Errorf("the default list still holds %v rows", total)
	}
	if total := h.get("/api/candidacies?archived=true").data()["total"]; total != float64(1) {
		t.Errorf("the archived list holds %v rows", total)
	}
	if r := h.post(path+"/unarchive", nil); r.StatusCode != 200 || r.data()["archivedAt"] != nil && r.data()["archivedAt"] != "" {
		t.Fatalf("unarchive: %d %s", r.StatusCode, r.body)
	}
	if total := h.get("/api/candidacies").data()["total"]; total != float64(1) {
		t.Errorf("the default list holds %v rows after unarchive", total)
	}
	if r := h.post("/api/candidacies/999/archive", nil); r.StatusCode != 404 {
		t.Errorf("unknown row: %d", r.StatusCode)
	}
}

func TestNotesAreCappedAndRecordedAsEvents(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	ok := h.noteRow("/api/candidacies/"+idOf(row)+"/notes", map[string]any{"text": "spoke with the recruiter"})
	if ok.StatusCode != 201 || !strings.Contains(ok.data()["notes"].(string), "spoke with the recruiter") {
		t.Fatalf("note: %d %s", ok.StatusCode, ok.body)
	}
	if r := h.noteRow("/api/candidacies/"+idOf(row)+"/notes", map[string]any{"text": strings.Repeat("n", 1001)}); r.StatusCode != 422 {
		t.Errorf("long note: %d %s", r.StatusCode, r.body)
	}
	if r := h.noteRow("/api/candidacies/999/notes", map[string]any{"text": "x"}); r.StatusCode != 404 {
		t.Errorf("unknown row: %d", r.StatusCode)
	}
}

func TestBulkStatusNeedsTheDashboardSession(t *testing.T) {
	h := newDomainHarness(t)
	a := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	b := h.addCandidacy("Zeta Works", "Engineer", "https://jobs.example.com/zeta/1")
	body := map[string]any{"ids": []int{int(a["id"].(float64)), int(b["id"].(float64))}, "status": "skipped"}
	refused := h.post("/api/candidacies/bulk-status", body)
	if refused.StatusCode != 403 || refused.errCode() != "forbidden" || refused.errReason() != wire.ReasonSessionOnly {
		t.Fatalf("the bearer token reached a session-only route: %d %s", refused.StatusCode, refused.body)
	}
	cookie := h.session()
	do := func(body map[string]any) result {
		return h.do(call{method: "POST", path: "/api/candidacies/bulk-status", cookie: cookie, contentType: "application/json",
			body: jsonBody(body), headers: map[string]string{"Origin": h.srv.Origin()}})
	}
	// All or nothing: one unknown id changes none of the rows.
	bad := do(map[string]any{"ids": []int{int(a["id"].(float64)), 9999}, "status": "skipped"})
	if bad.StatusCode != 404 || bad.errCode() != "not_found" {
		t.Fatalf("unknown id: %d %s", bad.StatusCode, bad.body)
	}
	if got := h.get("/api/candidacies/" + idOf(a)).data()["entry"].(map[string]any)["status"]; got != "evaluated" {
		t.Errorf("a failed bulk change moved row %s to %v", idOf(a), got)
	}
	ok := do(body)
	var env wire.Success[wire.BulkStatusResult]
	strictInto(t, ok.body, &env)
	if ok.StatusCode != 200 || len(env.Data.Results) != 2 || env.Data.Results[0].Entry.Status != "skipped" {
		t.Fatalf("bulk: %d %s", ok.StatusCode, ok.body)
	}
	if over := do(map[string]any{"ids": make([]int, 101), "status": "skipped"}); over.StatusCode != 422 {
		t.Errorf("101 ids: %d %s", over.StatusCode, over.body)
	}
}

// The dashboard lists the follow-ups due today and records one against the
// version it read.
func TestFollowupsDueAreListedAndRecorded(t *testing.T) {
	h := newDomainHarness(t)
	// A first follow-up is due 8 days after applying, by the harness clock.
	old := h.post("/api/candidacies", map[string]any{"company": "Acme Labs", "role": "Engineer", "status": "applied", "appliedAt": "2025-12-10"}).data()
	h.post("/api/candidacies", map[string]any{"company": "Zeta Works", "role": "Engineer", "status": "applied", "appliedAt": "2026-01-02"})
	var env wire.Success[wire.FollowupList]
	strictInto(t, h.get("/api/tracker/followups").body, &env)
	if want := (wire.FollowupSuggestion{ID: 1, Company: "Acme Labs", Role: "Engineer", Version: wire.SafeInt(old["version"].(float64)), Kind: "follow_up", Number: 1, Due: "2025-12-18"}); len(env.Data.Suggestions) != 1 || env.Data.Suggestions[0] != want {
		t.Fatalf("due follow-ups: %+v", env.Data)
	}
	path := "/api/candidacies/" + idOf(old) + "/followup"
	if r := h.post(path, map[string]any{}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Errorf("a press without the version read: %d %s", r.StatusCode, r.body)
	}
	v := old["version"]
	r := h.post(path, map[string]any{"expectedVersion": v})
	if r.StatusCode != 200 || len(h.get("/api/tracker/followups").data()["suggestions"].([]any)) != 0 {
		t.Fatalf("a recorded follow-up is still due: %d %s", r.StatusCode, r.body)
	}
	if r := h.post(path, map[string]any{"undo": true, "expectedVersion": v}); r.StatusCode != 409 || r.errCode() != "revision_conflict" {
		t.Errorf("a stale undo: %d %s", r.StatusCode, r.body)
	}
}

// The Tracker drafts a follow-up or an outreach note for the dashboard session
// from the user's choices, within the message's character limit (a LinkedIn
// connection note holds 300); nothing is recorded.
func TestMessageDraftsAreSessionOnlyAndRecordNothing(t *testing.T) {
	reply := `{"primary":{"angle":"","message":"Hi, any update on the Engineer role at Acme Labs?"},"alternatives":[{"angle":"short","message":"Any news on the Engineer role at Acme Labs?"}],"claims":[],"assumptions":["The recruiter handles it."]}`
	fake := llm.NewFake(llm.Fixture{Task: messages.Task, Contains: "Task: followup", Steps: []llm.Step{{Text: reply}}},
		llm.Fixture{Task: messages.Task, Contains: "Task: outreach", Steps: []llm.Step{{Text: strings.Replace(reply, `}],`, `},{"angle":"team","message":"Hello from a fan of Acme Labs."}],`, 1)}}})
	fake.SetGenerator(evaluate.FakeGenerator)
	h := newDomainHarness(t, func(c *Config) { c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake)) })
	row := h.post("/api/candidacies", map[string]any{"company": "Acme Labs", "role": "Engineer", "status": "applied", "appliedAt": "2025-12-10"}).data()
	for _, tc := range []struct {
		route, body, kind string
		max               int
	}{
		{"followup-draft", `{}`, messages.FollowupPing, 600},
		{"outreach-draft", `{"contact":"hiring_manager","name":"Sam"}`, messages.ContactHiringManager, 300},
	} {
		path := "/api/candidacies/" + idOf(row) + "/" + tc.route
		if r := h.post(path, json.RawMessage(tc.body)); r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
			t.Fatalf("the bearer token drafted: %d %s", r.StatusCode, r.body)
		}
		r := h.do(call{method: "POST", path: path, body: []byte(tc.body), contentType: "application/json", cookie: h.session(), headers: map[string]string{"Origin": h.srv.Origin()}})
		var env wire.Success[wire.MessageDraft]
		strictInto(t, r.body, &env)
		if d := env.Data; r.StatusCode != 200 || d.Kind != tc.kind || !strings.Contains(d.Primary.Message, "Acme Labs") || len(d.Alternatives) == 0 || d.MaxChars != tc.max {
			t.Fatalf("%s %s: %d %s", tc.route, tc.body, r.StatusCode, r.body)
		}
	}
	if r := h.do(call{method: "POST", path: "/api/candidacies/" + idOf(row) + "/outreach-draft", body: []byte(`{"contact":"friend"}`), contentType: "application/json", cookie: h.session(), headers: map[string]string{"Origin": h.srv.Origin()}}); r.StatusCode != 422 {
		t.Errorf("an unknown contact kind: %d %s", r.StatusCode, r.body)
	}
	if got := h.get("/api/tracker/followups").data()["suggestions"].([]any); len(got) != 1 {
		t.Errorf("drafting recorded a follow-up: %v", got)
	}
	// With no model routed the router's own error names a command. The page
	// gets the reason it links Settings, Models from, and no command.
	unrouted := newRoutingHarness(t)
	id := idOf(unrouted.post("/api/candidacies", map[string]any{"company": "Acme Labs", "role": "Engineer", "status": "applied"}).data())
	r := unrouted.do(call{method: "POST", path: "/api/candidacies/" + id + "/followup-draft", body: []byte(`{}`), contentType: "application/json", cookie: unrouted.session(), headers: map[string]string{"Origin": unrouted.srv.Origin()}})
	if msg, _ := r.json()["error"].(map[string]any)["message"].(string); r.errReason() != wire.ReasonNoModelRoute || msg == "" || strings.Contains(msg, "applysmith") {
		t.Errorf("unrouted follow-up: %d %s", r.StatusCode, r.body)
	}
}

func TestLookupResolvesEquivalentURLsAndNeverGuessesByName(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Platform Engineer", "https://boards.greenhouse.io/acme/jobs/4001")
	found := h.get("/api/applications/lookup?url=" + "https%3A%2F%2Fjob-boards.greenhouse.io%2Facme%2Fjobs%2F4001%3Fgh_src%3Dtracker")
	var env wire.Success[wire.ApplicationLookup]
	strictInto(t, found.body, &env)
	if found.StatusCode != 200 || !env.Data.Found || env.Data.Entry == nil || idOf(map[string]any{"id": float64(env.Data.Entry.ID)}) != idOf(row) || env.Data.IdentityKey == "" || len(env.Data.History) == 0 {
		t.Fatalf("lookup: %s", found.body)
	}
	miss := h.get("/api/applications/lookup?url=https%3A%2F%2Fother.example.com%2Fjobs%2F77")
	if miss.StatusCode != 200 || miss.data()["found"] != false {
		t.Errorf("a miss is not an error: %d %s", miss.StatusCode, miss.body)
	}
	for _, bad := range []string{"", "ftp%3A%2F%2Fexample.com%2Fx"} {
		if r := h.get("/api/applications/lookup?url=" + bad); r.StatusCode != 422 {
			t.Errorf("url %.30q: %d %s", bad, r.StatusCode, r.body)
		}
	}
	if h.get("/api/candidacies").data()["total"].(float64) != 1 {
		t.Error("a lookup created a row")
	}
}

func TestExportServesAFileWithAnExplicitType(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	h.noteRow("/api/candidacies/"+idOf(row)+"/notes", map[string]any{"text": "=HYPERLINK(\"http://x\")"})
	csv := h.get("/api/tracker/export?format=csv")
	if csv.StatusCode != 200 || csv.Header.Get("Content-Type") != "text/csv; charset=utf-8" || !strings.HasPrefix(csv.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("csv headers: %d %v", csv.StatusCode, csv.Header)
	}
	if !strings.Contains(string(csv.body), "id,date,company,role") || strings.Contains(string(csv.body), ",=HYPERLINK") {
		t.Errorf("csv body:\n%s", csv.body)
	}
	md := h.get("/api/tracker/export?format=md&status=evaluated")
	if md.StatusCode != 200 || md.Header.Get("Content-Type") != "text/markdown; charset=utf-8" || !strings.Contains(string(md.body), "| Acme Labs |") {
		t.Errorf("markdown: %d %s", md.StatusCode, md.body)
	}
	if r := h.get("/api/tracker/export?format=xlsx"); r.StatusCode != 422 {
		t.Errorf("format xlsx: %d", r.StatusCode)
	}
}

// Merge answers an undo that puts the rows back; a group the user called not
// duplicates leaves the list until that is taken back; bulk archive moves the
// closed rows and names the open ones.
func TestDuplicatesUndoAndBulkArchiveThroughTheRoutes(t *testing.T) {
	h := newDomainHarness(t)
	a := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-01-02', 'Acme Labs', 'Engineer', 'discarded', 'k:twin', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie := h.session()
	groups := func(r result) []any { return decode[map[string]any](t, r)["groups"].([]any) }
	first := groups(h.get("/api/tracker/duplicates"))
	if len(first) != 1 {
		t.Fatalf("groups: %v", first)
	}
	ids := []any{}
	for _, row := range first[0].(map[string]any)["rows"].([]any) {
		ids = append(ids, row.(map[string]any)["id"])
	}
	if g := groups(h.chat(cookie, "POST", "/api/tracker/duplicates/dismiss", map[string]any{"members": ids})); len(g) != 0 {
		t.Errorf("a dismissed group is still listed: %v", g)
	}
	if g := groups(h.chat(cookie, "POST", "/api/tracker/duplicates/dismiss", map[string]any{"members": ids, "undo": true})); len(g) != 1 {
		t.Errorf("the decision was not taken back: %v", g)
	}
	merged := decode[wire.DuplicateMergeResult](t, h.chat(cookie, "POST", "/api/tracker/duplicates/merge", map[string]any{"members": ids}))
	if merged.Undo == "" || h.get("/api/candidacies?archived=true").data()["total"] != float64(1) {
		t.Fatalf("merge: %+v", merged)
	}
	undo := map[string]any{"undo": merged.Undo}
	if r := h.chat(cookie, "POST", "/api/tracker/duplicates/merge", undo); r.StatusCode != 200 || h.get("/api/candidacies?archived=true").data()["total"] != float64(2) {
		t.Fatalf("undo: %d %s", r.StatusCode, r.body)
	}
	if r := h.chat(cookie, "POST", "/api/tracker/duplicates/merge", undo); r.StatusCode != 404 {
		t.Errorf("a second undo: %d %s", r.StatusCode, r.body)
	}
	moved := decode[wire.BulkArchiveResult](t, h.chat(cookie, "POST", "/api/candidacies/bulk-archive", map[string]any{"ids": ids, "archived": true}))
	if len(moved.Changed) != 1 || len(moved.Open) != 1 || idOf(a) != strconv.Itoa(int(moved.Open[0])) || h.get("/api/candidacies").data()["total"] != float64(1) {
		t.Errorf("bulk archive: %+v", moved)
	}
	back := decode[wire.BulkArchiveResult](t, h.chat(cookie, "POST", "/api/candidacies/bulk-archive", map[string]any{"ids": ids, "archived": false}))
	if len(back.Changed) != 1 || back.Changed[0] != moved.Changed[0] || len(back.Open) != 0 {
		t.Errorf("bulk unarchive: %+v", back)
	}
}

func postingOf(company string) identity.Posting {
	slug := strings.ToLower(strings.ReplaceAll(company, " ", "-"))
	return identity.Posting{Company: company, Role: "Engineer", URL: "https://jobs.example.com/" + slug + "/1"}
}

// patchRow changes a row's status the way the dashboard does: with the version
// it last read. A body that names its own expectedVersion is sent as it is.
func (h *harness) patchRow(path string, body map[string]any) result {
	h.t.Helper()
	return h.send("PATCH", path, withVersion(h, strings.TrimSuffix(path, "/notes"), body))
}

func (h *harness) noteRow(path string, body map[string]any) result {
	h.t.Helper()
	return h.post(path, withVersion(h, strings.TrimSuffix(path, "/notes"), body))
}

func withVersion(h *harness, rowPath string, body map[string]any) map[string]any {
	h.t.Helper()
	if _, ok := body["expectedVersion"]; ok {
		return body
	}
	out := map[string]any{"expectedVersion": 1}
	if cur := h.get(rowPath); cur.StatusCode == 200 {
		out["expectedVersion"] = cur.data()["entry"].(map[string]any)["version"]
	}
	for k, v := range body {
		out[k] = v
	}
	return out
}

func TestEditsWithoutAnExpectedVersionAreRefused(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Engineer", "https://jobs.example.com/acme/1")
	path := "/api/candidacies/" + idOf(row)
	for name, r := range map[string]result{
		"status, absent": h.send("PATCH", path, map[string]any{"status": "applied"}),
		"note, absent":   h.post(path+"/notes", map[string]any{"text": "called"}),
	} {
		if r.StatusCode != 422 || r.errCode() != "validation_failed" || !strings.Contains(string(r.body), "expectedVersion") {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if got := h.get(path).data()["entry"].(map[string]any); got["status"] != "evaluated" || got["notes"] != "" {
		t.Errorf("a refused edit changed the row: %v", got)
	}
}
