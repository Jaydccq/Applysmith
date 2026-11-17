// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the submit gates.
const (
	GateSubmitRegistration     Gate = "submit_registration"
	GateSubmitAuthorization    Gate = "submit_authorization"
	GateAgentSubmit            Gate = "agent_submit"
	GateHeldSubmitConfirmation Gate = "held_submit_confirmation"
	GateControlPress           Gate = "control_press"

	CodeSubmitNotRequested    = "submit_not_requested"
	CodeNoExplicitUserRequest = "no_explicit_user_request"
	CodeUnattendedCaller      = "unattended_caller"
	CodeSubmitDashboardOnly   = "submit_dashboard_only"
	CodeApplyDashboardOnly    = "apply_dashboard_only"

	CodeURLNotHTTP            = "url_not_http"
	CodeExactURLNamed         = "exact_url_named"
	CodeOutsideContentRead    = "outside_content_read"
	CodeHostNotNamedNotListed = "host_not_named_and_not_listed"
	CodeHostNamedOrListed     = "host_named_or_listed"
	CodeBatchOverUserList     = "batch_over_user_list"

	CodeRemoteCannotConfirm        = "remote_cannot_confirm"
	CodeConfirmationNotUserMessage = "confirmation_not_user_message"

	CodeWithdrawControl     = "withdraw_control"
	CodeDiscardControl      = "discard_control"
	CodeSubmitInWalk        = "submit_in_walk"
	CodeSubmitNotAuthorized = "submit_not_authorized"
	CodeSubmitClaimMissing  = "submit_claim_missing"
	CodeControlNotFound     = "control_not_found"
	CodeSubmitLabelMismatch = "submit_label_mismatch"
	CodeNotNavigation       = "control_not_navigation"
	CodeMixedScriptLabel    = "mixed_script_label"
	CodeNavigationInRung    = "navigation_in_rung"
	CodeDestructiveLabel    = "destructive_label"
)

// SubmitRegistration refuses any tool other than the two apply tools that
// declares the submit capability, or whose name contains submit or withdraw.
func SubmitRegistration(tool string, declaresSubmit bool) Decision {
	low := strings.ToLower(tool)
	if !inSubmitLane(tool) && (declaresSubmit || strings.Contains(low, "submit") || strings.Contains(low, "withdraw")) {
		return decide(GateSubmitRegistration, Deny, CodeToolNotInSubmitLane, "tool %q cannot carry the submit capability; only the two apply tools can", tool)
	}
	return decide(GateSubmitRegistration, Allow, CodeOK, "tool %q registers without the submit capability", tool)
}

// SubmitAuthInput holds the facts the submit authorization gate reads.
type SubmitAuthInput struct {
	Caller Caller
	Tool   string
	// Submit is the run's submit input; absent reads false.
	Submit bool
	// ExplicitRequest is derived by the caller from the user's own act, never
	// from a value the model chose: the Ask Agent's guard chain from the
	// user's message, the CLI from a typed --submit, the dashboard from the
	// confirmation flag its dialog sets.
	ExplicitRequest bool
	// Unattended marks a durable or background run, a scheduled or digest
	// turn, the read-only server, a remote device, or any run started without
	// a user-confirmed submit request.
	Unattended bool
	// Demo is the workspace's demo marker.
	Demo bool
}

// SubmitAuthorization decides whether a run may press a final Submit. A deny
// with CodeSubmitNotRequested is a dry run, not a failure.
func SubmitAuthorization(in SubmitAuthInput) Decision {
	const g = GateSubmitAuthorization
	switch {
	case in.Demo:
		return demoDeny(g, "a final Submit press")
	case !inSubmitLane(in.Tool):
		return decide(g, Deny, CodeToolNotInSubmitLane, "tool %q is not in the submit lane", in.Tool)
	case in.Caller == CallerTelegram && in.Submit:
		return decide(g, Deny, CodeSubmitDashboardOnly, "Submission is confirmed in the dashboard; Telegram cannot submit an application.")
	case in.Caller == CallerTelegram:
		return decide(g, Deny, CodeApplyDashboardOnly, "Applications are started in the dashboard; Telegram cannot start one.")
	case in.Unattended || in.Caller == CallerScheduler || !ValidCaller(in.Caller):
		return decide(g, Deny, CodeUnattendedCaller, "an unattended or unrecognised caller cannot use the submit capability")
	case !in.Submit:
		return decide(g, Deny, CodeSubmitNotRequested, "submit is not set, so this run is a dry run and stops at the Submit control")
	case !in.ExplicitRequest:
		return decide(g, Deny, CodeNoExplicitUserRequest, "the user did not explicitly ask to apply; a submit the model chose is refused")
	}
	return decide(g, Allow, CodeOK, "explicit user request and submit are both set")
}

