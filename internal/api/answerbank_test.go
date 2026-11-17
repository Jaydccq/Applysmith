package api

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/notify"
	"applysmith/internal/workspace"
)

func seededBankHarness(t *testing.T) *harness {
	t.Helper()
	h := newDomainHarness(t)
	if _, err := answerbank.New(h.ws, nil).Seed(); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAnswerBankUnansweredServesTheNeedsAnswerRule(t *testing.T) {
	h := seededBankHarness(t)
	r := h.get("/api/answer-bank/unanswered")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	d := r.data()
	unanswered, _ := d["unanswered"].([]any)
	blocking, _ := d["blocking"].([]any)
	review, _ := d["needsReview"].([]any)
	if len(unanswered) == 0 || len(blocking) == 0 || len(blocking) > len(unanswered) || len(review) == 0 {
		t.Fatalf("unanswered %d, blocking %d, review %d", len(unanswered), len(blocking), len(review))
	}
	for _, g := range blocking {
		if g.(map[string]any)["blocking"] != true {
			t.Fatalf("non-blocking gap in blocking: %v", g)
		}
	}
	if _, ok := d["askMe"].([]any); !ok {
		t.Fatalf("askMe must be a list: %s", r.body)
	}
}

// A fact the work-authorization step states answers the entry that asks it:
// the entry leaves the blocking list, is listed as derived with its step, and
// the file still holds it blank, so a later change of the step is followed.
func TestAnswerBankUnansweredListsTheEntriesASetupStepAnswers(t *testing.T) {
	h := seededBankHarness(t)
	listed := func(key, id string) map[string]any {
		for _, g := range h.get("/api/answer-bank/unanswered").data()[key].([]any) {
			if m := g.(map[string]any); m["id"] == id {
				return m
			}
		}
		return nil
	}
	if listed("blocking", "work-auth-us") == nil || listed("derived", "work-auth-us") != nil {
		t.Fatal("without a profile work-auth-us must block and not be derived")
	}
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte("version: 1\ncandidate:\n  full_name: Alex Example\n  email: alex@example.com\n  address:\n    region: CA\nwork_authorization:\n  visa_status: citizen\n  authorized_countries: [US]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := listed("derived", "work-auth-us")
	if listed("blocking", "work-auth-us") != nil || d == nil || d["step"] != "work-authorization" || d["answer"] != "Yes" {
		t.Fatalf("derived %v", d)
	}
	file := h.get("/api/answer-bank").data()["bank"].(map[string]any)
	for _, e := range file["entries"].([]any) {
		if m := e.(map[string]any); (m["id"] == "work-auth-us" || m["id"] == "email-address") && m["answer"] != nil && m["answer"] != "" {
			t.Fatalf("the file view carries the derived answer: %v", m)
		}
	}
}

// The questions the waiting runs left that the bank cannot answer yet are
// listed once each, by meaning, with how many runs wait on each, and one leaves
// the list with its first answer (flow round 3). Two wordings that reach the
// blank entry their answer is written into are one question; two that reach a
// blank entry an answer about this employer is not written into stay two; a
// question the bank leaves for another reason (here a control the entry does
// not take) is not listed, and neither is one about the user's experience that
// the fill answers No since none of the user's information mentions it (F-10).
func TestAnswerBankUnanswered_ListsEachQuestionTheWaitingRunsLeftOnce(t *testing.T) {
	h := seededBankHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileCV), []byte("# Alex Example\n\nBuilt a metrics service in Go.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	answeredRun(t, h, "run_a", "", "Favourite tool", "County", "If yes, who referred you?")
	answeredRun(t, h, "run_b", "", "favourite tool?", "What county do you live in?", "County of residence", "Years with Go", "Notice period", "Who referred you?",
		"Have you worked in the maritime shipping industry?", "Do you have experience with Haskell?")
	asked := func() []string {
		var out []string
		for _, a := range decode[wire.AnswerBankUnanswered](t, h.get("/api/answer-bank/unanswered")).Asked {
			out = append(out, fmt.Sprintf("%s=%d", cmp.Or(a.EntryID, learn.Normalize(a.Question)), a.Runs))
		}
		slices.Sort(out)
		return out
	}
	answer := func(run, question, text string) {
		t.Helper()
		if r := h.post("/api/apply/runs/"+run+"/answers", wire.ApplyAnswerRequest{Question: question, Answer: text, Everywhere: true}); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	for _, step := range []struct {
		run, question, answer string
		want                  []string
	}{
		// "Notice period" in a text box is the notice-period entry, blank here.
		{"", "", "", []string{"address-county=2", "availability-notice-period=1", "favourite tool=2", "if yes who referred you=1", "who referred you=1", "years with go=1"}},
		{"run_a", "Favourite tool", "Go", []string{"address-county=2", "availability-notice-period=1", "if yes who referred you=1", "who referred you=1", "years with go=1"}},
		{"run_b", "County of residence", "Kings", []string{"availability-notice-period=1", "if yes who referred you=1", "who referred you=1", "years with go=1"}},
	} {
		if step.run != "" {
			answer(step.run, step.question, step.answer)
		}
		if got := asked(); !slices.Equal(got, step.want) {
			t.Fatalf("after %q: asked %v, want %v", step.question, got, step.want)
		}
	}
}

// A statement about this application that only the user can make is asked
// like an open question, and the answer the user gives there answers it on the
// next route; a statement the page check answers is not asked, since no entry
// would answer it (flow round 4).
func TestAnswerBankUnanswered_AStatementOnlyTheUserMakesIsAskedAndTheirAnswerAnswersIt(t *testing.T) {
	h := seededBankHarness(t)
	const own = "Can you confirm that everything in this application is true and your own, including your experience and identity?"
	radio := func(label string, options ...string) apply.Leftover {
		return apply.Leftover{Label: label + " *", Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: "radio-group", Options: options}}
	}
	waitingWith(t, h, "run_own", "answer", "", radio(own, "YES", "NO"),
		radio(`Did you start your first written answer below with the exact phrase "Northwind Example"?`, "Yes", "No"))
	asked := func() []wire.AnswerBankAsked {
		return decode[wire.AnswerBankUnanswered](t, h.get("/api/answer-bank/unanswered")).Asked
	}
	if got := asked(); len(got) != 1 || got[0].Question != own || !slices.Equal(got[0].Options, []string{"YES", "NO"}) {
		t.Fatalf("asked %+v", got)
	}
	r := h.post("/api/apply/runs/run_own/answers", wire.ApplyAnswerRequest{Question: own, Answer: "YES"})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	saved := decode[wire.ApplyAnswerSaved](t, r)
	if got := asked(); len(got) != 0 {
		t.Fatalf("still asked once answered: %+v", got)
	}
	qs := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_own")).Questions
	if i := slices.IndexFunc(qs, func(q wire.ApplyQuestion) bool { return q.Question == own }); i < 0 || qs[i].AnsweredBy != saved.EntryID || qs[i].Answer != "YES" {
		t.Fatalf("the saved entry %s does not answer it: %+v", saved.EntryID, qs)
	}
}

// An answer written into a blank entry on the Answer Bank page moves the
// waiting runs it completes, as a run's own questions form does (F-3, flow
// round 3): the one with its tab kept goes on, the one without says it can be
// retried.
func TestAnswerBankSave_AnAnswerWrittenOnThePageMovesTheRunsItCompletes(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	if _, err := answerbank.New(h.ws, nil).Seed(); err != nil {
		t.Fatal(err)
	}
	answeredRun(t, h, "run_kept", "tab-1", "County")
	answeredRun(t, h, "run_gone", "", "What county do you live in?")
	file := decode[wire.AnswerBankFile](t, h.get("/api/answer-bank"))
	at := slices.IndexFunc(file.Bank.Entries, func(e wire.AnswerBankEntry) bool { return e.ID == "address-county" })
	if at < 0 {
		t.Fatal("the default bank has no address-county entry")
	}
	file.Bank.Entries[at].Answer = "Kings"
	if r := h.put("/api/answer-bank", wire.AnswerBankSave{ExpectedRevision: file.Revision, Bank: file.Bank}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if e := waitEntry(t, h, "run_kept", func(e notify.Entry) bool { return e.State != notify.NeedsYou && e.State != notify.Working }); e.State != notify.Done {
		t.Fatalf("the run with its tab kept: %+v", e)
	}
	if e := entryOf(t, h, "run_gone"); e.State != notify.NeedsYou || e.Detail != "Every question has an answer now; retry the run to fill the form with them." {
		t.Fatalf("the run without a tab: %+v", e)
	}
}

// Confirming a relationship list on the Profile page answers what waiting runs
// left about their posting's company ("Internal Candidate", Boeing on Taleo):
// the views route a run's questions for its own company, and a profile save
// moves the runs it completes as a bank save does. A run that records no
// company has no "this company" to read, so its question stays open.
func TestProfileSave_ConfirmingAListMovesTheRunsItAnswers(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	if _, err := answerbank.New(h.ws, nil).Seed(); err != nil {
		t.Fatal(err)
	}
	internalCandidates(t, h, map[string]string{"run_boeing": "Boeing", "run_labs": "Example Labs", "run_none": ""})
	open := func(id string) []wire.ApplyQuestion {
		return decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+id)).Questions
	}
	if qs := open("run_boeing"); len(qs) != 1 || qs[0].AnsweredBy != "" {
		t.Fatalf("before the list is confirmed: %+v", qs)
	}
	markEmploymentComplete(t, h)
	for _, id := range []string{"run_boeing", "run_labs"} {
		if e := entryOf(t, h, id); e.State != notify.NeedsYou || e.Detail != applyapi.AnsweredDetail {
			t.Fatalf("%s: %+v", id, e)
		}
		if qs := open(id); len(qs) != 0 {
			t.Fatalf("%s still asks: %+v", id, qs)
		}
	}
	if e := entryOf(t, h, "run_none"); e.Detail == applyapi.AnsweredDetail || len(open("run_none")) != 1 {
		t.Fatalf("a run with no company was told: %+v", e)
	}
}

// internalCandidates writes a profile whose one job is at Example Labs and a
// waiting run per id that left Taleo's "Internal Candidate" in an application
// to the company named.
func internalCandidates(t *testing.T, h *harness, companies map[string]string) {
	t.Helper()
	profile := "version: 1\ncandidate:\n  full_name: Alex Example\nhistory:\n  employment:\n    - employer: Example Labs\n      title: Engineer\n      start: \"2021-03\"\n      end: present\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, company := range companies {
		waitingWith(t, h, id, "answer", "", apply.Leftover{Label: "Internal Candidate *", Reason: apply.ReasonNeedsUser,
			Draft: &apply.EntryDraft{Kind: "select", Options: []string{"Please select", "No", "Yes"}}})
		if err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE apply_runs SET company = ? WHERE run_id = ?`, company, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// markEmploymentComplete ticks "This list is complete" under "Companies you
// worked for" on Profile.
func markEmploymentComplete(t *testing.T, h *harness) {
	t.Helper()
	rev := h.get("/api/config/profile").data()["revision"].(string)
	if r := h.patch("/api/config/profile", map[string]any{"expectedRevision": rev,
		"ops": []map[string]any{{"op": "set", "path": "company_relationships.complete.employment", "value": "2026-01-01"}}}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// A question only a newer default answers reads as answered at once, and the
// runs waiting on it are told so once, by the server's first start with those
// defaults (flow round 11, D-1, D-4): a load applies them with no save to move
// the runs. No run goes on at start, with its tab kept or not, and the same
// defaults tell no run again.
func TestAnswerBankDefaults_TheFirstStartWithNewerDefaultsTellsTheRunsTheyAnswer(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	def, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	for i := range def.Bank.Entries {
		if e := &def.Bank.Entries[i]; e.ID == "employer-history" {
			// The older shipped copy: a placeholder still blank.
			e.AnswerIfMatch, e.AnswerIfNoMatch, e.Placeholder = "", "", true
		}
	}
	data, err := answerbank.Encode(def.Bank)
	if err != nil {
		t.Fatal(err)
	}
	profile := "version: 1\ncandidate:\n  full_name: Alex Example\nhistory:\n  employment:\n    - employer: Example Labs\n      title: Engineer\n      start: \"2021-03\"\n      end: present\n"
	for f, b := range map[workspace.File][]byte{workspace.FileAnswerBank: data, workspace.FileProfile: []byte(profile),
		workspace.FileCV: []byte("# Alex Example\n\nBuilt a metrics service in Go.\n")} {
		if err := os.WriteFile(h.ws.Path(f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const former = "Are you a former Northwind Example employee?"
	waiting := func(id, tab string) {
		waitingWith(t, h, id, "answer", tab, apply.Leftover{Label: former + " *", Reason: apply.ReasonNeedsUser,
			Draft: &apply.EntryDraft{Kind: "radio-group", Options: []string{"Yes", "No"}}})
	}
	waiting("run_kept", "tab-1")
	waiting("run_gone", "")
	if qs := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_gone")).Questions; len(qs) != 1 || qs[0].AnsweredBy != "employer-history" || qs[0].Answer != "No" {
		t.Fatalf("the run's question: %+v", qs)
	}
	// A run whose one question the fill answers No, since none of the user's
	// information mentions it (F-10), lists no question and is told the same.
	answeredRun(t, h, "run_fill", "", "Have you worked in the maritime shipping industry?")
	if qs := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_fill")).Questions; len(qs) != 0 {
		t.Fatalf("the fill-answered run's questions: %+v", qs)
	}
	// The start before these runs waited told none; one with defaults the
	// workspace has not started with tells both and records those defaults.
	if err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM workspace_meta WHERE key = ?`, defaultsMeta)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	for _, id := range []string{"run_kept", "run_gone", "run_fill"} {
		waitFor(t, id+" told", func() bool { return entryOf(t, h, id).Detail == applyapi.AnsweredDetail })
		if e := entryOf(t, h, id); e.State != notify.NeedsYou {
			t.Fatalf("%s: %+v", id, e)
		}
	}
	st := h.srv.store.Load()
	waitFor(t, "the told defaults recorded", func() bool {
		var n int
		return st.Read().QueryRow(`SELECT count(*) FROM workspace_meta WHERE key = ?`, defaultsMeta).Scan(&n) == nil && n == 1
	})
	// The pass the next start runs with the same defaults tells no other run.
	waiting("run_later", "")
	h.srv.moveForDefaults(t.Context(), st)
	if e := entryOf(t, h, "run_later"); e.Detail == applyapi.AnsweredDetail {
		t.Fatalf("the same defaults told a run again: %+v", e)
	}
}

