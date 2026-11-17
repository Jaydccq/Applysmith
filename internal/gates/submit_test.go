// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

func TestGates_SubmitRegistration(t *testing.T) {
	for _, c := range []struct {
		name, tool string
		declares   bool
		v          Verdict
		code       string
	}{
		{"autofill tool", ToolAutofillApplication, true, Allow, CodeOK},
		{"list tool", ToolApplyFromList, true, Allow, CodeOK},
		{"other tool declares the capability", "send_email", true, Deny, CodeToolNotInSubmitLane},
		{"name contains submit", "submit_form", false, Deny, CodeToolNotInSubmitLane},
		{"name contains withdraw", "Withdraw_application", false, Deny, CodeToolNotInSubmitLane},
		{"read tool", "list_candidacies", false, Allow, CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			got(t, SubmitRegistration(c.tool, c.declares), GateSubmitRegistration, c.v, c.code)
		})
	}
}

func TestGates_SubmitLane_OnlyTwoApplyToolsCarrySubmit(t *testing.T) {
	d := SubmitAuthorization(SubmitAuthInput{Caller: CallerAskAgent, Tool: "other_tool", Submit: true, ExplicitRequest: true})
	got(t, d, GateSubmitAuthorization, Deny, CodeToolNotInSubmitLane)
}

func TestGates_SubmitAuthorization_CallerTable(t *testing.T) {
	apply := ToolAutofillApplication
	for _, c := range []struct {
		name string
		in   SubmitAuthInput
		v    Verdict
		code string
	}{
		{"cli typed submit", SubmitAuthInput{Caller: CallerCLI, Tool: apply, Submit: true, ExplicitRequest: true}, Allow, CodeOK},
		{"cli default is a dry run", SubmitAuthInput{Caller: CallerCLI, Tool: apply}, Deny, CodeSubmitNotRequested},
		{"dashboard with confirmation flag", SubmitAuthInput{Caller: CallerDashboard, Tool: apply, Submit: true, ExplicitRequest: true}, Allow, CodeOK},
		{"dashboard without the flag", SubmitAuthInput{Caller: CallerDashboard, Tool: apply, Submit: true}, Deny, CodeNoExplicitUserRequest},
		{"apply list from the user's list", SubmitAuthInput{Caller: CallerApplyList, Tool: ToolApplyFromList, Submit: true, ExplicitRequest: true}, Allow, CodeOK},
		{"model chose submit", SubmitAuthInput{Caller: CallerAskAgent, Tool: apply, Submit: true}, Deny, CodeNoExplicitUserRequest},
		{"unknown caller", SubmitAuthInput{Caller: "robot", Tool: apply, Submit: true, ExplicitRequest: true}, Deny, CodeUnattendedCaller},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, SubmitAuthorization(c.in), GateSubmitAuthorization, c.v, c.code) })
	}
}

func TestGates_SubmitAuthorization_ExplicitRequestAndSubmitTrueBothRequired(t *testing.T) {
	for _, c := range []struct {
		name             string
		submit, explicit bool
		v                Verdict
		code             string
	}{
		{"both", true, true, Allow, CodeOK},
		{"submit only", true, false, Deny, CodeNoExplicitUserRequest},
		{"request only", false, true, Deny, CodeSubmitNotRequested},
		{"neither", false, false, Deny, CodeSubmitNotRequested},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := SubmitAuthorization(SubmitAuthInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, Submit: c.submit, ExplicitRequest: c.explicit})
			got(t, d, GateSubmitAuthorization, c.v, c.code)
		})
	}
}

func TestGates_Unattended_DurableRunDeniedWhateverTheText(t *testing.T) {
	// The gate has no field for the content a background run read, so the text
	// cannot change the verdict.
	d := SubmitAuthorization(SubmitAuthInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, Submit: true, ExplicitRequest: true, Unattended: true})
	got(t, d, GateSubmitAuthorization, Deny, CodeUnattendedCaller)
}

func TestGates_Unattended_ScheduledTurnRegistryHasNoApplyTools(t *testing.T) {
	d := SubmitAuthorization(SubmitAuthInput{Caller: CallerScheduler, Tool: ToolApplyFromList, Submit: true, ExplicitRequest: true})
	got(t, d, GateSubmitAuthorization, Deny, CodeUnattendedCaller)
	row := NewAuditRow(t0, d, AuditContext{Caller: CallerScheduler, Tool: ToolApplyFromList})
	if row.Caller != CallerScheduler || row.Verdict != Deny {
		t.Fatalf("row = %+v", row)
	}
}

