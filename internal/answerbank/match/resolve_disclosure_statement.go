package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Statements about this application: a question whose truth depends on how this
// application was made. What the runtime can observe (a cover letter or résumé
// in the form's slot or attached by this run) is answered from observation only.
// Every other statement is answered only from an entry the user wrote, which the
// curated stages classify, so this step defers it to them; an acknowledgement
// entry is never one, since the legal-declaration setting would answer through
// it. A referral question also defers when a signature or route names the
// referral entry and the user wrote its answer (an owner decision). In every
// other case the result is the terminal context-policy abstain; a "No" is never
// inferred from the absence of an observation.

const statementFrame = `\b(?:did|have)\s+you\b[^?]{0,80}\b(?:submit|include|attach|upload|provide)\w*\b[^?]{0,80}\b`

// The applicant's authorship of this application: its content is their own,
// written by them, or made with or without AI, even when the same sentence also
// certifies that it is true. The words must be said of the application's
// content, so a truth certification that says nothing of authorship stays a
// legal declaration, an instruction to answer in one's own words stays the
// composer's machine-text refusal, and "your own GitHub profile" is no
// statement at all.
const (
	ownEndPhrase   = `(?:\s*[,.;:!?)]|\s*$|\s+(?:and|or|but|including|without|with|in|that|which|to|for)\b)`
	ownPhrase      = `(?:my|your)\s+own(?:\s+(?:work|words|writing))?` + ownEndPhrase
	contentPhrase  = `\b(?:application|answers?|responses?|submission|essays?|cover\s+letter|writing|everything)\b`
	degreePhrase   = `(?:(?:all|entirely|wholly|completely|solely|personally|only)\s+)?`
	authorAIPhrase = `(?:ai|artificial\s+intelligence|chat\s?gpt|generative(?:\s+ai)?|llms?|(?:large\s+)?language\s+models?)\b`
)

// aiRefrainPhrase is a request addressed to the applicant not to use AI.
const aiRefrainPhrase = `(?:\byou\b[^.?!]{0,30}|\bplease\s+)(?:don[’']?t|do\s+not|must\s+not|should\s+not|may\s+not|cannot|can[’']?t|not\s+to|refrain\s+from|avoid)\s+(?:the\s+)?(?:use\s+(?:of\s+)?|using\s+)?(?:any\s+)?(?:AI|artificial\s+intelligence|chat\s?gpt|generative\s+AI|LLMs?)\b`

var authorshipStatementRE = lazyregexp.New(`(?i)` +
	`\b(?:true|accurate|correct|complete|honest|genuine)\b[^.?!;]{0,30}\band\s+` + degreePhrase + ownPhrase +
	`|` + contentPhrase + `[^.?!;]{0,40}\b(?:is|are|was|were|has\s+been|have\s+been)\s+` + degreePhrase + `(?:written\s+)?(?:in\s+)?` + ownPhrase +
	`|\b(?:is|are|was|were)\s+(?:everything|(?:this|the|these|your|all)\b[^.?!;]{0,20}` + contentPhrase + `)[^.?!;]{0,60}\b` + ownPhrase +
	`|\b(?:is|are|was|were)\b[^.?!;]{0,40}\b(?:my|your)\s+own\s+(?:work|words|writing)` + ownEndPhrase +
	`|` + contentPhrase + `[^.?!;]{0,60}\b(?:written|composed|authored|drafted|prepared|completed|filled\s+(?:out|in))\s+` + degreePhrase + `by\s+(?:me|you|myself|yourself|the\s+(?:applicant|candidate)|an?\s+(?:real\s+|actual\s+)?(?:person|human(?:\s+being)?))\b` +
	// Kobie: "We include this question to ensure each application is
	// completed by a person. Please select "I confirm I am completing this
	// application myself"".
	`|\b(?:completing|filling\s+(?:out|in)|writing)\s+(?:this|the|my|your)\s+application\s+(?:myself|yourself|personally|on\s+(?:my|your)\s+own)\b` +
	`|` + contentPhrase + `[^.?!;]{0,60}\b(?:without\s+(?:the\s+)?(?:use|help|aid|assistance|support)\s+of\s+(?:any\s+)?(?:an?\s+)?|without\s+(?:using\s+)?(?:any\s+)?|(?:not\s+)?(?:been\s+)?(?:generated|written|created|produced|composed|drafted|completed)\s+(?:by|with|using)\s+(?:an?\s+|any\s+)?)` + authorAIPhrase +
	`|` + contentPhrase + `[^.?!;]{0,60}\b(?:ai|machine)[-\s]generated\b`)