func TestAnswerBankUnansweredWithoutABankIsEmptyAndABrokenBankIsConfigInvalid(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.get("/api/answer-bank/unanswered"); r.StatusCode != 200 || len(r.data()["unanswered"].([]any)) != 0 {
		t.Fatalf("no bank: %d %s", r.StatusCode, r.body)
	}
	if err := os.WriteFile(h.ws.Path(workspace.FileAnswerBank), []byte("version: 7\nentries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.get("/api/answer-bank/unanswered"); r.StatusCode != 422 || r.errCode() != "config_invalid" {
		t.Fatalf("broken bank: %d %s", r.StatusCode, r.body)
	}
}

func TestAnswerBankSettingsReadPatchAndRefuse(t *testing.T) {
	// The setting is explicit and round-trips independently of other settings.

	h := newDomainHarness(t)
	d := h.get("/api/answer-bank/settings").data()
	if d["llmFallbackEnabled"] != false || d["groundingMode"] != "strict" || d["autoAcceptLegalDeclarations"] != true || d["watchScans"] != false || d["allowGoogleSignIn"] != false {
		t.Fatalf("defaults = %v", d)
	}
	ok := h.send("PATCH", "/api/answer-bank/settings", map[string]any{"autoAcceptLegalDeclarations": true, "groundingMode": "flex"})
	if ok.StatusCode != 200 || ok.data()["autoAcceptLegalDeclarations"] != true || ok.data()["groundingMode"] != "flex" || ok.data()["llmFallbackEnabled"] != false {
		t.Fatalf("%d %s", ok.StatusCode, ok.body)
	}
	for name, body := range map[string]any{
		"unknown mode":  map[string]any{"groundingMode": "reckless", "llmFallbackEnabled": true},
		"unknown field": map[string]any{"dangerousMode": true},
	} {
		if r := h.send("PATCH", "/api/answer-bank/settings", body); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	after := h.get("/api/answer-bank/settings").data()
	if after["groundingMode"] != "flex" || after["llmFallbackEnabled"] != false || after["autoAcceptLegalDeclarations"] != true {
		t.Fatalf("a refused patch changed the settings: %v", after)
	}
}

func TestAnswerBankSettings_ALegalDeclarationsChangeIsAudited(t *testing.T) {
	h := newDomainHarness(t)
	count := func(code string) (n int, at string) {
		row := h.srv.store.Load().Read().QueryRow(`SELECT count(*), coalesce(max(at), '') FROM gate_decisions WHERE gate = 'legal_declarations_setting' AND code = ?`, code)
		if err := row.Scan(&n, &at); err != nil {
			t.Fatal(err)
		}
		return n, at
	}
	patch := func(v bool) {
		if r := h.send("PATCH", "/api/answer-bank/settings", map[string]any{"autoAcceptLegalDeclarations": v}); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	// The setting is on by default.
	patch(true)
	if n, _ := count("legal_setting_on"); n != 0 {
		t.Fatalf("a patch that changes nothing is not audited: %d", n)
	}
	patch(false)
	if n, at := count("legal_setting_off"); n != 1 || at == "" {
		t.Fatalf("turning it off: %d %q", n, at)
	}
	patch(false)
	if n, _ := count("legal_setting_off"); n != 1 {
		t.Fatalf("repeating the value is not a change: %d", n)
	}
	patch(true)
	if n, _ := count("legal_setting_on"); n != 1 {
		t.Fatalf("turning it on: %d", n)
	}
}

// jsonShape lists the JSON keys a type puts on the wire, with the options and
// the kind of value each carries, descending into nested structs.
func jsonShape(t reflect.Type, prefix string, out map[string]string) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			jsonShape(f.Type, prefix, out)
			continue
		}
		if name == "" {
			name = f.Name
		}
		ft := f.Type
		kind := ft.Kind().String()
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		out[prefix+name] = kind + "|" + opts
		if ft.Kind() == reflect.Struct {
			jsonShape(ft, prefix+name+".", out)
		}
	}
}