// ConfirmationInput holds the facts about a confirmation of a held submit.
type ConfirmationInput struct {
	Caller Caller
	// FromRemoteDevice is true for a paired remote credential.
	FromRemoteDevice bool
	// NewUserMessage is true only for a fresh message typed by the user or a
	// dashboard confirmation card; the model's own text never counts.
	NewUserMessage bool
}

// HeldSubmitConfirmation decides whether a confirmation may release a held
// submit.
func HeldSubmitConfirmation(in ConfirmationInput) Decision {
	const g = GateHeldSubmitConfirmation
	switch {
	case in.FromRemoteDevice:
		return decide(g, Deny, CodeRemoteCannotConfirm, "a remote device cannot confirm a held submit; confirm it in the dashboard")
	case in.Caller == CallerTelegram:
		return decide(g, Deny, CodeSubmitDashboardOnly, "Submission is confirmed in the dashboard; Telegram cannot confirm it.")
	case !in.NewUserMessage:
		return decide(g, Deny, CodeConfirmationNotUserMessage, "a held submit is confirmed by a new user message, never inferred from the model's own text")
	}
	return decide(g, Allow, CodeOK, "confirmed by a new user message")
}

// AgentSubmitInput holds the facts for an Ask Agent call with submit true.
type AgentSubmitInput struct {
	// Caller is the channel the turn came from. The gate itself refuses a
	// Telegram or scheduled turn, so no caller has to remember to.
	Caller Caller
	Tool   string
	// PostingURL is the single-posting tool's URL argument; unused for a list.
	PostingURL string
	// UserMessage is the user's own words in this turn: the caller leaves out
	// pasted text, so a URL inside it names nothing.
	UserMessage string
	// OutsideContentRead is true once the turn read a mail, a job description
	// or a page returned by a tool whose output is untrusted.
	OutsideContentRead bool
	// ListURLs are the canonical URLs of today's apply list. An unreadable
	// list is passed empty, so it vouches for nothing.
	ListURLs []string
}

// AgentSubmit is the Ask Agent's submit gate. It returns allow or hold, runs
// whether or not any other classifier exists, and has one door.
func AgentSubmit(in AgentSubmitInput) Decision {
	const g = GateAgentSubmit
	switch {
	case in.Caller == CallerTelegram:
		return decide(g, Deny, CodeSubmitDashboardOnly, "Submission is confirmed in the dashboard; Telegram cannot submit an application.")
	case in.Caller == CallerScheduler || !ValidCaller(in.Caller):
		return decide(g, Deny, CodeUnattendedCaller, "a scheduled or unrecognised turn cannot use the submit capability")
	}
	switch in.Tool {
	case ToolApplyFromList:
		if in.OutsideContentRead {
			return holdAgent(g, CodeOutsideContentRead, "this turn read outside content, so the batch needs your confirmation", "")
		}
		return decide(g, Allow, CodeBatchOverUserList, "batch over the user's own apply list; no outside content was read")
	case ToolAutofillApplication:
	default:
		return decide(g, Deny, CodeToolNotInSubmitLane, "tool %q is not in the submit lane", in.Tool)
	}
	posting, ok := parseRef(strings.TrimSpace(in.PostingURL))
	if !ok || !isHTTP(in.PostingURL) {
		return holdAgent(g, CodeURLNotHTTP, "the posting URL is not a plain http or https URL", "")
	}
	tokens := urlTokens(in.UserMessage)
	for _, t := range tokens {
		if sameURL(t, posting) {
			return decide(g, Allow, CodeExactURLNamed, "the user's message names this exact posting URL")
		}
	}
	if in.OutsideContentRead {
		return holdAgent(g, CodeOutsideContentRead, "this turn read outside content, which could have chosen the target", in.PostingURL)
	}
	for _, t := range tokens {
		if t.host == posting.host {
			return decide(g, Allow, CodeHostNamedOrListed, "the posting host is named in the user's message")
		}
	}
	for _, l := range in.ListURLs {
		if r, ok := parseRef(strings.TrimSpace(l)); ok && sameURL(r, posting) {
			return decide(g, Allow, CodeHostNamedOrListed, "the exact posting URL is on today's apply list")
		}
	}
	return holdAgent(g, CodeHostNotNamedNotListed, "your message does not name the posting's site and this posting is not on today's apply list", in.PostingURL)
}