// aiUseStatement asks whether AI helped with this application; route 593
// takes it to the ai-use-disclosure entry, which only the user's own answer
// fills.
const aiUseStatement = `\b(?:did|have|has)\s+you\b[^?]{0,60}\b(?:AI|artificial\s+intelligence|chat\s?gpt|generative)\b[^?]{0,80}\b(?:application|this\s+form)\b` +
	`|\b(?:was|has)\s+this\s+application\b[^?]{0,60}\b(?:AI|artificial\s+intelligence|chat\s?gpt|generative)\b` +
	`|\bapplication\b[^?]{0,40}\b(?:completed|filled\s+out|prepared|written)\b[^?]{0,20}\b(?:by|with|using)\b[^?]{0,20}\b(?:AI|artificial\s+intelligence|chat\s?gpt)\b`

var (
	coverLetterStatementRE = lazyregexp.New(`(?i)` + statementFrame + `cover\s+letters?\b`)
	resumeStatementRE      = lazyregexp.New(`(?i)` + statementFrame + `(?:r[eé]sum[eé]s?|cv\b)`)
	// resumeDoneOptionRE is an option saying the résumé was submitted
	// (YipitData: "I have submitted a PDF version of my resume."); every
	// résumé a run attaches is a PDF.
	resumeDoneOptionRE = lazyregexp.New(`(?i)^\W*i\s+(?:have\s+)?(?:submitted|uploaded|attached|included|provided)\s+(?:a|my|the)\s+(?:pdf\s+(?:version|copy|file)\s+of\s+my\s+)?(?:r[eé]sum[eé]|cv)\b`)

	// The unobservable statements: referral, AI assistance with the application
	// and the applicant's authorship of it.
	referralStatementRE      = lazyregexp.New(`(?i)\b(?:were|are)\s+you\s+(?:being\s+)?referred\b|\b(?:have|did)\s+you\s+(?:been\s+)?(?:referred|receive[d]?\s+a\s+referral)\b`)
	aiUseStatementRE         = lazyregexp.New(`(?i)` + aiUseStatement)
	unobservableStatementREs = []*lazyregexp.Regexp{
		referralStatementRE,
		aiUseStatementRE,
		// A request to the applicant not to use AI on the application, which a
		// Yes acknowledges (LiveKit: "for the application form ... we ask that
		// you don't use AI assistance"); the company's own use is not.
		lazyregexp.New(`(?i)\b(?:application|this\s+form|your\s+(?:answers|responses))\b[^.?!]{0,100}` + aiRefrainPhrase +
			`|` + aiRefrainPhrase + `[^.?!]{0,60}\b(?:application|this\s+form|your\s+(?:answers|responses))\b`),
		// A statement that no AI helped with the application ("I confirm that
		// I did not use AI to complete this application."); a company's own
		// use ("We do not use AI to screen your application") is not one.
		lazyregexp.New(`(?i)\b(?:did\s+not|didn[’']t|have\s+not|haven[’']t|never)\s+(?:use[ds]?|utiliz\w+|rel(?:y|ied)\s+on)\s+(?:any\s+|the\s+)?` + authorAIPhrase +
			`[^.?!]{0,80}\b(?:application|this\s+form|answers?|responses?|submission|cover\s+letter|essays?)\b`),
		authorshipStatementRE,
	}
	// A governmental referral is a disclosure of its own, and a referral by a
	// rehabilitation program is a fact about the applicant's past (a hiring-credit
	// screening question): neither depends on how this application was made.
	notStatementRE = lazyregexp.New(`(?i)\bgovernment\s+official\b|\bif\s+referred\b|\bvocational\s+rehab\w*|\bticket\s+to\s+work\b`)

	// The question whether an answer began with the posting's phrase, and the
	// posting's instruction to the applicant to begin with a quoted phrase.
	startPhraseStatementRE = lazyregexp.New(`(?i)\b(?:did|have)\s+you\s+(?:start|begin|began|begun|open)\w*\b[^?]{0,80}\b(?:answer|response|reply|letter)s?\b[^?]{0,80}\b(?:phrase|words?|sentence)\b`)
	startPhraseRE          = lazyregexp.New(`(?i)\b(?:start|begin|open)\s+(?:your|the)\s+(?:first\s+)?(?:written\s+)?(?:answer|response|reply|cover\s+letter)(?:\s+to\s+the\s+first\s+(?:application\s+)?question)?\s+with(?:\s+the)?(?:\s+exact)?(?:\s+(?:phrase|words?))?\s*:?\s*["\x{201C}\x{2018}]\s*([^"\x{201C}\x{201D}\x{2018}\x{2019}\n]{1,100}?)\s*["\x{201D}\x{2019}]`)
	aiAddressRE            = lazyregexp.New(`(?i)\b` + aiNounWordRE + `\b|\bchat\s?gpt\b`)
)