func TestAnswerBankWireTypesMirrorTheMatchTypes(t *testing.T) {
	for _, pair := range []struct {
		name         string
		match, wired any
	}{
		{"entry", match.Entry{}, wire.AnswerBankEntry{}},
		{"override", match.Override{}, wire.AnswerBankOverride{}},
		{"employer", match.Employer{}, wire.AnswerBankEmployer{}},
	} {
		got, want := map[string]string{}, map[string]string{}
		jsonShape(reflect.TypeOf(pair.wired), "", got)
		jsonShape(reflect.TypeOf(pair.match), "", want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the wire type and the match type disagree\nwire:  %v\nmatch: %v", pair.name, got, want)
		}
	}
}

func bankRevision(t *testing.T, h *harness) string {
	t.Helper()
	r := h.get("/api/answer-bank")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	return r.data()["revision"].(string)
}

func TestAnswerBankGetServesTheFileUnstamped(t *testing.T) {
	h := newDomainHarness(t)
	absent := h.get("/api/answer-bank")
	if absent.StatusCode != 200 || absent.data()["exists"] != false || absent.data()["revision"] != "absent" {
		t.Fatalf("%d %s", absent.StatusCode, absent.body)
	}
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte("version: 1\npreferences:\n  max_travel_percent: 25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := "version: 1\nemployers: []\nentries:\n" +
		"  - {id: willing-to-travel, description: d, kind: pick-option, preferred: ['Yes'], fallback_text: '', category: availability}\n" +
		"  - {id: none-chosen, description: d, kind: multi-select, selected: [], category: other}\n" +
		"  - {id: unchosen, description: d, kind: multi-select, placeholder: true, category: other}\n" +
		"overrides:\n  Jobs.Example.Test:\n    - {id: willing-to-travel, preferred: ['No']}\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileAnswerBank), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.get("/api/answer-bank")
	if r.StatusCode != 200 || r.data()["exists"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	bank := r.data()["bank"].(map[string]any)
	entries := bank["entries"].([]any)
	travel := entries[0].(map[string]any)["preferred"].([]any)
	if len(travel) != 1 || travel[0] != "Yes" {
		t.Fatalf("the profile stamp leaked into the read: %v", travel)
	}
	if sel, ok := entries[1].(map[string]any)["selected"].([]any); !ok || len(sel) != 0 {
		t.Fatalf("an empty answer must stay an empty list: %v", entries[1])
	}
	if sel, present := entries[2].(map[string]any)["selected"]; !present || sel != nil {
		t.Fatalf("an absent answer must be null: %v", entries[2])
	}
	if _, ok := bank["overrides"].(map[string]any)["Jobs.Example.Test"]; !ok {
		t.Fatalf("the host case was lost: %v", bank["overrides"])
	}
	// The shipped default ids travel with the file, so the page can say which
	// entry is back at the next load once deleted or renamed.
	def, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := r.data()["defaultIds"].([]any)
	if len(ids) != len(def.Bank.Entries) || ids[0] != def.Bank.Entries[0].ID {
		t.Fatalf("defaultIds: %d ids, want %d", len(ids), len(def.Bank.Entries))
	}
}

func TestAnswerBankPutRoundTripsAndQuotesTheRevision(t *testing.T) {
	h := seededBankHarness(t)
	rev := bankRevision(t, h)
	var file map[string]any
	if err := json.Unmarshal(h.get("/api/answer-bank").body, &file); err != nil {
		t.Fatal(err)
	}
	bank := file["data"].(map[string]any)["bank"].(map[string]any)
	first := bank["entries"].([]any)[0].(map[string]any)
	first["description"] = "edited by the test"

	if noRev := h.send("PUT", "/api/answer-bank", map[string]any{"bank": bank}); noRev.StatusCode != 422 || noRev.errCode() != "validation_failed" {
		t.Fatalf("missing revision: %d %s", noRev.StatusCode, noRev.body)
	}
	saved := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank})
	if saved.StatusCode != 200 || saved.data()["changed"] != true || saved.data()["revision"] == rev {
		t.Fatalf("%d %s", saved.StatusCode, saved.body)
	}
	first["description"] = "edited again"
	if again := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank}); again.StatusCode != 409 || again.errCode() != "revision_conflict" {
		t.Fatalf("stale revision: %d %s", again.StatusCode, again.body)
	}
	back := h.get("/api/answer-bank").data()["bank"].(map[string]any)["entries"].([]any)[0].(map[string]any)
	if back["description"] != "edited by the test" {
		t.Fatalf("the edit was not stored: %v", back)
	}
	text, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank))
	if !strings.Contains(string(text), "edited by the test") {
		t.Fatal("the file does not hold the edit")
	}
}

