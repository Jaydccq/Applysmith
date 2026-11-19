package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// CheckTiming holds the check's waits; a zero field takes the specified value.
type CheckTiming struct {
	// Reread is the wait before a second read of an uncommitted write; Settle
	// is the wait after re-writing dropped values.
	Reread, Settle time.Duration
}

func (t CheckTiming) withDefaults() CheckTiming {
	if t.Reread == 0 {
		t.Reread = 200 * time.Millisecond
	}
	if t.Settle == 0 {
		t.Settle = 1500 * time.Millisecond
	}
	return t
}

// Leftover reasons the check reports for a required row.
const (
	ReasonNotCommitted   = "write-not-committed"
	ReasonMismatch       = "write-mismatch"
	ReasonUnreadable     = "unreadable-control"
	ReasonCodeBox        = "verification-code"
	ReasonNeedsUser      = "needs-user"
	ReasonStatementBlank = "application-statement"
)

// Leftover is a required row the page still lacks an answer for, or whose
// answer the page does not hold. It is rung 4 of the completion ladder: the
// user's.
type Leftover struct {
	Selector string `json:"selector"`
	Label    string `json:"label"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
	// WrongPolarity marks a Yes/No answer the page holds the opposite of.
	WrongPolarity bool `json:"wrongPolarity,omitempty"`
	// Draft is the answer-bank entry that would answer the row.
	Draft *EntryDraft `json:"draft,omitempty"`
	// Why says what the user adds so the row can be answered.
	Why string `json:"why,omitempty"`
}

// pageState is the check's working record of one page.
type pageState struct {
	report    PageReport
	runID     string
	rows      []engine.Row
	leftovers []Leftover
	// notHeld are written values the page does not hold.
	notHeld []Leftover
	flags   []gates.LabeledFlag
	// reviewStatus is the advisory review's outcome for the readiness verdict.
	reviewStatus string
	mismatch     []gates.IdentityMismatch
	// unreadable are required rows whose control the re-read did not return.
	unreadable []Leftover
	writeKey   string
	reReadFail bool
	// answered maps a selector the check itself answered to its provenance.
	answered map[string]string
	// why maps a selector the ladder did not answer to the reason it stopped.
	why map[string]string
	// unwritten maps a selector to the ladder's last write the page did not take.
	unwritten map[string]cuWrite
	// asked holds the rows the fix planner was already asked about.
	asked map[string]bool
	// stand maps a dead copy of a re-rendered control to the row standing for it.
	stand map[int]standIn
	// timeline is set on a page with Oracle's work and education sections.
	timeline bool
}

// applicant is the part of the applicant snapshot the identity review reads.
type applicantSnapshot struct {
	Candidate struct {
		FullName  string `json:"fullName"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		Phone     string `json:"phone"`
		Address   struct {
			Country string `json:"country"`
		} `json:"address"`
	} `json:"candidate"`
	Links struct {
		LinkedIn  string `json:"linkedin"`
		GitHub    string `json:"github"`
		Portfolio string `json:"portfolio"`
	} `json:"links"`
	History wire.ApplicantHistory `json:"history"`
}

func (a applicantSnapshot) value(f gates.IdentityField) string {
	switch f {
	case gates.FieldFullName:
		return a.Candidate.FullName
	case gates.FieldFirstName:
		return a.Candidate.FirstName
	case gates.FieldLastName:
		return a.Candidate.LastName
	case gates.FieldEmail:
		return a.Candidate.Email
	case gates.FieldPhone, gates.FieldNationalPhone:
		return a.Candidate.Phone
	case gates.FieldLinkedIn:
		return a.Links.LinkedIn
	case gates.FieldGitHub:
		return a.Links.GitHub
	case gates.FieldPortfolio:
		return a.Links.Portfolio
	}
	return ""
}

// applicant reads the profile's own values. It reports false, with an empty
// snapshot, when the run has no profile reader or the profile cannot be read.
func (r *Runner) applicant(ctx context.Context) (applicantSnapshot, bool) {
	ap := r.d.Page.Applicant
	if ap == nil {
		return applicantSnapshot{}, false
	}
	var snap applicantSnapshot
	if raw, err := ap(ctx); err != nil || json.Unmarshal(raw, &snap) != nil {
		return applicantSnapshot{}, false
	}
	return snap, true
}

var identityExclude = []string{"reference", "referrer", "emergency", "manager", "supervisor", "previous", "former", "employer", "spouse", "parent", "school", "company", "institution", "preferred", "nickname", "recruiter", "friend"}

// phonePartRE names the parts of a phone that sit beside the number (Workday's
// Country Phone Code, Phone Extension, Phone Device Type, YC's Phone country);
// a label that also says "number" is the number itself.
var phonePartRE = lazyregexp.New(`\b(?:code|country|prefix|extension|ext|type|device)\b`)