func isHTTP(raw string) bool {
	low := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}

// holdAgent words a hold so the model neither retries in the turn nor
// confirms for the user. The URL is included only when it fits whole, because
// a clipped URL is not the exact URL the user must paste.
func holdAgent(g Gate, code, why, postingURL string) Decision {
	msg := "Held: " + why + ". Do not retry in this turn; ask the user to confirm in their next message"
	if postingURL == "" {
		return decide(g, Hold, code, "%s.", msg)
	}
	if full := msg + " by pasting the exact posting URL: " + postingURL; len([]rune(full)) <= MaxReasonRunes {
		return decide(g, Hold, code, "%s", full)
	}
	return decide(g, Hold, code, "%s by pasting the exact posting URL.", msg)
}

// ControlKind classifies a control by what it reads as.
type ControlKind string

const (
	ControlWithdraw   ControlKind = "withdraw"
	ControlDiscard    ControlKind = "discard"
	ControlSubmit     ControlKind = "submit"
	ControlNavigation ControlKind = "navigation"
	ControlOther      ControlKind = "other"
)

var (
	withdrawRE = lazyregexp.New(`\bwithdraw`)
	discardRE  = lazyregexp.New(`\bdiscard\b|\b(?:delete|remove|erase|deactivate|close|cancel|retract|rescind)\b.*\b(?:application|candidacy|draft|submission|account|profile)\b|\b(?:application|candidacy|draft|submission|account|profile)\b.*\b(?:delete|remove|erase|deactivate|retract|rescind)\b`)
	submitRE   = lazyregexp.New(`\bsubmit\b|\bsend (?:my |your |the )?application\b`)
	// newTabRE is the notice a link carries that it opens in a new tab or
	// window: it names no action, so it is no label of the control.
	newTabRE = lazyregexp.New(`[(\[]?\s*(?:[-\x{2013}\x{2014}|,]\s*)?(?:link )?opens? (?:in )?(?:a )?new (?:browser )?(?:tab|window)\s*[)\]]?`)
)

// navigationLabels are the whole labels the walk may press. It is an
// allowlist: a label that is not here is never navigation, whatever it
// resembles, so a destructive or terminal control cannot pass by being
// unfamiliar.
var navigationLabels = map[string]bool{
	"next": true, "next step": true, "next page": true, "continue": true, "review": true,
	"review and continue": true, "save and continue": true, "save and next": true, "proceed": true,
	"apply": true, "apply now": true, "easy apply": true, "start application": true, "start your application": true,
	"apply for this job": true, "apply to this job": true, "apply for this role": true, "apply to this role": true,
	"apply for this position": true, "apply to this position": true, "apply for position": true,
	"review application": true, "review your application": true,
	// The posting page of a draft the site already holds (ADP's "Pending
	// Submission") opens the same form from this control.
	"complete your application": true,
	// An application the site holds unfinished: ADP's posting page shows
	// "Applied" once Apply was pressed, and it continues that application.
	"applied": true,
	// The manual method of Eightfold's "Create your profile" chooser; its
	// "Upload your resume" stays the person's, as every upload method does.
	"enter information": true,
	// LinkedIn's "Job search safety reminder" between Easy Apply and its form.
	"continue applying": true,
}

// openerRE is the whole-label shape of an Apply control that reveals the form,
// matched on a folded and reduced label like the allowlist above: the wording
// sites put around "Apply" (online, for this job online, to position, on the
// company site, at <employer>), a start-the-application phrase, and the
// "I'm interested" a SmartRecruiters posting uses. It is anchored on the
// whole label, so a sentence that merely contains Apply is never navigation.
// openerNamedRE is an Apply control's second label that names what it applies
// for, as Dayforce's aria-label "Apply for Software Engineer" (or a Jibe career
// site's "Apply : Software Engineer I") does beside its visible "Apply". It counts only beside a bare Apply label, never alone (the
// vocabulary pins "Apply for Job" as no opener), and other ways to apply and
// anything that sends or defers are never openers.
var (
	openerNamedRE    = lazyregexp.New(`^apply(?: now| (?:for|to)| ?[:\x{2013}\x{2014}|-]) [^.!?]{1,70}$`)
	openerNamedNotRE = lazyregexp.New(`\b(?:linkedin|indeed|e ?mail|later|filters?|alerts?|sponsorship|submit|withdraw|account|sign ?in|log ?in)\b`)
)

