package classify

import (
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Classifier classifies messages against one taxonomy.
type Classifier struct {
	Tax *Taxonomy
	// Threshold is the review confidence floor; zero means ReviewThreshold.
	Threshold float64
	// Windows are the sign-up windows of the product's own apply runs; mail
	// they explain is demoted to low priority.
	Windows []SignupWindow
	// WindowsKnown says a window source answered, even with no windows. When
	// it is false the classifier cannot tell mail its own sign-up caused from
	// an employer's, so account-lifecycle mail is kept out of the actionable
	// tier and out of automatic linking.
	WindowsKnown bool
}

var (
	hardEventPhraseRE  = ci(`\b(?:offer letter|job offer|interview is scheduled|interview schedul(?:e|ing|ed)|(?:scheduled|booked) (?:the |your |a |an )?(?:[a-z0-9-]+ ){0,4}?interview|calendar invit(?:e|ation)|sent (?:you )?(?:a |an |the )?(?:google )?(?:calendar )?invite|(?:will|to|can) (?:be )?interview(?:ing)? you|your interviewer|the interview will (?:be|take|last|focus|cover)|(?:initial|introductory|first|second|final|screening|remote|virtual|technical|onsite|on-site|panel)(?:[- ]round)? (?:\d+[- ]?min(?:ute)?s? )?(?:remote |virtual |video |phone )?interview|meet\.google\.com|online assessment|general assessment|assessment (?:was )?successfully submitted|assessment completed|first call|intro call|intro invite|zoom(?:\s+video)?\s+(?:call|meeting)|(?:hackerrank|codesignal|codility)\b[\s\S]{0,120}\b(?:assessment|test|challenge)|sent you a message|book (?:a |your )?time|find a time|schedule\.lever\.co|cal\.com|teams\.microsoft\.com|microsoft teams meeting)\b`)
	hardRejectPhraseRE = ci(`\b(not moving forward|decided not to proceed|pause(?:d|s|ing)? hiring for (?:this|the) role|regret to inform|position has been filled|not been selected|not selected (?:for|to|at))\b`)
	receiptSubjectRE   = ci(`\b(thank you for applying|application received|application status|application update)\b`)
	subjectMatchRE     = ci(`\b(application update|interview|offer|assessment|first call|intro invite|intro call|next steps|sent you a message)\b`)
	weakHiringRE       = ci(`\b(?:candidate|recruiter|recruiting|careers?|hiring|position|role|job|opening|applicant)\b`)
)

var deadlineTypes = map[string]bool{
	TypeInterview: true, TypeReminder: true, TypeAssessment: true, TypeReplyNeeded: true, TypeActionNeeded: true,
}

func (c *Classifier) threshold() float64 {
	if c.Threshold > 0 {
		return c.Threshold
	}
	return ReviewThreshold
}

func (c *Classifier) artifactRE() *lazyregexp.Regexp { return c.Tax.schedulingArtifact() }

// direction is outgoing when the From address is the authorized mailbox, else
// when the message carries the SENT label.
func direction(in Input, fromEmail string) string {
	if mb := strings.ToLower(strings.TrimSpace(in.Mailbox)); mb != "" && strings.ToLower(strings.TrimSpace(fromEmail)) == mb {
		return DirOutgoing
	}
	for _, l := range in.Labels {
		if l == "SENT" {
			return DirOutgoing
		}
	}
	return DirIncoming
}

// coRecipientCompany takes the employer from the first co-recipient who is
// neither the user nor a personal or tool mailbox: a scheduling assistant
// writes on an interviewer's behalf and never names the employer itself.
func (c *Classifier) coRecipientCompany(in Input) *CompanyEvidence {
	for _, h := range []string{in.To, in.Cc} {
		for _, part := range splitAddresses(h) {
			r := parseAddress(part)
			if r.Email == "" || (in.Mailbox != "" && strings.EqualFold(r.Email, in.Mailbox)) {
				continue
			}
			rp := c.Tax.senderPolicy(r, "")
			if rp.Webmail || rp.SchedulingTool {
				continue
			}
			cand := c.Tax.companyFromDomain(r, rp)
			if cand == nil || c.Tax.isGeneric(cand.value) {
				continue
			}
			return &CompanyEvidence{Value: cand.value, Strategy: StratCoRecipient, Confidence: 0.5, Span: cand.span}
		}
	}
	return nil
}

// Classify turns a message into a signal or an ignored trace. It is a pure
// function of the message, the taxonomy and the sign-up windows.
func (c *Classifier) Classify(in Input) Result {
	if in.MessageID == "" {
		return Result{Reason: ReasonInvalidMessage}
	}
	tax := c.Tax
	from, to := parseAddress(in.From), parseAddress(in.To)
	subject := sanitize(in.Subject)
	body := compact(sanitize(in.Text), maxMatchChars)
	raw := compact(sanitize(in.Raw), maxRawChars)
	search := subject + "\n" + body
	ids := extractIdentifiers(subject, body, raw)
	policy := tax.senderPolicy(from, body)
	dir := direction(in, from.Email)

	ein := eventInput{subject: subject, body: body, from: from}
	ev := tax.classifyEventWith(ein, policy, c.artifactRE())
	cands := ev.candidates
	winner := ev.winner
	if winner == nil && dir == DirOutgoing && outboundConfirmRE.MatchString(search) {
		winner = &EventEvidence{Type: TypeReplyNeeded, RuleID: "outbound.reply_confirmation"}
		cands = append(cands, Candidate{Type: TypeReplyNeeded, RuleID: winner.RuleID, Source: "combined"})
	}
	suppressed := false
	if winner == nil && selfTriggered(c.Windows, dir, emailDomain(from.Email), subject, in.ReceivedAt) {
		winner = &EventEvidence{Type: TypeLowPriority, RuleID: SelfTriggeredRule}
	}
	if winner == nil && ev.blockedType != "" {
		winner = &EventEvidence{Type: ev.blockedType, RuleID: ev.blockedRule}
		suppressed = true
	}
	if winner == nil {
		reason := ReasonNoMatch
		if len(cands) > 0 {
			reason = ReasonAllBlocked
		}
		return Result{Reason: reason, Candidates: cands}
	}

	co := tax.resolveCompany(subject, body, from, policy)
	if policy.SchedulingTool && co.Strategy != StratSubject {
		if rc := c.coRecipientCompany(in); rc != nil {
			co = *rc
		}
	}
	if dir == DirOutgoing {
		rp := tax.senderPolicy(to, "")
		var rc CompanyEvidence
		if rp.SchedulingTool {
			if cr := c.coRecipientCompany(in); cr != nil {
				rc = *cr
			}
		}
		if rc.Value == "" {
			rc = tax.resolveCompany(subject, body, Sender{Email: to.Email}, rp)
		}
		if rc.Value != "" && !tax.isGeneric(rc.Value) {
			co = rc
		}
	}
	company := UnknownCompany
	if co.Value != "" && !tax.isGeneric(co.Value) {
		company = co.Value
	}
	role := UnknownRole
	re := resolveRole(subject, body, ifKnown(company))
	if re.Value != "" && !isGenericRole(re.Value) {
		role = re.Value
	}

	received := in.ReceivedAt
	anchored := tax.anchoredSender(policy, co)
	feat := confidenceFeatures{
		trustedSender:      anchored,
		explicitCompany:    company != UnknownCompany,
		explicitRole:       role != UnknownRole,
		hardEventPhrase:    hardRejectPhraseRE.MatchString(search) || hardEventPhraseRE.MatchString(search),
		weakEventPhrase:    weakHiringRE.MatchString(search),
		subjectMatch:       receiptSubjectRE.MatchString(subject) || subjectMatchRE.MatchString(subject),
		companyStrategy:    co.Strategy,
		roleStrippedPrefix: re.StrippedPrefix,
		eventType:          winner.Type,
	}
	conf := feat.score()
	breakdown := feat.breakdown(co, re)

	var due time.Time
	if deadlineTypes[winner.Type] {
		due = parseDeadline(search, received)
	}

	jobBoard := tax.JobBoardBrands[slugOf(company)]
	var reasons []string
	needsReview := false
	if winner.Type != TypeLowPriority {
		if conf < c.threshold() {
			reasons = append(reasons, ReviewLowConfidence)
		}
		if co.Strategy == StratUnknown {
			reasons = append(reasons, ReviewCompanyUnknown)
		}
		if co.Conflict {
			reasons = append(reasons, ReviewCompanyConflict)
		}
		if jobBoard {
			reasons = append(reasons, ReviewCompanyBoard)
		}
		needsReview = winner.Type == TypeReview || len(reasons) > 0
	}
	// A sender no list vouches for never makes a signal actionable on the
	// strength of its own words: a name, an address or a body can be written by
	// anyone. An outgoing message is the user's own and needs no anchor.
	if !anchored && dir == DirIncoming && !suppressed && winner.Type != TypeLowPriority && winner.Type != TypeReview && !needsReview {
		reasons = append(reasons, ReviewSenderUnverified)
		needsReview = true
	}
	if !c.WindowsKnown && dir == DirIncoming && !suppressed && winner.Type != TypeLowPriority && lifecycleSubjectRE.MatchString(subject) {
		reasons = append(reasons, ReviewSignupWindowUnknown)
		needsReview = true
	}
	tier := TierActionable
	switch {
	case suppressed:
		tier = TierSuppressed
	case needsReview:
		tier = TierReview
	case winner.Type == TypeLowPriority:
		tier = TierLow
	}

	snippet := compact(sanitize(firstNonEmptyStr(in.Snippet, body)), maxSnippet)
	sig := &Signal{
		MessageID: in.MessageID, ThreadID: firstNonEmptyStr(in.ThreadID, in.MessageID),
		Type: winner.Type, Tier: tier, Direction: dir, Company: company, Role: role,
		Confidence: conf, ReviewReasons: reasons, DueAt: due, ReceivedAt: received,
		Sender: in.From, Subject: subject, Snippet: snippet,
		Evidence: Evidence{
			Company: co, Role: re, Event: *winner, Identifiers: ids, Confidence: breakdown, Candidates: cands,
			Spans: eventSpans(winner, subject, body),
		},
	}
	if suppressed {
		sig.ReviewReasons = []string{"sender_restricted"}
	}
	c.demoteSelfTriggered(sig, from)
	return Result{Signal: sig, Candidates: cands}
}

func ifKnown(company string) string {
	if company == UnknownCompany {
		return ""
	}
	return company
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// eventSpans locates the text that fired the winning rule's family.
func eventSpans(w *EventEvidence, subject, body string) []Span {
	var spans []Span
	for _, r := range receiptRules {
		if r.id == w.RuleID {
			if loc := r.re.FindStringIndex(subject); loc != nil {
				spans = append(spans, Span{Source: "subject", Snippet: snippetOf(subject[loc[0]:loc[1]]), Start: loc[0]})
			} else if loc := r.re.FindStringIndex(body); loc != nil {
				spans = append(spans, Span{Source: "body", Snippet: snippetOf(body[loc[0]:loc[1]]), Start: loc[0]})
			}
		}
	}
	for _, rs := range [][]rule{hardRejection, softRejection} {
		for _, r := range rs {
			if r.id != w.RuleID {
				continue
			}
			if loc := r.re.FindStringIndex(subject); loc != nil {
				spans = append(spans, Span{Source: "subject", Snippet: snippetOf(subject[loc[0]:loc[1]]), Start: loc[0]})
			} else if loc := r.re.FindStringIndex(body); loc != nil {
				spans = append(spans, Span{Source: "body", Snippet: snippetOf(body[loc[0]:loc[1]]), Start: loc[0]})
			}
		}
	}
	return spans
}