// mobileWorkRE is "mobile" as a kind of software work, not a phone (Modern
// Health: "full-stack web and mobile development").
var mobileWorkRE = lazyregexp.New(`\bmobile\s+(?:development|developer|engineering|engineer|apps?|applications?|platforms?|games?|design|sdks?|frameworks?|teams?)\b`)

// networkFieldRE names the profile's LinkedIn or GitHub link as a field: the
// network alone, or followed by a field noun, a separator or the label's end.
// A question that only mentions the network ("contributions with links
// (GitHub PRs, issues ...)") is answered, not the profile's link.
var networkFieldRE = lazyregexp.New(`\b(linkedin|github)\b(?:\s*(?:$|[^\p{L}\s]|(?:profile|url|link|page|user ?name|handle|account|or|and)\b))`)

// exampleListRE is a list of examples, parenthesised ("(e.g., GitHub, Google
// Scholar, LinkedIn, personal website)") or running to the sentence's end
// ("through online channels such as LinkedIn, Instagram, Meta", Plain's "E.g.,
// LinkedIn, Built In, Wellfound"): a network named there is an example of what
// the box takes, not the box's field.
var exampleListRE = lazyregexp.New(`\(\s*(?:e\.g\.?|i\.e\.?|such as|for example|including)[^)]*\)|\b(?:e\.g\.|such as|for example|including)[,:]?\s[^.?!()]*`)

// firstAndLastRE is one box for the whole name: "First and Last Name",
// "First name & surname", Block's "First, Middle Initial, and Last Name",
// UiPath's "First Name, Middle Name (if applicable), then Last Name".
var firstAndLastRE = lazyregexp.New(`\bfirst(?: name)?\s*,?\s*(?:middle(?: name| initial)?\s*(?:\([^)]*\)\s*)?,?\s*)?(?:and|&|/|\+|then)\s*(?:last name|surname|family name)\b`)

// choiceQuestion reports a choice control whose label asks a question
// (Calendly's "In addition to email and phone communications, would you also
// like to receive communications via SMS ...?"): it holds the answer chosen,
// never the profile field the question mentions. A choice labelled as a field
// (LinkedIn's "Email address" list of the account's addresses) holds the field.
// A checkbox is ticked or not whatever it names, and a choice holding Yes or
// No answers a request (Dropbox's "Please email me about future job
// openings" list).
func choiceQuestion(r engine.Row) bool {
	switch r.Kind {
	case "checkbox":
		return true
	case "select", "custom-listbox", "radio-group":
		v := strings.TrimSpace(r.Value)
		return strings.Contains(r.Label, "?") || strings.EqualFold(v, "yes") || strings.EqualFold(v, "no")
	}
	return false
}

// identityFieldOf maps a row's label to the profile field it holds. A label
// that asks a question names its field before the question mark; a note after
// it (DoorDash's SMS opt-in: "If you select no, we will only communicate with
// you via email") names other things.
func identityFieldOf(label string) (gates.IdentityField, bool) {
	l := strings.ToLower(strings.Join(strings.Fields(label), " "))
	l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(l, "*"), " (required)"))
	if i := strings.Index(l, "?"); i >= 0 {
		l = l[:i+1]
	}
	for _, x := range identityExclude {
		if strings.Contains(l, x) {
			return "", false
		}
	}
	switch {
	case firstAndLastRE.MatchString(l):
		return gates.FieldFullName, true
	case strings.Contains(l, "first name") || strings.Contains(l, "given name"):
		return gates.FieldFirstName, true
	case strings.Contains(l, "last name") || strings.Contains(l, "surname") || strings.Contains(l, "family name"):
		return gates.FieldLastName, true
	case l == "name" || strings.HasPrefix(l, "full name") || strings.HasPrefix(l, "legal name") || strings.HasPrefix(l, "your name") || l == "full legal name":
		return gates.FieldFullName, true
	case strings.Contains(l, "email") || strings.Contains(l, "e-mail"):
		return gates.FieldEmail, true
	case (strings.Contains(l, "phone") || strings.Contains(l, "mobile") && !mobileWorkRE.MatchString(l) || strings.Contains(l, "telephone")) && (strings.Contains(l, "number") || !phonePartRE.MatchString(l)):
		return gates.FieldPhone, true
	}
	switch m := networkFieldRE.FindStringSubmatch(exampleListRE.ReplaceAllString(l, "")); {
	case m == nil:
	case m[1] == "linkedin":
		return gates.FieldLinkedIn, true
	default:
		return gates.FieldGitHub, true
	}
	return "", false
}

// otherNetwork is the second network a link label names beside the field it was
// read as ("Github Link or Linkedin Link"), and whether it names one.
func otherNetwork(label string, field gates.IdentityField) (gates.IdentityField, bool) {
	l := strings.ToLower(label)
	if !strings.Contains(l, "github") || !strings.Contains(l, "linkedin") {
		return "", false
	}
	switch field {
	case gates.FieldGitHub:
		return gates.FieldLinkedIn, true
	case gates.FieldLinkedIn:
		return gates.FieldGitHub, true
	}
	return "", false
}