var openerRE = lazyregexp.New(`^(?:(?:standard|quick|easy|external) )?apply(?: (?:now|here|online|with autofill|for (?:this (?:job|role)|(?:this )?position)(?: online)?|to (?:this )?(?:job|role|position)|on (?:(?:the )?(?:company|employer|original)(?:['\x{2019}]s)? )?(?:site|website)|at .{1,40}))?$|^(?:start(?: your)?|begin|continue to) application$|^complete your application$|^i['\x{2019}]?m interested$`)

// finalApplyRE is the whole raw label of a form's final control that reads
// Apply: the verb alone, or with "now" or "for" or "to" "this job, role or
// position" (evlo's "Apply to Job"); Gem's "Apply without saving", which sends
// this application alone (its "Apply and save" also keeps the applicant's
// profile at Gem, and is never one); or the bare "Send" a Recruitee form ends
// with. One closing "!" may end it (Toast's "Apply now!").
// It is matched on the label as the page spells it, so a lookalike or hidden
// character never makes a control a final Apply.
var finalApplyRE = lazyregexp.New(`^(?:apply(?: now| (?:for|to) (?:this )?(?:job|role|position)| without saving)?|send)!?$`)

// IsFinalApply reports whether every label of the control is a final-Apply
// label as the page spells it.
func IsFinalApply(labels []string) bool {
	seen := false
	for _, l := range labels {
		l = strings.ToLower(strings.Join(strings.Fields(l), " "))
		if l == "" {
			continue
		}
		if !finalApplyRE.MatchString(l) {
			return false
		}
		seen = true
	}
	return seen
}

// navigationLabel reduces a folded label to its words: "&" reads as "and" and
// arrows, stars and trailing punctuation are dropped.
func navigationLabel(folded string) string {
	folded = strings.ReplaceAll(folded, "&", " and ")
	return strings.Join(strings.Fields(strings.TrimFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })), " ")
}

// ClassifyControl reads every label, text and value of a control after
// folding lookalike spellings; the most dangerous reading wins, in the order
// withdraw, discard, submit. A control is navigation only when every label it
// carries is a known navigation label.
func ClassifyControl(labels ...string) ControlKind {
	var folded []string
	for _, l := range labels {
		if f := strings.TrimSpace(newTabRE.ReplaceAllString(foldText(l), "")); f != "" {
			folded = append(folded, f)
		}
	}
	joined := strings.Join(folded, " \n ")
	switch {
	case withdrawRE.MatchString(joined):
		return ControlWithdraw
	case discardRE.MatchString(joined):
		return ControlDiscard
	case submitRE.MatchString(joined):
		return ControlSubmit
	}
	bareApply := false
	for _, f := range folded {
		if l := navigationLabel(f); l == "apply" || l == "apply now" {
			bareApply = true
		}
	}
	for _, f := range folded {
		if l := navigationLabel(f); !navigationLabels[l] && !openerRE.MatchString(l) && !(bareApply && openerNamedRE.MatchString(l) && !openerNamedNotRE.MatchString(l)) {
			return ControlOther
		}
	}
	if len(folded) == 0 {
		return ControlOther
	}
	return ControlNavigation
}

// NamedApply reports a label that applies for what it names ("Apply Now for
// Agentic AI Engineer"), the openerNamedRE shape, with nothing in it that
// withdraws, discards, submits, alerts or applies another way. Alone it is no
// control the walk may press: only a link's address may be opened for it.
func NamedApply(label string) bool {
	f := strings.TrimSpace(newTabRE.ReplaceAllString(foldText(label), ""))
	if withdrawRE.MatchString(f) || discardRE.MatchString(f) || submitRE.MatchString(f) {
		return false
	}
	l := navigationLabel(f)
	return openerNamedRE.MatchString(l) && !openerNamedNotRE.MatchString(l)
}

// ControlStage says which part of the run asks for the press.
type ControlStage string

