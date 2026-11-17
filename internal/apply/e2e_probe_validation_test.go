//go:build browser

package apply

import (
	"slices"
	"testing"
)

// NVIDIA (Workday, live): the file field's live announcer is a role=alert that
// says the upload succeeded; read as a refusal it made the Review's disabled
// Submit final before Workday enabled it. A success announcement is not a
// validation word; an alert that refuses still is.
func TestApply_Probe_AnUploadSuccessAnnouncementIsNoValidationWord(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><div data-automation-id="ariaLive" style="display:block"><div data-automation-id="ariaLiveMessage" aria-atomic="true" aria-live="polite" role="alert">resume.pdf successfully uploaded</div></div>
<div role="alert">Please enter a value for State</div><label>State <input id="state"></label><button type="button" disabled>Submit</button>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Validation, []string{"Please enter a value for State"}) {
		t.Fatalf("validation %q", st.Validation)
	}
}

// Boeing (Taleo, live): the step after the sign-up holds a résumé box whose
// class is "file-uploader"; read as a loader it kept the page loading until the
// account stage gave up on a sign-up that had worked. A spinner still loads.
func TestApply_Probe_AFileUploaderIsNoLoader(t *testing.T) {
	step := `<title>Apply</title><h2>2. Your Information</h2><div class="form-group file-uploader"><button type="button">Upload resume</button></div><label>First Name <input name="first"></label>`
	for _, c := range []struct {
		markup string
		want   bool
	}{{step, false}, {step + `<div class="page-loader" style="width:40px;height:40px"></div>`, true}} {
		ctx, _, _, tab := htmlTab(t, c.markup)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		if st.Loading != c.want {
			t.Errorf("loading %v, want %v", st.Loading, c.want)
		}
	}
}

// Optiver draws its Apply link's words twice for a hover animation, one copy
// hidden from assistive tech: the link reads "Apply" once, when the page is
// read and again at the press, and opens the application.
func TestApply_Probe_AnApplyLinkDrawnTwiceReadsOnceAndOpens(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<main><h1>Software Engineer</h1><p>Austin</p>
<a href="/join-us/jobs/software-engineer/apply" style="display:inline-block;position:relative;overflow:hidden"><div><div aria-hidden="true" style="position:absolute;inset:0;transform:translateY(100%)"><span>Apply</span></div><div><span>Apply</span></div></div></a></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, false)
	if len(cats.Openers) != 1 || cats.Openers[0].Labels[0] != "Apply" {
		t.Fatalf("openers %+v, controls %+v", cats.Openers, st.Controls)
	}
	// The read the press gate is given at the press says the same words.
	if l, err := Locate(ctx, tab, cats.Openers[0].I); err != nil || len(l.Labels) == 0 || l.Labels[0] != "Apply" {
		t.Fatalf("located %+v, %v", l, err)
	}
}

// Toast's careers site draws its form's last button with three states, two
// of them hidden until it is pressed, beside a spinner icon hidden from
// assistive tech: the button reads only the words it shows, "Apply now!",
// when the page is read and at the press, and it is the form's final Apply,
// never a Next, though it is named next_step.
func TestApply_Probe_AButtonReadsOnlyTheStateItShows(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<style>.submit-in-progress,.submit-finished{display:none}</style><main><h1>Manager, Software Engineering</h1>
<form id="new_form_submission_2_0"><label for="fn">First name</label><input id="fn" name="first_name"><label for="cv">Resume</label><input id="cv" type="file" name="resume">
<button name="next_step" type="submit" id="form_submit_2_0"><span class="submit-state submit-start">Apply now!</span><span class="submit-state submit-in-progress"><i class="fa fa-spinner" aria-hidden="true"></i> Please wait... </span><span class="submit-state submit-finished"><i class="fa fa-check" aria-hidden="true"></i> Done </span></button></form></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, true)
	if len(cats.Submit) != 1 || !cats.Submit[0].FinalApply || cats.Submit[0].Labels[0] != "Apply now!" || len(cats.Nav) != 0 {
		t.Fatalf("categories %+v, controls %+v", cats, st.Controls)
	}
	if l, err := Locate(ctx, tab, cats.Submit[0].I); err != nil || len(l.Labels) == 0 || l.Labels[0] != "Apply now!" {
		t.Fatalf("located %+v, %v", l, err)
	}
}

