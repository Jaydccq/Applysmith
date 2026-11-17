package apply

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/engine"
)

// engineTab is a tab whose page the engine reads: Inventory runs the real
// readInventory over the rows the fake engine page lists and the placements
// the scripted page answers, as laneTab does.
type engineTab struct {
	*scriptTab
}

func (t engineTab) Inventory(ctx context.Context, frame string, forms []FormInfo) (Inventory, error) {
	return readInventory(ctx, t.Page(), t.scriptTab, frame, forms)
}

// rigEngineTab is a page with no field facts of its own, so anything in the
// walker's inventory came from the engine rows.
func rigEngineTab(rows []engine.InspectRow, inspects *atomic.Int32) engineTab {
	st := &PageState{URL: loginURL, Title: "Apply - Platform Engineer", Text: "Platform Engineer apply", Lines: []string{"Platform Engineer"},
		Forms:    []FormInfo{{ID: "app"}},
		Controls: []Control{{I: 90, Tag: "button", Labels: []string{"Submit application"}, Form: 0}}}
	tab := newScriptTab(st)
	tab.Page().(*fakePage).rows = func() []engine.InspectRow {
		if inspects != nil {
			inspects.Add(1)
		}
		return rows
	}
	tab.placeFn = func(sels []string) []placement {
		out := make([]placement, len(sels))
		for i := range sels {
			out[i] = placement{Found: true, I: i, Tag: "input", Type: "text", Form: 0}
		}
		return out
	}
	return engineTab{tab}
}

