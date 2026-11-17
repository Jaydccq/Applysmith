// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"regexp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Gate names and reason codes of the resubmit and correction doors.
const (
	GateResubmit   Gate = "resubmit_door"
	GateCorrection Gate = "correction_door"

	CodeResubmitNotRequested    = "resubmit_not_requested"
	CodeResubmitCompanyNotNamed = "resubmit_company_not_named"
	CodeResubmitWithList        = "resubmit_with_list"
	CodeResubmitCallerRefused   = "resubmit_caller_refused"
	CodeResubmitQuestion        = "resubmit_question"
	CodeResubmitNegated         = "resubmit_negated"

	CodeCorrectionSingleOnly       = "correction_single_ask_agent_only"
	CodeCorrectionNotRequested     = "correction_not_requested"
	CodeCorrectionQuestion         = "correction_question"
	CodeCorrectionNoCompany        = "correction_company_not_named"
	CodeCorrectionNoRow            = "correction_no_ledger_row"
	CodeCorrectionOnce             = "correction_once"
	CodeCorrectionLedgerUnreadable = "correction_ledger_unreadable"
	CodeCorrectionNoSubmit         = "correction_submit_false"
	CodeCorrectionNegated          = "correction_negated"
)

// ResubmitPhrases is the wording table of a resubmit request.
var ResubmitPhrases = []string{"resubmit", "re-submit", "resend", "re-send", "send it again", "submit it again", "submit again", "send again"}

// CorrectionPhrases is the wording table of a correction request. Adjectival
// uses ("the correct answer", "is correct") are not in it on purpose.
var CorrectionPhrases = []string{
	"correct the answers", "correct my answers", "correct the application", "correct my application",
	"correct the submission", "correct my submission", "redo the answers", "redo my answers",
}

func phraseRE(phrases []string) *lazyregexp.Regexp {
	parts := make([]string, len(phrases))
	for i, p := range phrases {
		parts[i] = strings.ReplaceAll(regexp.QuoteMeta(p), " ", `\s+`)
	}
	return lazyregexp.New(`(?:^|[^\p{L}\p{N}])(?:` + strings.Join(parts, "|") + `)(?:[^\p{L}\p{N}]|$)`)
}

var (
	negationRE = lazyregexp.New(`\b(?:not|never|stop|cancel|without|no need|do ?n'?t|won'?t|can'?t|cannot|shouldn'?t|mustn'?t)\b`)
	clauseRE   = lazyregexp.New(`[.,;:!?\n]+|\bbut\b|\bthen\b`)
)

// requestText prepares a user message for wording checks: folded, with pasted
// URLs removed so a slug or a question mark inside a link counts for nothing.
func requestText(msg string) string { return foldText(stripURLs(foldText(msg))) }

// asksFor reports whether the phrases appear in a clause that does not deny
// them ("do not resubmit", "no need to correct", "stop"), and whether any
// clause that has them denies them. A denied phrase refuses the request.
func asksFor(re *lazyregexp.Regexp, text string) (asked, negated bool) {
	for _, clause := range clauseRE.Split(text, -1) {
		if !re.MatchString(clause) {
			continue
		}
		if negationRE.MatchString(clause) {
			negated = true
		} else {
			asked = true
		}
	}
	return asked, negated
}

var (
	resubmitRE   = phraseRE(ResubmitPhrases)
	correctionRE = phraseRE(CorrectionPhrases)
)

// ResubmitInput holds the facts for the resubmit flag.
type ResubmitInput struct {
	Caller Caller
	// Message is the user's message (Ask Agent).
	Message string
	// Company is the full name of the company whose row is re-sent.
	Company string
	// CLIFlag is a typed --resubmit; List is true for a list request.
	CLIFlag, List bool
}

// ResubmitDoor accepts resubmit only from an explicit user request: the Ask
// Agent with the company named in full, or the CLI flag on a single posting.
func ResubmitDoor(in ResubmitInput) Decision {
	const g = GateResubmit
	switch in.Caller {
	case CallerCLI:
		switch {
		case !in.CLIFlag:
			return decide(g, Deny, CodeResubmitNotRequested, "--resubmit was not typed")
		case in.List:
			return decide(g, Deny, CodeResubmitWithList, "--resubmit applies to a single posting, never together with --list")
		}
		return decide(g, Allow, CodeOK, "--resubmit on a single posting")
	case CallerAskAgent:
		text := requestText(in.Message)
		asked, negated := asksFor(resubmitRE, text)
		switch {
		case !asked && !negated:
			return decide(g, Deny, CodeResubmitNotRequested, "the message does not ask to re-send an application")
		case strings.ContainsAny(text, "?"):
			return decide(g, Deny, CodeResubmitQuestion, "a question is not a request to re-send an application")
		case negated || !asked:
			return decide(g, Deny, CodeResubmitNegated, "the message tells the run not to re-send the application")
		case !namesWord(text, foldText(in.Company)):
			return decide(g, Deny, CodeResubmitCompanyNotNamed, "name the company in full to re-send its application")
		}
		return decide(g, Allow, CodeOK, "resubmit asked for %s by name", in.Company)
	}
	return decide(g, Deny, CodeResubmitCallerRefused, "resubmit is accepted only from the Ask Agent or the CLI")
}