// IsStartPhraseStatement reports the question whether an answer began with the
// posting's phrase; the apply check answers it from the page.
func IsStartPhraseStatement(text string) bool { return startPhraseStatementRE.MatchString(text) }

// StartPhrase is the phrase a posting tells the applicant to begin an answer
// with, or "" for none, two, or one addressed to an AI or a bot (a trap).
func StartPhrase(posting string) string {
	var phrase string
	for _, sent := range sentenceSplitRE.FindAllString(StripAIDirectives(posting), -1) {
		for _, m := range startPhraseRE.FindAllStringSubmatch(sent, -1) {
			p := strings.Join(strings.Fields(m[1]), " ")
			if aiAddressRE.MatchString(sent) || phrase != "" && p != phrase {
				return ""
			}
			phrase = p
		}
	}
	return phrase
}

// IsApplicationStatement reports a statement about how this application was
// made: the cover-letter and résumé statements, and the referral and AI-assistance
// statements the user alone can settle. It is the matcher ladder's own
// definition, shared with the model lanes so both refuse the same wordings.
func IsApplicationStatement(text string) bool {
	if notStatementRE.MatchString(text) {
		return false
	}
	if coverLetterStatementRE.MatchString(text) || resumeStatementRE.MatchString(text) || IsStartPhraseStatement(text) {
		return true
	}
	for _, re := range unobservableStatementREs {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// IsUserStatement reports a statement about this application that only an
// entry the user wrote answers: the referral, AI-assistance and authorship
// statements, not one that observation or the page check answers.
func IsUserStatement(text string) bool {
	if notStatementRE.MatchString(text) || coverLetterStatementRE.MatchString(text) || resumeStatementRE.MatchString(text) || IsStartPhraseStatement(text) {
		return false
	}
	for _, re := range unobservableStatementREs {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

func runContextEntry(s *Step, id string) *Entry {
	for i := range s.Entries {
		if s.Entries[i].RunContext && s.Entries[i].ID == id {
			return &s.Entries[i]
		}
	}
	return nil
}

func statementBlock(line string) Draft {
	return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: DetailApplicationStatement}, Trace: []string{line}}
}

// observableStatement answers "Did you submit a cover letter?" and "Did you
// attach a résumé?" from the request's observation.
func observableStatement(s *Step, id string, observed *bool, what string) Draft {
	if observed == nil || !*observed {
		return statementBlock("statement about this application: " + what + " was not observed -> left to the user")
	}
	e := runContextEntry(s, id)
	if e == nil {
		return statementBlock("statement about this application: no run-context entry for " + what)
	}
	d := bindEntryAnswer(s, e, "Yes", "statement about this application: "+what+" was observed -> Yes")
	if d.Abstain != nil {
		return d
	}
	d.Source = SourceObservation
	return d
}

// resumeDoneOption chooses the one option saying the résumé was submitted when
// the run observed it attached.
func resumeDoneOption(s *Step, o Option) Draft {
	if s.Req.ResumeAttached == nil || !*s.Req.ResumeAttached {
		return statementBlock("statement about this application: the resume was not observed -> left to the user")
	}
	d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, &Entry{}, SourceObservation, true)
	d.Start = startProfileDerive
	d.Trace = []string{"statement about this application: the resume, a PDF, was observed attached -> " + quoteTerm(o.Label)}
	return d
}

// The role this application is for: "Confirm which role you are interested in
// applying for" over options that each name a kind of role (Two Sigma:
// "Internship", "Campus Fulltime Role") takes the option of the kind the
// posting's own title names, when it names exactly one.
var (
	roleAskRE  = lazyregexp.New(`(?i)\b(?:which|what)\s+(?:role|position|opportunity|job)s?\b[^?]{0,60}\b(?:apply|applying|interested)\b`)
	internRE   = lazyregexp.New(`(?i)\bintern(?:ship)?s?\b|\bco-?op\b`)
	fullTimeRE = lazyregexp.New(`(?i)\bfull[-\s]?time\b|\bnew[-\s]grad(?:uate)?\b`)
)

// roleKind is the one kind of role a text names, else "".
func roleKind(text string) string {
	switch in, full := internRE.MatchString(text), fullTimeRE.MatchString(text); {
	case in && !full:
		return "internship"
	case full && !in:
		return "full-time"
	}
	return ""
}

// postingRole answers the role question from the posting title; an option of
// no recognised kind leaves it to the user.
func postingRole(s *Step) (Draft, bool) {
	want := roleKind(s.Req.Posting.Title)
	if want == "" || !roleAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	var chosen []Option
	for _, o := range realOptions(s.Q.Options) {
		switch roleKind(o.Label) {
		case "":
			return Draft{}, false
		case want:
			chosen = append(chosen, o)
		}
	}
	if len(chosen) != 1 {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: chosen[0].Label, Value: chosen[0].Value, Score: 1}, &Entry{}, SourceObservation, true)
	d.Start = startProfileDerive
	d.Trace = []string{"the role this application is for: the posting title names a " + want + " role -> " + quoteTerm(chosen[0].Label)}
	return d, true
}