func TestApply_Walk_InventoryComesOnlyFromEngineInspect(t *testing.T) {
	// The page probe lists no field: no enumeration code, and the state's
	// fields are never decoded from what the page returned.
	for _, banned := range []*regexp.Regexp{regexp.MustCompile(`const inputs\b`), regexp.MustCompile(`inputs\.push`), regexp.MustCompile(`tag !== "INPUT"`), regexp.MustCompile(`\.furniture|furniture:`)} {
		if banned.MatchString(probeJS) {
			t.Fatalf("probe.js still carries field enumeration or classification: %s", banned)
		}
	}
	f, _ := reflect.TypeOf(PageState{}).FieldByName("Inputs")
	if f.Tag.Get("json") != "-" {
		t.Fatalf("PageState.Inputs is decoded from the probe: tag %q", f.Tag)
	}

	var inspects atomic.Int32
	tab := rigEngineTab([]engine.InspectRow{
		{Selector: "#fn", Label: "First name", Kind: "text", Required: true},
		{Selector: "#q", Label: "Years of Go", Kind: "text", Value: "4"},
	}, &inspects)
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if n := inspects.Load(); n != 1 {
		t.Fatalf("%d inspect calls for one look, want 1", n)
	}
	if len(st.Inputs) != 2 || st.Inputs[0].Label != "First name" || st.Inputs[0].Selector != "#fn" || !st.Inputs[0].Required || st.Inputs[1].Label != "Years of Go" || !st.Inputs[1].Filled {
		t.Fatalf("inputs %+v: want the engine's two rows", st.Inputs)
	}
	if !st.HasForm() {
		t.Fatal("a page the engine lists fields on reads as having no form")
	}
	if fields, files := st.FormFields(); fields != 2 || files != 0 {
		t.Fatalf("fields %d files %d", fields, files)
	}
	if len(st.Stall.Empty) != 1 || st.Stall.Empty[0] != "First name" {
		t.Fatalf("empty required fields %v", st.Stall.Empty)
	}

	// A page the engine lists nothing on has no form, whatever the markup.
	empty := rigEngineTab(nil, nil)
	st, err = Probe(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() || len(st.Inputs) != 0 {
		t.Fatalf("inputs %+v with an empty inspect result", st.Inputs)
	}
}

func TestApply_Walk_PasswordAndAttachmentStructureKeptWithoutValuesOrBank(t *testing.T) {
	rows := []engine.InspectRow{
		{Selector: "#em", Label: "Email", Kind: "text", Required: true, Value: "alex@example.com"},
		{Selector: "#pw", Label: "Password", Kind: "text", Required: true, InspectionOnly: true},
		{Selector: "#cv", Label: "Resume", Kind: "file", Required: true, InspectionOnly: true},
	}
	tab := rigEngineTab(rows, nil)
	tab.placeFn = func(sels []string) []placement {
		return []placement{
			{Found: true, I: 0, Tag: "input", Type: "email", Form: 0, Name: "email"},
			{Found: true, I: 1, Tag: "input", Type: "password", Form: 0, Name: "pw", Filled: true},
			{Found: true, I: 2, Tag: "input", Type: "file", Form: 0},
		}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	var pw, cv *InputInfo
	for i := range st.Inputs {
		switch st.Inputs[i].Type {
		case "password":
			pw = &st.Inputs[i]
		case "file":
			cv = &st.Inputs[i]
		}
	}
	if pw == nil || cv == nil || !pw.InspectionOnly || !cv.InspectionOnly || !pw.Required {
		t.Fatalf("inputs %+v: the password box and the attachment slot keep their structure", st.Inputs)
	}
	if !pw.Filled {
		t.Fatal("a filled password box reads as empty: whether it holds text is a fact, the text is not")
	}
	if st.Forms[0].Files != 1 || !st.Forms[0].HasResume {
		t.Fatalf("form %+v: the resume slot is counted", st.Forms[0])
	}

	// A required password or attachment the walk has not filled is not a
	// leftover: only the fill's own rows go to the bank, and the reveal pass
	// skips what the engine marks inspection-only.
	l := newLadderEnv(t, fixOwn(), script("[]", "[]", "[]"), nil)
	l.page.rows = func() []engine.InspectRow {
		return append([]engine.InspectRow{{Selector: "#late", Label: "Referral name", Kind: "text", Required: true}}, rows[1:]...)
	}
	ps := &pageState{}
	l.s.reveal(context.Background(), ps)
	if len(ps.leftovers) != 1 || ps.leftovers[0].Selector != "#late" {
		t.Fatalf("leftovers %+v: only the revealed question may be open", ps.leftovers)
	}
	if n := l.model.count(taskComplete); n != 0 || len(l.page.answers) != 0 {
		t.Fatalf("%d model calls, %d writes for controls the engine never fills", n, len(l.page.answers))
	}
}

func TestApply_Walk_CompositeLookupMembersCountOnce(t *testing.T) {
	// The engine lists a combobox wrapper and the box inside it as one row
	// (engine test "a composite lookup is one row"); the inventory keeps one
	// input and one empty-required entry for it.
	tab := rigEngineTab([]engine.InspectRow{{Selector: "#team", Label: "Preferred team", Kind: "text", Required: true}}, nil)
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if fields, _ := st.FormFields(); fields != 1 || len(st.Inputs) != 1 || len(st.Stall.Empty) != 1 {
		t.Fatalf("fields %d inputs %d empty %v", fields, len(st.Inputs), st.Stall.Empty)
	}
}

func TestApply_Walk_ParserOnlyUploadStaysDistinctFromResume(t *testing.T) {
	rows := []engine.InspectRow{
		{Selector: "#auto", Label: "Autofill your application", Kind: "file", InspectionOnly: true, ParserOnly: true},
		{Selector: "#cv", Label: "Resume", Kind: "file", Required: true, InspectionOnly: true},
	}
	tab := rigEngineTab(rows, nil)
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "input", Type: "file", Form: 0}, {Found: true, I: 1, Tag: "input", Type: "file", Form: 0}}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Inputs[0].ParserOnly || st.Inputs[1].ParserOnly {
		t.Fatalf("inputs %+v", st.Inputs)
	}
	if fields, files := st.FormFields(); fields != 1 || files != 1 {
		t.Fatalf("fields %d files %d: the parser upload is a field, the resume the one attachment", fields, files)
	}
	if st.Forms[0].Files != 1 {
		t.Fatalf("form files %d", st.Forms[0].Files)
	}
	// Alone, the parser upload is no resume slot.
	only := rigEngineTab(rows[:1], nil)
	only.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "input", Type: "file", Form: 0}}
	}
	st, err = Probe(context.Background(), only)
	if err != nil {
		t.Fatal(err)
	}
	if st.Forms[0].HasResume || st.Forms[0].Files != 0 {
		t.Fatalf("form %+v", st.Forms[0])
	}
}

func TestApply_Walk_AnsweredButtonGroupRemains(t *testing.T) {
	answered := rigEngineTab([]engine.InspectRow{{Selector: "#adult", Label: "Are you 18 or older?", Kind: "radio-group", Required: true, Value: "Yes"}}, nil)
	st, err := Probe(context.Background(), answered)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Inputs) != 1 || st.Inputs[0].Type != "radio" || !st.Inputs[0].Filled || !st.Inputs[0].Checked || !st.HasForm() {
		t.Fatalf("inputs %+v: an answered group stays on the page", st.Inputs)
	}
	if len(st.Stall.Empty) != 0 {
		t.Fatalf("an answered group is listed empty: %v", st.Stall.Empty)
	}
}

