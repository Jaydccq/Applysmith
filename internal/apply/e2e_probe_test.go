//go:build browser

package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// htmlTab opens the apply lane's tab on a page of the given markup, served
// from the mock site, and returns it with the lane.
func htmlTab(t *testing.T, markup string) (context.Context, *browser.LaneHandle, string, Tab) {
	t.Helper()
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	b := st.add(&board{kind: "html", html: markup, marker: fmt.Sprintf("h%d%07d", 6, boardSeq.Add(1))})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	tab, err := LaneWindow{Lane: lane}.OpenTab(ctx, "probe", engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tab.Close(context.Background()) })
	if _, err := tab.Navigate(ctx, st.postingURL(b)); err != nil {
		t.Fatal(err)
	}
	return ctx, lane, tab.ID(), tab
}

func TestApply_Workday_ActiveStepAndLoadingDetection(t *testing.T) {
	step := func(id, status, name string) string {
		return fmt.Sprintf(`<li data-automation-id="%s"><label>%s</label><label>%s</label></li>`, id, status, name)
	}
	bar := `<ol data-automation-id="progressBar">` + step("progressBarCompletedStep", "completed step 2 of 5", "My Experience") +
		step("progressBarActiveStep", "current step 3 of 5", "Application Questions") + step("progressBarInactiveStep", "step 4 of 5", "Voluntary Disclosures") + `</ol>`
	loaded := `<div data-automation-id="applyFlowPage"><h3>Application Questions</h3><fieldset><legend>Are you at least 18 years old?<abbr title="required" class="requiredAsterisk">*</abbr></legend><button aria-haspopup="listbox">Select One</button><input type="text" style="width:0;height:0;border:0;padding:0"></fieldset></div>`
	skeleton := `<div data-automation-id="applyFlowPage"><h3>Application Questions</h3><div><div>Loading</div><div aria-hidden="true"><div style="height:20px;width:100px"></div></div></div></div>`
	spinner := `<div data-automation-id="applyFlowPage">` + bar + `<div data-automation-id="applyFlowMyExpPage"><input type="text" name="url" value="https://example.com"></div><div data-automation-id="applyFlowLoadingPage"><div style="height:10px"></div></div></div>`
	dots := `<div data-automation-id="applyFlowPage"><button data-automation-id="backToJobPosting">Back to Job Posting</button>` + bar + `<div><div class="a"></div><div class="b"></div></div></div>`
	outside := `<div><span>Loading</span><div aria-hidden="true"></div></div>` + loaded

	read := func(markup string) *PageState {
		ctx, _, _, tab := htmlTab(t, "<title>Apply</title>"+markup)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := read(bar); st.ActiveStep == nil || !strings.Contains(*st.ActiveStep, "current step 3 of 5") || !strings.Contains(*st.ActiveStep, "Application Questions") {
		t.Fatalf("active step %v: the progress bar names the step", st.ActiveStep)
	}
	if st := read(`<form><input name="q"></form>`); st.ActiveStep != nil {
		t.Fatalf("active step %q on a page with no progress bar", *st.ActiveStep)
	}
	if st := read(skeleton); !st.FlowLoading {
		t.Fatal("a Loading skeleton inside the apply flow is not loading")
	}
	if st := read(loaded); st.FlowLoading {
		t.Fatal("a rendered step reads as loading")
	}
	if st := read(spinner); !st.FlowLoading {
		t.Fatal("the apply flow's loading page over the previous step's fields is not loading")
	}
	if st := read(dots); !st.FlowLoading {
		t.Fatal("a step that drew only its progress bar is not loading")
	}
	if st := read(outside); st.FlowLoading {
		t.Fatal("a Loading word outside the apply flow reads as loading")
	}
	// The utility bar says whether the candidate is signed in: the account menu, or a Sign In button.
	for _, c := range []struct{ markup, want string }{
		{`<div data-automation-id="utilityButtonBar"><button data-automation-id="utilityButtonAccountTasksMenu">Account</button></div>` + loaded, accountSignedIn},
		{`<div data-automation-id="utilityButtonBar"><button data-automation-id="utilityButtonSignIn">Sign In</button></div>` + loaded, accountSignedOut},
		{loaded, ""},
	} {
		if st := read(c.markup); st.Account != c.want {
			t.Fatalf("account %q, want %q", st.Account, c.want)
		}
	}
	// The questionnaire's listbox-trigger button is one unknown field, named by its legend.
	st := read(loaded)
	var found []InputInfo
	for _, in := range st.Inputs {
		if in.Type == "listbox" {
			found = append(found, in)
		}
	}
	if len(found) != 1 || !strings.HasPrefix(found[0].Label, "Are you at least 18 years old?") || !found[0].Required || found[0].Filled {
		t.Fatalf("listbox trigger inputs %+v", found)
	}
}

// ADP draws a busy layer over the whole window while the next step loads; the
// previous step's fields under it are the step going away. The layer it keeps
// hidden between loads says nothing.
func TestApply_ProbeReadsABusyLayerOverTheStepAsLoading(t *testing.T) {
	step := `<h2>Preliminary Questions</h2><form><label><input type="radio" name="q" value="y">Yes</label><label><input type="radio" name="q" value="n">No</label><button type="button">Next</button></form>`
	layer := func(display string) string {
		return `<div id="standby"><div style="display: ` + display + `; opacity: 0.7; position: absolute; cursor: wait; background-color: rgb(255, 255, 255); width: 100vw; height: 100vh; top: 0px; left: 0px;"></div><div class="dojositeStandby">Please wait ...</div></div>`
	}
	read := func(markup string) *PageState {
		ctx, _, _, tab := htmlTab(t, "<title>Career Site</title>"+markup)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := read(step + layer("block")); !st.FlowLoading {
		t.Fatal("a busy layer over the step does not read as loading")
	}
	if st := read(step + layer("none")); st.FlowLoading {
		t.Fatal("the hidden busy layer reads as loading")
	}
}

func inventory(st *PageState) []string {
	var out []string
	for _, in := range st.Inputs {
		out = append(out, fmt.Sprintf("%s:%s:%s", in.Type, in.Name, in.ID))
	}
	for _, c := range st.Controls {
		out = append(out, "control:"+strings.Join(c.Labels, "/"))
	}
	sort.Strings(out)
	return out
}

const inspectionPage = `<title>Apply</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">
<label for="firstName">First Name *</label><input id="firstName" name="firstName" required>
<label for="email">Email *</label><input id="email" type="email" name="email" required>
<label for="pw">Password</label><input id="pw" type="password" name="pw">
<div style="display:none"><input id="ghost" type="file" name="ghost"></div>
<fieldset><legend>Will you now require visa sponsorship to work for us? *</legend>
<label><input type="radio" name="sp" value="Yes"> Yes</label><label><input type="radio" name="sp" value="No"> No</label></fieldset>
<shadow-step></shadow-step>
</form>
<script>
customElements.define('shadow-step', class extends HTMLElement {
  connectedCallback() { this.attachShadow({mode: 'open'}).innerHTML = '<button type="button">Continue</button>'; }
});
</script>`

func TestE2E_Apply_InventoryStableAfterFillOnSyntheticPage(t *testing.T) {
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	b := st.add(&board{kind: "html", html: inspectionPage, marker: fmt.Sprintf("i%d%07d", 6, boardSeq.Add(1))})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tab, err := LaneWindow{Lane: lane}.OpenTab(ctx, "inspect", engine.Host{Matcher: g.runner.d.Page.Matcher})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tab.Close(context.Background()) }()
	if _, err := tab.Navigate(ctx, st.postingURL(b)); err != nil {
		t.Fatal(err)
	}
	before, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	// The read-only look finds the password, the shadow control and the required
	// choice, and does not list the upload hidden behind a hidden ancestor.
	var haveRadio, havePw, haveGhost bool
	for _, in := range before.Inputs {
		haveRadio = haveRadio || (in.Type == "radio" && in.Name == "sp")
		havePw = havePw || in.Type == "password"
		haveGhost = haveGhost || in.Type == "file"
	}
	shadow := false
	for _, c := range before.Controls {
		shadow = shadow || strings.Join(c.Labels, "") == "Continue"
	}
	if !haveRadio || !havePw || haveGhost || !shadow {
		t.Fatalf("inventory before: radio %v password %v hidden upload listed %v shadow control %v", haveRadio, havePw, haveGhost, shadow)
	}
	opts := engine.FillOptions{Applicant: g.runner.d.Page.Applicant, Timing: fastEngine()}
	if _, err := tab.Page().Fill(ctx, opts); err != nil {
		t.Fatal(err)
	}
	after, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := strings.Join(inventory(before), "\n"), strings.Join(inventory(after), "\n"); a != b {
		t.Fatalf("the inventory changed by filling:\nbefore %s\nafter  %s", a, b)
	}
	for _, in := range after.Inputs {
		if in.Type == "password" && in.Filled {
			t.Fatal("the filler typed into the password box")
		}
	}
	var held string
	if err := tab.Eval(ctx, `document.getElementById('pw').value + '|' + document.getElementById('ghost').files.length`, &held); err != nil || held != "|0" {
		t.Fatalf("password and hidden upload hold %q (%v)", held, err)
	}
}

// countingDriver counts the bundle injections a page makes.
type countingDriver struct {
	engine.Driver
	injections atomic.Int32
}

func (d *countingDriver) Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error) {
	if len(expr) >= len(engine.Bundle()) {
		d.injections.Add(1)
	}
	return d.Driver.Eval(ctx, frame, world, expr)
}