const (
	// StageWalk is the page walk: navigation clicks only.
	StageWalk ControlStage = "walk"
	// StageSubmit is the submit stage, after the authorization and the claim.
	StageSubmit ControlStage = "submit"
	// StageEditorSave is the walk working an in-page editor (Oracle's work and
	// education history rows): it opens the editor with the timeline's add
	// button and presses the editor's own footer Save once it filled it. The
	// label must be exactly one of those; every other rule of the walk still
	// applies.
	StageEditorSave ControlStage = "editor_save"
	// StageAccount is the account stage pressing the sign-in or create-account
	// control of a login wall. Creating an account is a separate decision of
	// the account creation gate, made before this press.
	StageAccount        ControlStage = "account"
	StageGoogleSignIn   ControlStage = "google_signin"
	StageGoogleProvider ControlStage = "google_provider"
	// StageAccountFlow is the account stage pressing a control of an account
	// flow beside the sign-in wall: the terms link and the Accept of its
	// dialog, the tenant's own request-verification control and its
	// forgot-password control. Each press also passes its own gate (the account
	// creation gate, the resend and reset gates).
	StageAccountFlow ControlStage = "account_flow"
	// StageReset is the password reset of a known Workday account pressing the
	// submit of the forgot-password form, of the code form and of the new
	// password form. The reset gates decide whether it runs at all.
	StageReset ControlStage = "reset"
	// StageSigninDialog is the account stage pressing the Continue or Submit of
	// an email-first sign-in dialog (Eightfold's): the step after the email,
	// the review before a new account and the step that takes the emailed
	// code. It allows only a control that stands in a modal dialog
	// (InDialog); the caller guarantees that dialog holds no application
	// field, and the account creation gate decides a new account before.
	StageSigninDialog ControlStage = "signin_dialog"
	// StageProfileDialog is the walk pressing the Submit of the dialog that
	// saves the account's profile after sign-in (Eightfold's "Create your
	// profile"), which is not the application. It allows only a control that
	// stands in a modal dialog (InDialog); the caller guarantees that dialog
	// holds only identity boxes the walk filled.
	StageProfileDialog ControlStage = "profile_dialog"
	// StageChallenge is the walk pressing the plain button of a challenge-only
	// page after a solved token was delivered. The caller guarantees the page
	// holds no fillable field; the label must be one of a short list, and a
	// label that reads as Submit also needs the authorization and the claim,
	// unless the page is a listed bot manager's own (BotManagerHosts).
	StageChallenge ControlStage = "challenge"
	// StageDataConsent is the walk accepting a form-less data-consent page
	// before the form (Jobvite's "Data Consent") under the legal-declaration
	// setting, which the caller decides first: only an accept label.
	StageDataConsent ControlStage = "data_consent"
	// StageComputerUse is the completion ladder's computer-use rung (owner
	// decision F-9) pressing a control of the form it writes: a listbox, one of
	// its options, a radio, a checkbox or a text box. It never presses
	// navigation, a button that submits its form or a destructive label.
	StageComputerUse ControlStage = "computer_use"
)

// BotManagerHosts are the validation hosts of bot managers that put a
// challenge page in front of a tenant (parity row SG-113, the CGI interstitial
// on Radware). A page on one of them is the bot manager's own, never an
// application, so the plain Submit of its challenge-only page may be pressed
// before the walk reached any form. A host matches by equality or as a
// subdomain.
var BotManagerHosts = []string{"perfdrive.com"}

// BotManagerHost reports whether host is one of BotManagerHosts or a subdomain
// of one.
func BotManagerHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	for _, h := range BotManagerHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// challengeLabels are the whole labels of the plain button on a challenge-only
// page.
var challengeLabels = map[string]bool{
	"submit": true, "continue": true, "verify": true, "verify you are human": true, "i am human": true, "continue to site": true, "proceed": true,
}

// signinDialogLabels are the whole labels of the steps of an email-first
// sign-in dialog.
var signinDialogLabels = map[string]bool{"continue": true, "next": true, "submit": true, "verify": true}

var profileDialogLabels = map[string]bool{"submit": true}

var dataConsentLabels = map[string]bool{"accept": true, "i accept": true, "agree": true, "i agree": true, "i consent": true, "accept and continue": true, "agree and continue": true, "acknowledge": true, "i acknowledge": true}