// counterpartyRE is a question whether someone is employed by an organization
// that is a client, prime contractor or partner of the employer.
var counterpartyRE = lazyregexp.New(`(?i)\b(?:employee|employed|work(?:s|ing)?\s+for)\b[^?]*\b(?:organi[sz]ation|company|entity|firm)\b[^?]*\b(?:clients?|customers?|prime\s+contractors?|partners?)\b` +
	// Dimensional: "Does your current firm have a professional working
	// relationship with Dimensional Fund Advisors?" is the firm's tie, not the
	// applicant's employment there.
	`|\byour\s+(?:current\s+|present\s+)?(?:firm|employer|company|organi[sz]ation)\s+(?:have|has|had|maintains?|holds?)\b[^?]{0,60}\b(?:relationship|dealings|contracts?|business)\s+with\b`)

// IsCounterpartyQuestion reports a question whether the applicant or a family
// member works for a client, prime contractor or partner of the employer, or
// whether the applicant's firm deals with it.
func IsCounterpartyQuestion(text string) bool { return counterpartyRE.MatchString(text) }

var (
	relCurrentFirmRE = lazyregexp.New(`(?i)\b(?:current(?:ly)?|present(?:ly)?)\b`)
	formerRE         = lazyregexp.New(`(?i)\b(?:former|past|prior)\b`)
	ownFamilyRE      = lazyregexp.New(`(?i)` + ownFamily)
	// knowledgeOnlyRE is a question asked only to the best of the applicant's
	// knowledge, or a No option that says so.
	knowledgeOnlyRE = lazyregexp.New(`(?i)\bto\s+the\s+best\s+of\s+(?:your|my)\s+knowledge\b|\bnot\s+to\s+my\s+knowledge\b`)
)

// CounterpartyAsk is a counterparty question read against the user's records.
type CounterpartyAsk struct {
	// Family is a question about a family member's employer.
	Family bool
	// Companies are the employers to research: the applicant's recorded
	// employment and the jobs of the profile's history, or the companies the
	// user recorded a relative at. Listed says such a list stands; a relatives
	// list stands only while the user's statement that it is complete is
	// current, and then it may be empty.
	Companies []string
	Listed    bool
	Yes, No   Option
	// NoFromAbsence says no relationship found may answer No: the question asks
	// only to the best of the applicant's knowledge, or its No says so.
	NoFromAbsence bool
}

// ReadCounterparty reads a counterparty question on a control with a Yes and a
// No option; ok is false for any other question or control. today tells the
// jobs that go on from the ones that ended.
func ReadCounterparty(f Facts, q Question, today Date) (CounterpartyAsk, bool) {
	if !IsCounterpartyQuestion(q.Text) {
		return CounterpartyAsk{}, false
	}
	var a CounterpartyAsk
	var yes, no bool
	for _, o := range realOptions(q.Options) {
		switch l := normalizeLabel(o.Label); {
		case !yes && yesLabelRE.MatchString(l):
			a.Yes, yes = o, true
		case !no && noLabelRE.MatchString(l):
			a.No, no = o, true
		}
	}
	if !yes || !no {
		return CounterpartyAsk{}, false
	}
	a.Family = ownFamilyRE.MatchString(q.Text)
	a.NoFromAbsence = knowledgeOnlyRE.MatchString(q.Text) || knowledgeOnlyRE.MatchString(a.No.Label)
	family := FamilyEmployment
	if a.Family {
		family = FamilyRelative
	}
	// "Your current firm" is the employers the user works for now; "a current
	// or former employee" every one.
	current := !a.Family && relCurrentFirmRE.MatchString(q.Text) && !relEverRE.MatchString(q.Text) && !formerRE.MatchString(q.Text)
	seen := map[string]bool{}
	for _, r := range relRecorded(f, family, today) {
		if current && !r.Current {
			continue
		}
		if k := relCompanyKey(r.Company); k != "" && !seen[k] {
			seen[k] = true
			a.Companies = append(a.Companies, strings.TrimSpace(r.Company))
		}
	}
	a.Listed = len(a.Companies) > 0
	if a.Family {
		a.Listed = relAttested(f, FamilyRelative)
	}
	return a, true
}