// stallMatcher holds the first fill inside its match call until released.
type stallMatcher struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *stallMatcher) Match(ctx context.Context, _ engine.MatchRequest) (engine.MatchResponse, error) {
	m.once.Do(func() { close(m.entered) })
	select {
	case <-m.release:
	case <-ctx.Done():
	}
	return engine.MatchResponse{}, nil
}

func TestApply_Fill_EnsuresCurrentBundleAndRefusesSecondWriter(t *testing.T) {
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	b := st.add(&board{kind: "html", html: inspectionPage, marker: fmt.Sprintf("b%d%07d", 6, boardSeq.Add(1))})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id, err := lane.OpenTab(ctx, "bundle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lane.CloseTab(context.Background(), id) }()
	hooks, err := lane.Hooks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	drv := &countingDriver{Driver: hooks}
	m := &stallMatcher{entered: make(chan struct{}), release: make(chan struct{})}
	page, err := engine.NewPage(drv, engine.Host{Matcher: m})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lane.Navigate(ctx, id, st.postingURL(b)); err != nil {
		t.Fatal(err)
	}
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	opts := engine.FillOptions{Applicant: g.runner.d.Page.Applicant, Timing: fastEngine()}

	// A second start while a fill runs is refused as a second writer.
	first := make(chan error, 1)
	go func() { _, err := page.Fill(ctx, opts); first <- err }()
	select {
	case <-m.entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the first fill never reached the matcher")
	}
	_, err = page.Fill(ctx, opts)
	if code, _ := engine.CodeOf(err); code != engine.CodeBusy {
		t.Fatalf("a second fill while one runs: %v (code %q), want %q", err, code, engine.CodeBusy)
	}
	close(m.release)
	if err := <-first; err != nil {
		t.Fatalf("the first fill: %v", err)
	}
	if n := drv.injections.Load(); n != 1 {
		t.Fatalf("%d bundle injections for the first document, want 1", n)
	}

	// The same document again (a single-page-app step): the stamp matches, the
	// bundle is not evaluated again, and the new start begins a fresh run.
	r1, err := page.Fill(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := page.Fill(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if n := drv.injections.Load(); n != 1 || r1.RunID == r2.RunID || r1.Stamp != engine.Stamp() {
		t.Fatalf("injections %d, runs %q/%q, stamp %q: want one injection, two runs, the current stamp", n, r1.RunID, r2.RunID, r1.Stamp)
	}

	// A new document is a new injection.
	if _, err := lane.Navigate(ctx, id, st.postingURL(b)+"?again=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Fill(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if n := drv.injections.Load(); n != 2 {
		t.Fatalf("%d injections after a new document, want 2", n)
	}
}

func TestE2E_Apply_InventoryFromEngineOnRealPage(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">
<label for="pw">Password</label><input id="pw" type="password" name="pw" required>
<div><label for="auto">Autofill your application</label><input id="auto" type="file"></div>
<div><label for="cv">Resume *</label><input id="cv" type="file" required></div>
<div><label id="cl">City of residence</label><div role="combobox" aria-labelledby="cl"><input id="city" aria-autocomplete="list" value="Springfield"></div></div>
<div role="group" aria-labelledby="q"><span id="q">Are you 18 or older?</span><button type="button" id="yes" aria-pressed="true">Yes</button><button type="button" id="no" aria-pressed="false">No</button></div>
</form>`)
	var facts map[string]any
	if err := tab.Eval(ctx, probeExpr("probe", nil), &facts); err != nil {
		t.Fatal(err)
	}
	if _, listed := facts["inputs"]; listed {
		t.Fatal("the page probe lists fields")
	}
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]InputInfo{}
	for _, in := range st.Inputs {
		by[in.Label] = in
	}
	pw := by["Password"]
	if pw.Type != "password" || !pw.InspectionOnly || !pw.Required || pw.Filled || pw.I < 0 {
		t.Fatalf("password %+v", pw)
	}
	if auto := by["Autofill your application"]; !auto.ParserOnly || auto.Type != "file" {
		t.Fatalf("parser upload %+v", auto)
	}
	if cv := by["Resume"]; cv.ParserOnly || cv.Type != "file" || !cv.Required {
		t.Fatalf("resume %+v (inputs %+v)", cv, st.Inputs)
	}
	n := 0
	for _, in := range st.Inputs {
		if strings.HasPrefix(in.Label, "City") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the combobox composite is %d inputs, want 1 (%+v)", n, st.Inputs)
	}
	if g := by["Are you 18 or older?"]; g.Type != "radio" || !g.Filled {
		t.Fatalf("answered button group %+v", g)
	}
}

// Channable (Recruitee) shows a scam-warning notice in a plain fixed overlay
// (no dialog role) over its Apply tab; its only button is a close.
func TestApply_Probe_NoticeOverlayIsClosedByItsCloseButton(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Engineer</title><main><a href="#apply" role="button" style="display:block;margin:200px 20px">Apply</a></main>
<div id="popupModal" style="position:fixed;inset:0;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.3);z-index:1000"><div style="background:#fff;padding:20px;position:relative">
<button onclick="document.getElementById('popupModal').remove()">&times;</button><h2>Please watch out for recruitment scams!</h2>
<p>We will never request money or sensitive personal data. Notify us at <a href="mailto:privacy@example.com">privacy@example.com</a>.</p></div></div>`)
	g, err := ReadConsent(ctx, tab)
	if err != nil || !g.Found || g.Asks {
		t.Fatalf("consent %+v %v", g, err)
	}
	labels := make([]string, len(g.Controls))
	for i, b := range g.Controls {
		labels[i] = b.Label
	}
	if d := gates.CloseConsentGate(gates.CookieInput{Controls: labels}); !d.Allowed() || labels[d.Press] != "\u00d7" {
		t.Fatalf("decision %+v over %v", d, labels)
	}
	// A form in an overlay is never such a notice.
	ctx, _, _, tab = htmlTab(t, `<div style="position:fixed;inset:0;z-index:9"><input name="email"><button>&times;</button></div>`)
	if g, err := ReadConsent(ctx, tab); err != nil || g.Found {
		t.Fatalf("consent %+v %v: an overlay asking for input is not a notice", g, err)
	}
}

// TikTok's posting (live): a promo dialog for its careers assistant stands over
// Apply and closes with an icon button named "Close guide"; the page's job
// search box (a placeholder only) and the language picker in its <menu> drawer
// are the site's, so the page holds no form.
func TestApply_Probe_PromoDialogClosesAndSiteSearchIsNoForm(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Backend Engineer</title><menu><div><select required><option>English</option><option>\u65e5\u672c\u8a9e</option></select></div></menu>
<main><section><h2>Discover a career that energizes and excites you every day.</h2><input type="text" placeholder="Enter Title, Skill, or Location"></section>
<h1>Backend Engineer</h1><button type="button" data-tracking="job-apply-button" style="display:block;margin:200px 20px">Apply</button></main>
<div id="mask" style="position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:10"><section role="dialog" aria-modal="true" style="background:#fff;margin:60px;padding:20px">
<button aria-label="Close guide" type="button" onclick="document.getElementById('mask').remove()"><svg width="12" height="12"></svg></button>
<h2>Your AI assistant for careers. Ask anything.</h2><button type="button">Try Now</button><button type="button">Maybe Later</button></section></div>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the site's search box and language picker are a form", st.Inputs)
	}
	g, err := ReadConsent(ctx, tab)
	if err != nil || !g.Found || g.Asks {
		t.Fatalf("consent %+v %v", g, err)
	}
	labels := make([]string, len(g.Controls))
	for i, b := range g.Controls {
		labels[i] = b.Label
	}
	if d := gates.CloseConsentGate(gates.CookieInput{Controls: labels}); !d.Allowed() || labels[d.Press] != "Close guide" {
		t.Fatalf("decision %+v over %v", d, labels)
	}
}

// Workable's posting: an aria-modal cookie dialog with a full-page backdrop
// stands over an Apply anchor, and SmartRecruiters' "I'm interested" anchor is
// a control the probe lists. The consent read finds the dialog's buttons, the
// gate chooses "Decline all", and once it is pressed the Apply anchor is hit.
func TestApply_Probe_CookieBackdropCoversApplyAndConsentGateIsReadAndClosed(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Computational Linguist</title><main>
<a tabindex="0" role="button" data-ui="apply-button" href="/omilia/j/1F218B38D3/apply/" style="display:block;margin:200px 20px">Apply for this job</a>
<a class="button js-oneclick" id="st-apply" href="https://jobs.smartrecruiters.com/oneclick-ui/company/C1/publication/p1" style="display:block;margin:20px">I'm interested</a></main>
<div id="gate"><div data-ui="backdrop" style="position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:10"></div>
<div data-ui="cookie-consent" role="dialog" aria-modal="true" aria-label="Cookie Consent" style="position:fixed;left:0;right:0;bottom:0;z-index:11;background:#fff;padding:12px">
<span>This website uses cookies to improve your experience. You can accept all cookies, decline all optional cookies, or manage your cookie settings.</span>
<button onclick="return false">Cookies settings</button><button onclick="return false">Accept all</button><button onclick="document.getElementById('gate').remove()">Decline all</button></div></div>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	var apply, interested *Control
	for i := range st.Controls {
		switch st.Controls[i].Labels[0] {
		case "Apply for this job":
			apply = &st.Controls[i]
		case "I'm interested":
			interested = &st.Controls[i]
		}
	}
	if apply == nil || interested == nil {
		t.Fatalf("controls %+v: the Apply anchor and the I'm interested anchor are controls", st.Controls)
	}
	if cats := Categorize(st, false); len(cats.Openers) != 2 {
		t.Fatalf("categories %+v: both anchors are openers, and the cookie dialog is neither a modal nor a source of controls", cats)
	}
	if l, err := Locate(ctx, tab, apply.I); err != nil || !l.Found || l.Hit {
		t.Fatalf("located %+v %v: the backdrop covers the Apply anchor", l, err)
	}
	g, err := ReadConsent(ctx, tab)
	if err != nil || !g.Found || g.Asks {
		t.Fatalf("consent %+v %v", g, err)
	}
	labels := make([]string, len(g.Controls))
	for i, b := range g.Controls {
		labels[i] = b.Label
	}
	d := gates.CloseConsentGate(gates.CookieInput{Controls: labels, PreferenceCenter: g.Preference, AllOptionalOff: g.AllOff})
	if !d.Allowed() || labels[d.Press] != "Decline all" {
		t.Fatalf("decision %+v over %v", d, labels)
	}
	l, err := Locate(ctx, tab, g.Controls[d.Press].I)
	if err != nil || !l.Found || !l.Hit {
		t.Fatalf("located %+v %v", l, err)
	}
	if err := tab.Click(ctx, l.X, l.Y); err != nil {
		t.Fatal(err)
	}
	if l, err := Locate(ctx, tab, apply.I); err != nil || !l.Hit {
		t.Fatalf("located %+v %v: the Apply anchor is free once the notice is declined", l, err)
	}
	if g, err := ReadConsent(ctx, tab); err != nil || g.Found {
		t.Fatalf("consent %+v %v: nothing is left to close", g, err)
	}
}

func TestApply_Probe_AvatureCookiesPopupIsAConsentGateClosedWithRejectAll(t *testing.T) {
	// Avature (Lenovo) draws its popup as <div class="cookies"> with no dialog role.
	ctx, _, _, tab := htmlTab(t, `<title>AI Engineer</title><main><aside><div role="navigation">
<a href="/en_US/careers/Login?jobId=1" style="display:block;margin:200px 20px">Apply</a></div></aside></main>
<div class="cookies " id="gate" style="position:fixed;inset:0;z-index:10;background:#fff">
<p>We use cookies and similar technologies to personalize content and ads and analyze our traffic.</p>
<div class="cookies__settings" style="display:none"><input type="checkbox" id="cookie-option-2"></div>
<a href="#" class="cookiesButton cookiesButtonSettings" role="button" aria-label="Cookie settings">Cookie settings</a>
<a href="#" class="cookiesButton cookiesButtonAccept" role="button" onclick="return false">Accept all</a>
<a href="#" class="cookiesButton cookiesButtonReject" role="button" onclick="document.getElementById('gate').remove(); return false">Reject all</a></div>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, false)
	if len(cats.Openers) != 1 {
		t.Fatalf("openers %+v", cats.Openers)
	}
	if l, err := Locate(ctx, tab, cats.Openers[0].I); err != nil || l.Hit {
		t.Fatalf("located %+v %v: the popup covers the Apply link", l, err)
	}
	g, err := ReadConsent(ctx, tab)
	if err != nil || !g.Found {
		t.Fatalf("consent %+v %v: the popup is a consent gate", g, err)
	}
	labels := make([]string, len(g.Controls))
	for i, b := range g.Controls {
		labels[i] = b.Label
	}
	d := gates.CloseConsentGate(gates.CookieInput{Controls: labels, PreferenceCenter: g.Preference, AllOptionalOff: g.AllOff})
	if !d.Allowed() || labels[d.Press] != "Reject all" {
		t.Fatalf("decision %+v over %v", d, labels)
	}
}

// A form with a control named "id" shadows the form's id property with that
// control (CVS's Phenom search form). The probe still reads the page.
func TestApply_Probe_FormWithAControlNamedIdIsRead(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Analyst</title><main>
<form id="search-form"><input name="id" type="hidden" value="1"><label>Keyword <input name="q"></label></form>
<a href="/apply/1">Apply</a></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if cats := Categorize(st, false); len(cats.Openers) != 1 {
		t.Fatalf("openers %+v: the Apply link is an opener", cats.Openers)
	}
}

// Avature (Lenovo) draws its application methods as links; Without Resume is
// the one the account stage presses. Copy & Paste, a link with no address
// (Deloitte's only methods are it and From Device), is a control too.
func TestApply_Probe_AvatureWithoutResumeLinkIsAControl(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>AI Engineer</title><main><h2>Choose any option to apply</h2><ul>
<li><a id="methodButton--paste" role="button" tabindex="0">Copy &amp; Paste</a></li>
<li><a id="methodButton--later" role="button" tabindex="0" href="/en_US/careers/Register?jobId=1">Without Resume</a></li></ul>
<h2>Already registered?</h2><a href="/en_US/careers/Login" role="button">LOGIN</a></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if c := methodLater(st); c == nil || c.Labels[0] != "Without Resume" {
		t.Fatalf("method %+v: Without Resume is a control", c)
	}
	if !pasteMethod(st) {
		t.Fatalf("controls %+v: Copy & Paste is a control", st.Controls)
	}
}

// CVS's Phenom posting: a job-alert box whose heading sits outside its form,
// beside the posting's Apply link. The box is the site's furniture.
func TestApply_Probe_JobAlertFormUnderAnOutsideHeadingIsFurniture(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Staff Engineer</title><main><h1>Staff Engineer</h1>
<a href="/apply/1" phw-tk="apply_click">Apply Now</a>
<section><div role="region" aria-label="Get notified for similar jobs"><h2>Get notified for similar jobs</h2><p>Sign up to receive job alerts</p>
<label for="notifiedEmail">Email address</label><form><input id="notifiedEmail" type="email"><button type="submit">Submit</button></form></div></section></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the job-alert box is no application", st.Inputs)
	}
	if len(Categorize(st, false).Openers) != 1 {
		t.Fatal("the Apply link is the opener")
	}
}

// A page that redraws between two probes (Rockwell's Workday sign-in: the
// password went into the Email box) keeps every handle on its element, and a
// password is focused only in a password box.
func TestApply_Probe_HandlesOutliveALaterProbeAndASecretGoesOnlyToAPasswordBox(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Sign In</title><main><h1>Sign In</h1><div id="top"></div><form>
<label>Email Address <input type="email" name="email"></label>
<label>Password <input type="password" name="password"></label>
<button type="submit">Sign In</button></form></main>`)
	first, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	w := findWall(first)
	if w == nil || w.email == nil || w.password == nil {
		t.Fatalf("wall %+v", w)
	}
	var none any
	if err := tab.Eval(ctx, `(() => { const top = document.getElementById("top"); top.innerHTML = '<label>Search <input type="text" name="q"></label><button>Accept cookies</button>'; return null; })()`, &none); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, tab); err != nil {
		t.Fatal(err)
	}
	if ok, err := FocusSecret(ctx, tab, w.password.I); err != nil || !ok {
		t.Fatalf("focus %v %v: the first probe's password handle still names the password box", ok, err)
	}
	if ok, _ := FocusSecret(ctx, tab, w.email.I); ok {
		t.Fatal("a password is never focused into the Email box")
	}
}