func TestAnswerBankPutRefusesWhatTheSaveRulesRefuse(t *testing.T) {
	h := seededBankHarness(t)
	rev := bankRevision(t, h)
	bank := func(entry map[string]any) map[string]any {
		return map[string]any{"version": 1, "employers": []any{}, "entries": []any{entry}, "overrides": map[string]any{}}
	}
	for name, entry := range map[string]map[string]any{
		"empty body without placeholder": {"id": "a", "description": "d", "kind": "fixed", "category": "other"},
	} {
		r := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank(entry)})
		if r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if bankRevision(t, h) != rev {
		t.Fatal("a refused save changed the file")
	}
	unknown := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank(map[string]any{"id": "a", "description": "d", "kind": "fixed", "answer": "x", "colour": "red"})})
	if unknown.StatusCode != 422 {
		t.Fatalf("an unknown key must be refused, not dropped: %d %s", unknown.StatusCode, unknown.body)
	}
}

// The route takes a bank over the JSON limit; the refusal past 4 MiB is
// checked on the body reader in TestReadBodyLargeClassStopsAtFourMiB.
func TestAnswerBankPutBodyClassAllowsFourMiBAndNoMore(t *testing.T) {
	h := seededBankHarness(t)
	rev := bankRevision(t, h)
	entries := make([]any, 0, 600)
	for i := 0; i < 600; i++ {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("entry-%03d", i), "description": "d", "kind": "fixed", "category": "other",
			"answer": "a", "examples": []string{strings.Repeat("q", 600), strings.Repeat("r", 600), strings.Repeat("s", 600)}})
	}
	body := map[string]any{"expectedRevision": rev, "bank": map[string]any{"version": 1, "employers": []any{}, "entries": entries, "overrides": map[string]any{}}}
	if raw := jsonBody(body); len(raw) < 1<<20 || len(raw) > 4<<20 {
		t.Fatalf("test body is %d bytes, want between 1 and 4 MiB", len(raw))
	}
	if r := h.send("PUT", "/api/answer-bank", body); r.StatusCode != 200 {
		t.Fatalf("a bank over the json limit but under 4 MiB: %d %s", r.StatusCode, r.body)
	}
}