// phoneHeld reports whether the page's phone value is the profile's phone in
// another format (national form, spaces, a dialling code the widget holds
// separately).
func phoneHeld(page, profile string) bool {
	a, b := gates.NormalizeIdentity(gates.FieldPhone, page), gates.NormalizeIdentity(gates.FieldPhone, profile)
	if len(a) < 7 || len(b) < 7 {
		return a == b
	}
	return strings.HasSuffix(a, b) || strings.HasSuffix(b, a)
}

var placeholderValues = map[string]bool{
	"": true, "select": true, "select...": true, "select one": true, "select an option": true, "choose": true, "choose...": true,
	"please select": true, "-- select --": true, "--": true, "none": true,
}

func isPlaceholder(v string) bool {
	return placeholderValues[strings.ToLower(strings.Join(strings.Fields(v), " "))]
}

func normValue(v string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(v)), " "))
}

// polarity returns +1 for a Yes, -1 for a No, 0 otherwise.
func polarity(v string) int {
	switch n := normValue(v); {
	case n == "yes" || n == "true" || strings.HasPrefix(n, "yes,") || strings.HasPrefix(n, "yes -"):
		return 1
	case n == "no" || n == "false" || strings.HasPrefix(n, "no,") || strings.HasPrefix(n, "no -"):
		return -1
	}
	return 0
}

// verdictOf compares what the run wrote with what the page holds now: kept,
// uncommitted (nothing or a placeholder) or the opposite polarity.
type writeVerdict int

const (
	writeKept writeVerdict = iota
	writeUncommitted
	writeWrongOption
)

func verify(written, held string) writeVerdict {
	switch {
	case normValue(written) == normValue(held):
		return writeKept
	case isPlaceholder(held):
		return writeUncommitted
	case polarity(written) != 0 && polarity(held) == -polarity(written):
		return writeWrongOption
	}
	return writeKept
}

// identityChanged reports an identity control that holds another value than
// the run wrote, by the identity review's own normalization: a site's import
// that lands after the fill is a write the page did not keep, written once
// more, not a value it reformatted (Oracle's résumé parse put "JordanAvery"
// back into First Name).
func identityChanged(r engine.Row, written, held string) bool {
	field, ok := identityFieldOf(r.Label)
	if !ok {
		return false
	}
	h := gates.NormalizeIdentity(field, held)
	return h != "" && h != gates.NormalizeIdentity(field, written)
}

// onlyUploads reports fill rows that are all file uploads the fill made.
func onlyUploads(rows []engine.Row) bool {
	for _, r := range rows {
		if r.Kind != "file" || r.Outcome != "filled" {
			return false
		}
	}
	return len(rows) > 0
}

// codeBoxRE reads a verification-code box by its label vocabulary. "otp" is a
// whole word: the letters inside "footprint" are no code box. "one-time" names
// one only with the code, password or PIN that is sent once: a question about a
// one-time bonus is the form's own.
var codeBoxRE = lazyregexp.New(`verification code|security code|one[- ]time (?:code|password|pin)\b|\botp\b|passcode|authentication code|confirmation code`)

func isCodeBox(label string) bool {
	return codeBoxRE.MatchString(strings.ToLower(label))
}