func TestApply_Walk_LinkedInSummaryOnlyStepIdentityIsModalText(t *testing.T) {
	step := func(heading, text string) *PageState {
		return &PageState{URL: "https://www.linkedin.com/jobs/view/1/", Dialogs: []DialogInfo{{Heading: heading, Text: text}}}
	}
	review, contact := step("Review your application", "Review your application Alex Example"), step("Contact info", "Contact info Phone")
	if stateKey(review) == stateKey(contact) {
		t.Fatal("two summary-only steps under one address share an identity")
	}
	if stateKey(review) != stateKey(step("Review your application", "Review your application Alex Example")) {
		t.Fatal("the same step reads as two")
	}
	// With questions on the page, the questions are the identity.
	withInputs := step("Contact info", "Contact info Phone")
	withInputs.Inputs = []InputInfo{{Label: "Phone"}}
	other := step("Other", "Other text")
	other.Inputs = []InputInfo{{Label: "Phone"}}
	if stateKey(withInputs) != stateKey(other) {
		t.Fatal("modal text changes the identity of a step that asks questions")
	}
}

func TestApply_Walk_FurnitureComesFromPlacementAndFormFacts(t *testing.T) {
	rows := []engine.InspectRow{{Selector: "#kw", Label: "Keywords", Kind: "text"}, {Selector: "#loc", Label: "Location", Kind: "text"}, {Selector: "#chat", Label: "Message", Kind: "text"}}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = []FormInfo{{ID: "jobsearch", Role: "search"}}
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "input", Type: "text", Form: 0}, {Found: true, I: 1, Tag: "input", Type: "text", Form: 0}, {Found: true, I: 2, Tag: "input", Type: "text", Form: -1, Chat: true}}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Forms[0].Furniture || st.HasForm() {
		t.Fatalf("form %+v inputs %+v: a search form and a chat widget are furniture", st.Forms[0], st.Inputs)
	}
	if strings.Join(st.Stall.Empty, ",") != "" {
		t.Fatalf("empty %v", st.Stall.Empty)
	}
}

// A SuccessFactors careers page (RWE's job portal) sets its job search filters
// outside the keyword form, named optionsFacetsDD_*: they are the site's search,
// not an application form; a Country select outside any form stays a field.
func TestApply_Walk_SearchFacetsOutsideAFormAreFurniture(t *testing.T) {
	rows := []engine.InspectRow{{Selector: "#f1", Label: "Country", Kind: "select"}, {Selector: "#f2", Label: "Location", Kind: "select"}}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = nil
	name := "optionsFacetsDD_country"
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "select", Type: "select", Form: -1, Name: name, ID: name}, {Found: true, I: 1, Tag: "select", Type: "select", Form: -1, Name: "optionsFacetsDD_location"}}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: search facets are furniture", st.Inputs)
	}
	name = "country"
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "select", Type: "select", Form: -1, Name: name, ID: name}, {Found: true, I: 1, Tag: "select", Type: "select", Form: -1, Name: "state"}}
	}
	if st, err = Probe(context.Background(), tab); err != nil || !st.HasForm() {
		t.Fatalf("inputs %+v (%v): a plain Country select is a field", st.Inputs, err)
	}
}

// Teamtailor's "Already working at <employer>? Log in" referral box posts to
// /auto_join; it is site furniture, so the posting's own Apply link stays the
// opener of the page.
func TestApply_Walk_TeamtailorAutoJoinFormIsFurniture(t *testing.T) {
	rows := []engine.InspectRow{{Selector: "#full_email", Label: "Email address without domain", Kind: "text"}}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = []FormInfo{{Action: "https://example.teamtailor.com/en-GB/auto_join", Cls: "flex items-center", Text: "@example.com Log in"}}
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "input", Type: "email", Form: 0}}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Forms[0].Furniture || st.HasForm() {
		t.Fatalf("form %+v inputs %+v: the auto-join box is furniture", st.Forms[0], st.Inputs)
	}
}

