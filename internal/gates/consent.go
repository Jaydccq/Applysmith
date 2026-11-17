// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the consent and statement gates.
const (
	GateConsentControl Gate = "consent_control"
	GateStatement      Gate = "application_statement"
	GateCookieConsent  Gate = "cookie_consent"
	// GateLegalSetting records a change of the autoAcceptLegalDeclarations
	// setting, so the audit shows when it was turned on or off.
	GateLegalSetting Gate = "legal_declarations_setting"
	// GateCLIInstall records the user making or removing the applysmith command
	// link, or being refused because a different file is in its place.
	GateCLIInstall Gate = "cli_install"

	CodeNotConsentControl   = "not_consent_control"
	CodeLegalDeclarationOn  = "legal_declaration_on"
	CodeLegalDeclarationOff = "legal_declaration_off"
	CodeMarketingOptIn      = "marketing_opt_in"
	CodeUserBankAnswer      = "user_bank_answer"
	CodeOptionalNeedsUser   = "optional_needs_user"
	CodeSelfIDCheckbox      = "self_identification_checkbox"

	CodeApplicationStatement = "application_statement"

	CodeCLILinkCreated = "cli_link_created"
	CodeCLILinkRemoved = "cli_link_removed"
	CodeCLILinkForeign = "cli_link_foreign"

	CodeLegalSettingOn  = "legal_setting_on"
	CodeLegalSettingOff = "legal_setting_off"

	CodeNotConsentGate      = "not_a_consent_gate"
	CodeNoClosingControl    = "no_closing_control"
	CodeAcceptAllLastResort = "accept_all_last_resort"
)

// LegalSettingChange is the audit decision of a change of the
// autoAcceptLegalDeclarations setting made by the user.
func LegalSettingChange(on bool) Decision {
	if on {
		return decide(GateLegalSetting, Allow, CodeLegalSettingOn, "autoAcceptLegalDeclarations was turned on by the user")
	}
	return decide(GateLegalSetting, Allow, CodeLegalSettingOff, "autoAcceptLegalDeclarations was turned off by the user")
}

// CLIInstallChange is the audit decision of the user making the applysmith
// command link (made true) or removing it (made false).
func CLIInstallChange(made bool) Decision {
	if made {
		return decide(GateCLIInstall, Allow, CodeCLILinkCreated, "the applysmith command link was made by the user")
	}
	return decide(GateCLIInstall, Allow, CodeCLILinkRemoved, "the applysmith command link was removed by the user")
}

// CLIInstallRefused is the audit decision of a request that found a different
// file where the link belongs and left it alone.
func CLIInstallRefused() Decision {
	return decide(GateCLIInstall, Deny, CodeCLILinkForeign, "a file that is not the applysmith command link was left in place")
}

var (
	// marketingRE matches an optional marketing opt-in on folded text.
	// "future opportunities" counts only as the object of an ask to contact,
	// consider, notify or keep the applicant informed; "updates" and "new
	// roles" only as the object of an opt-in ("receive updates", "notify me of
	// new openings"), never a policy its company may update (Suno) or a
	// question on looking for a new role (Databento).
	marketingRE = lazyregexp.New(`\b(?:marketing|newsletters?|promotions?|promotional|(?:special|exclusive|partner) offers?|job alerts?|sms|text messages?|mailing list|subscrib(?:e|es|ed|ing)|sign me up|talent (?:community|pool|network)|portfolio companies|similar (?:jobs?|roles?|positions?|openings?)|(?:about|of|on|for|me) new (?:openings?|roles?|jobs?|positions?|vacancies)|(?:receive|get|send(?: me)?|sign up for|subscribe to|opt in to|e-?mail me)\b[^.]{0,30}\bupdates?|updates? (?:about|on|from|regarding)|e-?mails? from|e-?mail me|keep me (?:posted|informed|updated|in the loop)|notify me|let me know|hear (?:from|about))\b|\b(?:contact|consider|notify|inform|reach out to|keep (?:me )?informed|e-?mail|message|text|call|alert|update|tell|share|in touch)\b[^.]{0,80}\b(?:future|other)\b[^.]{0,30}\b(?:jobs?|roles?|openings?|positions?|vacancies|opportunities)\b`)
	// declarationRE matches wording of a consent, certification or
	// acknowledgement, on whole words only.
	declarationRE = lazyregexp.New(`\b(?:privacy (?:policy|notice|statement)|terms(?: and conditions| of (?:use|service))?|terms & conditions|data processing|certify|certification|attest|background (?:check|investigation)|at[- ]will|e-?sign(?:ature)?|electronic signature|acknowledg(?:e|es|ement|ment)|i (?:have )?(?:read|agree|accept|understand|consent|authori[sz]e)|arbitration|consent|declaration|policy)\b` +
		// Greenlight: "Do you confirm that the information provided ... is
		// accurate and complete?" certifies its truth.
		`|\bconfirm\s+that\s+(?:the|all|my)\s+information\b[^.?!]*\b(?:accurate|true|correct|complete)\b`)
	optionalRE = lazyregexp.New(`\boptional\b`)
)