func TestAnswerBankGetRefusesToServeABrokenFile(t *testing.T) {
	h := newDomainHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileAnswerBank), []byte("version: 1\nentries: [Hunter Two Private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.get("/api/answer-bank")
	if r.StatusCode != 422 || r.errCode() != "config_invalid" || strings.Contains(string(r.body), "Hunter") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// The server answers an over-limit body without reading it all, so the limit is
// checked on the body reader directly instead of through a client that would
// be reset mid-upload.
func TestReadBodyLargeClassStopsAtFourMiB(t *testing.T) {
	post := func(n int64, known bool, class ...Body) *apikit.Error {
		r := httptest.NewRequest("PUT", "/api/answer-bank", io.LimitReader(zeroReader{}, n))
		r.Header.Set("Content-Type", "application/json")
		if known {
			r.ContentLength = n
		} else {
			r.ContentLength = -1
		}
		_, err := readBody(httptest.NewRecorder(), r, append(class, BodyJSONLarge)[0])
		return err
	}
	if err := post(maxJSONBody+1, true, BodyJSON); err == nil || err.Code != wire.CodePayloadTooLarge {
		t.Fatalf("the ordinary json limit still holds for other routes: %v", err)
	}
	if err := post(4<<20, true); err != nil {
		t.Fatalf("exactly 4 MiB: %v", err)
	}
	for name, known := range map[string]bool{"declared length": true, "streamed": false} {
		if err := post(4<<20+1, known); err == nil || err.Code != wire.CodePayloadTooLarge {
			t.Errorf("%s over 4 MiB: %v", name, err)
		}
	}
	r := httptest.NewRequest("PUT", "/api/answer-bank", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "text/plain")
	if _, err := readBody(httptest.NewRecorder(), r, BodyJSONLarge); err == nil || err.Code != wire.CodeUnsupportedMediaType {
		t.Fatalf("content type: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A write of the bank file from outside the server (the CLI, a hand edit)
// moves the answerbank board topic, so an open Answer Bank page reads it again.
func TestAnswerBank_AWriteFromOutsideTheServerMovesTheBoardTopic(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.bankWatchEvery = 20 * time.Millisecond })
	before := h.boardRevision("answerbank")
	if err := os.WriteFile(h.ws.Path(workspace.FileAnswerBank), []byte("version: 1\nentries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(10 * time.Second); h.boardRevision("answerbank") == before; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the topic did not move")
		}
	}
}

func TestAnswerBankPutRefusesAChangedAnswerOutsideItsShape(t *testing.T) {
	h := seededBankHarness(t)
	var file map[string]any
	if err := json.Unmarshal(h.get("/api/answer-bank").body, &file); err != nil {
		t.Fatal(err)
	}
	data := file["data"].(map[string]any)
	shapes := data["shapes"].(map[string]any)
	bank := data["bank"].(map[string]any)
	var yesNo map[string]any
	for _, e := range bank["entries"].([]any) {
		e := e.(map[string]any)
		if shapes[e["id"].(string)].(map[string]any)["type"] == "yes-no" {
			yesNo = e
			break
		}
	}
	if yesNo == nil {
		t.Fatal("the bank read names no yes or no entry")
	}
	rev := bankRevision(t, h)
	yesNo["answer"], yesNo["placeholder"] = "Maybe", false
	if r := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank}); r.StatusCode != 422 || r.errCode() != "validation_failed" || !strings.Contains(string(r.body), "one of: Yes, No") {
		t.Fatalf("an answer outside its shape: %d %s", r.StatusCode, r.body)
	}
	yesNo["answer"] = "No"
	if r := h.send("PUT", "/api/answer-bank", map[string]any{"expectedRevision": rev, "bank": bank}); r.StatusCode != 200 {
		t.Fatalf("an answer that fits: %d %s", r.StatusCode, r.body)
	}
}