// Phenom's posting page (RTX) lists each job with a "Save <title> to job cart"
// toggle, named save-<job id>, outside any form; they are the site's, so the
// posting's own Apply stays the opener of the page.
func TestApply_Walk_SaveToJobCartTogglesAreFurniture(t *testing.T) {
	rows := []engine.InspectRow{
		{Selector: "#a", Label: "SAP BTP Integration Suite Developer (Remote) Location austin, Texas Job ID: 01857907", Kind: "checkbox"},
		{Selector: "#b", Label: "Save Middleware Integration API-SOA Developer 01864993 to job cart", Kind: "checkbox"},
		{Selector: "#chat", Label: "Chatbot User Input box with Send button", Kind: "text"},
	}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = nil
	tab.placeFn = func(sels []string) []placement {
		return []placement{
			{Found: true, I: 0, Tag: "input", Type: "checkbox", Form: -1, Name: "save-RAYTGLOBAL01857907EXTERNALENGLOBAL"},
			{Found: true, I: 1, Tag: "input", Type: "checkbox", Form: -1}, // known by its label alone
			{Found: true, I: 2, Tag: "input", Type: "text", Form: -1, Chat: true},
		}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the save toggles and the chat box are furniture", st.Inputs)
	}
	// A question about keeping the applicant's details is the application's.
	tab.scriptTab.state.Forms = nil
	rows[0] = engine.InspectRow{Selector: "#a", Label: "Save my information for future applications", Kind: "checkbox"}
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "input", Type: "checkbox", Form: -1, Name: "save-info"}, {Found: true, I: 1, Tag: "input", Type: "checkbox", Form: -1, Name: "save-RAYTGLOBAL01864993EXTERNALENGLOBAL"}, {Found: true, I: 2, Tag: "input", Type: "text", Form: -1, Chat: true}}
	}
	if st, err = Probe(context.Background(), tab); err != nil || !st.HasForm() {
		t.Fatalf("err %v inputs %+v: a save-my-information box is a field", err, st.Inputs)
	}
}

// HelloFresh's Phenom page adds a sticky header toggle, "Save Job <title>",
// whose id is saveJob-<job id>: the site's too, so the page holds no form and
// its Apply Now stays the opener.
func TestApply_Walk_SaveJobHeaderToggleIsFurniture(t *testing.T) {
	rows := []engine.InspectRow{
		{Selector: "#a", Label: "Save Job WMS Software Engineer [OPERATIONS TECHNOLOGY]", Kind: "checkbox"},
		{Selector: "#b", Label: "WMS Software Engineer [OPERATIONS TECHNOLOGY]", Kind: "checkbox"},
	}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = nil
	tab.placeFn = func(sels []string) []placement {
		return []placement{
			{Found: true, I: 0, Tag: "input", Type: "checkbox", Form: -1}, // known by its label alone
			{Found: true, I: 1, Tag: "input", Type: "checkbox", Form: -1, ID: "saveJob-HELLGLOBAL8313385EXTERNALENGLOBAL"},
		}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the save toggles are furniture", st.Inputs)
	}
}

// TikTok's posting page sets its language picker in a footer drawn from plain
// <div>s, a required select listing "English" and Japanese in its own name: the site's, so the
// posting's own Apply stays the opener of the page. A language question that
// names languages in English stays a field.
func TestApply_Walk_ALanguagePickerOutsideAFormIsFurniture(t *testing.T) {
	rows := []engine.InspectRow{{Selector: "#lang", Label: "English", Kind: "select", Required: true, Value: "English"}}
	tab := rigEngineTab(rows, nil)
	tab.scriptTab.state.Forms = nil
	options := []string{"English", "\u65e5\u672c\u8a9e"}
	tab.placeFn = func(sels []string) []placement {
		return []placement{{Found: true, I: 0, Tag: "select", Type: "select", Form: -1, Options: options}}
	}
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the language picker is furniture", st.Inputs)
	}
	options = []string{"", "English", "Japanese", "Spanish"}
	if st, err = Probe(context.Background(), tab); err != nil || !st.HasForm() {
		t.Fatalf("err %v inputs %+v: a language question is a field", err, st.Inputs)
	}
}

// IBM's sign-in ("IBMid - Sign in or create an IBMid") holds its username box,
// Continue and a "Remember me" box: the box is the sign-in's own, so the page
// is the email-first sign-in the account stage works, never a form to fill.
func TestApply_Walk_ARememberMeBoxLeavesASignInAnEmailStep(t *testing.T) {
	inv := buildInventory([]engine.InspectRow{
		{Selector: `[id="username"]`, Label: "IBMid", Kind: "text"},
		{Selector: `[id="remember"]`, Label: "Remember me", Kind: "checkbox"},
	}, []placement{{Found: true, I: 1, Tag: "input", Type: "text", Name: "username", ID: "username", Form: 0}, {Found: true, I: 2, Tag: "input", Type: "checkbox", ID: "remember", Form: 0}}, []FormInfo{{}})
	st := &PageState{URL: "https://login.ibm.com/authsvc/mtfim/sps/authsvc", Title: "IBMid - Sign in or create an IBMid", Text: "Log in to IBM IBMid Continue Remember me",
		Inputs: inv.Inputs, Forms: inv.Forms, Controls: []Control{{I: 3, Tag: "button", Labels: []string{"Continue"}, Form: 0}}}
	if !bareSignInGate(st) || readEmailStep(st) == nil {
		t.Fatalf("inputs %+v: the sign-in is an email step", st.Inputs)
	}
}