// counterpartyEmployer keeps a counterparty question from every entry: no
// entry says which organizations deal with the employer, and the government
// it names as an exception is not its subject (Booz Allen). Only the assist
// lane's research on the user's employers answers it.
func counterpartyEmployer(s *Step) (Draft, bool) {
	if !IsCounterpartyQuestion(s.Q.Text) {
		return Draft{}, false
	}
	return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: DetailCounterpartyEmployer},
		Trace: []string{"whether you, a family member or your firm deal with the employer as its client, contractor or partner: no entry holds it -> the research on your employers, else you"}}, true
}

// applicationStatement is the ApplicationStatement slot of step 4.
func applicationStatement(s *Step) (Draft, bool) {
	q := s.Q
	if notStatementRE.MatchString(q.Text) {
		return Draft{}, false
	}
	switch {
	case coverLetterStatementRE.MatchString(q.Text):
		return observableStatement(s, IDCoverLetterSubmitted, s.Req.CoverLetterAttached, "the cover letter"), true
	case resumeStatementRE.MatchString(q.Text):
		return observableStatement(s, IDResumeSubmitted, s.Req.ResumeAttached, "the resume"), true
	case len(realOptions(q.Options)) == 1 && resumeDoneOptionRE.MatchString(realOptions(q.Options)[0].Label):
		return resumeDoneOption(s, realOptions(q.Options)[0]), true
	case IsStartPhraseStatement(q.Text):
		return statementBlock("statement about this application: the start phrase is the page check's"), true
	}
	for _, re := range unobservableStatementREs {
		if !re.MatchString(q.Text) {
			continue
		}
		if userEntryNamesStatement(s) {
			s.Tracef("statement about this application: a curated entry the user wrote names the question -> step 5 answers it")
			return Draft{}, false
		}
		if re == referralStatementRE && statementAnswerWritten(s, IDReferredByCurrentEmployee) {
			s.Tracef("statement about this application: a route names %s and the user wrote its answer -> step 8 answers it", IDReferredByCurrentEmployee)
			return Draft{}, false
		}
		if re == aiUseStatementRE && statementAnswerWritten(s, IDAIUseDisclosure) {
			s.Tracef("statement about this application: a route names %s and the user wrote its answer -> step 8 answers it", IDAIUseDisclosure)
			return Draft{}, false
		}
		return statementBlock("statement about this application: no entry the user wrote names the question -> left to the user"), true
	}
	return Draft{}, false
}

// userEntryNamesStatement reports a curated entry that names the question and
// holds an answer the user wrote (Entry.UserWritten).
func userEntryNamesStatement(s *Step) bool {
	cl, hit := newClassifier(s.Index, s.Q, s.scorer()).curated()
	return hit && cl.entry.UserWritten()
}

// IDAIUseDisclosure is the user's own answer to whether AI helped with the
// application. It ships blank, so the question stays the user's until they
// write one.
const IDAIUseDisclosure = "ai-use-disclosure"

// IDReferredByCurrentEmployee is the user's own answer to "were you referred by
// an employee".
const IDReferredByCurrentEmployee = "referred-by-current-employee"

// statementAnswerWritten reports a statement that a form signature or a
// pattern route classifies to the entry with the id, holding an answer the
// user wrote: a shipped body is never the user's.
func statementAnswerWritten(s *Step, id string) bool {
	cl := newClassifier(s.Index, s.Q, s.scorer()).general()
	if cl.entry == nil || cl.entry.ID != id || (cl.stage != stageSignature && cl.stage != stageRoute) {
		return false
	}
	return cl.entry.UserWritten() && !cl.entry.Placeholder && ownText(cl.entry) != ""
}
