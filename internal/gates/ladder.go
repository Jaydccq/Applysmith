// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"slices"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Exclusion codes recorded on a leftover the completion ladder never sees. A
// statement about the application keeps its own code.
const (
	ExcludedIdentity         = "excluded_identity"
	ExcludedLegalDeclaration = "excluded_legal_declaration"
	ExcludedMarketingOptIn   = "excluded_marketing_opt_in"
	ExcludedWorkHistory      = "excluded_work_history"
	ExcludedSignature        = "excluded_signature"
	ExcludedSelfID           = "excluded_self_identification"
	ExcludedWorkAuth         = "excluded_work_authorization"
	ExcludedTaskAtLink       = "excluded_task_at_link"
	ExcludedPromptOnly       = "excluded_prompt_only"
	ExcludedCriminalHistory  = "excluded_criminal_history"
	ExcludedFollowUpDetail   = "excluded_followup_detail"
	ExcludedThirdParty       = "excluded_third_party_contact"
	ExcludedTrapField        = "excluded_trap_field"
)

// maxSelfIDLabelWords: a longer label is an ordinary question that merely
// mentions a category word. minFreeAskWords is the length from which a
// free-text ask ("describe ...", "why ...") that mentions one is ordinary too.
const (
	maxSelfIDLabelWords = 18
	minFreeAskWords     = 9
)

var (
	signatureRE = lazyregexp.New(`(?i)\b(?:signature|sign here|initials|e-?sign\w*|signed)\b`)
	// selfIDStrongRE names an axis that is self-identification wherever it
	// appears, a free-text ask included. selfIDWeakRE words also turn up in
	// ordinary long questions, so a long free-text ask that merely mentions
	// one stays with the ladder; a short one does not.
	selfIDStrongRE = lazyregexp.New(`(?i)\b(?:gender identity|sexual orientation|orientation|nationality|national origin|citizenship|citizen of|immigration status|date of birth|birth\w*|born|how old|years? old|dob|pronouns?|transgender|lgbt\w*)\b|\b(?:over|above|at least)\s+(?:the age of\s+)?(?:16|18|21)\b\s*(?:\(?years? of age|\?|$)|\b(?:16|18|21)\s*or (?:older|over|above)`)
	selfIDWeakRE   = lazyregexp.New(`(?i)\b(?:male|female|woman|non-?binary|race|ethnic\w*|gender|sex|age|disabilit\w*|disabled|veteran|military (?:status|service)|religio\w*|hispanic|latino)\b`)
	freeAskRE      = lazyregexp.New(`(?i)^\s*(?:describe|tell us|explain|what experience|how (?:do|did|would|have)|why)\b`)
	// workAuthRE reads a work-authorization question by its family, not by one
	// phrasing: a legal, permission or eligibility word near a work or
	// employment verb, the reverse order, and the sponsorship, visa and
	// immigration vocabulary.
	workAuthRE = lazyregexp.New(`(?i)\b(?:legally|lawfully|authori[sz]ed|permitted|allowed|eligible|entitled|right|permission|cleared)\b[^?.]{0,40}\b(?:work|working|be employed|employed|employment|live and work|reside and work)\b|\bwork\w*\s+(?:authori[sz]ation|permit|permission|eligibility|status|visa)\b|\bemployment (?:authori[sz]ation|eligibility|status)\b|\b(?:sponsor\w*|visa|work permit|h-?1b|green card|permanent resident|residence permit|immigration(?: status)?|stem opt)\b`)
	// jobSourceRE is the "how did you hear about this job" question, which
	// marketingRE's "hear about" opt-in wording also matches, and whose
	// channels may name veterans ("Veteran Partner") without asking about the
	// applicant.
	jobSourceRE = lazyregexp.New(`(?i)\b(?:how|where) (?:did|do) you (?:first )?hear about\b`)
	linkTaskRE  = lazyregexp.New(`(?i)https?://|\bwww\.|\bat the (?:following )?link\b|\bclick (?:here|the link)\b|\bvia the link\b|\bfollow the link\b`)
	// linkAsideRE is the optional pointer after a question ("You can check
	// out our video: <link>"): the question is answered without the link.
	linkAsideRE  = lazyregexp.New(`(?i)^\W*(?:you (?:can|may|might)(?: also)?|feel free to|optionally,?)?\s*(?:check out|watch|see|view|read|learn more)\b`)
	criminalRE   = lazyregexp.New(`(?i)(?:explain|describe|provide details|elaborate)[^?]{0,80}\b(?:convict\w*|criminal|felony|offen[sc]e|arrest\w*)\b|\b(?:convict\w*|criminal|felony)\b[^?]{0,60}\b(?:explain|describe|details)\b`)
	thirdPartyRE = lazyregexp.New(`(?i)\b(?:reference|referee|supervisor|manager|former employer)\b[^?]{0,60}\b(?:e-?mail|phone|telephone|contact)\b|\b(?:their|his|her|referr\w*|recruiter)\b[^?.]{0,60}\b(?:name|e-?mail|phone|telephone|contact)\b|\b(?:e-?mail|phone|telephone)\b[^?]{0,40}\b(?:of|for) (?:your |a |the )?(?:reference|referee|supervisor|manager)\b`)
	trapRE       = lazyregexp.New(`(?i)leave (?:this|the|it)\b[^.]{0,20}\b(?:blank|empty)|leave (?:blank|empty)|if you are a (?:human|person)`)
	dateLabelRE  = lazyregexp.New(`(?i)^\s*(?:from|to|start|end|start date|end date|from date|to date|date from|date to|month|year)\s*\*?\s*$`)
)