// CorrectionRow is a ledger row of the posting, for the correction door.
type CorrectionRow struct {
	ID string
	At int64
	// Company is the ledger row's company; ReportCompany is the bound
	// report's, used when the row names none.
	Company, ReportCompany string
	// Supersedes is set on a correction row; SupersededByOther on a row that
	// another row supersedes.
	Supersedes, SupersededByOther bool
}

// CorrectionInput holds the facts for the correction door. Every fact comes
// from the user's message and the ledger, never from a tool argument.
type CorrectionInput struct {
	Caller         Caller
	SinglePosting  bool
	Message        string
	Submit         bool
	LedgerReadable bool
	// Rows are the ledger rows of the posting's identity key.
	Rows []CorrectionRow
}

// CorrectionDecision is the door's verdict. On allow, SupersedesRowID names
// the row the new row supersedes and Trace is the learning trace to carry.
type CorrectionDecision struct {
	Decision
	SupersedesRowID string
	Trace           string
}

// CorrectionTrace is carried by every learning event of a correction run.
const CorrectionTrace = "submission-correction"

// CorrectionDoor decides whether a confirmed submission may be re-sent as a
// correction. It opens only for a single posting through the Ask Agent.
func CorrectionDoor(in CorrectionInput) CorrectionDecision {
	const g = GateCorrection
	refuse := func(code, why string) CorrectionDecision {
		return CorrectionDecision{Decision: decide(g, Deny, code, "correction refused: %s", why).withClass(ClassPosting)}
	}
	text := requestText(in.Message)
	asked, negated := asksFor(correctionRE, text)
	switch {
	case in.Caller != CallerAskAgent || !in.SinglePosting:
		return refuse(CodeCorrectionSingleOnly, "a correction is offered only for a single posting through the Ask Agent")
	case !asked && !negated:
		return refuse(CodeCorrectionNotRequested, "the message carries no correction wording")
	case strings.ContainsAny(text, "?"):
		return refuse(CodeCorrectionQuestion, "a question is not a correction request")
	case negated || !asked:
		return refuse(CodeCorrectionNegated, "the message tells the run not to correct the submission")
	case !in.Submit:
		return refuse(CodeCorrectionNoSubmit, "a correction re-sends the application, so submit must be true")
	case !in.LedgerReadable:
		return refuse(CodeCorrectionLedgerUnreadable, "the submission ledger cannot be read")
	case len(in.Rows) == 0:
		return refuse(CodeCorrectionNoRow, "no ledger row for this posting")
	}
	newest := in.Rows[0]
	for _, r := range in.Rows {
		if r.At >= newest.At {
			newest = r
		}
		if r.Supersedes || r.SupersededByOther {
			return refuse(CodeCorrectionOnce, "a posting is corrected once")
		}
	}
	company := newest.Company
	if strings.TrimSpace(company) == "" {
		company = newest.ReportCompany
	}
	if !namesWord(text, foldText(company)) {
		return refuse(CodeCorrectionNoCompany, "the message must name the company")
	}
	return CorrectionDecision{
		Decision:        decide(g, Allow, CodeOK, "correction of the confirmed submission for %s", company),
		SupersedesRowID: newest.ID,
		Trace:           CorrectionTrace,
	}
}

// CorrectionAsked reports whether the message asks for a correction in a
// clause that does not deny it. Such a message makes a run a correction
// request, which CorrectionDoor then decides; a question or a denial in
// another clause is refused there, never read as an ordinary run.
func CorrectionAsked(message string) bool {
	asked, _ := asksFor(correctionRE, requestText(message))
	return asked
}

// CorrectionMessage is the exact request the runs board offers for a
// submitted application. It names the company outside the URL, carries a
// phrase of the table and the apply verbs the Ask Agent's submit gate reads,
// and is no question, so CorrectionDoor decides it on the ledger alone.
func CorrectionMessage(company, postingURL string) string {
	return "Correct the application for " + strings.TrimSpace(company) + ": fill it in again with the right answers and submit it. " + postingURL
}