var googleSignInLabels = map[string]bool{"sign in with google": true, "log in with google": true, "login with google": true, "continue with google": true, "sign up with google": true, "register with google": true}

// GoogleSignInControl reports a complete Google sign-in label, never consent or signup.
func GoogleSignInControl(labels []string) bool {
	return !anyMixedScript(labels) && isLabelIn(labels, googleSignInLabels)
}

// accountLabels are the whole labels the account stage may press. A bare
// "Create" is a create-account form's own submit (TikTok's).
var accountLabels = map[string]bool{
	"sign in": true, "log in": true, "login": true, "sign up": true, "create account": true, "create an account": true,
	"create profile": true, "create": true, "register": true, "continue": true, "next": true, "verify": true,
}

// accountFlowRE matches the whole label of a control the account flows press.
var accountFlowRE = lazyregexp.New(`^(?:(?:i )?(?:accept|agree)(?: and continue)?|terms(?: and conditions| of use| of service)?(?: and privacy(?: policy)?)?|privacy(?: policy| notice| statement)?|` +
	// SuccessFactors' terms link, alone or under its field caption:
	// "Terms of Use Read and accept the data privacy statement. Required".
	`(?:terms(?: and conditions| of use| of service)? )?(?:read and )?(?:accept|agree to) (?:the )?(?:data )?(?:privacy (?:policy|notice|statement)|terms(?: and conditions| of use| of service)?)(?:\.? required)?|` +
	`(?:re ?send|request|send)(?: me)?(?: a)?(?: new)?(?: account)? (?:verification|activation|confirmation)(?: e ?mail| link| message)?(?: again)?|` +
	`(?:re ?send|send|get|request)(?: me)?(?: a| the)?(?: new)? (?:pass ?code|(?:verification |security |e ?mail )?code)|` +
	`forgot(?: your)?(?: password)?|sign in with e ?mail|(?:(?:apply|continue|proceed) )?without (?:a )?(?:resume|cv))$`)

// resetControlRE matches the whole label of a button on the reset forms.
var resetControlRE = lazyregexp.New(`^(?:submit|send|continue|next|verify|save|reset(?: my)? password|set(?: new)? password|change password|send(?: me)?(?: a)?(?: password)? reset(?: link| e ?mail| instructions)?|e ?mail me(?: a)? reset(?: link)?)$`)

// isResetControl reports whether every label of the control is a reset-form
// label.
func isResetControl(labels []string) bool {
	seen := false
	for _, l := range labels {
		f := navigationLabel(foldText(l))
		if f == "" {
			continue
		}
		if !resetControlRE.MatchString(strings.ReplaceAll(f, "-", " ")) {
			return false
		}
		seen = true
	}
	return seen
}

// isAccountFlowControl reports whether every label of the control is an
// account-flow label.
func isAccountFlowControl(labels []string) bool {
	seen := false
	for _, l := range labels {
		f := navigationLabel(foldText(l))
		if f == "" {
			continue
		}
		if !accountFlowRE.MatchString(strings.ReplaceAll(f, "-", " ")) {
			return false
		}
		seen = true
	}
	return seen
}

// ControlInput holds the facts for one press, read at the moment of the press.
type ControlInput struct {
	Stage                 ControlStage
	AllowGoogleSignIn     bool
	GoogleProviderControl bool
	// Found is false when no control matched the selector at press time.
	Found bool
	// Labels are the accessible label, text and value read at press time.
	Labels []string
	// SubmitAuthorized is true when SubmitAuthorization and AgentSubmit (or
	// the dashboard confirmation) allowed; ClaimHeld when PressClaim allowed.
	SubmitAuthorized, ClaimHeld bool
	// Unattended is true for a scheduled job or any run nobody is watching;
	// such a run presses nothing in the apply window.
	Unattended bool
	// TopHost is the host of the top document, read with the page the press
	// acts on; InFrame is true for a control inside an embedded frame, which
	// is never the top document's own.
	TopHost string
	InFrame bool
	// InDialog is set for a control that stands in a modal dialog of the page.
	InDialog bool
	// FinalApply is set by the submit stage, and by nothing else, for a control
	// the page probe read as the filled form's own final control when that
	// control reads Apply instead of Submit (a button of type submit inside the
	// form with no Next beside it). It makes the control a Submit for the
	// submit stage's rules and refuses it to every other stage.
	FinalApply bool
	// FormSubmitter is set for a control that submits its form when pressed (a
	// button whose type is submit); the computer-use rung never presses one.
	FormSubmitter bool
	// Link is set for a press that would follow a link out of the page; the
	// computer-use rung never presses one.
	Link bool
	// Tab is set for a control whose role is tab; the walk presses a tab only
	// when it names the application (IsApplicationTab).
	Tab bool
}

