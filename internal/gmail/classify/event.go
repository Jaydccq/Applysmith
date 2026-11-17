package classify

import (
	"regexp"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// eventInput is what the family rules read.
type eventInput struct {
	subject, body string
	from          Sender
}

func (in eventInput) haystack() string { return in.subject + "\n" + in.body }

func (in eventInput) senderText() string { return in.from.Name + " " + in.from.Email }

// Blocking guards recorded on the trace.
const (
	blockSenderPolicy = "sender_policy"
	blockLowerPrec    = "lower_precedence"
)

// schedulingArtifact is a concrete sign that a meeting exists: a scheduling
// tool's booking address (from the taxonomy), a meeting-platform link, or a
// stated date and time.
func (t *Taxonomy) schedulingArtifact() *lazyregexp.Regexp {
	t.artOnce.Do(func() {
		var hosts []string
		for h := range t.SchedulingTools {
			hosts = append(hosts, regexp.QuoteMeta(h))
		}
		sort.Strings(hosts)
		src := `\b(?:` + meetingPlatformSrc
		if len(hosts) > 0 {
			src += "|" + strings.Join(hosts, "|")
		}
		t.art = ci(src + `)\b|` + datetimeSrc)
	})
	return t.art
}

// ownWords is the text above the first quoted attribution line: only that is
// the sender's own words.
func ownWords(body string) string {
	if loc := quotedReplyRE.FindStringIndex(body); loc != nil {
		return body[:loc[0]]
	}
	return body
}

// hasHiringContext is true for trusted and vendor senders, a recruiting name
// on a non-webmail sender, or hiring vocabulary in the message.
func hasHiringContext(in eventInput, p Policy) bool {
	return p.Trusted || p.VendorATS ||
		(!p.Webmail && broadIdentity.MatchString(in.senderText())) ||
		hiringContextRE.MatchString(in.haystack())
}

func trustedClaimSender(in eventInput, p Policy) bool {
	return claimTrustNameRE.MatchString(in.senderText()) || hasHiringContext(in, p) || interviewedWithUs.MatchString(in.haystack())
}

// offerGateFailure names the failed offer guard; template says the mail reads
// as a newsletter or job alert.
func offerGateFailure(in eventInput, p Policy, template bool) string {
	h := in.haystack()
	trusted := p.Trusted || p.VendorATS || claimTrustNameRE.MatchString(in.senderText())
	switch {
	case !trusted:
		return "offer_untrusted_sender"
	case offerDisclaimer.MatchString(h):
		return "offer_negative_disclaimer"
	case template:
		return "offer_negative_template"
	case !candidateSpecRE.MatchString(h):
		return "offer_missing_candidate_specific_phrase"
	case !offerIntentRE.MatchString(h):
		return "offer_missing_offer_intent"
	}
	return ""
}

var institutionalTLD = lazyregexp.New(`(?i)\.(?:edu|gov)$`)
var namePartRE = lazyregexp.New(`(?i)^[a-z][a-z'\x{2019}-]+$`)

// interviewGateFailure is the corroboration gate of every interview-family
// claim. It returns the guard that failed, or "" when the claim stands.
func (t *Taxonomy) interviewGateFailure(in eventInput, p Policy, artifact *lazyregexp.Regexp, lifecycle string) string {
	h := in.haystack()
	institutionalPerson := false
	if !p.Webmail && institutionalTLD.MatchString(p.Domain) {
		n := 0
		for _, part := range strings.FieldsFunc(strings.TrimSpace(in.from.Name), func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
			if namePartRE.MatchString(part) {
				n++
			}
		}
		institutionalPerson = n >= 2
	}
	trusted := p.Trusted || p.VendorATS || institutionalPerson || (!p.Webmail && broadIdentity.MatchString(in.senderText()))
	if bulkTemplate(h, lifecycle == lifecycleInterview) {
		return "interview_negative_template"
	}
	if (screeningActRE.MatchString(h) || (p.Domain == "jobmail.io" && recruitingScreenSubj.MatchString(in.subject))) && !artifact.MatchString(h) {
		return "interview_screening_action"
	}
	switch {
	case trusted, artifact.MatchString(h):
		return ""
	case !p.Webmail && !p.SchedulingTool && !coldSourcingRE.MatchString(in.body) && candidateInterviewCtx.MatchString(h):
		return ""
	}
	return "interview_untrusted_no_artifact"
}

func assessmentHiringContext(t *Taxonomy, in eventInput, p Policy, hiring bool) bool {
	if p.VendorATS {
		return t.namesEmployer(in)
	}
	return hiring
}

func isLinkedInApplicationRejection(in eventInput, p Policy, h string) bool {
	if p.Domain != "linkedin.com" || !jobsNoReplyRE.MatchString(in.from.Email) {
		return false
	}
	tpl := linkedInRejectedTpl.MatchString(h) || linkedInUpdateFrom.MatchString(h)
	return tpl && linkedInWillNot.MatchString(h) && linkedInAppliedOn.MatchString(h)
}

func hasUnconditionalHardRejection(text string) bool {
	for _, r := range hardRejection {
		for _, loc := range r.re.FindAllStringIndex(text, -1) {
			before := text[max(0, loc[0]-200):loc[0]]
			clause := before
			if i := strings.LastIndexAny(before, ".!?"); i >= 0 {
				clause = before[i+1:]
			}
			if !conditionalClauseRE.MatchString(clause) {
				return true
			}
		}
	}
	return false
}

func (t *Taxonomy) candidateRejectionBeatsJobAlert(in eventInput, p Policy, h string) bool {
	if jobDigestRE.MatchString(h) {
		return false
	}
	if isLinkedInApplicationRejection(in, p, h) {
		return true
	}
	if !p.allows(TypeRejection) || !trustedClaimSender(in, p) {
		return false
	}
	if !candidateApplicationRE.MatchString(h) {
		return false
	}
	if hasUnconditionalHardRejection(h) {
		return true
	}
	return softDecisionRE.MatchString(in.body) && hiringNounRE.MatchString(in.body) &&
		(firstRule(receiptRules, in.body) == nil || softVerdictRE.MatchString(in.body))
}

// lifecycleGuard names the guard that keeps a low-priority family, and a list
// footer, from claiming mail that states a lifecycle event of this applicant's
// own application (a decision, a receipt, an offer, an interview invitation, a
// requested action): such mail carries a survey invitation or a list footer of
// its own. It is "" when the low-priority claim stands.
func (t *Taxonomy) lifecycleGuard(in eventInput, p Policy, h string, receipt *rule, artifact *lazyregexp.Regexp) string {
	if jobDigestRE.MatchString(h) {
		return ""
	}
	switch {
	case t.candidateRejectionBeatsJobAlert(in, p, h):
		return "candidate_rejection"
	case firstRule(hardRejection, h) != nil:
		return ""
	case explicitOfferRE.MatchString(h) && offerGateFailure(in, p, false) == "":
		return lifecycleOffer
	case (p.Trusted || p.VendorATS) && artifact.MatchString(h) && (candidateApplicationRE.MatchString(h) || candidateInterviewInviteRE.MatchString(h)) &&
		(interviewStrongRE.MatchString(in.body) || interviewRE.MatchString(h)):
		return lifecycleInterview
	case receipt != nil && p.allows(TypeApplied) && trustedClaimSender(in, p):
		return "application_receipt"
	case actionRequiredRE.MatchString(h) && candidateApplicationRE.MatchString(h) && trustedClaimSender(in, p):
		return "candidate_action"
	}
	return ""
}

// Lifecycle guards whose own family a list footer does not veto.
const (
	lifecycleOffer     = "candidate_offer"
	lifecycleInterview = "candidate_interview"
)

// bulkTemplate says the mail reads as a newsletter or job alert. A list footer
// does not count in mail that states the event the claim is about (stated).
func bulkTemplate(h string, stated bool) bool {
	if stated {
		h = listFooterRE.ReplaceAllString(h, "")
	}
	return newsletterRE.MatchString(h)
}

// interviewEvent picks the interview-family type for a phrase hit.
func interviewEvent(text string) (string, string) {
	switch {
	case reminderRE.MatchString(text) || reminderSubjRE.MatchString(text):
		return TypeReminder, "interview.reminder_phrase"
	case calendarSubjRE.MatchString(text):
		return TypeInterview, "interview.calendar_invite_subject"
	case replyNeededRE.MatchString(text):
		return TypeReplyNeeded, "interview.reply_needed_phrase"
	}
	return TypeInterview, "interview.scheduling_phrase"
}

// eventResult is the outcome of the family evaluation.
type eventResult struct {
	winner     *EventEvidence
	candidates []Candidate
	// blockedType is the first type a restricted sender was refused.
	blockedType, blockedRule string
}

// classifyEventWith evaluates the families in precedence order; artifact is the
// scheduling-artifact pattern of this taxonomy.
func (t *Taxonomy) classifyEventWith(in eventInput, p Policy, artifact *lazyregexp.Regexp) eventResult {
	subject, body := in.subject, in.body
	h := in.haystack()
	lower := strings.ToLower(h)
	hiring := hasHiringContext(in, p)
	hard := firstRule(hardRejection, h)
	incidentalOffer := hard != nil && !explicitOfferRE.MatchString(lower)
	receiptSubject := firstRule(receiptRules, subject)
	receiptBody := firstRule(receiptRules, body)
	receipt := receiptSubject
	if receipt == nil {
		receipt = receiptBody
	}
	lifecycle := t.lifecycleGuard(in, p, h, receipt, artifact)
	interviewFailure := t.interviewGateFailure(in, p, artifact, lifecycle)

	var res eventResult
	add := func(typ, id, src, blocked string) {
		res.candidates = append(res.candidates, Candidate{Type: typ, RuleID: id, Source: src, BlockedBy: blocked})
	}
	claim := func(typ, id, src string) {
		switch {
		case res.winner != nil:
			add(typ, id, src, blockLowerPrec)
		case !p.allows(typ) && typ != TypeReview && typ != TypeLowPriority:
			add(typ, id, src, blockSenderPolicy)
			if res.blockedType == "" {
				res.blockedType, res.blockedRule = typ, id
			}
		default:
			res.winner = &EventEvidence{Type: typ, RuleID: id}
			add(typ, id, src, "")
		}
	}
	claimOverride := func(typ, id, src string) {
		if res.winner != nil {
			add(typ, id, src, blockLowerPrec)
			return
		}
		res.winner = &EventEvidence{Type: typ, RuleID: id}
		add(typ, id, src, "")
	}
	veto := func(typ, id, src, guard string) { add(typ, id, src, guard) }
	claimInterview := func(typ, id, src string) {
		if interviewFailure == "" {
			claim(typ, id, src)
			return
		}
		veto(typ, id, src, interviewFailure)
		if interviewFailure == "interview_screening_action" || interviewFailure == "interview_negative_template" {
			return
		}
		if hiring || jobVocabularyRE.MatchString(h) {
			claim(TypeReview, "review.untrusted_interview_claim", src)
		} else {
			veto(TypeReview, "review.untrusted_interview_claim", src, "missing_hiring_context")
		}
	}

	claimLow := func(id, src string) {
		if lifecycle != "" {
			veto(TypeLowPriority, id, src, lifecycle)
			return
		}
		claim(TypeLowPriority, id, src)
	}

	// Offer.
	if offerRE.MatchString(lower) {
		failure := ""
		if incidentalOffer {
			failure = "hard_rejection_language"
		} else {
			failure = offerGateFailure(in, p, bulkTemplate(h, lifecycle == lifecycleOffer))
		}
		if failure != "" {
			veto(TypeOffer, "offer.offer_letter", "combined", failure)
			if newsletterRE.MatchString(h) {
				claimLow("low_priority.newsletter_or_job_alert_offer_wording", "combined")
			}
		} else {
			claim(TypeOffer, "offer.offer_letter", "combined")
		}
	}

	// Low-priority families.
	if surveyRE.MatchString(h) {
		claimLow("low_priority.candidate_feedback_survey", "combined")
	}
	if jobAlertLowRE.MatchString(h) {
		pureNews := pureNewsletterRE.MatchString(h) && !newsletterCtxRE.MatchString(h)
		activeInterview := interviewRE.MatchString(lower) || assessmentRE.MatchString(lower) ||
			interviewStrongRE.MatchString(body) || interviewWeakRE.MatchString(body) || assessmentActRE.MatchString(body)
		switch {
		case jobAlertSubjectRE.MatchString(subject):
			claim(TypeLowPriority, "low_priority.job_alert_subject", "subject")
		case pureNews:
			veto(TypeLowPriority, "low_priority.job_alert", "combined", "pure_newsletter")
		case activeInterview:
			veto(TypeLowPriority, "low_priority.job_alert", "combined", "active_interview_or_assessment")
		default:
			claimLow("low_priority.job_alert", "combined")
		}
	}
	if practiceToolRE.MatchString(h) {
		claim(TypeLowPriority, "low_priority.practice_interview_tool", "combined")
	}
	screenAction := p.Domain == "jobmail.io" && recruitingScreenSubj.MatchString(subject)
	if identityVerifyRE.MatchString(h) && !screenAction {
		claim(TypeLowPriority, "low_priority.identity_verification", "combined")
	}
	if p.Domain == "devpost.com" && hackathonRE.MatchString(h) {
		claim(TypeLowPriority, "low_priority.hackathon_platform", "combined")
	}
	if coldSourcingRE.MatchString(body) && interviewFailure != "" {
		claim(TypeLowPriority, "low_priority.cold_sourcing_template", "combined")
	}

	// Hard rejection; wording found only inside a condition ("if you are not
	// selected ...") is not a decision.
	if hard != nil {
		switch {
		case isLinkedInApplicationRejection(in, p, h):
			claimOverride(TypeRejection, "rejection.platform.application_rejected_update", "combined")
		case !hasUnconditionalHardRejection(h):
			veto(TypeRejection, hard.id, "combined", "conditional_rejection")
		case !trustedClaimSender(in, p):
			veto(TypeRejection, hard.id, "combined", "rejection_untrusted_sender")
			claim(TypeReview, "review.untrusted_rejection_claim", "combined")
		default:
			claim(TypeRejection, hard.id, "combined")
		}
	}

	// Soft rejection, guarded by the receipt in the body unless it states the
	// decision ("Thank you for your application. Unfortunately, we have decided ...").
	// softVerdictRE is softDecisionRE without "other candidates", which a receipt
	// also says ("we receive many applications from other candidates").
	soft := firstRule(softRejection, body)
	passive := passiveReviewRE.MatchString(body)
	if soft != nil {
		switch {
		case !hiringNounRE.MatchString(strings.ToLower(body)):
			veto(TypeRejection, soft.id, "body", "missing_hiring_noun")
		case receiptBody != nil && !softVerdictRE.MatchString(body):
			veto(TypeRejection, soft.id, "body", "receipt_in_body")
		default:
			claim(TypeRejection, soft.id, "body")
		}
	}

	// Assessment submitted.
	if assessmentDoneRE.MatchString(h) {
		if firstRule(receiptRules, h) != nil {
			veto(TypeAssessment, RuleAssessmentSubmitted, "combined", "application_receipt")
		} else {
			claim(TypeAssessment, RuleAssessmentSubmitted, "combined")
		}
	}

	// Interview and reply-needed claims by action verb.
	strong := interviewStrongRE.MatchString(body)
	weak := interviewWeakRE.MatchString(body)
	replyOverride := replyOverrideRE.MatchString(body)
	assessAct := assessmentActRE.MatchString(body)
	receiptBlocksStrong := receiptBody != nil && passive
	conditional := receiptBody != nil && conditionalSchedRE.MatchString(body)
	pureReceipt := pureReceiptSubj.MatchString(subject)
	blocksInterview := receiptBlocksStrong || conditional || pureReceipt
	guardName := func() string {
		if conditional {
			return "conditional_scheduling"
		}
		return "passive_review"
	}
	if replyOverride {
		if blocksInterview {
			veto(TypeReplyNeeded, "interview.reply_needed_overrides_receipt", "body", guardName())
		} else {
			claimInterview(TypeReplyNeeded, "interview.reply_needed_overrides_receipt", "body")
		}
	}
	switch {
	case (strong && !blocksInterview) || (weak && receiptBody == nil):
		if weak && !strong {
			claimInterview(TypeReplyNeeded, "interview.reply_needed_action_verb", "body")
		} else {
			claimInterview(TypeInterview, "interview.action_verb_overrides_receipt", "body")
		}
	case strong:
		g := "receipt_blocks_strong"
		if conditional {
			g = "conditional_scheduling"
		}
		veto(TypeInterview, "interview.action_verb_overrides_receipt", "body", g)
	case weak:
		veto(TypeReplyNeeded, "interview.reply_needed_action_verb", "body", "receipt_in_body")
	}

	// Assessment by action verb.
	if assessAct {
		switch {
		case !assessmentHiringContext(t, in, p, hiring):
			veto(TypeAssessment, "assessment.action_verb_overrides_receipt", "body", "missing_hiring_context")
		case receiptBlocksStrong:
			veto(TypeAssessment, "assessment.action_verb_overrides_receipt", "body", "receipt_blocks_strong")
		default:
			claim(TypeAssessment, "assessment.action_verb_overrides_receipt", "body")
		}
	}

	// Take-home as a next step beats the receipt family below.
	if takeHomeRE.MatchString(h) {
		if !hiring {
			veto(TypeAssessment, "assessment.next_step_take_home", "combined", "missing_hiring_context")
		} else {
			claim(TypeAssessment, "assessment.next_step_take_home", "combined")
		}
	}

	// Application receipt.
	if receipt != nil {
		src := "body"
		if receiptSubject != nil {
			src = "subject"
		}
		if !trustedClaimSender(in, p) {
			veto(TypeApplied, receipt.id, src, "applied_untrusted_sender")
			claim(TypeReview, "review.untrusted_applied_claim", src)
		} else {
			claim(TypeApplied, receipt.id, src)
		}
	}

	// Interview by phrase, reminder, or calendar-invitation subject.
	if interviewRE.MatchString(lower) || reminderSubjRE.MatchString(subject) || calendarSubjRE.MatchString(subject) {
		typ, id := interviewEvent(h)
		if blocksInterview {
			veto(typ, id, "combined", guardName())
		} else {
			claimInterview(typ, id, "combined")
		}
	}

	// Human-written scheduling.
	own := ownWords(body)
	humanReply := humanReplyRE.MatchString(own)
	humanScheduled := humanScheduledRE.MatchString(subject+"\n"+own) || namedInterview.MatchString(subject) ||
		(namedInterview.MatchString(own) && artifact.MatchString(own))
	if humanReply || humanScheduled {
		typ, id, src := TypeInterview, "interview.human_scheduling_phrase", "combined"
		if humanReply {
			typ, id, src = TypeReplyNeeded, "interview.human_reply_needed", "body"
		}
		if blocksInterview {
			veto(typ, id, "combined", guardName())
		} else {
			claimInterview(typ, id, src)
		}
	}

	// Assessment by phrase.
	if assessmentRE.MatchString(lower) {
		if assessmentHiringContext(t, in, p, hiring) {
			claim(TypeAssessment, "assessment.phrase", "combined")
		} else {
			veto(TypeAssessment, "assessment.phrase", "combined", "missing_hiring_context")
		}
	}

	// Action required.
	if actionRequiredRE.MatchString(lower) {
		switch {
		case !hiring:
			veto(TypeActionNeeded, "action_required.phrase", "combined", "missing_hiring_context")
		case pureNewsletterRE.MatchString(h) && !newsletterCtxRE.MatchString(h):
			veto(TypeActionNeeded, "action_required.phrase", "combined", "pure_newsletter")
		default:
			claim(TypeActionNeeded, "action_required.phrase", "combined")
		}
	}

	// Active application status.
	if strings.Contains(lower, "application status") && activeStatusRE.MatchString(lower) {
		switch {
		case passiveReviewRE.MatchString(lower):
			veto(TypeActionNeeded, "responded.application_status_active", "combined", "passive_review")
		case passiveStatusRE.MatchString(lower):
			veto(TypeActionNeeded, "responded.application_status_active", "combined", "passive_application_status")
		default:
			claim(TypeActionNeeded, "responded.application_status_active", "combined")
		}
	}

	// Active recruiter phrase.
	if respondedActive.MatchString(lower) {
		switch {
		case passiveReviewRE.MatchString(lower):
			veto(TypeActionNeeded, "responded.active_recruiter_phrase", "combined", "passive_review")
		case receipt != nil:
			veto(TypeActionNeeded, "responded.active_recruiter_phrase", "combined", "receipt_present")
		default:
			claimInterview(TypeReplyNeeded, "responded.active_recruiter_phrase", "combined")
		}
	}
	return res
}
