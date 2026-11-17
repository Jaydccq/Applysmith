//go:build browser

package apply

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"applysmith/internal/engine"
)

// domScenario serves one page for every address of a board and returns a tab
// opened on it, for the checks that read the real DOM.
func domScenario(t *testing.T, page func(b *board, r *http.Request) string) (*scenario, Tab) {
	t.Helper()
	s := newScenario(t, "custom", modeAccept, Hooks{})
	s.b.render = page
	win := LaneWindow{Lane: s.lane}
	tab, err := win.OpenTab(context.Background(), "dom-test", engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tab.Close(context.Background()) })
	if _, err := tab.Navigate(context.Background(), s.url); err != nil {
		t.Fatal(err)
	}
	return s, tab
}

func pageLabels(st *PageState) []string {
	var out []string
	for _, c := range st.Controls {
		out = append(out, strings.Join(c.Labels, "/"))
	}
	return out
}

func TestE2E_Probe_ChatWidgetShadowUploadIsFurnitureAndTheRealApplyIsFollowed(t *testing.T) {
	t.Parallel()
	page := func(b *board, r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/apply/") {
			return `<title>Apply - Platform Engineer</title><h1>Platform Engineer</h1><form id="f" onsubmit="return false">` + fieldNames + fieldQuestions +
				`<button type="button" onclick="sendApplication()">Submit application</button></form>` + submitScript(b, successHTML)
		}
		return `<title>Platform Engineer - Example Corp</title><h1>Platform Engineer</h1><p>Build platform tools.</p>
<apply-widget id="w"></apply-widget>
<script>
const root = document.getElementById('w').attachShadow({mode: 'open'});
root.innerHTML = '<div><p>Hi, I am Nora. Upload a resume to get started.</p><input type="file" name="candidate_upload_resume"></div>';
</script>
<a href="/apply/` + b.marker + `">Apply for this position</a>`
	}
	s, tab := domScenario(t, page)
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() || len(st.Inputs) != 1 || !st.Inputs[0].Furniture {
		t.Fatalf("inputs %+v: the assistant's upload is not the application", st.Inputs)
	}
	if got := Categorize(st, false).Openers; len(got) != 1 || got[0].Labels[0] != "Apply for this position" {
		t.Fatalf("openers %v", pageLabels(st))
	}
	run := s.run(dryInput(s.url))
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || len(run.Pages) != 1 || !strings.Contains(stageOf(run, StageFill).Detail, "ready for Submit") {
		t.Fatalf("outcome %+v, %d pages", run.Outcome, len(run.Pages))
	}
}

func TestE2E_Probe_FurnitureAndControlsReadAsThePageShowsThem(t *testing.T) {
	t.Parallel()
	_, tab := domScenario(t, func(b *board, r *http.Request) string {
		return `<title>Careers at Example Corp</title><h1>Careers</h1>
<section id="tc"><h3>Join our talent community</h3><p>Get job alerts.</p><label for="e">Email address</label><input id="e" type="email"><button type="button">Join our talent community</button></section>
<ukg-button id="u">Apply now</ukg-button>
<button id="r"><span style="display:none">Apply</span><span>Apply Now</span></button>
<div role="menu"><a role="menuitem" href="/cas">Apply Now</a></div>`
	})
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasForm() || len(st.Inputs) != 1 || !st.Inputs[0].Furniture {
		t.Fatalf("inputs %+v: a talent-community email box is furniture", st.Inputs)
	}
	var custom, responsive, menu bool
	for _, c := range st.Controls {
		switch {
		case c.Tag == "ukg-button" && c.Labels[0] == "Apply now":
			custom = true
		case c.Tag == "button" && len(c.Labels) == 1 && c.Labels[0] == "Apply Now":
			responsive = true
		case c.Role == "menuitem" && c.Labels[0] == "Apply Now":
			menu = true
		}
	}
	if !custom || !responsive || !menu {
		t.Fatalf("custom %v responsive %v menu %v in %v", custom, responsive, menu, pageLabels(st))
	}
	if n := len(Categorize(st, false).Openers); n != 3 {
		t.Fatalf("%d openers in %v", n, pageLabels(st))
	}
}