// PressControl decides whether the browser layer may press a control. No
// setting, flag or caller lifts the Withdraw rule.
func PressControl(in ControlInput) Decision {
	const g = GateControlPress
	if in.Unattended {
		return decide(g, Deny, CodeUnattendedCaller, "an unattended run presses no control in the apply window")
	}
	if !in.Found {
		return decide(g, Deny, CodeControlNotFound, "no control matched at press time; nothing is pressed")
	}
	switch kind := ClassifyControl(in.Labels...); {
	case kind == ControlWithdraw:
		return decide(g, Deny, CodeWithdrawControl, "a control that reads as Withdraw is never pressed")
	case kind == ControlDiscard:
		return decide(g, Deny, CodeDiscardControl, "a control that reads as Discard or Delete an application is never pressed")
	case in.Stage == StageReset && !anyMixedScript(in.Labels) && isResetControl(in.Labels):
		return decide(g, Allow, CodeOK, "a button of the password reset forms")
	case in.FinalApply && in.Stage != StageSubmit:
		return decide(g, Deny, CodeSubmitInWalk, "a form's final Apply is its Submit and is pressed only by the submit stage, never by the walk")
	case in.Stage == StageSigninDialog && in.InDialog && !anyMixedScript(in.Labels) && isLabelIn(in.Labels, signinDialogLabels):
		return decide(g, Allow, CodeOK, "the Continue or Submit of an email-first sign-in dialog")
	case in.Stage == StageProfileDialog && in.InDialog && !anyMixedScript(in.Labels) && isLabelIn(in.Labels, profileDialogLabels):
		return decide(g, Allow, CodeOK, "the Submit of a profile dialog that holds only identity boxes")
	case in.Stage != StageSubmit && in.Stage != StageChallenge && kind == ControlSubmit:
		return decide(g, Deny, CodeSubmitInWalk, "a Submit control is pressed only by the submit stage, never by the walk")
	case in.Stage == StageChallenge && kind == ControlSubmit && !in.InFrame && BotManagerHost(in.TopHost) && !anyMixedScript(in.Labels) && isLabelIn(in.Labels, challengeLabels):
		return decide(g, Allow, CodeOK, "the plain Submit of the bot-manager validation page on %s, which is not an application", strings.ToLower(in.TopHost))
	case in.Stage == StageChallenge && kind == ControlSubmit && !in.SubmitAuthorized:
		// The plain button of a challenge page can be the form's own final Submit.
		return decide(g, Deny, CodeSubmitNotAuthorized, "a control that reads as Submit on a challenge page needs the submit authorization; nothing is pressed")
	case in.Stage == StageChallenge && kind == ControlSubmit && !in.ClaimHeld:
		return decide(g, Deny, CodeSubmitClaimMissing, "a control that reads as Submit on a challenge page needs the press claim; nothing is pressed")
	case anyMixedScript(in.Labels):
		return decide(g, Deny, CodeMixedScriptLabel, "a label spells a word from more than one alphabet, so it cannot be trusted to say what it looks like; nothing is pressed")
	case in.Stage == StageEditorSave && isEditorSave(in.Labels):
		return decide(g, Allow, CodeOK, "the footer Save of the editor the walk filled")
	case in.Stage == StageDataConsent && isLabelIn(in.Labels, dataConsentLabels):
		return decide(g, Allow, CodeOK, "the accept of a data-consent page, under the legal-declaration setting")
	case in.Stage == StageChallenge && isLabelIn(in.Labels, challengeLabels):
		return decide(g, Allow, CodeOK, "the plain button of a challenge-only page")
	case in.Stage == StageGoogleProvider:
		if in.AllowGoogleSignIn && in.GoogleProviderControl && in.TopHost == "accounts.google.com" {
			return decide(g, Allow, CodeOK, "the user enabled the verified Google account or basic permission control")
		}
		return decide(g, Deny, CodeNotNavigation, "Google authorization needs its setting and a verified provider control")
	case in.Stage == StageGoogleSignIn && in.AllowGoogleSignIn && isLabelIn(in.Labels, googleSignInLabels):
		return decide(g, Allow, CodeOK, "the user enabled Google sign-in on a login wall")
	case in.Stage == StageGoogleSignIn:
		return decide(g, Deny, CodeNotNavigation, "Google sign-in is off or the control is not an explicit Google sign-in")
	case in.Stage == StageAccount && isAccountControl(in.Labels):
		return decide(g, Allow, CodeOK, "the sign-in or create-account control of a login wall")
	case in.Stage == StageAccountFlow && isAccountFlowControl(in.Labels):
		return decide(g, Allow, CodeOK, "a control of an account flow")
	case in.Stage == StageComputerUse && (kind == ControlNavigation || in.Link):
		return decide(g, Deny, CodeNavigationInRung, "the walk's navigation is the walk's; the computer-use rung presses only the control it writes")
	case in.Stage == StageComputerUse && in.FormSubmitter:
		return decide(g, Deny, CodeSubmitInWalk, "a button that submits its form is pressed only by the submit stage")
	case in.Stage == StageComputerUse && destructiveLabel(in.Labels):
		return decide(g, Deny, CodeDestructiveLabel, "the computer-use rung never presses a control that deletes, pays, buys or sends")
	case in.Stage == StageComputerUse:
		return decide(g, Allow, CodeOK, "a control of the form the computer-use rung writes")
	case in.Stage == StageWalk && in.Tab && IsApplicationTab(in.Labels):
		return decide(g, Allow, CodeOK, "a tab that shows the application form; pressing it only switches the panel the page shows")
	case in.Stage != StageSubmit && kind != ControlNavigation:
		return decide(g, Deny, CodeNotNavigation, "the walk presses only Next, Continue, Review, Save and continue and an Apply opener; this control is none of them")
	case in.Stage != StageSubmit:
		return decide(g, Allow, CodeOK, "navigation control")
	case in.FinalApply && !((kind == ControlNavigation || kind == ControlOther) && IsFinalApply(in.Labels)):
		return decide(g, Deny, CodeSubmitLabelMismatch, "the control no longer reads as the form's final Apply at press time; nothing is pressed")
	case !in.FinalApply && kind != ControlSubmit:
		return decide(g, Deny, CodeSubmitLabelMismatch, "the control no longer reads as Submit at press time; nothing is pressed")
	case !in.FinalApply && !submitRE.MatchString(strings.ToLower(strings.Join(in.Labels, " \n "))):
		return decide(g, Deny, CodeSubmitLabelMismatch, "the control reads as Submit only through lookalike or hidden characters; nothing is pressed")
	case !in.SubmitAuthorized:
		return decide(g, Deny, CodeSubmitNotAuthorized, "the submit authorization has not allowed this press")
	case !in.ClaimHeld:
		return decide(g, Deny, CodeSubmitClaimMissing, "the press claim is not held")
	}
	return decide(g, Allow, CodeOK, "Submit pressed after authorization and claim")
}