func TestApply_Probe_SignUpLinkUnderASignInFormIsTheWallsCreateControl(t *testing.T) {
	// UKG's Auth0 page: "Don't have an account? <a>Sign up</a>" under the form.
	ctx, _, _, tab := htmlTab(t, `<title>Log in</title><main><h1>Welcome, come on in!</h1><form>
<label for="u">Email address*</label><input id="u" name="username" type="text"><label for="p">Password*</label><input id="p" name="password" type="password">
<p><a href="/u/login/password-reset-start">Create or reset your password</a></p><button type="submit" name="action">Sign in</button></form>
<p>Don't have an account? <a href="/u/signup?state=x">Sign up</a></p></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	w := findWall(st)
	if w == nil || w.create == nil || w.create.Labels[0] != "Sign up" || w.signin == nil {
		t.Fatalf("wall %+v of controls %+v: the Sign up link creates the account", w, st.Controls)
	}
}

// Teamtailor's cookie notice is a modal dialog that also holds the preference
// switches (checkboxes). It is still a cookie notice: the page has no form, and
// the posting's Apply link is an opener that the consent gate then uncovers.
func TestApply_Probe_CookieDialogWithPreferenceSwitchesIsNotTheForm(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Fullstack Developer</title><main><a href="/jobs/1/applications/new" style="display:block;margin:200px 20px">Apply for this job</a></main>
<div role="dialog" aria-modal="true" style="position:fixed;inset:20px;background:#fff;z-index:11;padding:12px"><h2>This website uses cookies to ensure you get the best experience.</h2>
<label><input type="checkbox" name="strictly_necessary" checked disabled> Strictly necessary</label><label><input type="checkbox" name="analytics"> Analytics</label>
<button>Accept all cookies</button><button>Decline all non-necessary cookies</button></div>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() {
		t.Fatalf("inputs %+v: the cookie switches are not an application form", st.Inputs)
	}
	if cats := Categorize(st, false); len(cats.Openers) != 1 {
		t.Fatalf("categories %+v: the Apply anchor is the one opener", cats)
	}
}

// Two Sigma's site menu is a role=dialog aria-modal=true in its header, drawn
// open on a wide window: it blocks nothing, so the form's Next is the step.
func TestApply_Probe_SiteMenuDialogInTheHeaderIsNoModal(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Applying</title><header class="globalHeader"><div class="globalHeader__menu" role="dialog" aria-modal="true" aria-label="navigation">
<a href="/businesses">Businesses</a><button type="button">Businesses submenus</button><form action="/search"><input type="search" name="q" aria-label="Search"><input type="submit" value="Search"></form></div></header>
<main><h1>Application</h1><form><label for="f">First name</label><input id="f" name="first" required><label for="e">Email</label><input id="e" name="email" type="email" required>
<label for="r">Upload Resume</label><input id="r" name="resume" type="file"><button type="submit" onclick="return false">Next</button></form></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, true)
	if st.Modal || len(cats.Nav) != 1 || cats.Nav[0].Labels[0] != "Next" {
		t.Fatalf("modal %v nav %+v: the site menu is no modal over the form", st.Modal, cats.Nav)
	}
}

// A button's numeric value is form data, not a second label; a worded value
// still reads, so a Next that posts "submit" stays a Submit.
func TestApply_Probe_AButtonsNumericValueIsNoLabel(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"292", "nav"}, {"submit", "submit"}} {
		ctx, _, _, tab := htmlTab(t, `<form><label for="f">First name</label><input id="f" name="first" required>
<label for="e">Email</label><input id="e" name="email" type="email" required>
<button type="submit" name="next" value="`+tc.value+`" onclick="return false">Next</button></form>`)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		cats := Categorize(st, true)
		if got := map[bool]string{true: "nav", false: "submit"}[len(cats.Nav) == 1]; got != tc.want || len(cats.Nav)+len(cats.Submit) != 1 {
			t.Errorf("value %q: nav %+v submit %+v, want %s", tc.value, cats.Nav, cats.Submit, tc.want)
		}
	}
}

// Jobvite's data-consent page as Optimizely draws it: the residence menu, then
// the accept the choice reveals.
func TestApply_Probe_JobviteDataConsentPageIsReadAndAnswered(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<h3>Data Consent</h3><form><label for="r">Location of Residence and Language:</label>
<select id="r" required onchange="terms.hidden=false"><option value="">Select your location</option><option>Germany</option><option>Rest of World</option></select>
<label><input type="checkbox"> Send me job alerts</label><div id="terms" hidden><button type="submit" onclick="return false">I Accept</button> <a href="/j">I Decline</a></div></form>`)
	st, err := Probe(ctx, tab)
	if err != nil || dataConsentPage(st, false) == "" || st.Inputs[0].Tag != "select" || match.ResidenceOption(st.Inputs[0].Options, "US") != 2 {
		t.Fatalf("state %+v %v", st, err)
	}
	if ok, err := ChooseOption(ctx, tab, st.Inputs[0].I, 2); !ok || err != nil {
		t.Fatalf("choose %v %v", ok, err)
	}
	if st, err = Probe(ctx, tab); err != nil || dataConsentPage(st, false) == "" || !slices.ContainsFunc(st.Controls, func(c Control) bool { return dataAcceptRE.MatchString(c.Labels[0]) }) {
		t.Fatalf("state %+v %v: the consent page with its accept", st, err)
	}
}