// check runs the page check: it re-reads the page, verifies every write the
// fill made against the page, answers application statements from
// observation only, calls match.TimingForReview on each timing answer, runs the
// identity review and computes the page's report.
func (s *session) check(ctx context.Context, res *engine.PageResult, ps *pageState) {
	start := s.r.d.Now()
	defer func() { ps.report.CheckMs = s.r.d.Now().Sub(start).Milliseconds() }()
	tm := s.r.d.Page.Check.withDefaults()
	ps.rows = rowsOf(res)
	rows, err := s.inspect(ctx)
	if err != nil {
		// One more look: a read that failed once is not yet an unreadable page.
		if sleepCtx(ctx, tm.Reread) {
			rows, err = s.inspect(ctx)
		}
	}
	if err != nil {
		ps.reReadFail = true
		ps.report.Notes = append(ps.report.Notes, "the page re-read failed: "+err.Error())
		return
	}
	if len(rows) == 0 && onlyUploads(ps.rows) {
		// The page's only control was the upload the fill made, and the page
		// swapped it for the file it took (evlo): nothing is left to verify.
		ps.report.Notes = append(ps.report.Notes, "the upload took the page's only control; the re-read finds nothing else to verify")
		return
	}
	if len(rows) == 0 || len(ps.rows) == 0 {
		// A page with no control read says nothing about the application: the
		// verdict treats it as a page the check could not verify.
		ps.reReadFail = true
		ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("the page read no controls (%d on the re-read, %d in the fill), so nothing on it is verified", len(rows), len(ps.rows)))
		return
	}
	held, live := indexHeld(rows, ps.rows)

	// Verify what each filled row achieved instead of trusting the writer.
	var rewrite []engine.Resolution
	var pending []engine.Row
	for _, r := range ps.rows {
		if r.Outcome != "filled" {
			continue
		}
		written := firstNonEmpty(r.OptionLabel, r.Value)
		if written == "" {
			continue
		}
		cur, ok := held[r.Selector]
		if !ok {
			if r.Required {
				ps.unreadable = append(ps.unreadable, Leftover{Selector: r.Selector, Label: r.Label, Reason: ReasonUnreadable})
			}
			continue
		}
		switch v := verify(written, cur.Value); {
		case v == writeKept && !identityChanged(r, written, cur.Value):
			ps.report.Filled++
		case v == writeWrongOption:
			ps.notHeld = append(ps.notHeld, Leftover{Selector: r.Selector, Label: r.Label, Reason: ReasonMismatch, WrongPolarity: true,
				Detail: fmt.Sprintf("wrote %q, the page holds %q", clipValue(written), clipValue(cur.Value))})
		default:
			pending = append(pending, r)
		}
	}
	if len(pending) > 0 {
		sleepCtx(ctx, tm.Reread)
		again, err := s.inspect(ctx)
		if err == nil {
			held, live = indexHeld(again, ps.rows)
		}
		for _, r := range pending {
			written := firstNonEmpty(r.OptionLabel, r.Value)
			cur := held[r.Selector].Value
			changed := identityChanged(r, written, cur)
			if verify(written, cur) == writeKept && !changed {
				ps.report.Filled++
				continue
			}
			rewrite = append(rewrite, engine.Resolution{Selector: r.Selector, Outcome: "filled", Value: r.Value, OptionLabel: r.OptionLabel, Overwrite: changed})
		}
	}
	if len(rewrite) > 0 {
		// One re-write through the engine's writer: no model call, no new answer.
		// The write goes to the control as the page holds it now: a framework that
		// renumbers its element ids on every render leaves the fill's selector
		// pointing at nothing.
		now := make([]engine.Resolution, len(rewrite))
		for i, rw := range rewrite {
			now[i] = rw
			if sel, ok := live[rw.Selector]; ok {
				now[i].Selector = sel
			}
		}
		_, _ = s.answer(ctx, res.RunID, now)
		sleepCtx(ctx, tm.Settle)
		again, err := s.inspect(ctx)
		if err == nil {
			held, _ = indexHeld(again, ps.rows)
		}
		for _, rw := range rewrite {
			row := rowFor(ps.rows, rw.Selector)
			written := firstNonEmpty(rw.OptionLabel, rw.Value)
			switch verify(written, held[rw.Selector].Value) {
			case writeKept:
				ps.report.Filled++
			case writeWrongOption:
				ps.notHeld = append(ps.notHeld, Leftover{Selector: rw.Selector, Label: row.Label, Reason: ReasonMismatch, WrongPolarity: true})
			default:
				ps.report.Uncommitted = append(ps.report.Uncommitted, row.Label)
				if row.Required {
					ps.notHeld = append(ps.notHeld, Leftover{Selector: rw.Selector, Label: row.Label, Reason: ReasonNotCommitted})
				}
			}
		}
	}
	for _, r := range ps.rows {
		if r.Outcome == "prefilled" {
			ps.report.Prefilled++
		}
	}

	s.statements(ctx, res, ps, held)
	ps.stand = standIns(ps.rows, held)
	s.adopt(res.RunID, ps)
	s.ladder(ctx, res, ps)
	s.startPhrase(ctx, res, ps)
	s.leftovers(ctx, res, ps)
	s.savedFormGaps(ctx, ps)
	s.timing(ctx, res, ps)
	s.presumedCountry(res, ps)
	s.identity(ctx, ps, held)
	s.shapeFlags(ps)
	s.review(ctx, ps)
	ps.writeKey = writeKeyOf(ps.rows)
}

func clipValue(v string) string {
	r := []rune(v)
	if len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return v
}

func rowsOf(res *engine.PageResult) []engine.Row {
	var out []engine.Row
	if res.Top.Summary != nil {
		out = append(out, res.Top.Summary.Rows...)
	}
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil {
			out = append(out, f.Status.Summary.Rows...)
		}
	}
	return out
}

func rowFor(rows []engine.Row, selector string) engine.Row {
	for _, r := range rows {
		if r.Selector == selector {
			return r
		}
	}
	return engine.Row{Selector: selector}
}