// ConsentSurface says where the control stands; a sign-up form that must
// refuse stops with the credential class.
type ConsentSurface string

const (
	SurfaceForm           ConsentSurface = "form"
	SurfaceSignup         ConsentSurface = "signup"
	SurfaceAccountDetails ConsentSurface = "account_details"
	SurfaceSigninWidget   ConsentSurface = "signin_widget"
)

// ConsentInput holds the facts about one checkbox.
type ConsentInput struct {
	Label string
	// PageMarksRequired is true only when the page marks the control required
	// (a required attribute, ARIA or a marker on its label), never by the
	// engine's default of required.
	PageMarksRequired bool
	SettingOn         bool
	// Answer attests where the value to enter comes from. Only the caller that
	// read the user's own bank entry for this exact question may set it.
	Answer  AnswerProvenance
	Surface ConsentSurface
}

// AnswerProvenance says where a value comes from. The zero value is no
// provenance, so a caller that forgets the field is held back.
type AnswerProvenance string

// AnswerUserBankExact is an entry the user wrote into their own answer bank
// for this exact question.
const AnswerUserBankExact AnswerProvenance = "user_bank_exact"

// ConsentControl decides whether a checkbox may be ticked. A box that reads as
// self-identification is never ticked here, required or not and whatever the
// caller attests: the user's own answer reaches it only through the
// answer-bank matcher's self-identification policy. An optional box is ticked
// only from the user's own bank entry for that exact question: one that reads
// as marketing, consent or acknowledgement is refused as such, and any other
// is left for the user because no vocabulary list is complete. A required one
// follows the setting.
func ConsentControl(in ConsentInput) Decision {
	const g = GateConsentControl
	label := foldText(in.Label)
	marketing, declaration := marketingRE.MatchString(label), declarationRE.MatchString(label)
	optional := !in.PageMarksRequired || optionalRE.MatchString(label)
	switch {
	case selfIDVocabulary(label):
		return decide(g, Deny, CodeSelfIDCheckbox, "self-identification is answered only through your own answer or the page's decline option; left for you")
	case in.Answer == AnswerUserBankExact:
		return decide(g, Allow, CodeUserBankAnswer, "the user's own bank answer applies as written")
	case !marketing && !declaration && optional:
		return decide(g, Deny, CodeOptionalNeedsUser, "optional checkbox with no answer of yours on file; left for you")
	case !marketing && !declaration:
		return decide(g, Allow, CodeNotConsentControl, "not a consent or acknowledgement control")
	case optional:
		return decide(g, Deny, CodeMarketingOptIn, "optional opt-in or acknowledgement; left unticked")
	case in.SettingOn:
		return decide(g, Allow, CodeLegalDeclarationOn, "required declaration; autoAcceptLegalDeclarations is on")
	}
	d := decide(g, Deny, CodeLegalDeclarationOff, "required declaration left for you; autoAcceptLegalDeclarations is off")
	if in.Surface == SurfaceSignup {
		d = d.withClass(ClassCredential)
	}
	return d
}

// StatementKind separates statements the runtime can observe from the rest.
type StatementKind string

const (
	StatementNone       StatementKind = ""
	StatementObservable StatementKind = "observable"
	StatementOther      StatementKind = "other"
)

// aiRefrain is a request addressed to the applicant not to use AI, in folded text.
const aiRefrain = `(?:\byou\b[^.?!]{0,30}|\bplease )(?:don'?t|do not|must not|should not|may not|cannot|can'?t|not to|refrain from|avoid) (?:the )?(?:use (?:of )?|using )?(?:any )?(?:ai|artificial intelligence|chatgpt|generative ai|llms?)\b`