// The employer a Teamtailor page states: its JobPosting's hiringOrganization
// before og:site_name.
func TestApply_Probe_PageCompanyIsWhatThePageStates(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<meta property="og:site_name" content="TixTrack Careers"><script type="application/ld+json">{"@type":"JobPosting","title":"Software Engineer","hiringOrganization":{"@type":"Organization","name":"TixTrack"}}</script><h1>Software Engineer</h1>`)
	if got := PageCompany(ctx, tab); got != "TixTrack" {
		t.Fatalf("company %q", got)
	}
}

// The role, employer and place a posting page's JobPosting data names, in the
// shapes the sites emit it: Workday's pretty-printed block with the type last,
// Ashby's compact one, a graph and an array that hold other nodes, an employer
// written as text. The place is the jobLocation address as "Locality, Region,
// Country" (Workday's remote locality already names its country), else a remote
// posting's applicant country (Workable's), never the page's text; several
// locations give the place they share, so a posting open in two countries is
// placed in neither; SmartRecruiters writes its posting as
// microdata, whose place is read the same way and whose names are not. A page
// that declares several different postings is a listing and states none, and
// og:site_name is a site's name, not a posting's employer.
func TestApply_Probe_PostingStatementIsWhatTheJobPostingDataStates(t *testing.T) {
	ld := func(block string) string { return `<script type="application/ld+json">` + block + `</script>` }
	workday := ld(`
{
  "jobLocation" : {
    "@type" : "Place",
    "address" : {
      "@type" : "PostalAddress",
      "addressCountry" : "United States",
      "addressLocality" : "US, NC, Durham"
    }
  },
  "hiringOrganization" : {
    "name" : "Northwind Example Inc",
    "@type" : "Organization",
    "sameAs" : ""
  },
  "identifier" : {
    "name" : "Platform Engineer",
    "@type" : "PropertyValue",
    "value" : "JR100200"
  },
  "datePosted" : "2027-01-05",
  "employmentType" : "FULL_TIME",
  "title" : "Platform Engineer",
  "description" : "<p>Build the platform.</p>",
  "@context" : "http://schema.org",
  "@type" : "JobPosting"
}`)
	ashby := ld(`{"@context":"https://schema.org/","@type":"JobPosting","title":"Engineering Manager - EU","description":"<p>Lead the team.</p>","directApply":true,` +
		`"identifier":{"@type":"PropertyValue","name":"Northwind Example","value":"0a1b2c3d-0000-4000-8000-000000000001"},"datePosted":"2027-01-05",` +
		`"hiringOrganization":{"@type":"Organization","name":"Northwind Example","sameAs":"https://www.example.com","logo":"https://example.com/logo.png"},` +
		`"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"","addressRegion":"","addressCountry":"European Union"}},"employmentType":"FULL_TIME"}`)
	posting := func(title, org string) string {
		return `{"@context":"https://schema.org","@type":"JobPosting","title":"` + title + `","hiringOrganization":` + org + `}`
	}
	org := `{"@type":"Organization","name":"Northwind Example"}`
	placed := func(where string) string {
		return ld(`{"@context":"https://schema.org/","@type":"JobPosting","title":"Data Analyst","hiringOrganization":` + org + `,` + where + `}`)
	}
	address := func(locality, region, country string) string {
		return `{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"` + locality + `","addressRegion":"` + region + `","addressCountry":"` + country + `"}}`
	}
	microdata := `<main class="jobad-main job" itemscope itemtype="http://schema.org/JobPosting" role="main"><h1 class="job-title" itemprop="title">Data Analyst</h1>` +
		`<ul class="job-details"><li itemprop="jobLocation" itemscope itemtype="http://schema.org/Place"><span class="job-detail" itemprop="address" itemscope itemtype="http://schema.org/PostalAddress">` +
		`<spl-job-location formattedaddress="Remote, Durham, North Carolina, United States" workplacetype="remote"></spl-job-location><meta itemprop="addressCountry" content="United States">` +
		`<meta itemprop="addressLocality" content="Durham"><meta itemprop="addressRegion" content="North Carolina"><meta itemprop="streetAddress" content="Remote"></span></li></ul>` +
		`<div itemprop="hiringOrganization" itemscope itemtype="http://schema.org/Organization"><meta itemprop="name" content="Northwind Example"></div></main>`
	for _, c := range []struct {
		name, head string
		want       Stated
	}{
		{"Workday's block", workday, Stated{"Northwind Example Inc", "Platform Engineer", "US, NC, Durham, United States"}},
		{"Ashby's block", ashby, Stated{"Northwind Example", "Engineering Manager - EU", "European Union"}},
		{"an office posting", placed(`"jobLocation":` + address("Durham", "North Carolina", "United States")), Stated{"Northwind Example", "Data Analyst", "Durham, North Carolina, United States"}},
		{"Workday's remote posting", placed(`"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressCountry":"Poland","addressLocality":"Poland, Remote"}}`),
			Stated{"Northwind Example", "Data Analyst", "Poland, Remote"}},
		{"a remote posting with a country requirement", placed(`"jobLocationType":"TELECOMMUTE","applicantLocationRequirements":[{"@type":"Country","name":"United States"}]`),
			Stated{"Northwind Example", "Data Analyst", "Remote, United States"}},
		{"a posting in two countries", placed(`"jobLocation":[` + address("Durham", "North Carolina", "United States") + `,` + address("Toronto", "Ontario", "Canada") + `]`),
			Stated{"Northwind Example", "Data Analyst", ""}},
		{"a posting in two cities of one country", placed(`"jobLocation":[` + address("Durham", "North Carolina", "United States") + `,` + address("Austin", "Texas", "United States") + `]`),
			Stated{"Northwind Example", "Data Analyst", "United States"}},
		{"a remote posting open in two countries", placed(`"jobLocationType":"TELECOMMUTE","applicantLocationRequirements":[{"@type":"Country","name":"United States"},{"@type":"Country","name":"Canada"}]`),
			Stated{"Northwind Example", "Data Analyst", ""}},
		{"a graph beside a site node", ld(`{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"Northwind Careers"},{"@type":"JobPosting","title":"Support Engineer","hiringOrganization":` + org + `}]}`),
			Stated{"Northwind Example", "Support Engineer", ""}},
		{"an array beside a breadcrumb", ld(`[{"@type":"BreadcrumbList","itemListElement":[]},` + posting("Data Analyst", org) + `]`), Stated{"Northwind Example", "Data Analyst", ""}},
		{"an employer written as text", ld(posting("Data Analyst", `"Northwind Example"`)), Stated{"Northwind Example", "Data Analyst", ""}},
		{"a block that is not JSON, then the posting", ld(`{"title": nope`) + ld(posting("Data Analyst", org)), Stated{"Northwind Example", "Data Analyst", ""}},
		{"one posting declared twice", ld(posting("Data Analyst", org)) + ld(posting("Data Analyst", org)), Stated{"Northwind Example", "Data Analyst", ""}},
		{"a listing of two postings", placed(`"jobLocation":`+address("Durham", "North Carolina", "United States")) + ld(posting("Support Engineer", org)), Stated{}},
		{"an employer and no title", ld(`{"@type":"JobPosting","hiringOrganization":` + org + `}`), Stated{Company: "Northwind Example"}},
		{"no posting: a site name names none", `<meta property="og:site_name" content="Northwind Careers">` + ld(`{"@type":"Organization","name":"Northwind Example"}`), Stated{}},
		{"no structured data: the text's location line is not the page's statement", `<p>Location: Durham, NC</p>`, Stated{}},
		{"SmartRecruiters' microdata states its place", microdata, Stated{Location: "Durham, North Carolina, United States"}},
		{"a microdata listing of two postings", microdata + microdata, Stated{}},
	} {
		ctx, _, _, tab := htmlTab(t, "<title></title>"+c.head+"<h1>Careers</h1>")
		if got := PageStated(ctx, tab); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A dialog that asks for a password is no consent gate: the read reports that
// it asks and lists no button of it.
func TestApply_Probe_DialogThatAsksForAPasswordIsNoConsentGate(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Sign in</title><div role="dialog" aria-modal="true" aria-label="Cookie privacy" style="position:fixed;inset:20px;background:#fff">
We use cookies and your personal data to sign you in. <input type="password" name="pw"><button>Accept</button></div>`)
	if _, err := Probe(ctx, tab); err != nil {
		t.Fatal(err)
	}
	g, err := ReadConsent(ctx, tab)
	if err != nil || g.Found || !g.Asks || len(g.Controls) != 0 {
		t.Fatalf("consent %+v %v", g, err)
	}
}

// A footer inside the page's main region is the form's own action bar
// (SmartRecruiters draws its Next there); the site's footer outside it stays
// chrome and offers nothing to press.
func TestApply_Probe_FooterInsideMainIsTheFormsActionBarNotSiteChrome(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><main><h1>Personal information</h1><label>First name <input name="f"></label>
<footer><button type="button">Next</button></footer></main><footer><button type="button">Continue</button></footer>`)
	page, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	var inMain, site *Control
	for i := range page.Controls {
		switch page.Controls[i].Labels[0] {
		case "Next":
			inMain = &page.Controls[i]
		case "Continue":
			site = &page.Controls[i]
		}
	}
	if inMain == nil || inMain.Chrome || site == nil || !site.Chrome {
		t.Fatalf("controls %+v: the Next in main is the form's, the Continue in the site footer is chrome", page.Controls)
	}
}

func TestApply_Probe_PostingApplyIsAnOpenerUnderAChatbotBodyClassAndAnInMainNavigationBar(t *testing.T) {
	for _, tc := range []struct{ name, markup string }{
		// Phenom (CVS) marks the whole document's body for its chatbot.
		{"chatbot class on body", `<title>Job</title><body class="ph-chatbot-parent-body"><main><h1>Engineer</h1><a href="/apply?job=1">Apply Now</a></main>
<div class="chatbot-container"><button type="button">I'm interested</button></div></body>`},
		// Avature (Lenovo) puts the posting's Apply in a navigation landmark of its aside.
		{"navigation landmark in main", `<title>Job</title><header><nav><a href="/why">Why us</a></nav></header><main><h1>Engineer</h1>
<aside><div role="navigation"><a href="/Login?jobId=1">Apply</a></div></aside><nav aria-label="pages"><a href="?p=2">Next page</a></nav></main>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, _, tab := htmlTab(t, tc.markup)
			page, err := Probe(ctx, tab)
			if err != nil {
				t.Fatal(err)
			}
			cats := Categorize(page, false)
			if len(cats.Openers) != 1 || !isApplyLabel(cats.Openers[0].Labels) {
				t.Fatalf("openers %+v of controls %+v: the posting's own Apply is the one opener", cats.Openers, page.Controls)
			}
		})
	}
}

// A contenteditable box has no .form property but belongs to the form around
// it, so a form of only such a box is still classified (Teamtailor's auto-join
// box, "Already working at <employer>? Log in", posts to /auto_join).
func TestE2E_Probe_ContentEditableBoxBelongsToItsForm(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Job</title><h1>Engineer</h1><a href="/apply" style="display:block;margin:20px">Apply for this job</a>
<form action="/en-GB/auto_join" method="post"><div id="ce" contenteditable="true" role="textbox" aria-label="Email address without domain" style="min-width:60px;border:1px solid #888">x</div><a href="/login">Log in</a></form>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Forms) != 1 || !st.Forms[0].Furniture || st.HasForm() {
		t.Fatalf("forms %+v inputs %+v: the auto-join box is furniture, not the page's form", st.Forms, st.Inputs)
	}
}

// iCIMS draws the whole posting, and its Apply link, in a same-origin frame
// under a header-only top document. The probe presents the frame's Apply link
// as the page's opener, and the link can be found and hit in page coordinates.
func TestApply_Probe_ApplyLinkInASameOriginFrameIsTheOpenerOfThePage(t *testing.T) {
	_, st := sharedBrowser(t)
	inner := st.add(&board{kind: "html", html: `<title>Job</title><h1>Engineer</h1><p>Build things.</p><a href="/jobs/5478/login" style="display:block;margin:30px">Apply for this job online</a>`, marker: fmt.Sprintf("h%d%07d", 7, boardSeq.Add(1))})
	ctx, _, _, tab := htmlTab(t, `<title>Careers</title><h1>Careers</h1><iframe id="icims_content_iframe" src="`+st.postingURL(inner)+`" style="width:900px;height:600px;border:0"></iframe>`)
	page, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(page, false)
	if len(cats.Openers) != 1 || cats.Openers[0].Labels[0] != "Apply for this job online" || cats.Openers[0].I < frameBase {
		t.Fatalf("openers %+v controls %+v: the frame's Apply link stands for the page", cats.Openers, page.Controls)
	}
	if l, err := Locate(ctx, tab, cats.Openers[0].I); err != nil || !l.Found || !l.Hit {
		t.Fatalf("located %+v %v", l, err)
	}
}

// DataDome's interstitial (SmartRecruiters after "I'm interested") is a top
// document of one script and one captcha-delivery.com frame; the copy the person
// reads lives inside that cross-origin frame. The probe reads the frame's
// address as a standing datadome challenge that is the whole page.
func TestApply_Probe_DataDomeInterstitialFrameIsTheWholePageChallenge(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<!doctype html><html lang="en"><head><title>smartrecruiters.com</title><style>#cmsg{animation: A 1.5s;}@keyframes A{0%{opacity:0;}99%{opacity:0;}100%{opacity:1;}}</style><meta name="viewport" content="width=device-width, initial-scale=1.0"></head><body style="margin:0"><script data-cfasync="false">var dd={'rt':'c','t':'fe','host':'geo.captcha-delivery.com'}</script><iframe src="https://geo.captcha-delivery.com/captcha/?initialCid=AHrlqAAAAAMAb1YCF9RfFYcAmCx3Tw%3D%3D&amp;t=fe&amp;dm=cd" sandbox="allow-scripts allow-same-origin allow-forms" title="DataDome CAPTCHA" width="100%" height="100%" style="height:100vh;" frameborder="0" border="0" scrolling="yes"></iframe></body></html>`)
	page, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	d := DetectChallenge(page)
	if d == nil || d.Kind != gates.ChallengeDataDome || !d.ChallengeOnly {
		t.Fatalf("captcha %+v detected %+v: the DataDome frame is the page's challenge", page.Captcha, d)
	}
}

// Workday's "Errors Found" panel (markup captured on a live dry run): each
// refused field is a button naming it with the page's message beside it; the
// prompt's red icon carries aria-invalid with no name. The validation words
// lead with the panel's message and the stall reason quotes it.
func TestApply_Probe_WorkdayErrorsFoundPanelNamesTheFieldAndMessage(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><div><h3><button aria-expanded="true"><div>Errors Found</div></button></h3>
<div data-automation-id="errorHeading"><ol><li value="1"><div><button aria-describedby="hint9"><span>Error</span><div aria-hidden="true">-</div>How Did You Hear About Us?</button>
<div id="hint9">The field How Did You Hear About Us? is required and must have a value.</div></div></li></ol></div></div>
<label for="source--source"><span>How Did You Hear About Us?<abbr aria-hidden="true">*</abbr></span></label>
<input placeholder="Search" aria-invalid="true" aria-required="true" id="source--source" value="">
<span data-automation-id="promptIcon" aria-hidden="true" aria-invalid="true" style="display:inline-block;width:24px;height:24px">&#9679;</span>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	const msg = "The field How Did You Hear About Us? is required and must have a value."
	if len(st.Validation) == 0 || st.Validation[0] != msg || slices.Contains(st.Validation, "invalid") {
		t.Fatalf("validation %q", st.Validation)
	}
	if w := stallWords(st); !strings.Contains(w, "the page says: "+msg) || strings.Contains(w, ": invalid") {
		t.Fatalf("stall words %q", w)
	}
	// The error-summary list of the GOV.UK pattern: a message that does not
	// name its field gets the field from the link.
	ctx, _, _, tab = htmlTab(t, `<title>Apply</title><div class="govuk-error-summary"><h2>There is a problem</h2>
<ul><li><a href="#nino">National Insurance number</a></li><li><a href="#email">Enter an email address</a></li></ul></div><label>Email <input id="email"></label>`)
	if st, err = Probe(ctx, tab); err != nil {
		t.Fatal(err)
	}
	if len(st.Validation) < 2 || st.Validation[0] != "National Insurance number" || st.Validation[1] != "Enter an email address" {
		t.Fatalf("summary %q", st.Validation)
	}
}

// The pre-press scans on the Node fixtures: multistep.workday-review-submit
// (Mizuho's Review recap) and the toggle shapes submit.ts names (Ashby's
// _yesno_ buttons, the tabindex=-1 proxy twin, Snowflake's résumé slot that is
// not one, and a required native radio group).
func TestApply_Probe_PrePressReadsNoResponseAndUnansweredToggles(t *testing.T) {
	read := func(markup string) PrePress {
		ctx, _, _, tab := htmlTab(t, "<title>Review</title>"+markup)
		p, err := ReadPrePress(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	review := map[string]struct {
		markup string
		want   []string
	}{
		"a lost Work Experience row":       {`<div role="group" aria-labelledby="we"><h4 id="we">Work Experience</h4><span class="css-1j5bq6h">No Response</span></div>`, []string{"Work Experience"}},
		"a fully answered Review":          {`<div role="group" aria-labelledby="we"><h4 id="we">Work Experience</h4><div role="group" aria-labelledby="we1"><h5 id="we1">Work Experience 1</h5><span>Researcher Intern</span></div></div>`, nil},
		"a wrapping ancestor reports once": {`<div role="group" aria-labelledby="we"><h4 id="we">Work Experience</h4><div><span>No Response</span></div></div>`, []string{"Work Experience"}},
		"required but not optional":        {`<div role="group" aria-labelledby="q1"><h4 id="q1">Are you at least 18 years of age?*</h4><span>No Response</span></div><div role="group" aria-labelledby="q2"><h4 id="q2">Preferred pronouns</h4><span>No Response</span></div>`, []string{"Are you at least 18 years of age?*"}},
	}
	for name, c := range review {
		if got := read(c.markup).NoResponse; !slices.Equal(got, c.want) {
			t.Errorf("%s: no response %q, want %q", name, got, c.want)
		}
	}
	toggles := map[string]struct {
		markup string
		want   []string
	}{
		"Ashby yes/no unanswered":    {`<div data-field-path="q1"><label class="_required_x">Are you authorized to work?</label><div class="_yesno_abc"><button type="button">Yes</button><button type="button">No</button></div></div>`, []string{"Are you authorized to work?"}},
		"Ashby yes/no answered":      {`<div data-field-path="q1"><label class="_required_x">Are you authorized to work?</label><div class="_yesno_abc"><button type="button" aria-pressed="true">Yes</button><button type="button">No</button></div></div>`, nil},
		"optional yes/no":            {`<div data-field-path="q1"><label>Newsletter?</label><div class="_yesno_abc"><button type="button">Yes</button><button type="button">No</button></div></div>`, nil},
		"proxy twin":                 {`<fieldset><legend>Will you need sponsorship? *</legend><div><input tabindex="-1" type="text"><button type="button">Yes</button><button type="button">No</button></div></fieldset>`, []string{"Will you need sponsorship? *"}},
		"a résumé slot is no toggle": {`<div data-field-path="resume"><label class="_required_x">Resume</label><div><input tabindex="-1" type="file"><button type="button">Delete file</button><button type="button">Replace</button></div></div>`, nil},
		"required radio group":       {`<fieldset><label class="_required_q">Have you worked for PwC?</label><label><input type="radio" name="pwc" value="y">Yes</label><label><input type="radio" name="pwc" value="n">No</label></fieldset>`, []string{"Have you worked for PwC?"}},
		"answered radio group":       {`<fieldset><label class="_required_q">Have you worked for PwC?</label><label><input type="radio" name="pwc" value="y">Yes</label><label><input type="radio" name="pwc" value="n" checked>No</label></fieldset>`, nil},
	}
	for name, c := range toggles {
		if got := read(c.markup).Toggles; !slices.Equal(got, c.want) {
			t.Errorf("%s: toggles %q, want %q", name, got, c.want)
		}
	}
}

// The probe marks a control's <aside> rail and its job card, as Node's opener
// enumeration does (ZipRecruiter's rail of article cards).
func TestApply_Probe_ControlsNameTheirAsideRailAndJobCard(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Engineer</title><main><button>Apply now</button></main>
<aside><article id="job-card-1"><button>Apply</button></article><article id="job-card-2"><button>Apply</button></article></aside>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, c := range st.Controls {
		got[c.Labels[0]] = append(got[c.Labels[0]], fmt.Sprintf("%v/%d", c.Aside, c.Card))
	}
	if strings.Join(got["Apply now"], ",") != "false/0" || strings.Join(got["Apply"], ",") != "true/1,true/2" {
		t.Fatalf("controls %v", got)
	}
	if c := Categorize(st, false); len(c.Openers) != 1 || c.Openers[0].Labels[0] != "Apply now" {
		t.Fatalf("openers %+v", c.Openers)
	}
}

// Workday redraws the step it is leaving before the next one comes: a press
// has moved the page only once the progress bar names another step.
func TestApply_Workday_PressMovesOnlyWhenTheStepChanges(t *testing.T) {
	bar := func(n int, name string) string {
		return fmt.Sprintf(`<ol data-automation-id="progressBar"><li data-automation-id="progressBarActiveStep"><label>current step %d of 5</label><label>%s</label></li></ol>`, n, name)
	}
	markup := `<title>Apply</title><div data-automation-id="applyFlowPage"><div id="bar">` + bar(1, "My Information") + `</div>
<div id="form"><label for="a">Address Line 2</label><input id="a"><button id="next">Next</button></div></div>
<script>
document.getElementById("next").addEventListener("click", () => {
  document.getElementById("form").insertAdjacentHTML("beforeend", "<span>Saving your information</span>");
  setTimeout(() => {
    document.getElementById("bar").innerHTML = ` + "`" + bar(2, "My Experience") + "`" + `;
    document.getElementById("form").innerHTML = '<h3>Work Experience</h3><button>Add</button><button>Next</button>';
  }, 700);
});
</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil || st.ActiveStep == nil {
		t.Fatalf("probe: %v %v", err, st)
	}
	before, err := Signature(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Eval(ctx, `document.getElementById("next").click()`, nil); err != nil {
		t.Fatal(err)
	}
	w := &walker{s: &session{tab: tab}, tm: WalkTiming{PressWait: 5 * time.Second}}
	if !w.moved(ctx, before, st.ActiveStep) {
		t.Fatal("the press that led to the next step did not move the page")
	}
	after, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if after.ActiveStep == nil || !strings.Contains(*after.ActiveStep, "My Experience") {
		t.Fatalf("the walk went on while the page still showed %q", firstNonEmpty(ptrText(after.ActiveStep), "no step"))
	}
}

func ptrText(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// A Workday step behind its loading page is saving: the walk waits for the
// next step rather than reading the fields of the one going away.
func TestApply_Workday_ASavingStepIsWaitedForNotRead(t *testing.T) {
	bar := func(n int, name string) string {
		return fmt.Sprintf(`<ol data-automation-id="progressBar"><li data-automation-id="progressBarActiveStep"><label>current step %d of 5</label><label>%s</label></li></ol>`, n, name)
	}
	markup := `<title>Apply</title><div data-automation-id="applyFlowPage" id="flow">` + bar(2, "My Experience") +
		`<div><label for="d">Degree</label><input id="d" value="Bachelors"></div><div data-automation-id="applyFlowLoadingPage"><div style="height:10px"></div></div></div>
<script>
setTimeout(() => {
  document.getElementById("flow").innerHTML = ` + "`" + bar(3, "Application Questions") + "`" + ` + '<label for="q">Are you at least 18 years old?</label><input id="q">';
}, 2500);
</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	w := &walker{s: &session{tab: tab}, e: &Exec{Run: &Run{}}, tm: WalkTiming{AfterForm: time.Second, Poll: 200 * time.Millisecond, Saving: 8 * time.Second}}
	st, s := w.look(ctx, true)
	if s != nil {
		t.Fatalf("look stopped: %+v", s)
	}
	if st.FlowLoading || st.ActiveStep == nil || !strings.Contains(*st.ActiveStep, "Application Questions") {
		t.Fatalf("the walk read the saving step: loading=%v step=%q", st.FlowLoading, ptrText(st.ActiveStep))
	}
}

// ADP's phone box is a tel input inside its widget's shadow root: the focus
// lands on the input while the document reports the widget as active.
func TestApply_FocusOfABoxInsideAShadowRootIsTaken(t *testing.T) {
	markup := `<title>Apply</title><form><label>First Name <input id="f"></label><phone-box></phone-box></form>
<script>
customElements.define("phone-box", class extends HTMLElement {
  connectedCallback() { const r = this.attachShadow({mode: "open"}); r.innerHTML = '<select aria-label="Country code"><option>US</option></select><input id="input" type="tel" aria-label="Phone Number">'; }
});
</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	box := -1
	for _, in := range st.Inputs {
		if in.Type == "tel" {
			box = in.I
		}
	}
	if box < 0 {
		t.Fatalf("no tel box read: %+v", st.Inputs)
	}
	if ok, err := Focus(ctx, tab, box); err != nil || !ok {
		t.Fatalf("focus = %v, %v", ok, err)
	}
}

// ADP's phone widget keeps its own model from keydown: typed characters that
// raise no keydown leave the model behind what the box shows.
func TestApply_TypedTextReachesAWidgetThatReadsKeydown(t *testing.T) {
	markup := `<title>Apply</title><form><label>Phone <input id="p" type="tel"></label></form>
<script>
window.model = "";
document.getElementById("p").addEventListener("keydown", (e) => { if (e.key.length === 1) window.model += e.key; });
</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := Focus(ctx, tab, st.Inputs[0].I); err != nil || !ok {
		t.Fatalf("focus = %v, %v", ok, err)
	}
	if err := tab.TypeText(ctx, "+1 843-555-0142"); err != nil {
		t.Fatal(err)
	}
	var got struct{ Model, Value string }
	if err := tab.Eval(ctx, `({Model: window.model, Value: document.getElementById("p").value})`, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "+1 843-555-0142" || got.Value != "+1 843-555-0142" {
		t.Fatalf("model %q value %q", got.Model, got.Value)
	}
}

// ADP's career-site passcode pane is a custom element, not a dialog: the
// probe reads it, with the box in its input's shadow root, as a code pane.
// ADP opens its employer privacy pane inside the passcode pane once the code
// is taken: the pane on top is the dialog, not the passcode under it.
func TestApply_ProbeReadsAPaneOpenedInsideThePasscodePane(t *testing.T) {
	markup := `<title>Career Site</title><cx-verification-otp><div aria-hidden="true"><h5>Please check your email for the security code</h5>
<label>Passcode <input type="text" aria-label="Passcode" value="482916"></label><a href="#">Resend</a></div>
<cx-privacy-statement><sdf-focus-pane><span slot="title">Employer Privacy Policy</span><p>Applicant/Employee Privacy Notice. This notice describes the categories of personal information.</p></sdf-focus-pane></cx-privacy-statement></cx-verification-otp>
<script>customElements.define("sdf-focus-pane", class extends HTMLElement { connectedCallback() { this.style.display = "block"; this.attachShadow({mode: "open"}).innerHTML = '<div role="dialog" aria-modal="true"><h2><slot name="title"></slot></h2><slot></slot><button>Disagree</button><button>Agree</button></div>'; } });</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := classifyCodeEntry(st.Dialogs); ok {
		t.Fatalf("the privacy pane read as a code entry: %+v", st.Dialogs)
	}
	agree := false
	for _, d := range st.Dialogs {
		for _, b := range d.Buttons {
			agree = agree || b.Label == "Agree" && policyTitleRE.MatchString(d.Heading+" "+d.Text)
		}
	}
	if !agree {
		t.Fatalf("no policy dialog with its Agree: %+v", st.Dialogs)
	}
}

// ADP's "Errors Found" notice closes only through dojox's untitled closer.
func TestApply_ProbeReadsTheCloseControlOfANotice(t *testing.T) {
	markup := `<title>Career Site</title><form><label>Candidate Source <select><option>LinkedIn</option></select></label></form>
<div class="dijitDialogFixed dojoxDialog" role="dialog" style="position:absolute;left:40px;top:40px;width:320px;height:120px">
<div class="dojoxDialogTitleBar"><div class="dojoxDialogTitle">Errors Found</div></div>
<div class="dojoxDialogPaneContent">Before you continue, correct the indicated errors.</div>
<div class="dojoxDialogCloser" style="position:absolute;right:4px;top:4px;width:20px;height:20px"><span class="dojoxDialogCloserIcon"></span></div></div>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Dialogs) != 1 || st.Dialogs[0].Closer == nil || len(st.Dialogs[0].Buttons) != 0 {
		t.Fatalf("dialogs %+v", st.Dialogs)
	}
}

func TestApply_ProbeReadsADPsPasscodePane(t *testing.T) {
	markup := `<title>Career Site</title><cx-verification-otp><h5>Please check your email for the security code</h5><span>a***@example.com</span>
<label>Passcode <otp-box></otp-box></label><a>Resend</a></cx-verification-otp>
<script>customElements.define("otp-box", class extends HTMLElement { connectedCallback() { this.attachShadow({mode: "open"}).innerHTML = '<input id="input" type="text" aria-label="Passcode">'; } });</script>`
	ctx, _, _, tab := htmlTab(t, markup)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	ce, ok := classifyCodeEntry(st.Dialogs)
	if !ok || len(ce.boxes) != 1 || ce.hasVerify {
		t.Fatalf("dialogs %+v", st.Dialogs)
	}
}