// selfIDOptions are options that, by themselves, name a self-identification
// axis. They are compared whole, so a country list holding "Isle of Man" is
// not read as a gender set.
var selfIDOptions = map[string]bool{
	"hispanic or latino": true, "not hispanic or latino": true, "hispanic/latino": true, "latino": true,
	"black or african american": true, "native hawaiian or other pacific islander": true,
	"american indian or alaska native": true, "two or more races": true, "non-binary": true, "nonbinary": true,
	"heterosexual": true, "straight": true, "gay": true, "lesbian": true, "bisexual": true, "queer": true,
	"asexual": true, "transgender": true, "protected veteran": true, "not a protected veteran": true,
}

// LadderRow is a control offered to the completion ladder.
type LadderRow struct {
	Label, Legend, Placeholder string
	Options                    []string
	// IdentityBound is true when the row is bound to a profile identity key.
	IdentityBound bool
	// LegalSettingOn is the autoAcceptLegalDeclarations setting.
	LegalSettingOn bool
	// EntryRow is true for a work-history or education entry row by control
	// identity; UnderEntryHeading for a row below an entry heading, which a
	// short date-type label then marks as an entry row.
	EntryRow, UnderEntryHeading bool
	// FollowUpDetailNoAnswer is true for the free-text detail slot of a
	// conditional follow-up whose parent has no answer.
	FollowUpDetailNoAnswer bool
}

// LadderExclusion decides, before any model call and as an input filter
// rather than a prompt instruction, whether a row may reach the ladder. The
// returned code is recorded on the leftover; a statement about the
// application is recorded as "application_statement".
func LadderExclusion(r LadderRow) (code string, excluded bool) {
	label, legend, placeholder := foldText(r.Label), foldText(r.Legend), foldText(r.Placeholder)
	text := legend + " " + label
	switch {
	case r.IdentityBound:
		return ExcludedIdentity, true
	case marketingRE.MatchString(text) && !jobSourceRE.MatchString(text):
		return ExcludedMarketingOptIn, true
	case !r.LegalSettingOn && declarationRE.MatchString(label):
		return ExcludedLegalDeclaration, true
	case r.EntryRow || r.UnderEntryHeading && dateLabelRE.MatchString(label):
		return ExcludedWorkHistory, true
	case signatureRE.MatchString(label):
		return ExcludedSignature, true
	case ClassifyStatement(label) != StatementNone:
		return CodeApplicationStatement, true
	// An option can state the work standing the label leaves unnamed (n8n:
	// "I can be based in the UK and do not need visa support").
	case workAuthRE.MatchString(text) || slices.ContainsFunc(r.Options, func(o string) bool { return workAuthRE.MatchString(foldText(o)) }):
		return ExcludedWorkAuth, true
	case selfIDOption(r.Options) && !jobSourceRE.MatchString(text) || ladderSelfIDText(text, label):
		return ExcludedSelfID, true
	case linkTaskRE.MatchString(label) && !linkAside(label):
		return ExcludedTaskAtLink, true
	case trapRE.MatchString(label):
		return ExcludedTrapField, true
	case criminalRE.MatchString(label):
		return ExcludedCriminalHistory, true
	case thirdPartyRE.MatchString(label):
		return ExcludedThirdParty, true
	case r.FollowUpDetailNoAnswer:
		return ExcludedFollowUpDetail, true
	case placeholder != "" && label == placeholder:
		return ExcludedPromptOnly, true
	}
	return "", false
}