// A page's own mark decides what the refusal reader takes, whatever the words;
// the words ("please enter", "please select", "highlighted") only qualify a
// message the page did not mark as a stall hint.
func TestE2E_Probe_RefusalWordsAreReadWhenMarkedAndHintsWhenUnmarked(t *testing.T) {
	t.Parallel()
	_, tab := domScenario(t, func(b *board, r *http.Request) string {
		return `<title>Apply</title><h1>Platform Engineer</h1><form onsubmit="return false"><label for="s">State</label><input id="s" name="state" required>
<div role="alert">Please enter a value for State</div>
<span class="text-red-600">Please select a country</span>
<span class="text-red-600">The highlighted fields need your attention</span>
<span class="text-red-600">Thank you for applying</span>
<button type="button">Next</button></form>`
	})
	st, err := Probe(context.Background(), tab)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Validation) != 1 || st.Validation[0] != "Please enter a value for State" {
		t.Fatalf("validation %v", st.Validation)
	}
	if got := strings.Join(st.Stall.Messages, " | "); got != "Please select a country | The highlighted fields need your attention" {
		t.Fatalf("stall hints %q", got)
	}
}

func TestE2E_Probe_FormValidationRevalidateReleasesHiddenAsksVisibleNeverSubmits(t *testing.T) {
	t.Parallel()
	_, tab := domScenario(t, func(b *board, r *http.Request) string {
		return `<title>Apply</title><h1>Platform Engineer</h1>
<form id="f" onsubmit="window.__submitted = (window.__submitted||0)+1; return false"><input id="a" name="email" value="alex@example.com"><div style="display:none"><input id="h" name="followUp"></div>
<input type="submit" id="sub" value="Submit" disabled></form>
<script>
const calls = []; window.__calls = calls;
const fields = {email: {enabled: true}, followUp: {enabled: true}};
const els = {email: [document.getElementById('a')], followUp: [document.getElementById('h')]};
const fv = {options: {fields}, getFieldElements: (n) => els[n], isValidField: (n) => n !== 'followUp' || fields.followUp.enabled === false,
  enableFieldValidators: (n, on) => { calls.push('enable:' + n + ':' + on); fields[n].enabled = on; },
  revalidateField: (n) => calls.push('revalidate:' + n), validate: () => calls.push('validate'), disableSubmitButtons: () => calls.push('disableSubmitButtons')};
window.jQuery = (el) => ({data: (k) => k === 'formValidation' ? fv : null});
</script>`
	})
	ctx := context.Background()
	rv, err := revalidateForm(ctx, tab)
	if err != nil || rv == nil {
		t.Fatalf("revalidation %+v err %v", rv, err)
	}
	if strings.Join(rv.HiddenReleased, ",") != "followUp" || strings.Join(rv.Revalidated, ",") != "email" || len(rv.Invalid) != 0 {
		t.Fatalf("%+v", rv)
	}
	var calls []string
	if err := tab.Eval(ctx, `window.__calls`, &calls); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "enable:followUp:false,revalidate:email" {
		t.Fatalf("calls %s: never validate, never the buttons", got)
	}
	var disabled bool
	var submitted *int
	_ = tab.Eval(ctx, `document.getElementById('sub').disabled`, &disabled)
	_ = tab.Eval(ctx, `window.__submitted || null`, &submitted)
	if !disabled || submitted != nil {
		t.Fatalf("disabled %v submitted %v: the page's own verdict decides, nothing is submitted", disabled, submitted)
	}
	// A page with no such instance answers nothing.
	if _, err := tab.Navigate(ctx, "about:blank"); err == nil {
		if rv, err := revalidateForm(ctx, tab); err != nil || rv != nil {
			t.Fatalf("blank page: %+v %v", rv, err)
		}
	}
}