func TestGates_Unattended_TelegramApplyToolsDeniedBothSubmitValues(t *testing.T) {
	for _, tool := range []string{ToolAutofillApplication, ToolApplyFromList} {
		for _, c := range []struct {
			submit bool
			code   string
		}{{true, CodeSubmitDashboardOnly}, {false, CodeApplyDashboardOnly}} {
			d := SubmitAuthorization(SubmitAuthInput{Caller: CallerTelegram, Tool: tool, Submit: c.submit, ExplicitRequest: true})
			got(t, d, GateSubmitAuthorization, Deny, c.code)
		}
	}
	d := SubmitAuthorization(SubmitAuthInput{Caller: CallerTelegram, Tool: ToolApplyFromList, Submit: true})
	if !strings.Contains(d.Reason, "dashboard") {
		t.Fatalf("reason %q does not point to the dashboard", d.Reason)
	}
}

func TestGates_HeldSubmitConfirmation(t *testing.T) {
	for _, c := range []struct {
		name string
		in   ConfirmationInput
		v    Verdict
		code string
	}{
		{"new user message", ConfirmationInput{Caller: CallerAskAgent, NewUserMessage: true}, Allow, CodeOK},
		{"remote device", ConfirmationInput{Caller: CallerDashboard, FromRemoteDevice: true, NewUserMessage: true}, Deny, CodeRemoteCannotConfirm},
		{"telegram", ConfirmationInput{Caller: CallerTelegram, NewUserMessage: true}, Deny, CodeSubmitDashboardOnly},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, HeldSubmitConfirmation(c.in), GateHeldSubmitConfirmation, c.v, c.code) })
	}
}

func TestGates_AgentSubmit_ConfirmationIsNewUserMessageNeverModelText(t *testing.T) {
	got(t, HeldSubmitConfirmation(ConfirmationInput{Caller: CallerAskAgent, NewUserMessage: false}), GateHeldSubmitConfirmation, Deny, CodeConfirmationNotUserMessage)
}

const jobURL = "https://jobs.example.test/apply/42"

func TestGates_AgentSubmit_NoDoorSkipsTheGate(t *testing.T) {
	// Every call to either apply tool reaches the same function; an unknown
	// tool is refused rather than skipped.
	got(t, AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: "legacy_apply", PostingURL: jobURL}), GateAgentSubmit, Deny, CodeToolNotInSubmitLane)
}

func TestGates_AgentSubmit_OutsideContentReadHolds(t *testing.T) {
	d := AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: "https://other.example.test/jobs/7", UserMessage: "apply to https://boards.example.test/x/1", OutsideContentRead: true})
	got(t, d, GateAgentSubmit, Hold, CodeOutsideContentRead)
	if !strings.Contains(d.Reason, "https://other.example.test/jobs/7") || !strings.Contains(strings.ToLower(d.Reason), "do not retry") {
		t.Fatalf("hold reason %q must carry the exact URL and the no-retry instruction", d.Reason)
	}
}

func TestGates_AgentSubmit_HostNotNamedNotListedHolds(t *testing.T) {
	for _, c := range []struct {
		name string
		in   AgentSubmitInput
		v    Verdict
		code string
	}{
		{"host named with a trailing slash", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "go to jobs.example.test/ please", PostingURL: jobURL}, Allow, CodeHostNamedOrListed},
		{"www is ignored", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "see www.jobs.example.test/x", PostingURL: jobURL}, Allow, CodeHostNamedOrListed},
		{"nothing names it", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "apply to the one I found", PostingURL: jobURL}, Hold, CodeHostNotNamedNotListed},
		{"shared host listed for another company", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "apply", PostingURL: "https://boards.example.test/other/jobs/2", ListURLs: []string{"https://boards.example.test/acme/jobs/1"}}, Hold, CodeHostNotNamedNotListed},
		{"exact URL on the list", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "apply", PostingURL: "https://boards.example.test/acme/jobs/1", ListURLs: []string{"https://boards.example.test/acme/jobs/1/"}}, Allow, CodeHostNamedOrListed},
		{"not an http URL", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "apply", PostingURL: "javascript:alert(1)"}, Hold, CodeURLNotHTTP},
		{"ftp URL", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "ftp://jobs.example.test/apply/42", PostingURL: "ftp://jobs.example.test/apply/42"}, Hold, CodeURLNotHTTP},
		{"userinfo in the posting URL", AgentSubmitInput{Caller: CallerAskAgent, UserMessage: "apply", PostingURL: "https://jobs.example.test@evil.test/x"}, Hold, CodeURLNotHTTP},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.in.Tool = ToolAutofillApplication
			d := AgentSubmit(c.in)
			got(t, d, GateAgentSubmit, c.v, c.code)
			// The list vouches for a posting, never for a host, and the hold says so:
			// a shared host listed for another company is "this posting is not listed".
			if c.code == CodeHostNotNamedNotListed && (!strings.Contains(d.Reason, "this posting is not on today's apply list") || strings.Contains(d.Reason, "host is neither")) {
				t.Fatalf("hold reason %q", d.Reason)
			}
		})
	}
}