var (
	// ownHistoryRE opens, at the start of the label or of one of its sentences,
	// a yes/no question about the applicant's own experience, skills,
	// industries, environments, programmes, credentials or track record; a
	// background only as a background in or with a subject.
	ownHistoryRE = lazyregexp.New(`(?:^|[.?!:;]\s+)(?:` +
		`do you have (?:[\w/+#.-]+ ){0,3}?(?:experience|exposure|knowledge|familiarity|expertise|proficiency|skills?|background (?:in|with))\b` +
		`|do you have (?:an?|any) (?:[\w-]+ ){0,3}?track ?record\b` +
		`|have you (?:(?:ever|previously|already|recently|personally|professionally|directly|actively) )*(?:worked|supported|used|built|developed|managed|led|deployed|implemented|maintained|contributed|participated|completed|attended|held|owned|operated|administered|designed|created|shipped|launched|delivered|handled|authored)\b` +
		`|do you (?:(?:actively|regularly|currently) )?contribute to\b` +
		`|are you (?:an? )?(?:graduate|alumn[a-z]*|proficient|experienced|skilled|familiar|certified|fluent)\b` +
		`|did you (?:graduate|participate|complete|attend)\b)`)
	// ownThresholdRE is a threshold the role sets: a number of years or months.
	ownThresholdRE = lazyregexp.New(`\b\d+ ?\+|\b(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten) ?(?:\+ ?|or more |plus )?(?:years?|yrs?|months?)\b|\b(?:at least|minimum of|more than) (?:\d|one|two|three|four|five|six|seven|eight|nine|ten)\b`)
	// roleRequirementRE asks whether the applicant meets what the role
	// requires or matches it: the required, necessary or minimum experience,
	// a similar or comparable role, this position.
	roleRequirementRE = lazyregexp.New(`\b(?:required|requirements?|requisite|prerequisites?|necessary|needed|minimum|mandatory|similar|comparable|equivalent)\b|\b(?:this|the|our) (?:role|position|job|opening|opportunity|posting)\b`)
	// disclosureRE is what the user's information need not mention even when
	// the true answer is Yes, so its absence is no evidence (F-10): military or
	// veteran service, a demographic or protected group, disability or health,
	// criminal history, lawsuits or bankruptcy, sanctions or exclusion lists,
	// discipline or termination, restrictive covenants, a security clearance,
	// citizenship or immigration, another name, and the applicant's ties to the
	// company, which the company_relationships lane answers.
	disclosureRE = lazyregexp.New(`\b(?:militar\w*|veterans?|armed forces|army|navy|air force|marines?|coast guard|national (?:guard|service)|reservists?|uniformed services?|enlist\w*|combat` +
		`|underrepresented|under-represented|minorit(?:y|ies)|protected (?:class|group|categor\w*|status)|identif(?:y|ies) as|indigenous|tribal|pregnan\w*|marital|religio\w*|caste|(?:labou?r|trade) unions?|union members?|unioni[sz]ed|collective bargaining` +
		`|disabilit\w*|disabled|handicap\w*|(?:physical|mental|visual|hearing|cognitive) impairments?|medical (?:condition|history|leave|treatment|reason|record|exam\w*)s?|physical exam\w*|health (?:condition|issue|problem)s?|illness\w*|injur\w*|workers'? comp\w*|drug (?:test|screen)\w*|substance abuse|controlled substances?|vaccinat\w*|covid` +
		`|criminal|convict\w*|felon\w*|misdemeanou?r|arrest\w*|charged|indict\w*|guilty|offen[cs]es?|prosecut\w*|incarcerat\w*|jail|prison|parole|probation|background (?:check|screening|investigation)s?` +
		`|lawsuits?|litigat\w*|sued|legal (?:action|proceeding|claim|dispute)s?|court|bankrupt\w*|insolven\w*` +
		`|sanction\w*|debar\w*|exclusion lists?|excluded from|denied part(?:y|ies)|ofac|ineligib\w*` +
		`|terminated|termination of (?:your |my )?employment|fired|dismiss\w*|discharged|let go|laid off|resign\w*|disciplin(?:ary|ed)|misconduct|reprimand\w*|suspended from|performance improvement plan` +
		`|non-?compet\w*|non-?solicit\w*|restrictive covenants?|garden leave` +
		`|clearances?|top secret|ts/sci|polygraph|public trust` +
		`|citizen\w*|passports?|naturali[sz]\w*|visas?|sponsor\w*|green card|permanent resident\w*|immigra\w*|work permit|right to work` +
		`|(?:other|another|different|former|previous|maiden) names?|known by` +
		`|relatives?|family members?|spouse|friends?|know anyone|interview\w*|rehire\w*|re-hire\w*|former employee|previously employed|(?:worked|employed) here|(?:with|for|at|by) us|(?:this|our) (?:company|organi[sz]ation|firm|employer))\b`)
	// ownWillingRE is a preference or a willingness.
	ownWillingRE = lazyregexp.New(`\b(?:willing|comfortable|open to|prefer\w*|interested|able to|plan to|want to|would you|could you|can you)\b`)
	// ownEmployerRE names an employer, which the user's employer-history entry
	// answers: worked at or for a proper name, employed by, in any capacity.
	ownEmployerRE = lazyregexp.New(`\b(?:[Ww]ork(?:ed|ing)|[Ii]nterned|[Ee]mployed) (?:at|for|by) [A-Z0-9]|(?i:\bin any capacity\b|\bformer employee\b|\bemployed by\b|\bemployee of\b)`)
)