var (
	observableRE = lazyregexp.New(`\b(?:did|have|has) you (?:submit|attach|upload|include|provide|add)(?:ted|ed|d)?\b[^?]*\b(?:cover letter|resume|cv)\b`)
	statementRE  = lazyregexp.New(
		`\bwere you referred\b|\breferred\b[^?]*\bby\b|\bdid (?:an?|any|someone|anyone)\b[^?]*\brefer you\b` +
			`|\bdid you use (?:an? )?(?:ai|artificial intelligence|chatgpt)\b` +
			// AI makes a statement only beside the application, the form or its
			// answers (Cameo: "How have you used AI-assisted development" is the
			// applicant's work).
			`|\b(?:did|have|has) you (?:use|used|using|utili[sz]e\w*|rely|relied|leverage\w*)\b[^?]*\b(?:ai|artificial intelligence|chatgpt|gpt|llm|copilot|generative|language model)\b[^?]*\b(?:application|answers?|responses?|form|submission|resume|cover letter)\b` +
			`|\b(?:ai|artificial intelligence|chatgpt|llm)\b[^?]*\b(?:help|assist|complet|fill|writ|generat|draft|prepar)\w*\b[^?]*\bapplication\b` +
			`|^(?:did|have|has) you\b[^?]*\b(?:(?:this|the|your|our) (?:job )?application|appl(?:y|ied|ying)|submitted)\b` +
			// A request to the applicant not to use AI on the application, which
			// a Yes acknowledges (LiveKit: "for the application form ... we ask
			// that you don't use AI assistance"); the company's own use is not.
			`|\b(?:application|this form|your answers|your responses)\b[^.?!]{0,100}` + aiRefrain +
			`|` + aiRefrain + `[^.?!]{0,60}\b(?:application|this form|your answers|your responses)\b`)
	// authorshipRE reads a statement that the application's content is the
	// applicant's own work, written by them or made with or without AI, said of
	// that content, even when the sentence also certifies that it is true. A
	// truth certification alone, an instruction to answer in one's own words and
	// "your own GitHub profile" are not one.
	authorshipRE = lazyregexp.New(
		`\b(?:true|accurate|correct|complete|honest|genuine)\b[^.?!;]{0,30}\band\s+` + ownDegree + ownWords +
			`|` + ownContent + `[^.?!;]{0,40}\b(?:is|are|was|were|has\s+been|have\s+been)\s+` + ownDegree + `(?:written\s+)?(?:in\s+)?` + ownWords +
			`|\b(?:is|are|was|were)\s+(?:everything|(?:this|the|these|your|all)\b[^.?!;]{0,20}` + ownContent + `)[^.?!;]{0,60}\b` + ownWords +
			`|\b(?:is|are|was|were)\b[^.?!;]{0,40}\b(?:my|your)\s+own\s+(?:work|words|writing)` + ownEnd +
			`|` + ownContent + `[^.?!;]{0,60}\b(?:written|composed|authored|drafted|prepared|completed)\s+` + ownDegree + `by\s+(?:me|you|myself|yourself|the\s+(?:applicant|candidate))\b` +
			`|` + ownContent + `[^.?!;]{0,60}\b(?:without\s+(?:the\s+)?(?:use|help|aid|assistance|support)\s+of\s+(?:any\s+)?(?:an?\s+)?|without\s+(?:using\s+)?(?:any\s+)?|(?:not\s+)?(?:been\s+)?(?:generated|written|created|produced|composed|drafted|completed)\s+(?:by|with|using)\s+(?:an?\s+|any\s+)?)` + ownAI +
			`|` + ownContent + `[^.?!;]{0,60}\b(?:ai|machine)[-\s]generated\b`)
)

// The pieces of authorshipRE, read on folded text.
const (
	ownEnd     = `(?:\s*[,.;:!?)]|\s*$|\s+(?:and|or|but|including|without|with|in|that|which|to|for)\b)`
	ownWords   = `(?:my|your)\s+own(?:\s+(?:work|words|writing))?` + ownEnd
	ownContent = `\b(?:application|answers?|responses?|submission|essays?|cover\s+letter|writing|everything)\b`
	ownDegree  = `(?:(?:all|entirely|wholly|completely|solely|personally|only)\s+)?`
	ownAI      = `(?:ai|artificial\s+intelligence|chat\s?gpt|generative(?:\s+ai)?|llms?|(?:large\s+)?language\s+models?)\b`
)

// ClassifyStatement reads a question that states a fact about this
// application. Any "did you ... this application" or "have you applied ..."
// question counts, so a wording the lists below do not name still never
// reaches a model rung.
func ClassifyStatement(label string) StatementKind {
	label = foldText(label)
	switch {
	case observableRE.MatchString(label):
		return StatementObservable
	case statementRE.MatchString(label) || authorshipRE.MatchString(label):
		return StatementOther
	}
	return StatementNone
}