func TestGates_AgentSubmit_TokenizerRejectsLookalikeAndUserinfo(t *testing.T) {
	msg := "use https://jobs.example.test@evil.test/x and example.test.evil.test/y"
	got(t, AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: jobURL, UserMessage: msg}), GateAgentSubmit, Hold, CodeHostNotNamedNotListed)
	for _, c := range []struct {
		name, msg string
		want      bool
	}{
		{"host inside another URL's path", "see https://news.example.test/redirect/jobs.example.test/apply", false},
		{"host inside another URL's query", "see https://news.example.test/r?u=jobs.example.test/x", false},
		{"bare host without a path is prose", "jobs.example.test is fine", false},
		{"quoted URL", `apply "https://jobs.example.test/apply/9"`, true},
		{"bracketed URL", "apply (https://jobs.example.test/apply/9)", true},
		{"trailing punctuation", "apply at https://jobs.example.test/apply/9.", true},
		{"URL glued to CJK text", "apply " + string([]rune{0x8bf7}) + "https://jobs.example.test/apply/9" + string([]rune{0x8c22}), true},
		{"uppercase host", "JOBS.EXAMPLE.TEST/apply", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			named := false
			for _, r := range urlTokens(c.msg) {
				named = named || r.host == "jobs.example.test"
			}
			if named != c.want {
				t.Fatalf("host named = %v, want %v", named, c.want)
			}
		})
	}
}

func TestGates_AgentSubmit_ExactURLNamedAllowsOverOutsideContent(t *testing.T) {
	for _, c := range []struct {
		name, posting, msg string
		v                  Verdict
		code               string
	}{
		{"exact URL with outside content read", jobURL, "please apply to " + jobURL, Allow, CodeExactURLNamed},
		{"trailing slash ignored", jobURL, "apply to " + jobURL + "/", Allow, CodeExactURLNamed},
		{"default port ignored", jobURL, "apply to https://jobs.example.test:443/apply/42", Allow, CodeExactURLNamed},
		{"path is case sensitive", jobURL, "apply to https://jobs.example.test/Apply/42", Hold, CodeOutsideContentRead},
		{"query must be identical", jobURL + "?id=1", "apply to " + jobURL + "?id=2", Hold, CodeOutsideContentRead},
		{"query identical", jobURL + "?id=1", "apply to " + jobURL + "?id=1", Allow, CodeExactURLNamed},
		{"root path vouches for nothing", "https://jobs.example.test/", "apply to https://jobs.example.test/", Hold, CodeOutsideContentRead},
		{"other port", jobURL, "apply to https://jobs.example.test:8443/apply/42", Hold, CodeOutsideContentRead},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: c.posting, UserMessage: c.msg, OutsideContentRead: true})
			got(t, d, GateAgentSubmit, c.v, c.code)
		})
	}
}

func TestGates_AgentSubmit_HoldWhenOutsideContentOrHostNotNamedClassifierIrrelevant(t *testing.T) {
	in := AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: jobURL, UserMessage: "apply"}
	got(t, AgentSubmit(in), GateAgentSubmit, Hold, CodeHostNotNamedNotListed)
	in.OutsideContentRead = true
	got(t, AgentSubmit(in), GateAgentSubmit, Hold, CodeOutsideContentRead)
}

func TestGates_AgentSubmit_BatchOverUserList(t *testing.T) {
	got(t, AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolApplyFromList}), GateAgentSubmit, Allow, CodeBatchOverUserList)
	got(t, AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolApplyFromList, OutsideContentRead: true}), GateAgentSubmit, Hold, CodeOutsideContentRead)
}