// OwnHistoryQuestion reports a yes/no question about the applicant's own
// experience, skills, industries, environments, programmes, credentials or
// track record ("Do you have experience …", "Have you (ever / previously)
// worked, supported, used or built …", "Do you contribute to …", "Are you a
// graduate of …", "Have you
// participated in …", "Do you have a … track record of …", "Do you have a
// background in …"). The user's information answers it: Yes only with
// evidence, No when nothing mentions what it asks about (F-10). A threshold or
// requirement the role sets, a preference or willingness, an employer by name,
// a disclosure (Disclosure), work authorization and every row LadderExclusion
// keeps are not of this shape.
func OwnHistoryQuestion(label string) bool {
	f := foldText(label)
	if !ownHistoryRE.MatchString(f) || ownThresholdRE.MatchString(f) || roleRequirementRE.MatchString(f) || ownWillingRE.MatchString(f) ||
		ownEmployerRE.MatchString(label) || disclosureRE.MatchString(f) {
		return false
	}
	_, excluded := LadderExclusion(LadderRow{Label: label})
	return !excluded
}

// RoleRequirement reports a question about whether the applicant meets what
// the role requires or matches it ("the experience required for this role",
// "a similar role"), which no list of the applicant's skills settles.
func RoleRequirement(text string) bool { return roleRequirementRE.MatchString(foldText(text)) }

// Disclosure reports a question about the applicant that their information
// need not mention even when the true answer is Yes (disclosureRE): only the
// user's own answer settles it, never the absence of a mention (F-10).
func Disclosure(text string) bool { return disclosureRE.MatchString(foldText(text)) }

// ComputerUseExclusion decides whether the ladder's computer-use rung (owner
// decision F-9) may write a row whose value the chain already chose: never a
// self-identification question, a signature, or a legal declaration while the
// setting is off. The value is the chain's, so the model rungs' other
// exclusions (identity, work authorization) do not apply.
func ComputerUseExclusion(r LadderRow) (code string, excluded bool) {
	label, legend := foldText(r.Label), foldText(r.Legend)
	switch {
	case signatureRE.MatchString(label):
		return ExcludedSignature, true
	case !r.LegalSettingOn && declarationRE.MatchString(label):
		return ExcludedLegalDeclaration, true
	case selfIDOption(r.Options) && !jobSourceRE.MatchString(legend+" "+label) || ladderSelfIDText(legend+" "+label, label):
		return ExcludedSelfID, true
	}
	return "", false
}

// selfIDVocabulary reports whether folded text names a self-identification
// axis. The ladder and the consent gate both read it, so the two cannot drift.
func selfIDVocabulary(text string) bool {
	return selfIDStrongRE.MatchString(text) || selfIDWeakRE.MatchString(text)
}