// StatementInput holds the facts for one statement control.
type StatementInput struct {
	Kind StatementKind
	// Observed is true when a filled cover-letter or document slot, or a file
	// this run attached, was seen.
	Observed bool
	// UserEntry is the answer the user wrote into their own bank.
	UserEntry    string
	HasUserEntry bool
}

// StatementResult carries the answer, empty when the control stays blank.
type StatementResult struct {
	Decision
	Answer string
}

// AnswerStatement answers a statement about the application. An observable
// statement is Yes only from observation, never from the bank; any other is
// answered only from the user's own entry; otherwise the control is blank.
func AnswerStatement(in StatementInput) StatementResult {
	const g = GateStatement
	left := StatementResult{Decision: decide(g, Deny, CodeApplicationStatement, "a statement about this application with nothing observed or written by you; left blank")}
	switch in.Kind {
	case StatementObservable:
		if in.Observed {
			return StatementResult{decide(g, Allow, CodeOK, "observed on this run"), "Yes"}
		}
	case StatementOther:
		if in.HasUserEntry {
			return StatementResult{decide(g, Allow, CodeOK, "answered from the user's own bank entry"), in.UserEntry}
		}
	}
	return left
}

var (
	// rejectRE also takes a negated accept (Toast's "I do not accept"), which
	// acceptRE would otherwise read as an accept-all.
	rejectRE    = lazyregexp.New(`(?i)\b(?:reject|decline|deny|refuse|disagree|opt out|no thanks)\b|\b(?:do\s+not|don['\x{2019}]?t|not)\s+(?:accept|agree|allow|consent)\b`)
	necessaryRE = lazyregexp.New(`(?i)\b(?:necessary|essential|required only|only required|strictly|minimal)\b`)
	closeRE     = lazyregexp.New(`(?i)^\s*(?:(?:close|dismiss)(?:\s+(?:this\s+)?(?:guide|dialog|modal|pop-?up|window|banner|notice|message|overlay|panel|tour))?|x|\x{00d7})\s*$`)
	saveRE      = lazyregexp.New(`(?i)\b(?:save|confirm)\s+(?:my\s+)?(?:changes|preferences|settings|choices|selection)\b`)
	acceptRE    = lazyregexp.New(`(?i)\b(?:accept|agree|allow all|allow cookies|enable all)\b`)
	toggleAllRE = lazyregexp.New(`(?i)\b(?:select|unselect|deselect|toggle|check|uncheck|enable|disable)\s+all\b`)
)

// CookieInput holds the controls of a cookie banner, preference center or
// privacy dialog that stands between the Apply control and the form.
type CookieInput struct {
	Controls []string
	// PreferenceCenter is true for a preference center; AllOptionalOff when
	// every optional category stands off, so saving keeps the defaults.
	PreferenceCenter, AllOptionalOff bool
	// AsksAboutApplicant is true for a dialog that asks for a password, a
	// signature or anything about the applicant.
	AsksAboutApplicant bool
}

// CookieDecision names the control to press, -1 when none.
type CookieDecision struct {
	Decision
	Press int
}

// CloseConsentGate chooses the least consent that closes the dialog: reject,
// necessary-only, the save of untouched defaults, close, and accept-all
// last. A control that only toggles every category is never pressed.
func CloseConsentGate(in CookieInput) CookieDecision {
	const g = GateCookieConsent
	if in.AsksAboutApplicant {
		return CookieDecision{decide(g, Deny, CodeNotConsentGate, "a dialog that asks about the applicant is not a consent gate"), -1}
	}
	rank := func(label string) int {
		switch {
		case toggleAllRE.MatchString(label):
			return 0
		case rejectRE.MatchString(label):
			return 1
		case necessaryRE.MatchString(label):
			return 2
		case saveRE.MatchString(label) && in.PreferenceCenter && in.AllOptionalOff:
			return 3
		case closeRE.MatchString(label):
			return 4
		case acceptRE.MatchString(label):
			return 5
		}
		return 0
	}
	best, bestRank := -1, 0
	for i, c := range in.Controls {
		if r := rank(c); r != 0 && (bestRank == 0 || r < bestRank) {
			best, bestRank = i, r
		}
	}
	switch bestRank {
	case 0:
		return CookieDecision{decide(g, Deny, CodeNoClosingControl, "no control closes the dialog with consent no wider than needed"), -1}
	case 5:
		return CookieDecision{decide(g, Allow, CodeAcceptAllLastResort, "only accept-all closes the dialog; pressed last"), best}
	}
	return CookieDecision{decide(g, Allow, CodeOK, "least-consent control %q", in.Controls[best]), best}
}