// indexHeld indexes a read of the page by selector and, for each filled row
// whose selector the read no longer holds, by that stale selector too when the
// control can be told from its label and kind alone (exactly one filled row
// and one control carry them). Some frameworks number their element ids anew
// on every render, so the fill's selector can stand for a control that is on
// screen and holding its value under another id. live maps each such stale
// selector to the selector the control has now.
func indexHeld(rows []engine.InspectRow, filled []engine.Row) (map[string]engine.InspectRow, map[string]string) {
	held := indexRows(rows)
	live := map[string]string{}
	type key struct{ label, kind string }
	norm := func(label, kind string) key { return key{normValue(label), kind} }
	sources, targets := map[key][]engine.Row{}, map[key][]engine.InspectRow{}
	for _, r := range filled {
		if r.Outcome == "filled" {
			k := norm(r.Label, r.Kind)
			sources[k] = append(sources[k], r)
		}
	}
	for _, r := range rows {
		k := norm(r.Label, r.Kind)
		targets[k] = append(targets[k], r)
	}
	for k, src := range sources {
		if k.label == "" || len(src) != 1 || len(targets[k]) != 1 {
			continue
		}
		if _, ok := held[src[0].Selector]; ok {
			continue
		}
		cur := targets[k][0]
		held[src[0].Selector], live[src[0].Selector] = cur, cur.Selector
	}
	return held, live
}

func indexRows(rows []engine.InspectRow) map[string]engine.InspectRow {
	m := make(map[string]engine.InspectRow, len(rows))
	for _, r := range rows {
		m[r.Selector] = r
	}
	return m
}