// ladderSelfIDText applies the length rules that keep an ordinary long
// question, or a free-text ask that merely mentions a weak word, with the
// ladder.
func ladderSelfIDText(text, label string) bool {
	words := len(strings.Fields(label))
	if words > maxSelfIDLabelWords {
		return false
	}
	return selfIDStrongRE.MatchString(text) || selfIDWeakRE.MatchString(text) && !(words >= minFreeAskWords && freeAskRE.MatchString(label))
}

// selfIDOption reports whether any option names a self-identification axis:
// a distinctive option, a man and woman (or male and female) pair, white with
// asian, or an option naming veteran or disability status.
func selfIDOption(opts []string) bool {
	have := map[string]bool{}
	for _, o := range opts {
		n := foldText(o)
		have[n] = true
		if selfIDOptions[n] || strings.Contains(n, "veteran") || strings.Contains(n, "disabilit") {
			return true
		}
	}
	return have["man"] && have["woman"] || have["male"] && have["female"] || have["white"] && have["asian"]
}

// ladderFactFamilies are the leading words of the profile fact keys the
// ladder may be handed: what the applicant's work, skills, schooling,
// preferences and availability are. Anything else is withheld by default, so
// a key nobody has classified yet never reaches a model.
var ladderFactFamilies = map[string]bool{
	"skill": true, "skills": true, "experience": true, "years": true, "education": true, "degree": true,
	"school": true, "university": true, "major": true, "field": true, "language": true, "languages": true,
	"certification": true, "certifications": true, "summary": true, "headline": true, "bio": true,
	"interest": true, "interests": true, "project": true, "projects": true, "industry": true,
	"current": true, "target": true, "title": true, "role": true, "roles": true,
	"preferred": true, "preference": true, "preferences": true, "relocation": true, "remote": true,
	"availability": true, "start": true, "notice": true, "salary": true, "compensation": true,
	"timezone": true, "city": true, "country": true, "location": true,
}

// ladderFactPrefixes are the two-word openings of a family that is safe only
// with its second word: "work" alone also starts work-authorization keys.
var ladderFactPrefixes = map[string]bool{"work experience": true, "work history": true}

// withheldFactStems veto a key with a word that contains any of them: the
// authorization, sponsorship, immigration and status vocabulary in every
// spelling, so a variant nobody listed is still withheld.
var withheldFactStems = []string{"permit", "eligib", "sponsor", "visa", "status", "authori", "citizen", "immigra"}

// withheldFactWords veto a key that carries any of them, whatever it starts
// with: identity facts (name, contact details, profile links, address) and
// demographic and work-authorization facts.
var withheldFactWords = map[string]bool{
	"name": true, "names": true, "email": true, "mail": true, "phone": true, "mobile": true, "telephone": true, "tel": true,
	"linkedin": true, "github": true, "gitlab": true, "portfolio": true, "website": true, "url": true, "link": true,
	"address": true, "street": true, "zip": true, "zipcode": true, "postal": true,
	"gender": true, "sex": true, "race": true, "racial": true, "ethnicity": true, "ethnic": true, "disability": true,
	"disabled": true, "veteran": true, "orientation": true, "age": true, "birth": true, "birthday": true, "dob": true,
	"religion": true, "pronoun": true, "pronouns": true, "citizenship": true, "citizen": true, "nationality": true,
	"immigration": true, "visa": true, "sponsorship": true, "authorization": true, "authorisation": true,
	"auth": true, "marital": true, "identity": true, "lgbt": true, "transgender": true, "hispanic": true, "latino": true,
}

// LadderFactAllowed reports whether a profile fact may be handed to the
// ladder. It is an allowlist of fact families with a veto for identity and
// demographic words; an unknown key is denied.
func LadderFactAllowed(key string) bool {
	words := strings.FieldsFunc(foldText(key), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 || !ladderFactFamilies[words[0]] && !(len(words) > 1 && ladderFactPrefixes[words[0]+" "+words[1]]) {
		return false
	}
	for _, w := range words {
		if withheldFactWords[w] {
			return false
		}
		for _, stem := range withheldFactStems {
			if strings.Contains(w, stem) {
				return false
			}
		}
	}
	return true
}

// linkAside reports a label whose question, up to its first question mark,
// names no link and is followed only by an optional pointer to one.
func linkAside(label string) bool {
	question, rest, ok := strings.Cut(label, "?")
	return ok && !linkTaskRE.MatchString(question) && linkAsideRE.MatchString(rest)
}
