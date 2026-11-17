package match

import (
	"slices"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// ackByYesRE reads an acknowledgement whose own text says that answering "Yes"
// gives it ("Please acknowledge ... by responding "Yes."", "Enter Yes if you
// acknowledge").
// The quotes may stand apart from the word (SurveyMonkey: "by selecting ' Yes '
// below").
var ackByYesRE = lazyregexp.New(`(?i)\bby\s+(?:responding|answering|selecting|entering|choosing|checking|marking)\s+["'“”‘’]?\s*yes\b|` +
	`\b(?:enter|select|choose|answer|respond|mark|check)\s+["'“”‘’]?\s*yes\s*["'“”‘’]?\s+(?:if|to)\s+(?:you\s+)?(?:acknowledge|confirm|agree|understand)\b|` +
	// A question that asks for the agreement or the consent itself: its Yes
	// gives it (UT Austin; Knit: "Do you consent to Knit's Privacy Policy?").
	`^\W*(?:do|will|would)\s+you\s+(?:agree|consent)\s+to\b|` +
	// A list of expectations closing with the acknowledgement it asks for (P&G),
	// or a closing statement of understanding (Datadog: "I understand my
	// application will be processed in accordance with ..."), or a closing
	// request to confirm it (Suno: "Please confirm that you have read and
	// understand Suno's in-office expectations and policy.").
	`\bi\s+(?:hereby\s+)?(?:acknowledge|understand)\b[^.?!•]*[.!]?\s*$|` +
	`\bplease\s+confirm\s+(?:that\s+)?you\s+(?:have\s+)?(?:read\s+and\s+)?(?:understand|acknowledge|accept|agree)\b[^.?!•]*[.!?]?\s*$`)

// selectSeveralRE reads a checkbox group that asks for every option that
// applies, or for several.
var selectSeveralRE = regexpMust(`(?i)\b(?:all|any|each)\s+(?:that|which)\s+(?:apply|applies)\b|\b(?:select|check|tick|choose|mark|pick)\s+(?:all|multiple|several|one\s+or\s+more|up\s+to)\b`)

// defaultInputTypes are the types an entry of each kind accepts when it lists
// none. long_text is deliberately absent from fixed, so a bare scalar never
// lands in a narrative box.
func defaultInputTypes(kind Kind) []InputType {
	switch kind {
	case KindFixed:
		return []InputType{InputText, InputSingleSelect, InputYesNo, InputNumber, InputDate, InputURL, InputCheckbox, InputAddress}
	case KindPickOption:
		return []InputType{InputSingleSelect, InputYesNo, InputText}
	case KindCompensationRange:
		return []InputType{InputText, InputNumber, InputSingleSelect}
	case KindLanguageProficiency:
		return []InputType{InputSingleSelect, InputText, InputMultiSelect}
	case KindEmployerHistory:
		return []InputType{InputSingleSelect, InputYesNo, InputCheckbox}
	case KindAutoAcknowledge:
		return []InputType{InputCheckbox, InputYesNo, InputSingleSelect}
	case KindMultiSelect:
		return []InputType{InputMultiSelect, InputCheckbox}
	}
	return nil
}

func (e *Entry) allowsInput(t InputType) bool {
	if len(e.InputTypes) == 0 {
		return slices.Contains(defaultInputTypes(e.Kind), t)
	}
	return slices.Contains(e.InputTypes, string(t))
}

const scalarMaxLen = 80

// compensationFigureRE is a stored pay figure: an amount, maybe with a
// currency sign, thousands separators or cents.
var compensationFigureRE = lazyregexp.New(`^\s*[$\x{20AC}\x{00A3}]?\s*\d{1,3}(?:,?\d{3})*(?:\.\d{1,2})?\s*$`)

func singleLine(s string) bool { return !strings.ContainsAny(s, "\r\n") }

func isSingleLineScalar(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= scalarMaxLen && singleLine(s)
}

// pastSectionTitle is a question without the section title a form writes in
// front of it ("New Grad 2027 Application Questions : What is ...").
func pastSectionTitle(text string) string {
	if i := strings.LastIndex(text, " : "); i >= 0 {
		return text[i+3:]
	}
	return text
}

// isDateShaped reports a stored date: YYYY-MM[-DD] or M/D/YY[YY].
func isDateShaped(s string) bool {
	return dateShapedRE.MatchString(strings.TrimSpace(s))
}

// shapeVerdict is the outcome of control-shape acceptance. Note is non-empty
// when a widening applied and the trace should record the shape gap.
type shapeVerdict struct {
	OK   bool
	Note string
}

// acceptsControl decides whether an entry can drive the control from the
// control's input type: the declared or default types, then four widenings.
// AcceptsControl reports whether the entry's input types, or one of the
// widening rules, accept the prepared question's control. A caller that offers
// entries for a control (the recognition lane) uses it, so it never holds a
// looser notion of acceptance than step 10.
func AcceptsControl(e Entry, q Question) bool { return acceptsControl(&e, q).OK }

func acceptsControl(e *Entry, q Question) shapeVerdict {
	t := q.InputType
	// The engine sends a lone box as yes_no; an entry that ticks boxes takes it.
	if e.allowsInput(t) || q.Kind == KindCheckbox && t == InputYesNo && len(q.Options) <= 1 && e.allowsInput(InputCheckbox) {
		return shapeVerdict{OK: true}
	}
	// Three or more sentence options that each open with Yes or No ("Yes, I'm
	// eligible now, but will need sponsorship in the future.", Partly) read as a
	// Yes/No but choose one statement among several: an entry that picks one
	// option takes them.
	if t == InputYesNo && len(realOptions(q.Options)) > 2 && !bareYesNo(q.Options) && e.allowsInput(InputSingleSelect) {
		return shapeVerdict{OK: true, Note: "a choice among statements that open with Yes or No"}
	}
	// A sponsorship question whose Yes is split by visa (GitLab: "No" and six
	// "Yes, <visa>" options) arrives as a single select, or as a group of
	// boxes of which one is ticked (Truveta); the sponsorship resolver chooses
	// its lone No and leaves a Yes to the user.
	if (t == InputSingleSelect || q.Kind == KindCheckbox && (t == InputCheckbox || t == InputMultiSelect)) && e.ID == idSponsorshipNowOrFuture && yesSplitByDetail(q.Options) {
		return shapeVerdict{OK: true, Note: "a sponsorship Yes/No whose Yes is split by detail"}
	}
	// A Yes/No question whose one No stands beside Yes options split by detail
	// (Hatch: "No", "Yes – I am currently interviewing ...", "Yes – I
	// interviewed here ...") arrives as a single select, or as a multi-value
	// select (Ginkgo's relatives in government): an entry that answers No
	// takes the No; which Yes would be the user's to choose.
	if (t == InputSingleSelect || t == InputMultiSelect) && e.Kind == KindFixed && e.allowsInput(InputYesNo) && yesSplitByDetail(q.Options) && plainNoRE.MatchString(e.Answer) {
		return shapeVerdict{OK: true, Note: "a Yes/No question whose Yes is split by detail, answered No"}
	}
	// Ashby draws a Yes/No question as a group of two boxes, "No" and "Yes" (a
	// multi-value select, Plain): one box is ticked, as one option is chosen.
	if q.Kind == KindCheckbox && (t == InputCheckbox || t == InputMultiSelect) && len(realOptions(q.Options)) == 2 &&
		isYesNoOptionSet(realOptions(q.Options)) && e.allowsInput(InputYesNo) {
		return shapeVerdict{OK: true, Note: "a Yes/No question drawn as two boxes"}
	}
	// A Yes/No question whose two options are sentences ("Yes, I am authorized
	// to work ...", "No, I am not ...") arrives as a single select.
	if t == InputSingleSelect && len(q.Options) == 2 && isYesNoOptionSet(q.Options) && e.allowsInput(InputYesNo) {
		return shapeVerdict{OK: true, Note: "a Yes/No question with sentence options"}
	}
	// An age threshold asked over age statements (Sweetgreen: "I am 18 years
	// of age or older", "I am 17 years of age or younger"): the age resolver
	// chooses the statement that holds the applicant's age.
	if t == InputSingleSelect && e.ID == IDAgeMinimumThreshold && allAgeRanges(realOptions(q.Options)) {
		return shapeVerdict{OK: true, Note: "an age threshold asked over age statements"}
	}
	// A bare Yes and No beside an unsure or not-applicable option (Fluidstack:
	// "Yes", "No", "Not Sure", "N/A") arrive as a single select: the entry's
	// Yes or No is one of them.
	if t == InputSingleSelect && len(q.Options) <= 6 && e.allowsInput(InputYesNo) && yesNoWithAsides(realOptions(q.Options)) {
		return shapeVerdict{OK: true, Note: "a Yes/No question with unsure or not-applicable options"}
	}
	selectish := q.Kind == KindSelect || q.Kind == KindCustomListbox
	// A required acknowledgement offered as a menu or a radio group with one real
	// option ("Yes, I have read and agree ...", GC AI's "Continue"); the
	// acknowledge stage decides whether it is chosen.
	// The engine reads a lone radio as yes_no.
	oneChoice := selectish && t == InputSingleSelect || q.Kind == KindRadioGroup && (t == InputSingleSelect || t == InputYesNo)
	if oneChoice && e.Kind == KindAutoAcknowledge && len(realOptions(q.Options)) == 1 {
		return shapeVerdict{OK: true, Note: "an acknowledgement offered as a one-option choice"}
	}
	// An acknowledgement offered as a choice between accepting and declining
	// (HRS: "Accept" and "Decline"; Coterie's two sentences, which the scan
	// reads as a Yes/No, though a bare Yes and No stays rule (i)'s); the
	// acknowledge stage decides whether it is given.
	if (t == InputSingleSelect || t == InputYesNo && !bareYesNo(q.Options)) && e.Kind == KindAutoAcknowledge && ackPair(realOptions(q.Options)) {
		return shapeVerdict{OK: true, Note: "an acknowledgement offered as an accept-or-decline pair"}
	}
	// An acknowledgement that offers an option placing the applicant outside a
	// named state (Alnylam's "I am not a California applicant"); the acknowledge
	// stage reads it against the record and the posting.
	if e.Kind == KindAutoAcknowledge && (t == InputSingleSelect || t == InputYesNo) {
		if _, _, ok := outOfStateOption(realOptions(q.Options)); ok {
			return shapeVerdict{OK: true, Note: "an acknowledgement with an option that places the applicant outside its state"}
		}
	}
	// A group of two boxes, at least one an acknowledgement, which the engine
	// reads as a multi-value control (Gemini: "By clicking this box ..., you
	// consent to our Applicant Privacy Statement." beside the same consent to
	// a background check); the acknowledge stage decides which are ticked.
	if e.Kind == KindAutoAcknowledge && q.Kind == KindCheckbox && t == InputMultiSelect && len(realOptions(q.Options)) == 2 &&
		slices.ContainsFunc(realOptions(q.Options), func(o Option) bool { return ackPhraseIn(o.Label) }) {
		return shapeVerdict{OK: true, Note: "a group of two boxes, at least one an acknowledgement"}
	}
	// An acknowledgement whose own text says that answering "Yes" gives it, asked
	// as a Yes/No; the acknowledge stage decides whether it is given.
	if e.Kind == KindAutoAcknowledge && (t == InputYesNo || t == InputSingleSelect && isYesNoOptionSet(realOptions(q.Options))) && ackByYesRE.MatchString(q.Text) {
		return shapeVerdict{OK: true, Note: "an acknowledgement its own text gives by answering Yes"}
	}
	// A list of the languages one speaks (Datadog: "Please select all the
	// languages you speak fluently."): the language resolver selects the
	// recorded one when its level fits. A list of programming languages is a
	// skill checklist's.
	if e.Kind == KindLanguageProficiency && t == InputMultiSelect && (q.Kind == KindCheckbox || selectish) && spokenLanguageList(q) {
		return shapeVerdict{OK: true, Note: "a list of the languages spoken"}
	}
	// A multi-value listbox that asks for one employer is the same (Motional's
	// Greenhouse "Current / Most Recent Employer"): one value is selected.
	if selectish && (t == InputSingleSelect || t == InputMultiSelect && !selectSeveralRE.MatchString(q.Text)) && allowsEmployerSelection(e, q.Text) {
		return shapeVerdict{OK: true, Note: "open-set employer selection"}
	}
	// (e) a checkbox group or multi-select list; (f) a self-ID choice read as Yes/No.
	if e.Kind == KindPickOption && (t == InputMultiSelect && (q.Kind == KindCheckbox || selectish) && len(q.Options) > 1 && slices.Contains(e.InputTypes, string(InputCheckbox)) ||
		t == InputYesNo && q.Kind != KindCheckbox && hasRenderedOptions(q.Options) && e.Category == CategorySelfIdentification && e.allowsInput(InputSingleSelect)) {
		return shapeVerdict{OK: true, Note: "options bound by label"}
	}
	// (e2) a single choice the entry picks, asked as a checkbox group that does
	// not ask for every one that applies (Microchip's Workday "Please select the
	// career level desired?"): one box is ticked. A multi-value listbox is the
	// same (Figure's Greenhouse "What state do you currently reside in?"): one
	// value is selected.
	if e.Kind == KindPickOption && (q.Kind == KindCheckbox || selectish) && t == InputMultiSelect && len(realOptions(q.Options)) > 1 &&
		e.allowsInput(InputSingleSelect) && !selectSeveralRE.MatchString(q.Text) {
		return shapeVerdict{OK: true, Note: "a single choice in a checkbox group that asks for one"}
	}
	// A years entry asked as a multi-value listbox of year ranges that asks
	// for one (Fay's Greenhouse "How many years of experience do you have?"
	// over "1-2" to "10+"): one range is selected.
	if isYearsEntry(e) && selectish && t == InputMultiSelect && e.allowsInput(InputSingleSelect) && !selectSeveralRE.MatchString(q.Text) {
		if _, ok := yearsEntryBucket(e.Answer, q.Options); ok {
			return shapeVerdict{OK: true, Note: "one year range in a multi-value listbox that asks for one"}
		}
	}
	// (e3) a Yes or No the entry answers, asked as a checkbox group of Yes and No
	// (and at most one more box) that does not ask for every one that applies
	// (Truveta's Greenhouse "If not located in the greater Seattle area, are you
	// willing to relocate?" with Yes, No and "Not Applicable"): one box is ticked.
	if e.allowsInput(InputYesNo) && q.Kind == KindCheckbox && t == InputMultiSelect && isYesNoOptionSet(realOptions(q.Options)) && !selectSeveralRE.MatchString(q.Text) {
		return shapeVerdict{OK: true, Note: "a Yes or No in a checkbox group of Yes and No that asks for one"}
	}
	// (e4) a Yes or No the entry answers, asked as the list of which ones apply
	// "if any" with one option for none (SoFi: "What FINRA license(s), if any,
	// do you currently hold?" over "N/A" and each Series): a No ticks the none.
	if e.allowsInput(InputYesNo) && (q.Kind == KindCheckbox && (t == InputCheckbox || t == InputMultiSelect) || selectish && t == InputMultiSelect) {
		if _, ok := ifAnyNoneOption(q); ok {
			return shapeVerdict{OK: true, Note: "a Yes or No asked as which ones apply, if any, with one option for none"}
		}
	}
	// A self-identification answer is one box of a "select all that apply"
	// group (Netflix's gender identities), or one selection of a multi-value
	// listbox (Faire's Greenhouse demographic questions): the recorded choice,
	// or the page's decline, is the one chosen.
	if e.Kind == KindPickOption && e.Category == CategorySelfIdentification &&
		(q.Kind == KindCheckbox && (t == InputCheckbox || t == InputMultiSelect) || selectish && t == InputMultiSelect) &&
		len(realOptions(q.Options)) > 1 && e.allowsInput(InputSingleSelect) {
		return shapeVerdict{OK: true, Note: "self-identification answer in a select-all group"}
	}
	// A text-consent entry ticks the channels of a list that asks for several
	// ("Email", "Phone Call", "Text Message", "WhatsApp").
	if e.ID == textConsentEntryID && (q.Kind == KindCheckbox && (t == InputCheckbox || t == InputMultiSelect) || selectish && t == InputMultiSelect) && channelList(q.Options) {
		return shapeVerdict{OK: true, Note: "text-consent ticks a channel list"}
	}
	// A text-consent entry answers a channel list ("Email only" beside "SMS text and email").
	if selectish && t == InputSingleSelect && e.ID == textConsentEntryID {
		if _, _, ok := textChannelOptions(q.Options); ok {
			return shapeVerdict{OK: true, Note: "text-consent bound to a channel list"}
		}
	}
	// (g) an employer-history Yes/No in a text box that asks it.
	if e.Kind == KindEmployerHistory && t == InputText && len(q.Options) == 0 && polarQuestionRE.MatchString(q.Text) {
		return shapeVerdict{OK: true, Note: "employer-history Yes/No in a text box"}
	}
	// (h) a Yes/No fact the profile answers, or a recorded work-authorization
	// Yes or No (Qrypt's multi-line "Are you currently eligible to work in the
	// United States of America?"), in a text box that asks it; a multi-line
	// box (Envoy) only when it asks for nothing beyond the Yes or No.
	// A short topic header ("Immigration: Will you ...") and a closing optional
	// invitation to explain (Ello) leave the question a Yes/No.
	asked := topicHeaderRE.ReplaceAllString(optionalExplainRE.ReplaceAllString(q.Text, "?"), "")
	recordedAuth := e.Kind == KindFixed && e.Category == CategoryWorkAuthorization && (plainYesRE.MatchString(e.Answer) || plainNoRE.MatchString(e.Answer))
	if (e.derived() || recordedAuth) && e.allowsInput(InputYesNo) && (t == InputText || t == InputLongText && !followUpRE.MatchString(q.Text)) &&
		len(q.Options) == 0 && askedYesNoRE.MatchString(asked) && !narrativeVerbsRE.MatchString(asked) {
		return shapeVerdict{OK: true, Note: "a profile-derived Yes/No in a text box that asks it"}
	}
	// (k) a pick-option entry's typed text in a multi-line box whose short
	// question asks for no narrative ("How did you hear about this job?").
	// A closing "For example: ..." sentence shows answers and asks nothing
	// (Avride); a closing "If ... a current team member, please share their
	// name" asks only for a referrer (Close).
	if head := withoutClosingAside(q.Text); e.Kind == KindPickOption && t == InputLongText && len(q.Options) == 0 && e.allowsInput(InputText) &&
		len(head) <= scalarMaxLen && !narrativeVerbsRE.MatchString(head) {
		return shapeVerdict{OK: true, Note: "a pick-option entry's text in a multi-line box with a short question"}
	}
	// (l) the typed signature in a multi-line box whose text asks for the name
	// to be typed (GDIT's Workday questionnaire renders every text answer as a
	// textarea).
	if e.ID == IDApplicantFullName && t == InputLongText && len(q.Options) == 0 && typedNameAskRE.MatchString(q.Text) {
		return shapeVerdict{OK: true, Note: "the typed signature in a multi-line box"}
	}
	// (m) a compensation figure in a multi-line box ("If you have specific
	// salary expectations, please list below"); the narrative gate has already
	// deferred a box that asks for a story.
	if e.Kind == KindCompensationRange && t == InputLongText && len(q.Options) == 0 && e.allowsInput(InputText) {
		return shapeVerdict{OK: true, Note: "a compensation figure in a multi-line box"}
	}
	// A fixed compensation figure ("58" for the hourly rate) is one as well
	// (Avride: "Please share your hourly salary requirements for this role.").
	if e.Kind == KindFixed && e.Category == CategoryCompensation && t == InputLongText && len(q.Options) == 0 && compensationFigureRE.MatchString(e.Answer) {
		return shapeVerdict{OK: true, Note: "a compensation figure in a multi-line box"}
	}
	// (p) a pick-option entry's own typed text, a short scalar, in a text box
	// whose question, past a section title, is short and asks for no
	// narrative (Nominal's textarea "New Grad 2027 Application Questions : What
	// is your preferred programming language for interviews?": "Python").
	if e.Kind == KindPickOption && (t == InputText || t == InputLongText) && len(q.Options) == 0 && isSingleLineScalar(e.FallbackText) &&
		len(pastSectionTitle(q.Text)) <= scalarMaxLen && !narrativeVerbsRE.MatchString(shareScalarRE.ReplaceAllString(q.Text, "")) {
		return shapeVerdict{OK: true, Note: "a pick-option entry's typed text in a box whose short question asks for no story"}
	}
	// (r) a frequency list the entry's own answer names one of (Fairmarkit:
	// "How often do you use AI coding agents ...?" over "Daily", ...; the
	// answer says "my daily driver"): fixedEntry picks it.
	if t == InputSingleSelect && e.Kind == KindFixed && howOftenRE.MatchString(q.Text) {
		if _, ok := frequencyOption(e.Answer, realOptions(q.Options)); ok {
			return shapeVerdict{OK: true, Note: "a frequency the entry's own answer names"}
		}
	}
	// (r2) a choice of tools the entry's own answer names one of (Regard:
	// "Which AI coding tool do you use most often ...?" over Cursor, Claude
	// Code, ...; the answer names "Claude Code" first): fixedEntry picks it.
	if t == InputSingleSelect && e.Kind == KindFixed && whichToolAskRE.MatchString(q.Text) {
		if _, ok := toolNamedFirst(e.Answer, realOptions(q.Options)); ok {
			return shapeVerdict{OK: true, Note: "a tool the entry's own answer names first"}
		}
	}
	// (h2) a stored bare No in a text box whose question asks a Yes or No and
	// wants more only of a Yes (Openly: "Do you have a close relative ... who
	// is currently employed at Openly? If yes, please share the name of the
	// employee and the relationship."): the No answers it whole.
	if e.Kind == KindFixed && e.allowsInput(InputYesNo) && (t == InputText || t == InputLongText) && len(q.Options) == 0 &&
		plainNoRE.MatchString(e.Answer) && askedYesNoRE.MatchString(q.Text) {
		if loc := ifYesTailRE.FindStringIndex(q.Text); loc != nil && !narrativeVerbsRE.MatchString(q.Text[:loc[0]]) {
			return shapeVerdict{OK: true, Note: "a stored No in a text box that wants more only of a Yes"}
		}
	}
	// (s) a link in a text or multi-line box whose one sentence opens by
	// asking for one link and asks for no words about it (Roboflow's Ashby
	// textarea "Share one link to a page or project you built that showcases
	// why you'd be great at this role."): fixedEntry types the URL.
	if e.allowsInput(InputURL) && (t == InputText || t == InputLongText) && len(q.Options) == 0 &&
		linkOnlyAskRE.MatchString(q.Text) && !linkWordsAskRE.MatchString(q.Text) && len(sentencesOf(q.Text)) == 1 {
		return shapeVerdict{OK: true, Note: "a link asked alone in a text box"}
	}
	// (q) a phone number in a number box (Outtake's "Contact Number" is an
	// <input type="number">): fixedEntry types its digits.
	if e.ID == idPhoneMobile && t == InputNumber && len(q.Options) == 0 && e.Kind == KindFixed {
		return shapeVerdict{OK: true, Note: "a phone number in a number box, typed as its digits"}
	}
	// (a) an async-search combobox, or a stale single placeholder option.
	if selectish && t == InputSingleSelect && len(q.Options) <= 1 && e.Kind == KindFixed && e.allowsInput(InputText) {
		return shapeVerdict{OK: true, Note: "async-search combobox accepts a free-text entry"}
	}
	if len(q.Options) == 0 && e.Kind == KindFixed && !e.BodyEmpty() {
		// (b) a short scalar in a single-line text box; a bare Yes or No never
		// answers a question that asks when, what or which, or a date label,
		// nor one that asks more of the answer (Black Canyon Consulting: "...
		// within the DC Metro Area? If not, are you willing to relocate ...":
		// the bare Yes read as living there).
		if t == InputText && isSingleLineScalar(e.Answer) && !(bareYesNoFill.MatchString(e.Answer) && (notPolarRE.MatchString(q.Text) || leavesFollowUp(e.Answer, q.Text))) {
			return shapeVerdict{OK: true, Note: "entry input types omit " + string(t) + ": typed the stored scalar into an option-less single-line control (shape gap)"}
		}
		// (o) the user's own written answer, one paragraph, in a single-line box
		// that admits its length (Ashby draws "What are some AI specific
		// technologies you are comfortable with?" as a short-text input).
		if t == InputText && e.allowsInput(InputLongText) && !strings.Contains(strings.TrimSpace(e.Answer), "\n") &&
			(q.MaxLength == 0 || utf8.RuneCountInString(e.Answer) <= q.MaxLength) {
			return shapeVerdict{OK: true, Note: "a written answer in a single-line box that admits its length"}
		}
		if t == InputLongText {
			// (c) an exact scalar in a multi-line box, or a link in a box whose short label asks for one.
			if exactScalarInTextarea(e, q.Text) {
				return shapeVerdict{OK: true, Note: "exact scalar example in a multi-line control"}
			}
			if linkBox(e, q) && bareURLRE.MatchString(strings.TrimSpace(e.Answer)) {
				return shapeVerdict{OK: true, Note: "a link in a multi-line control whose label asks for one"}
			}
			// (n) a one-line answer the entry types into text boxes, in a multi-line box whose short question asks
			// for no narrative: Workday's questionnaire renders every text answer as a textarea (Procore's "What is
			// your current or most recent job title?").
			if e.allowsInput(InputText) && isSingleLineScalar(e.Answer) && len(q.Text) <= scalarMaxLen && !narrativeVerbsRE.MatchString(q.Text) {
				return shapeVerdict{OK: true, Note: "a one-line answer in a multi-line box whose short question asks for no narrative"}
			}
		}
	}
	// (d) a date whose layout the label names, or whose question asks for a date
	// and nothing else.
	if t == InputLongText && len(q.Options) == 0 && (e.futureOnlyDate() || (e.Kind == KindFixed && isDateShaped(e.Answer))) {
		if dateLayoutNameRE.MatchString(q.Text) {
			return shapeVerdict{OK: true, Note: "date entry in a multi-line control that names its layout"}
		}
		if askedDateRE.MatchString(q.Text) && !narrativeVerbsRE.MatchString(q.Text) {
			return shapeVerdict{OK: true, Note: "date entry in a multi-line control whose question asks for a date"}
		}
	}
	return shapeVerdict{}
}

// ackPair reports two options of which one accepts and the other declines.
func ackPair(opts []Option) bool {
	return len(opts) == 2 && (isAffirmativeConsentLabel(opts[0].Label) && isNegatedConsentLabel(opts[1].Label) ||
		isAffirmativeConsentLabel(opts[1].Label) && isNegatedConsentLabel(opts[0].Label))
}

// linkBox reports a multi-line box with no options whose short label, with no
// narrative verb, asks for a link, and a link entry to answer it (Workable's
// "Github profile" and "Linkedin" textareas). A "share" whose object is the
// links is no narrative (Krea's Ashby "Share relevant links from your work or
// portfolio.").
func linkBox(e *Entry, q Question) bool {
	// The ask is the label's first sentence; an aside after it (Rilla: "Your
	// URLs Github, Linkedin, ... Whatever. You know the gist.") adds no ask,
	// and a prose ask anywhere still makes the box a narrative's.
	// A bracketed aside adds no ask either (Basis: "Please share a link to
	// your LinkedIn profile (for identity verification purposes)."), and nor
	// does an invitation to add more if one likes (Gumloop's "Link to
	// Portfolio" run into "Feel free to provide context about what you worked
	// on specifically if needed.").
	ask := optionalInviteRE.ReplaceAllString(q.Text, "")
	if loc := sentenceBoundaryRE.FindStringIndex(ask); loc != nil {
		ask = ask[:loc[0]]
	}
	ask = bracketAsideRE.ReplaceAllString(ask, "")
	return e.Kind == KindFixed && e.Category == CategoryLinks && q.InputType == InputLongText && len(q.Options) == 0 &&
		len(ask) <= scalarMaxLen && !narrativeVerbsRE.MatchString(shareScalarRE.ReplaceAllString(q.Text, "")) && questionAsksForLocator(ask)
}

var bracketAsideRE = regexpMust(`\s*\([^()]*\)`)

// optionalInviteRE is a sentence that invites more only if the applicant
// likes ("Feel free to provide context ... if needed."): it asks nothing the
// box needs.
var optionalInviteRE = regexpMust(`(?i)\s*\bfeel\s+free\s+to\b[^.?!]*[.?!]?\s*$`)

// shareScalarRE is a request to share a scalar, whose object asks for no
// story: links (Rilla), a link to a profile or a time zone (Basis).
var shareScalarRE = regexpMust(`(?i)\bshare\s+(?:any\s+|some\s+|the\s+|your\s+|a\s+)?(?:relevant\s+)?(?:links?|urls?|time[\s-]*zones?)\b`)

// polarQuestionRE is a question that opens with an auxiliary verb and "you".
var polarQuestionRE = regexpMust(`(?i)^[\W\d]*(?:have|are|were|do|did)\s+you\b`)

// askedYesNoRE is a question that asks for a Yes or a No ("Will you require
// sponsorship ...?"), after at most one lead clause set off by a comma
// (Arcadia: "In order for you to obtain or to maintain employment
// eligibility, will you now or in the future require ...?"). What the wording
// asks is still the resolver's to read.
var askedYesNoRE = regexpMust(`(?i)^[\W\d]*(?:[^,.?!]{1,120},\s*)?(?:are|were|do|did|have|will|would|can|could)\s+you\b`)

// followUpRE asks for more than the Yes or No ("If yes, please list your visa
// type.", "When does your status expire?"), which a multi-line box invites.
var followUpRE = regexpMust(`(?i)\bif\s+(?:yes|so|not|no|applicable)\b|\bplease\s+(?:list|provide|specify|include|detail|state|give|add)\b|[.?!]\s+(?:what|which|when|where|who|how)\b`)

// ifNotRE and ifSoRE open a second question that hangs on the first answer.
var (
	ifNotRE = regexpMust(`(?i)\bif\s+(?:not|no)\b`)
	ifSoRE  = regexpMust(`(?i)\bif\s+(?:yes|so)\b`)
)

// leavesFollowUp reports a bare Yes or No that leaves the second question
// unanswered or reads as the answer to the wrong one: a Yes under any "if",
// a No under "if not". A No under "if yes" answers it whole (Leap), and so
// does a Yes when the "if not" asks only to explain the No (Relativity Space).
func leavesFollowUp(answer, text string) bool {
	if wantsWordsOnlyOfANo(text) {
		return !plainYesRE.MatchString(answer)
	}
	return ifNotRE.MatchString(text) || !plainNoRE.MatchString(answer) && ifSoRE.MatchString(text)
}

// notPolarRE is a question no Yes or No answers: one that opens with a
// question word, a label that ends naming a date or a year, or one that asks
// the applicant to enter something of theirs (Thinking Machines: "Please
// enter your current title").
var notPolarRE = regexpMust(`(?i)^[\W\d]*(?:when|what|which|where|who|how)\b|\b(?:date|year)\s*[:*?]*\s*$|\bplease\s+(?:enter|list|provide|type|write|state|give)\s+your\b`)

var (
	narrativeVerbsRE = regexpMust(`(?i)\b(?:describe|explain|why|tell|share|elaborate)\b`)
	// topicHeaderRE is a short topic header before the question ("Immigration: ").
	topicHeaderRE = regexpMust(`^\W*[A-Za-z][A-Za-z /&-]{0,30}:\s+`)
	// ifYesTailRE is a closing sentence that asks for more only of a Yes.
	ifYesTailRE = regexpMust(`(?i)[.?!]\s+if\s+(?:yes|so)\b[^?]*$`)
	// linkOnlyAskRE opens a question by asking for one link, or for the
	// applicant's GitHub (Breeze: "Please share your GitHub or any other
	// relevant links ..."); linkWordsAskRE asks for words beside it.
	linkOnlyAskRE  = regexpMust(`(?i)^\W*(?:please\s+)?(?:share|provide|paste|enter|include|add|submit|send)\s+(?:us\s+)?(?:(?:one|a|an|your)\s+(?:url|link)|your\s+git\s?hub)\b`)
	linkWordsAskRE = regexpMust(`(?i)\b(?:describe|explain|tell|elaborate|summari[sz]e)\b`)
	typedNameAskRE = regexpMust(`(?i)` + typedNameAsk)
	// closingAsideRE is a closing sentence of examples after the question, one
	// that asks for a referrer's name only if someone referred the applicant,
	// or a closing "Whether ..." list of channels, after an optional "We'd love
	// to know ..." (Resend). Examples in parentheses close the question too
	// when only that referrer's name follows them (Windfall: "(e.g. LinkedIn,
	// social network, referral, etc) If a referral, please state the
	// referral's name.").
	closingAsideRE = regexpMust(`(?i)[.?!)]\s+(?:for example|for instance|e\.g\.)\s*[:,]` +
		`|[.?!]\s+` + referrerNameAside +
		`|[.?!]\s*\(\s*(?:e\.g\.|for example|for instance|such as)[^()]*\)\s*(?:$|` + referrerNameAside + `)` +
		`|[.?!]\s+(?:we(?:['’]d|\s+would)\s+love\s+to\s+(?:know|hear)\b[^.?!]*[.?!]+\s+)?whether\b[^.?!]*[.?!]?\s*$`)
)

// referrerNameAside is a closing sentence that asks for a referrer's name only
// if someone referred the applicant.
const referrerNameAside = `if\b[^.?!]{0,100}\b(?:referr(?:ed|al)|employee|team\s+member|staff\s+member)\b[^.?!]{0,80}\bnames?\b[^.?!]*[.?!]?\s*$`

// withoutClosingAside is the text before a closing aside closingAsideRE names.
func withoutClosingAside(text string) string {
	if loc := closingAsideRE.FindStringIndex(text); loc != nil {
		return text[:loc[0]+1]
	}
	return text
}

// askedDateRE is a question whose answer is a date: "what is the earliest date
// you would want to start", "which date can you start".
var askedDateRE = regexpMust(`(?i)\b(?:what|which)\s+(?:is\s+|would\s+be\s+)?(?:the\s+|your\s+)?(?:(?:earliest|first|preferred|possible|potential|expected|anticipated|estimated|target|ideal|desired|proposed|planned)\s+)*(?:start(?:ing)?\s+)?date\b`)

// exactScalarInTextarea implements widening (c): a curated entry whose own
// example is the question (any answer length), or a short question without a
// narrative verb that equals one of the entry's examples while the answer is a
// short single line.
func exactScalarInTextarea(e *Entry, text string) bool {
	want := normalizeExample(text)
	if want == "" {
		return false
	}
	equalsExample := false
	for _, ex := range e.Examples {
		n := normalizeExample(ex)
		if n == want {
			equalsExample = true
			break
		}
		if tail, ok := headingStripped(text); ok && normalizeExample(tail) == n {
			equalsExample = true
			break
		}
	}
	if !equalsExample {
		return false
	}
	if e.curated() && strings.TrimSpace(e.Answer) != "" {
		return true
	}
	return len(text) <= scalarMaxLen && !narrativeVerbsRE.MatchString(text) && isSingleLineScalar(e.Answer)
}

// canBind reports whether the entry's preferred values bind a rendered option.
// A control without a real option, or an entry without the check, always can.
func canBind(e *Entry, q Question, sc *optionScorer) bool {
	if !hasRenderedOptions(q.Options) {
		return true
	}
	if e.bindsAnyOption(q.Options, sc) {
		return true
	}
	// relocation-or-local answers a located / relocate / no set from the
	// policy, whatever its own words bind; such a set is a handful of
	// options, so a long list is never read for it. The status picker answers
	// a list of plain standings from the recorded status.
	return e.ID == IDRelocationOrLocal && len(q.Options) <= branchSetMaxOptions && isBranchSet(q.Options) ||
		e.ID == idUSWorkAuthStatus && standingList(q.Options)
}

// branchSetMaxOptions bounds the options a located / relocate / no set has,
// placeholders and side options included.
const branchSetMaxOptions = 6

// asideOptionRE is an option beside a Yes/No that answers neither, a hedge
// included (Rain: "Open to relocation", "Open to discussing").
var asideOptionRE = lazyregexp.New(`(?i)^\W*(?:not\s+sure|unsure|n\s*/\s*a|not\s+applicable|i\s+don['\x{2019}]?t\s+know|don['\x{2019}]?t\s+know|prefer\s+not\s+to\s+(?:say|answer)|open\s+to\s+(?:discuss\w*|relocat\w*|negotiat\w*))\W*$`)

// yesNoWithAsides reports exactly one bare Yes, one bare No and only aside
// options besides them.
func yesNoWithAsides(opts []Option) bool {
	yes, no, aside := 0, 0, 0
	for _, o := range opts {
		switch {
		case plainYesRE.MatchString(o.Label):
			yes++
		case plainNoRE.MatchString(o.Label):
			no++
		case asideOptionRE.MatchString(o.Label):
			aside++
		default:
			return false
		}
	}
	return yes == 1 && no == 1 && aside > 0
}