// writeKeyOf fingerprints what the page's rows hold, for the stall rule.
func writeKeyOf(rows []engine.Row) string {
	var parts []string
	for _, r := range rows {
		if r.Outcome == "filled" {
			parts = append(parts, r.Selector+"="+normValue(firstNonEmpty(r.OptionLabel, r.Value)))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// standIn is the row standing for a dead copy, and whether the page holds it.
type standIn struct {
	row  int
	live bool
}

// standIns maps each open row whose selector the page no longer holds to the
// row standing for its control. A control the page re-rendered under new ids
// during the fill left a row per render, of one label, kind and section: the
// copy the page holds stands for the others, else the first open copy.
func standIns(rows []engine.Row, held map[string]engine.InspectRow) map[int]standIn {
	type control struct{ label, kind, section string }
	key := func(r engine.Row) control {
		return control{normValue(r.Label), r.Kind, strings.Join(r.Section, "\x00")}
	}
	lead, out := map[control]standIn{}, map[int]standIn{}
	for i, r := range rows {
		_, live := held[r.Selector]
		if k := key(r); k.label != "" && r.Required && (live || open(r)) {
			if l, ok := lead[k]; !ok || live && !l.live {
				lead[k] = standIn{i, live}
			}
		}
	}
	for i, r := range rows {
		if _, live := held[r.Selector]; !live && open(r) {
			if l, ok := lead[key(r)]; ok && l.row != i {
				out[i] = l
			}
		}
	}
	return out
}

// replaced reports a dead copy of a control the page holds: the ladder works
// on the live copy only.
func (ps *pageState) replaced(i int) bool { return ps.stand[i].live }

// adopt gives the live copy of a re-rendered control no lane claimed the
// abstain of the copy it replaced: the matcher's record, whose terminal verdict
// the row alone may not carry, and the row's outcome.
func (s *session) adopt(runID string, ps *pageState) {
	for i, dead := range ps.rows {
		live := &ps.rows[ps.stand[i].row]
		if !ps.replaced(i) || live.Outcome != "skipped" {
			continue
		}
		s.r.d.Page.Matches.adopt(runID, dead.Selector, live.Selector)
		if dead.Outcome == "abstained" {
			live.Outcome, live.Reason, live.MatcherReason = dead.Outcome, dead.Reason, dead.MatcherReason
		}
	}
}

// leftovers lists the required rows left without an answer: rung 4 of the
// ladder. A verification-code box belongs to the code lane and is never sent
// anywhere else; the others are the user's, each with its reason and a
// ready-to-accept answer-bank entry draft. A control re-rendered under new ids
// during the fill is one leftover: the live selector, the most telling reason.
func (s *session) leftovers(ctx context.Context, res *engine.PageResult, ps *pageState) {
	at, score := map[int]int{}, map[int]int{}
	for i, r := range ps.rows {
		c, dead := ps.stand[i]
		if !open(r) || dead && !open(ps.rows[c.row]) {
			continue
		}
		kind := ReasonNeedsUser
		switch {
		case isCodeBox(r.Label):
			kind = ReasonCodeBox
		case match.IsUserStatement(r.Label):
			kind = ReasonStatementBlank
		}
		detail := reasonOf(r)
		told := 0
		if r.Outcome == "abstained" {
			told = 1
		}
		if w := ps.why[r.Selector]; w != "" && w != detail {
			detail, told = w, told+2
			if r.Outcome == "abstained" && !strings.HasPrefix(w, "abstained:") {
				detail = reasonOf(r) + "; " + w
			}
		}
		if !dead {
			c.row = i
		}
		l := Leftover{Selector: ps.rows[c.row].Selector, Label: r.Label, Reason: kind, Detail: detail}
		noteTranscript(&l, r, s.docs.noTranscript())
		if kind != ReasonCodeBox && r.Outcome != "failed" {
			l.Draft = s.draftOf(res.RunID, i, r)
		}
		if j, ok := at[c.row]; ok {
			if told > score[j] {
				ps.leftovers[j], score[j] = l, told
			}
			continue
		}
		at[c.row], score[len(ps.leftovers)] = len(ps.leftovers), told
		ps.leftovers = append(ps.leftovers, l)
	}
	for _, l := range ps.leftovers {
		ps.report.Unresolved = append(ps.report.Unresolved, fmt.Sprintf("%s (%s)", l.Label, l.Detail))
	}
}

// recordLeftovers keeps the user's leftovers in the page's report, those with
// no entry draft (a value the site refused) too: the user completes them in the
// kept tab, and the agent is told so.
func (ps *pageState) recordLeftovers() {
	for _, l := range ps.leftovers {
		if l.Reason == ReasonNeedsUser || l.Reason == ReasonStatementBlank {
			ps.report.Leftovers = append(ps.report.Leftovers, l)
		}
	}
}

// reasonOf is why the engine left a required row: an abstain keeps the
// matcher's own reason, anything else the match route's verdict, followed by
// the site's own message when it refused the value.
// transcriptWhy says where a transcript is added, for an upload the page names
// a transcript and the run had none to give: the stop alone read "required and
// not answered".
func transcriptWhy(r engine.Row, noneStored bool) string {
	if !noneStored || r.Kind != "file" || !strings.Contains(strings.ToLower(r.Label), "transcript") {
		return ""
	}
	return "no transcript is stored: add your transcript PDF on the Answer Bank's Transcript tab"
}

// noteTranscript marks a transcript upload left with none stored as the user's
// missing data, said with where one is added, so a list runs the row again
// once a transcript is saved (userDataStop).
func noteTranscript(l *Leftover, r engine.Row, noneStored bool) {
	if why := transcriptWhy(r, noneStored); why != "" {
		l.Why, l.Detail = why, reasonMissingData
	}
}

func reasonOf(r engine.Row) string {
	why := reasonCode(r)
	if r.Detail != "" {
		why += ": " + r.Detail
	}
	return why
}

// reasonCode is reasonOf without the site's message: codes only.
func reasonCode(r engine.Row) string {
	why := firstNonEmpty(r.MatcherReason, r.Reason, "no-answer")
	if r.Outcome == "abstained" {
		why = "abstained:" + why
	}
	return why
}

// EntryDraft is an answer-bank entry the user can accept in one action.
type EntryDraft struct {
	Pattern string   `json:"pattern"`
	Kind    string   `json:"kind"`
	Options []string `json:"options,omitempty"`
}

func (s *session) draftOf(runID string, idx int, r engine.Row) *EntryDraft {
	l := s.ladderRowOf(runID, idx, r)
	return &EntryDraft{Pattern: normText(match.Sanitize(r.Label, nil)), Kind: l.kind, Options: l.options}
}

// statements answers a statement about this application from observation only.
func (s *session) statements(ctx context.Context, res *engine.PageResult, ps *pageState, held map[string]engine.InspectRow) {
	for i, r := range ps.rows {
		kind := gates.ClassifyStatement(r.Label)
		if kind == gates.StatementNone || r.Outcome == "filled" || r.Outcome == "prefilled" || match.IsStartPhraseStatement(r.Label) {
			continue
		}
		s.statement(ctx, res.RunID, ps, i, gates.StatementInput{Kind: kind, Observed: s.statementObserved(r.Label, held)})
	}
}

// statement writes the gate's answer to one statement row, when it gives one.
func (s *session) statement(ctx context.Context, runID string, ps *pageState, i int, in gates.StatementInput) {
	out := gates.AnswerStatement(in)
	_ = s.r.audit(s.e, out.Decision, s.e.Run.Input.URL, false)
	if out.Answer == "" {
		return
	}
	got, err := s.answer(ctx, runID, []engine.Resolution{{Selector: ps.rows[i].Selector, Outcome: "filled", Value: out.Answer, OptionLabel: out.Answer}})
	if err == nil && len(got) == 1 && got[0].Outcome == "filled" {
		ps.rows[i].Outcome, ps.rows[i].Value = "filled", out.Answer
		if ps.answered == nil {
			ps.answered = map[string]string{}
		}
		ps.answered[ps.rows[i].Selector] = ProvenanceCheck
	}
}

// startPhrase puts the posting's phrase in front of the first written answer
// (the first narrative box below the question about it) this run wrote, and
// answers that question Yes only when the page holds it so.
func (s *session) startPhrase(ctx context.Context, res *engine.PageResult, ps *pageState) {
	confirm, t := -1, -1
	for i, r := range ps.rows {
		switch {
		case confirm < 0 && match.IsStartPhraseStatement(r.Label):
			confirm, t = i, -1
		case t < 0 && (r.Kind == "textarea" || r.Kind == "text") && !letterBox(r.Kind, r.Label) && (match.IsEssayQuestion(r.Label) || match.IsNarrativeText(r.Label)):
			t = i
		}
	}
	entry := ""
	if s.entry != nil {
		entry = s.entry.Text
	}
	phrase := match.StartPhrase(s.jd + "\n" + entry)
	// Only the run's first written answer takes the phrase: a later page's box never does.
	if t >= 0 && s.firstAnswer == nil {
		s.firstAnswer = ps
	}
	if phrase == "" || t < 0 || s.firstAnswer != ps || ps.rows[t].Outcome != "filled" {
		t = -1
	}
	starts := func(v string) bool { return strings.HasPrefix(strings.TrimLeft(v, " \t\r\n"), phrase) }
	if t >= 0 && !starts(ps.rows[t].Value) {
		v := phrase + map[bool]string{true: "\n\n", false: " "}[ps.rows[t].Kind == "textarea"] + strings.TrimSpace(ps.rows[t].Value)
		if got, err := s.answer(ctx, res.RunID, []engine.Resolution{{Selector: ps.rows[t].Selector, Outcome: "filled", Value: v}}); err == nil && len(got) == 1 && got[0].Outcome == "filled" {
			ps.rows[t].Value = v
		}
	}
	if confirm < 0 || ps.rows[confirm].Outcome == "filled" || ps.rows[confirm].Outcome == "prefilled" {
		return
	}
	observed := false
	if t >= 0 {
		if rows, err := s.inspect(ctx); err == nil {
			held, _ := indexHeld(rows, ps.rows)
			h, ok := held[ps.rows[t].Selector]
			observed = ok && starts(h.Value)
		}
	}
	s.statement(ctx, res.RunID, ps, confirm, gates.StatementInput{Kind: gates.StatementObservable, Observed: observed})
}

var (
	statementCoverRE  = lazyregexp.New(`(?i)\bcover\s+letter\b`)
	statementResumeRE = lazyregexp.New(`(?i)\b(?:r[eé]sum[eé]|cv\b|curriculum vitae\b)`)
)

// statementObserved reports whether every document an observable statement
// names was seen on this run: a version this run attached, or a file slot
// labelled for that document that holds a file, or a letter box that holds
// text. A statement that names no document the run can see is never observed.
func (s *session) statementObserved(label string, held map[string]engine.InspectRow) bool {
	var named []documents.Kind
	if statementCoverRE.MatchString(label) {
		named = append(named, documents.KindCover)
	}
	if statementResumeRE.MatchString(label) {
		named = append(named, documents.KindResume)
	}
	for _, kind := range named {
		if !s.documentSeen(kind, held) {
			return false
		}
	}
	return len(named) > 0
}

func (s *session) documentSeen(kind documents.Kind, held map[string]engine.InspectRow) bool {
	if s.docs != nil && s.docs.observed(kind) {
		return true
	}
	re := statementResumeRE
	if kind == documents.KindCover {
		re = statementCoverRE
	}
	for _, h := range held {
		if h.FileName != "" && re.MatchString(h.Label) && (kind == documents.KindCover || !statementCoverRE.MatchString(h.Label)) {
			return true
		}
		if kind == documents.KindCover && letterBox(h.Kind, h.Label) && strings.TrimSpace(h.Value) != "" {
			return true
		}
	}
	return false
}

// timing calls match.TimingForReview on each filled answer the matcher's own
// result is known for: a sponsorship "No" to a now-only question reads beside
// the user's later need, so a reviewer does not take it for a contradiction.
func (s *session) timing(ctx context.Context, res *engine.PageResult, ps *pageState) {
	if s.r.d.Page.Matches == nil || s.r.d.Page.Facts == nil {
		return
	}
	facts, err := s.r.d.Page.Facts(ctx)
	if err != nil {
		return
	}
	for _, r := range ps.rows {
		if r.Outcome != "filled" {
			continue
		}
		m, ok := s.r.d.Page.Matches.get(res.RunID, r.Selector)
		if !ok {
			continue
		}
		ps.report.TimingAnswers++
		if t, has := match.TimingForReview(facts, m.Q, m.R); has {
			ps.report.TimingReviewed++
			ps.report.Notes = append(ps.report.Notes, "timing: "+r.Label+" answered No now; "+timingWords(t))
		}
	}
}

// presumedCountry names the answers the matcher gave as for a posting in the
// United States because nothing placed the posting (owner decision, flow round
// 8), in this page's report and in the fill stage's detail.
func (s *session) presumedCountry(res *engine.PageResult, ps *pageState) {
	var labels []string
	for _, r := range ps.rows {
		if r.Outcome != "filled" {
			continue
		}
		if m, ok := s.r.d.Page.Matches.get(res.RunID, r.Selector); ok && m.R.Detail == match.DetailPostingCountryPresumed {
			labels = append(labels, fmt.Sprintf("%q", clip(r.Label, 80)))
		}
	}
	if len(labels) == 0 {
		return
	}
	line := strings.Join(labels, ", ") + " answered as if the posting is in the United States: its location could not be read"
	ps.report.Notes = append(ps.report.Notes, line)
	if !slices.Contains(s.walkNotes, line) {
		s.walkNotes = append(s.walkNotes, line)
	}
}

func timingWords(t match.SponsorshipTiming) string {
	f := func(b *bool) string {
		switch {
		case b == nil:
			return "unset"
		case *b:
			return "yes"
		}
		return "no"
	}
	return "sponsorship needed now: " + f(t.Now) + ", later: " + f(t.Future)
}

// identity runs the identity review over the controls the fill bound to an
// identity field.
func (s *session) identity(ctx context.Context, ps *pageState, held map[string]engine.InspectRow) {
	a, ok := s.r.applicant(ctx)
	if !ok {
		return
	}
	// The computer-use rung writes by trusted input after the page was read:
	// the review reads what the page holds now (ADP's phone box retyped).
	if ps.report.Ladder.ComputerUse > 0 {
		if again, err := s.inspect(ctx); err == nil {
			held, _ = indexHeld(again, ps.rows)
		}
	}
	var controls []gates.IdentityControl
	for _, r := range ps.rows {
		field, ok := identityFieldOf(r.Label)
		if !ok || r.Outcome == "skipped" || r.Outcome == "abstained" || choiceQuestion(r) {
			continue
		}
		cur, found := held[r.Selector]
		page := cur.Value
		// "GitHub or LinkedIn" takes either of the applicant's own links.
		if other, both := otherNetwork(r.Label, field); both && a.value(other) != "" && gates.NormalizeIdentity(other, page) == gates.NormalizeIdentity(other, a.value(other)) {
			field = other
		}
		prof := a.value(field)
		if field == gates.FieldPhone && phoneHeld(page, prof) {
			page = prof
		}
		controls = append(controls, gates.IdentityControl{Selector: r.Selector, Label: r.Label, Field: field, PageValue: page, ProfileValue: prof, ReRead: found})
	}
	out := gates.ReviewIdentity(controls)
	_ = s.r.audit(s.e, out.Decision, s.e.Run.Input.URL, false)
	ps.mismatch = out.Mismatches
	for _, m := range out.Mismatches {
		ps.report.Mismatches = append(ps.report.Mismatches, m.Label)
	}
	ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("identity: %d controls, %d unverified", len(controls), len(out.Unverified)))
}

// shapeFlags raises the two deterministic flags on the rows this run wrote.
func (s *session) shapeFlags(ps *pageState) {
	for _, r := range ps.rows {
		if r.Outcome != "filled" {
			continue
		}
		f, ok := gates.ShapeFlag(gates.ReviewRow{Label: r.Label, Value: r.Value, TextControl: r.Kind == "text" || r.Kind == "textarea", HasOptions: r.OptionLabel != "", WrittenByRun: true})
		if !ok {
			continue
		}
		d := gates.FlagGate(f, r.Label, "")
		_ = s.r.audit(s.e, d, s.e.Run.Input.URL, false)
		ps.flags = append(ps.flags, gates.LabeledFlag{Label: r.Label, Flag: f})
		ps.report.Flags = append(ps.report.Flags, r.Label)
	}
}

// gatesReport converts the page's findings for the readiness verdict.
func (ps *pageState) gatesReport(n int) gates.PageReport {
	pr := gates.PageReport{Number: n, ReReadFailed: ps.reReadFail, Flags: ps.flags, ReviewStatus: firstNonEmpty(ps.reviewStatus, "skipped")}
	for _, m := range ps.mismatch {
		prefilled := slices.ContainsFunc(ps.rows, func(r engine.Row) bool { return r.Selector == m.Selector && r.Outcome == "prefilled" })
		pr.IdentityMismatches = append(pr.IdentityMismatches, gates.Item{Page: n, Label: m.Label, SitePrefilled: prefilled})
	}
	for _, l := range ps.leftovers {
		it := gates.Item{Page: n, Label: l.Label, UserStatement: l.Reason == ReasonStatementBlank, Why: l.Why}
		// A value the page's own bound refused is said with the bound.
		if i := slices.IndexFunc(ps.rows, func(r engine.Row) bool { return r.Selector == l.Selector }); i >= 0 && ps.rows[i].Reason == "out-of-range" {
			it.Refusal = ps.rows[i].Detail
		}
		pr.RequiredUnanswered = append(pr.RequiredUnanswered, it)
	}
	// An unreadable required control is a defect, never a gap in the user's data.
	for _, l := range ps.unreadable {
		pr.NotHeld = append(pr.NotHeld, gates.Item{Page: n, Label: l.Label})
	}
	for _, l := range ps.notHeld {
		pr.NotHeld = append(pr.NotHeld, gates.Item{Page: n, Label: l.Label, WrongPolarity: l.WrongPolarity})
	}
	return pr
}