// Stanford's Oracle email step says the visitor may choose "your preferred way
// to stay in touch on the next page": a form that long is the application's
// step, while a short form under the same words is a job-alert box.
func TestApply_Probe_AnApplicationStepThatMentionsStayingInTouchIsAForm(t *testing.T) {
	for _, c := range []struct {
		name, markup string
		form         bool
	}{
		{"application step", `<title>Software Developer 1 - Careers</title><main><form><section><h2>Let's get started</h2>
<p>Share your email or phone number to begin. Your profile will stay current and automatically update as you add new details. You can choose your preferred way to stay in touch on the next page. Please note that our system is currently designed to send SMS updates and notifications to U.S. phone numbers only; for international updates, please use your email address. For university employees, please apply through the internal marketplace.</p>
<label for="primary-email-1">Email Address</label><input id="primary-email-1" name="primary-email" type="email" autocomplete="email" aria-required="true">
<button type="button">Cancel</button><button type="button">Next</button></section></form></main>`, true},
		{"job-alert box", `<title>Careers</title><main><form><h3>Stay in touch</h3><p>Hear about new roles.</p>
<label for="e">Email Address</label><input id="e" type="email"><button type="submit">Sign up</button></form></main>`, false},
	} {
		ctx, _, _, tab := htmlTab(t, c.markup)
		st, err := Probe(ctx, tab)
		if err != nil {
			t.Fatal(err)
		}
		if st.HasForm() != c.form {
			t.Errorf("%s: has a form %v, want %v (inputs %+v, forms %+v)", c.name, st.HasForm(), c.form, st.Inputs, st.Forms)
		}
	}
}

// IBM (IBMid, live): the password step offers "Create an IBMid" as a link, and
// the probe kept only links whose words named an account or a profile, so the
// account stage saw no way to create one.
func TestApply_Probe_ACreateLinkNamedForTheSitesAccountIsKept(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>IBMid - Sign in or create an IBMid</title><form><div class="heading">Log in to IBM</div>
<div>Don’t have an account? <a href="https://www.example.com/account/reg/signup">Create an IBMid</a></div>
<label for="pw">Password</label><input id="pw" name="password" type="password"><button type="submit">Log in</button>
<a href="https://www.example.com/reset-password">Forgot password?</a></form>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	if w := findWall(st); w == nil || w.create == nil {
		t.Fatalf("wall %+v, controls %+v", w, st.Controls)
	}
}

// Fivetran's careers page (Webflow tabs): the "Application" tab is a link
// with role="tab" whose panel holds the Greenhouse frame. A tab link only
// switches the panel, so it is read as a control whatever its words; another
// link that says neither Apply nor Next is still not one.
func TestApply_Probe_ATabLinkIsAControl(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<main><h1>Manager Software Engineering</h1><div role="tablist">
<a role="tab" href="#pane-0" aria-selected="true">About the role</a><a role="tab" href="#pane-1" aria-selected="false" tabindex="-1">Application</a></div>
<div id="pane-0" role="tabpanel"><p>About us</p><a href="/roles">View all open roles</a></div></main>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	cats := Categorize(st, false)
	if len(cats.Openers) != 1 || cats.Openers[0].Labels[0] != "Application" || cats.Openers[0].Role != "tab" {
		t.Fatalf("openers %+v, controls %+v", cats.Openers, st.Controls)
	}
	for _, c := range st.Controls {
		if c.Labels[0] == "View all open roles" {
			t.Fatalf("a plain link read as a control: %+v", c)
		}
	}
}
