package learn

import (
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// Withheld names why a control's value is never stored. The server decides it
// from the control itself, whatever the client sends or flags.
type Withheld string

const (
	WithheldNone Withheld = ""
	// The value of these is never stored, and nothing about the edit beyond
	// the fact of it is kept.
	WithheldPassword     Withheld = "password"
	WithheldOneTimeCode  Withheld = "one-time-code"
	WithheldPayment      Withheld = "payment"
	WithheldGovernmentID Withheld = "government-id"
	// For these the fact that an edit happened is stored, the value is not.
	WithheldSelfID          Withheld = "self-id"
	WithheldDateOfBirth     Withheld = "date-of-birth"
	WithheldProfileIdentity Withheld = "profile-identity"
)

var (
	rePassword = lazyregexp.New(`(?i)\b(?:password|passcode|pass\s*phrase|pin|secret\s+(?:answer|phrase|word|key|question)|security\s+(?:answer|question)|memorable\s+(?:word|phrase|information)|mother'?s\s+maiden\s+name)\b`)
	reOTP      = lazyregexp.New(`(?i)\b(?:one[- ]time\s+(?:code|password|pass\s*code|pin|token)|otp|2fa|two[- ]factor|authenticator\s+code|(?:verification|security|confirmation|authentication|login|sign[- ]?in)\s+(?:code|pin|token)|6[- ]digit\s+code|(?:sms|text|email|e-mail)\s+code|codes?\s+(?:we|i|that\s+we|which\s+we|sent|emailed|e-mailed|texted|mailed|messaged|delivered|from\s+(?:the\s+|your\s+)?(?:email|e-mail|text|sms|phone|app|authenticator))|(?:sent|emailed|e-mailed|texted|mailed)\s+(?:to\s+)?(?:you\s+)?(?:a\s+|the\s+)?(?:\w+\s+)?code)\b`)
	// reBareSecret is a label that is only the word for a secret, after the
	// leading verb and article a form puts in front of it.
	reBareSecret = lazyregexp.New(`^(?:code|otp|token|passcode|pin)$`)
	askVerb      = lazyregexp.New(`^(?:(?:please\s+)?(?:enter|type|input|provide)\s+)?(?:(?:the|your|a)\s+)?`)
	rePayment    = lazyregexp.New(`(?i)\b(?:(?:credit|debit|payment)\s+card|card\s+(?:number|holder|no)|cvv|cvc|security\s+code\s+\(?cvv|card\s+expir(?:y|ation)|iban|swift\s+code|swift\s*/\s*bic|bic\s*/\s*swift|routing\s+number|sort\s+code|bank\s+account|account\s+number)\b`)
	// reBareExpiry is a label that is only "expiry date": with nothing beside
	// it to say whose, it is read as a card's. A label that names what expires
	// ("Work authorization expiration date") is an ordinary question.
	reBareExpiry = lazyregexp.New(`(?i)^\W*expir(?:y|ation)(?:\s+date)?\W*$`)
	reGovID      = lazyregexp.New(`(?i)\b(?:ssn|social\s+security|national\s+insurance|national\s+(?:id|identity|identification)|passport\s+(?:number|no)|driver'?s?\s+licen[cs]e|driving\s+licen[cs]e|tax\s+(?:id|identification|payer)|taxpayer|tin|aadhaar|nric|sin\s+number|government[- ]issued\s+id(?:entification)?\s+number|identity\s+card\s+number|visa\s+number|alien\s+(?:registration\s+)?number|a-number|(?:^|uscis\s+|your\s+)a\s+number)\b`)
	reDOB        = lazyregexp.New(`(?i)\b(?:date\s+of\s+birth|birth\s*date|birthday|dob|born\s+on|year\s+of\s+birth)\b`)

	// reIdentityLabel is a short label that names the applicant's own name,
	// e-mail, phone or street address.
	reIdentityLabel = lazyregexp.New(`(?i)^(?:your\s+)?(?:(?:first|last|middle|preferred|full|legal|given|family|sur)\s*){0,2}name$|^(?:your\s+)?(?:e[- ]?mail|email)(?:\s+address)?$|^(?:your\s+)?(?:phone|telephone|mobile|cell)(?:\s*(?:number|no))?$|^(?:your\s+)?(?:street\s+)?address(?:\s+line\s*\d?)?$|^(?:your\s+)?(?:linkedin|github)(?:\s+(?:profile|url))?$`)
	askLead         = lazyregexp.New(`^(?:what(?:'s|\s+is)|please\s+(?:enter|provide)|enter|provide)\s+`)
	// notOwnIdentity marks the identity of somebody else.
	notOwnIdentity = lazyregexp.New(`(?i)\b(?:referr?er|reference|emergency|manager|supervisor|recruiter|employer|company|school|university|friend|spouse|partner|contact|beneficiary|dependent|relative)\b`)
)

// maxIdentityLabel is the longest label judged an identity datum.
const maxIdentityLabel = 60

// Classify decides, from the question text, the control and its options,
// whether the control's value must never be stored. inputType and autocomplete
// are the control's own attributes (empty when the sender has none); a choice
// control's option set is read as a self-identification set, a text control has
// none.
func Classify(question, controlKind, inputType, autocomplete string, options []string) Withheld {
	w, _ := classify(question, controlKind, inputType, autocomplete, options)
	return w
}

// ClassifyEdit is Classify for an edit of the control, as Ingest classifies
// one: an edit whose value, chosen label or replaced value is itself a
// self-identification answer on a control with options is withheld too.
func ClassifyEdit(question, controlKind, inputType, autocomplete string, options []string, values ...string) Withheld {
	w, _ := classifyEdit(question, controlKind, inputType, autocomplete, options, values...)
	return w
}

// classifyEdit is ClassifyEdit with classify's category label.
func classifyEdit(question, controlKind, inputType, autocomplete string, options []string, values ...string) (Withheld, string) {
	w, label := classify(question, controlKind, inputType, autocomplete, options)
	if w == WithheldNone && valueIsSelfIDOption(options, values...) {
		return WithheldSelfID, selfIDOther
	}
	return w, label
}

// classify is Classify with the category label that is stored in place of the
// question of a withheld control. The label is "secret:password",
// "self-id:gender" and the like, so nothing the page said survives.
func classify(question, controlKind, inputType, autocomplete string, options []string) (Withheld, string) {
	q := strings.TrimSpace(question)
	it, ac := strings.ToLower(strings.TrimSpace(inputType)), strings.ToLower(strings.TrimSpace(autocomplete))
	tokens := strings.Fields(ac)
	bare := bareLabel(q)
	var w Withheld
	switch {
	case it == "password" || slices.Contains(tokens, "current-password") || slices.Contains(tokens, "new-password") ||
		rePassword.MatchString(q) || bare == "pin" || bare == "passcode":
		w = WithheldPassword
	case slices.Contains(tokens, "one-time-code") || reOTP.MatchString(q) || bare == "code" || bare == "otp" || bare == "token":
		w = WithheldOneTimeCode
	case strings.HasPrefix(ac, "cc-") || strings.Contains(ac, " cc-") || rePayment.MatchString(q) || reBareExpiry.MatchString(q):
		w = WithheldPayment
	case reGovID.MatchString(q):
		w = WithheldGovernmentID
	case strings.Contains(ac, "bday") || reDOB.MatchString(q):
		w = WithheldDateOfBirth
	}
	if w != WithheldNone {
		return w, category(w, "")
	}
	if g, ok := termGroupOf(q); ok {
		return g.withheld, category(g.withheld, g.sub)
	}
	switch {
	case selfID(q, options):
		return WithheldSelfID, category(WithheldSelfID, "")
	case ownIdentity(q, it, ac):
		return WithheldProfileIdentity, category(WithheldProfileIdentity, "")
	}
	return WithheldNone, ""
}

// category is the label stored for a withheld control.
func category(w Withheld, sub string) string {
	switch w {
	case WithheldPassword, WithheldOneTimeCode, WithheldPayment, WithheldGovernmentID:
		return "secret:" + string(w)
	case WithheldDateOfBirth:
		return "self-id:date-of-birth"
	case WithheldSelfID:
		if sub == "" {
			sub = "other"
		}
		return "self-id:" + sub
	}
	return string(w)
}

// selfIDOther is the category of a self-identification control whose kind is
// not known.
var selfIDOther = category(WithheldSelfID, "")

// reExtraSelfID adds what the matcher's list of protected characteristics does
// not name: pronouns, LGBTQ identification and the ways a form asks for either
// without the word.
var reExtraSelfID = lazyregexp.New(`(?i)\b(?:pronouns?|lgbtq\w*|gender\s+expression|what\s+do\s+you\s+go\s+by|how\s+(?:do|should)\s+(?:you|we)\s+(?:identify|address|refer\s+to)|refer\s+to\s+you)\b`)

// bareLabel is the label with a leading ask and article, trailing marks and a
// required note removed, lowercased.
func bareLabel(question string) string {
	q := strings.TrimSpace(strings.TrimRight(question, "*:?. "))
	q = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(q), "(required)"))
	q = askVerb.ReplaceAllString(q, "")
	if reBareSecret.MatchString(q) {
		return q
	}
	return ""
}

func selfID(question string, options []string) bool {
	if match.IsSensitiveCategoryQuestion(question) || reExtraSelfID.MatchString(question) {
		return true
	}
	if len(options) == 0 {
		return false
	}
	if optionsLookSelfID(options) {
		return true
	}
	opts := make([]match.Option, len(options))
	for i, o := range options {
		opts[i] = match.Option{Value: o, Label: o}
	}
	return match.IsSelfIdentificationSet(opts)
}

func ownIdentity(question, inputType, autocomplete string) bool {
	switch inputType {
	case "email", "tel":
		return !notOwnIdentity.MatchString(question)
	}
	for _, t := range []string{"given-name", "family-name", "additional-name", "name", "email", "tel", "street-address", "address-line1", "address-line2"} {
		if autocomplete == t || strings.HasSuffix(autocomplete, " "+t) {
			return !notOwnIdentity.MatchString(question)
		}
	}
	q := strings.TrimSpace(strings.TrimRight(question, "*:? "))
	q = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(q), "(required)"))
	q = askLead.ReplaceAllString(q, "")
	if len([]rune(q)) > maxIdentityLabel || notOwnIdentity.MatchString(q) {
		return false
	}
	return reIdentityLabel.MatchString(q)
}