func TestGates_AgentSubmit_LongURLIsNotClippedInTheHold(t *testing.T) {
	long := "https://jobs.example.test/apply/" + strings.Repeat("a", 400)
	d := AgentSubmit(AgentSubmitInput{Caller: CallerAskAgent, Tool: ToolAutofillApplication, PostingURL: long, UserMessage: "apply"})
	got(t, d, GateAgentSubmit, Hold, CodeHostNotNamedNotListed)
	if strings.Contains(d.Reason, "https://jobs.example.test/apply/a") {
		t.Fatalf("a clipped URL must not appear in %q", d.Reason)
	}
}

func TestGates_Controls_WithdrawDeniedDiscardDeleteNeverWalked(t *testing.T) {
	for _, c := range []struct {
		name string
		in   ControlInput
		v    Verdict
		code string
	}{
		{"withdraw in the walk", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Withdraw application"}}, Deny, CodeWithdrawControl},
		{"withdraw at the submit stage, fully authorized", ControlInput{Stage: StageSubmit, Found: true, Labels: []string{"Withdraw application"}, SubmitAuthorized: true, ClaimHeld: true}, Deny, CodeWithdrawControl},
		{"withdraw hidden in the value", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Continue", "WITHDRAW"}}, Deny, CodeWithdrawControl},
		{"discard application", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Discard"}}, Deny, CodeDiscardControl},
		{"delete application", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Delete application"}}, Deny, CodeDiscardControl},
		{"delete draft", ControlInput{Stage: StageSubmit, Found: true, Labels: []string{"Delete my draft"}, SubmitAuthorized: true, ClaimHeld: true}, Deny, CodeDiscardControl},
		{"unattended run, even a navigation control", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Continue"}, Unattended: true}, Deny, CodeUnattendedCaller},
		{"continue", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Continue"}}, Allow, CodeOK},
		{"next", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Next"}}, Allow, CodeOK},
		{"apply modal opener", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Apply now"}}, Allow, CodeOK},
		{"opener of a draft the site holds", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Complete Your Application"}}, Allow, CodeOK},
		{"LinkedIn's safety reminder goes on to the form", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Continue applying"}, InDialog: true}, Allow, CodeOK},
		{"the reminder's other button is not navigation", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Review job post"}, InDialog: true}, Deny, CodeNotNavigation},
		{"submit label in the walk (dry run)", ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Submit application"}}, Deny, CodeSubmitInWalk},
	} {
		t.Run(c.name, func(t *testing.T) { got(t, PressControl(c.in), GateControlPress, c.v, c.code) })
	}
	// With a withdraw button and a continue button on one page, only Continue
	// is a navigation candidate.
	if ClassifyControl("Withdraw application") == ControlNavigation || ClassifyControl("Continue") != ControlNavigation {
		t.Fatal("classification is wrong for the status page")
	}
}

func TestGates_Submit_ControlLabelReadAtPressNothingClickedOtherwise(t *testing.T) {
	ok := ControlInput{Stage: StageSubmit, Found: true, Labels: []string{"Submit application"}, SubmitAuthorized: true, ClaimHeld: true}
	for _, c := range []struct {
		name   string
		mutate func(*ControlInput)
		v      Verdict
		code   string
	}{
		{"final Submit", func(*ControlInput) {}, Allow, CodeOK},
		{"mid-wizard Next on the same selector", func(i *ControlInput) { i.Labels = []string{"Next"} }, Deny, CodeSubmitLabelMismatch},
		{"no matching control", func(i *ControlInput) { i.Found = false }, Deny, CodeControlNotFound},
		{"authorization missing", func(i *ControlInput) { i.SubmitAuthorized = false }, Deny, CodeSubmitNotAuthorized},
		{"claim missing", func(i *ControlInput) { i.ClaimHeld = false }, Deny, CodeSubmitClaimMissing},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.mutate(&in)
			got(t, PressControl(in), GateControlPress, c.v, c.code)
		})
	}
}

// The Apply wording of systems that reached no form in a live survey is
// navigation; the pattern is anchored on the whole label, so Submit, Withdraw,
// Finish later and sentences that merely contain Apply stay out.
func TestGates_Submit_ApplyOpenerVocabularyIsAWholeLabelPattern(t *testing.T) {
	for _, l := range []string{
		"I'm interested", "I’m interested", "Apply To Position", "Apply Online", "Apply for this position online",
		"Apply for this job online", "Apply for this job", "Apply to job", "Apply on company site", "Apply on the employer's website",
		"Apply at Example Corp", "Quick Apply", "External apply", "Begin application", "Continue to application", "Apply here",
	} {
		if got := ClassifyControl(l); got != ControlNavigation {
			t.Errorf("%q = %s, want navigation", l, got)
		}
	}
	// A control that carries several labels is navigation only when each one is.
	if ClassifyControl("Apply Online", "Apply for this position online") != ControlNavigation {
		t.Error("the Taleo button (value and title) is not navigation")
	}
	if ClassifyControl("Apply", "Apply for Software Engineer") != ControlNavigation {
		t.Error("the Dayforce button (text and aria-label) is not navigation")
	}
	if ClassifyControl("Apply", "Apply : Software Engineer I") != ControlNavigation {
		t.Error("the iCIMS career-site link (text and aria-label) is not navigation")
	}
	if ClassifyControl("Apply Now", "Apply Now Staff Software Development Engineer - Java") != ControlNavigation {
		t.Error("the Phenom posting link (text and aria-label) is not navigation")
	}
	// Expedia's careers page (live): the visible link's aria-label is only the new-tab notice.
	if ClassifyControl("Apply Now", "(opens in a new tab)") != ControlNavigation || ClassifyControl("Apply now - opens in new window") != ControlNavigation {
		t.Error("a new-tab notice beside or after an Apply label is not navigation")
	}
	if ClassifyControl("(opens in a new tab)") == ControlNavigation || ClassifyControl("Withdraw (opens in a new tab)") != ControlWithdraw {
		t.Error("a new-tab notice alone is navigation, or hides a Withdraw")
	}
	if ClassifyControl("Apply Now Staff Software Engineer") == ControlNavigation || ClassifyControl("Apply Now", "Apply now for job alerts") == ControlNavigation {
		t.Error("an Apply now form counts only beside a bare Apply and never when it names an alert")
	}
	if ClassifyControl("Apply : Software Engineer I") == ControlNavigation || ClassifyControl("Apply", "Apply - job alerts") == ControlNavigation {
		t.Error("a colon or dash form counts only beside a bare Apply and never when it names an alert")
	}
	if ClassifyControl("Apply for Software Engineer") == ControlNavigation || ClassifyControl("Apply", "Apply for job alerts") == ControlNavigation {
		t.Error("a named Apply label counts only beside a bare Apply and never when it names an alert")
	}
	// Elastic's posting link: a named Apply whose address alone may be opened.
	for label, want := range map[string]bool{
		"Apply Now for Agentic AI Engineer": true, "Apply for Software Engineer": true, "Apply : Software Engineer I": true,
		"Apply Now": false, "Apply for job alerts": false, "Apply for Software Engineer and submit": false, "Apply to this job with LinkedIn": false,
		"Apply now and submit your application": false, "Apply for jobs by email": false,
	} {
		if NamedApply(label) != want {
			t.Errorf("NamedApply(%q) = %v", label, !want)
		}
	}
	if ClassifyControl("Apply Online", "Withdraw application") != ControlWithdraw {
		t.Error("a Withdraw label beside an Apply label did not win")
	}
	for _, l := range []string{
		"Apply later", "Finish later", "Save and finish later", "Apply with LinkedIn", "Apply by email", "Apply filters", "Applying is easy and fun",
		"Apply for jobs by email", "Apply to this job with LinkedIn", "Apply for job alerts", "Apply for Software Engineer and submit", "Not interested", "I'm interested in alerts", "Interested?", "Apply and submit", "Submit application", "Apply now and submit your application",
	} {
		if got := ClassifyControl(l); got == ControlNavigation {
			t.Errorf("%q read as navigation", l)
		}
	}
	if d := PressControl(ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Apply Online", "Apply for this position online"}}); !d.Allowed() {
		t.Errorf("the walk may not press the Taleo opener: %+v", d)
	}
	if d := PressControl(ControlInput{Stage: StageWalk, Found: true, Labels: []string{"Apply and submit"}}); d.Allowed() {
		t.Errorf("a Submit-reading Apply label was pressed by the walk: %+v", d)
	}
}