// The Answer Bank's transcript over HTTP: an upload stores the PDF in place of
// the one before, the read names it, a removal deletes it, and a refused
// upload is validation_failed (the store's rules and that a refusal changes
// nothing are TestTranscriptIsOnePDFThatAnUploadReplacesAndARemovalDeletes).
func TestAnswerBankTranscript_UploadReplacesReadNamesRemoveDeletes(t *testing.T) {
	h := newHarness(t)
	upload := func(name string, body []byte) result {
		return h.do(call{method: "POST", path: "/api/answer-bank/transcript?name=" + name, useToken: true, contentType: "application/pdf", body: body})
	}
	if r := h.get("/api/answer-bank/transcript"); r.StatusCode != 200 || r.data()["fileName"] != "" {
		t.Fatalf("no transcript yet: %d %s", r.StatusCode, r.body)
	}
	if r := upload("fall-term", []byte("%PDF-1.4 first")); r.StatusCode != 200 || r.data()["fileName"] != "fall-term.pdf" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	pdf := []byte("%PDF-1.7 unofficial")
	if r := upload("unofficial-transcript", pdf); r.StatusCode != 200 || r.data()["fileName"] != "unofficial-transcript.pdf" {
		t.Fatalf("replace: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/answer-bank/transcript"); r.data()["fileName"] != "unofficial-transcript.pdf" {
		t.Fatalf("read: %s", r.body)
	}
	if got, err := os.ReadFile(filepath.Join(h.ws.Root, "transcript", "unofficial-transcript.pdf")); err != nil || string(got) != string(pdf) {
		t.Fatalf("stored: %q %v", got, err)
	}
	if r := upload("notes", []byte("plain text")); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Fatalf("not a PDF: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{method: "DELETE", path: "/api/answer-bank/transcript", useToken: true}); r.StatusCode != 200 || r.data()["fileName"] != "" {
		t.Fatalf("remove: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/answer-bank/transcript"); r.data()["fileName"] != "" {
		t.Fatalf("removed, still read: %s", r.body)
	}
}