// editorLabels are the whole labels the editor stage may press: the timeline's
// add button that opens an editor and the editor's own footer Save.
var editorLabels = map[string]bool{
	"save": true, "add": true, "add experience": true, "add education": true, "add work experience": true, "add another": true,
}

// applicationTabLabels are the whole labels of a tab that shows a posting's
// application form (Airbnb's "Application" beside "Role overview").
var applicationTabLabels = map[string]bool{"application": true, "application form": true}

// IsApplicationTab reports whether every label of a tab names the application.
func IsApplicationTab(labels []string) bool { return isLabelIn(labels, applicationTabLabels) }

// isEditorSave reports whether every label of the control is one of them.
func isEditorSave(labels []string) bool { return isLabelIn(labels, editorLabels) }

// isAccountControl reports whether every label of the control is a sign-in or
// create-account label.
func isAccountControl(labels []string) bool { return isLabelIn(labels, accountLabels) }

// isLabelIn reports whether every non-empty label of the control is in set.
func isLabelIn(labels []string, set map[string]bool) bool {
	seen := false
	for _, l := range labels {
		f := navigationLabel(foldText(l))
		if f == "" {
			continue
		}
		if !set[f] {
			return false
		}
		seen = true
	}
	return seen
}

func anyMixedScript(labels []string) bool {
	for _, l := range labels {
		if hasMixedScriptWord(l) {
			return true
		}
	}
	return false
}
