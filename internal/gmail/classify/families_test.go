package classify

import (
	"testing"
)

func atsMail(subject, text string) Input {
	return Input{From: from("Example Corp Recruiting", "no-reply", atsHost), Subject: subject, Text: text}
}

func blockedBy(r Result, ruleID string) string {
	for _, c := range r.Candidates {
		if c.RuleID == ruleID {
			return c.BlockedBy
		}
	}
	return "<absent>"
}

func TestGmail_Classify_FamilyPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		in       Input
		want     string
		loserID  string // a family that matched and lost
		loserWhy string
	}{
		{"offer over receipt",
			atsMail("Offer from Example Corp", "We are pleased to extend you an offer of employment. Thank you for applying. Your offer letter is attached."),
			TypeOffer, "applied.receipt.thank_you_for_applying", blockLowerPrec},
		{"rejection over interview phrase",
			atsMail("Update on your application", "We will not be moving forward with your application. We enjoyed the phone screen."),
			TypeRejection, "interview.scheduling_phrase", blockLowerPrec},
		{"assessment submitted over receipt wording",
			atsMail("Assessment received", "Thank you for completing the coding challenge for Example Corp. We received your assessment submission."),
			TypeAssessment, "", ""},
		{"interview action verb over assessment verb",
			atsMail("Next steps at Example Corp", "We would like to invite you to a 30-minute intro call. Please complete the assessment before then."),
			TypeInterview, "assessment.action_verb_overrides_receipt", blockLowerPrec},
		{"take-home as next step over receipt",
			atsMail("Thank you for applying to Example Corp", "Thank you for applying. Next step: a take-home assignment which you should complete within a week."),
			TypeAssessment, "applied.receipt.thank_you_for_applying", blockLowerPrec},
		{"receipt over interview phrase",
			atsMail("Thank you for applying", "We have received your application. If selected, a recruiter will discuss the phone screen."),
			TypeApplied, "", ""},
		{"action required over active status",
			atsMail("Action required for your application", "Please complete your profile. Check your application status below."),
			TypeActionNeeded, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := classify(t, c.in)
			s := mustSignal(t, r)
			if s.Type != c.want {
				t.Fatalf("type = %s (rule %s), want %s; candidates %+v", s.Type, s.Evidence.Event.RuleID, c.want, r.Candidates)
			}
			if c.loserID != "" {
				if got := blockedBy(r, c.loserID); got != c.loserWhy {
					t.Fatalf("loser %s blocked by %q, want %q; candidates %+v", c.loserID, got, c.loserWhy, r.Candidates)
				}
			}
		})
	}
}

func TestGmail_Classify_HardRejectionBeatsIncidentalOffer(t *testing.T) {
	r := classify(t, atsMail("Update on your application at Example Corp",
		"Unfortunately we are not moving forward with your application for the Backend Engineer role. If you are receiving this email prior to your start date, please contact us."))
	s := mustSignal(t, r)
	if s.Type != TypeRejection {
		t.Fatalf("type = %s", s.Type)
	}
	if got := blockedBy(r, "offer.offer_letter"); got != "hard_rejection_language" {
		t.Fatalf("offer blocked by %q", got)
	}
	// Explicit offer phrasing addressed to the candidate keeps the offer.
	r = classify(t, atsMail("Your offer from Example Corp",
		"We are not moving forward with the other role, but we are pleased to extend you an offer letter for the Backend Engineer position. Please accept the offer by Friday."))
	if s := mustSignal(t, r); s.Type != TypeOffer {
		t.Fatalf("explicit offer lost: %s", s.Type)
	}
}

func TestGmail_Classify_ReceiptThatPromisesSchedulingIsAReceipt(t *testing.T) {
	r := classify(t, atsMail("Thank you for applying to Example Corp",
		"We have received your application. If you are selected we will send scheduling information for a phone screen. Our team will review your application."))
	s := mustSignal(t, r)
	if s.Type != TypeApplied {
		t.Fatalf("type = %s via %s", s.Type, s.Evidence.Event.RuleID)
	}
	for _, c := range r.Candidates {
		if c.BlockedBy == "" && c.Type != TypeApplied {
			t.Fatalf("an interview was recorded: %+v", c)
		}
	}
}

func TestGmail_Classify_RejectionVetoesJobAlert(t *testing.T) {
	r := classify(t, atsMail("Your application to Example Corp",
		"Thank you for your interest. Unfortunately we have decided not to move forward with your application for the Backend Engineer position. Create a job alert to hear about new roles. Join our talent network. Unsubscribe."))
	if s := mustSignal(t, r); s.Type != TypeRejection {
		t.Fatalf("type = %s", s.Type)
	}
	if got := blockedBy(r, "low_priority.job_alert"); got != "candidate_rejection" {
		t.Fatalf("job alert blocked by %q", got)
	}
	// A digest that lists jobs, and conditional wording, do not veto the alert claim.
	digest := classify(t, Input{From: from("", "alerts", otherHost), Subject: "New jobs picked for you",
		Text: "Recommended jobs picked for you from top careers. View all jobs. Unsubscribe."})
	if s := mustSignal(t, digest); s.Type != TypeLowPriority {
		t.Fatalf("digest type = %s", s.Type)
	}
	cond := classify(t, atsMail("Your application", "Thank you for applying. If you are not selected for this position you will hear from us. Create a job alert. Unsubscribe."))
	if s := mustSignal(t, cond); s.Type == TypeRejection {
		t.Fatalf("conditional wording was read as a rejection")
	}
}

func TestGmail_Classify_ConditionalRejectionWordingIsNoRejection(t *testing.T) {
	r := classify(t, atsMail("Your application", "Thank you for applying. If you are not selected for this position you will hear from us."))
	if s := mustSignal(t, r); s.Type != TypeApplied {
		t.Fatalf("type = %s via %s; candidates %+v", s.Type, s.Evidence.Event.RuleID, r.Candidates)
	}
	if got := blockedBy(r, "rejection.hard.not_selected_for"); got != "conditional_rejection" {
		t.Fatalf("hard rejection blocked by %q", got)
	}
	// The same wording outside a condition is a rejection.
	if s := mustSignal(t, classify(t, atsMail("Your application", "Thank you for applying. You were not selected for this position."))); s.Type != TypeRejection {
		t.Fatalf("unconditional wording = %s", s.Type)
	}
}

func TestGmail_Classify_RejectionAndReceiptBeatSurveyAndFooterWording(t *testing.T) {
	cases := []struct {
		name, subject, text, want, loser, guard string
	}{
		{"rejection with a survey invitation", "Update on your application",
			"We will not be moving forward with your application for the Backend Engineer role. We value your feedback: please take our candidate experience survey.",
			TypeRejection, "low_priority.candidate_feedback_survey", "candidate_rejection"},
		{"receipt with an unsubscribe footer", "Thank you for applying to Example Corp",
			"We have received your application for the Backend Engineer role. Unsubscribe.",
			TypeApplied, "low_priority.job_alert", "application_receipt"},
		{"receipt with a survey invitation", "Thank you for applying to Example Corp",
			"We have received your application. We value your feedback on the application form.",
			TypeApplied, "low_priority.candidate_feedback_survey", "application_receipt"},
		{"rejection with an incidental offer word and a footer", "Update on your application at Example Corp",
			"Unfortunately we are not moving forward with your application for the Backend Engineer role. If you are receiving this email prior to your start date, please contact us. Unsubscribe.",
			TypeRejection, "low_priority.newsletter_or_job_alert_offer_wording", "candidate_rejection"},
		{"receipt with an incidental offer word and a footer", "Thank you for applying to Example Corp",
			"We have received your application. We will ask about your earliest start date later. Unsubscribe.",
			TypeApplied, "low_priority.newsletter_or_job_alert_offer_wording", "application_receipt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := classify(t, atsMail(c.subject, c.text))
			s := mustSignal(t, r)
			if s.Type != c.want {
				t.Fatalf("type = %s (rule %s), want %s; candidates %+v", s.Type, s.Evidence.Event.RuleID, c.want, r.Candidates)
			}
			if got := blockedBy(r, c.loser); got != c.guard {
				t.Fatalf("%s blocked by %q, want %q", c.loser, got, c.guard)
			}
		})
	}
	// A digest that thanks the reader for applying, and a receipt whose only
	// rejection wording is conditional, stay low priority.
	for name, in := range map[string]Input{
		"digest":      atsMail("New jobs picked for you", "Thank you for applying. Recommended jobs picked for you from top careers. View all jobs. Unsubscribe."),
		"plural jobs": atsMail("New jobs picked for you", "Thank you for applying. Recommended jobs picked for you. View all jobs. Unsubscribe."),
		"conditional": atsMail("Your application", "Thank you for applying. If you are not selected for this position you will hear from us. Create a job alert. Unsubscribe."),
		"survey":      atsMail("How was your experience?", "Please take our candidate experience survey. We value your feedback."),
	} {
		if s := mustSignal(t, classify(t, in)); s.Type != TypeLowPriority {
			t.Errorf("%s = %s via %s", name, s.Type, s.Evidence.Event.RuleID)
		}
	}
}

func TestGmail_Classify_RejectionPhrases(t *testing.T) {
	hard := []string{
		"We are not moving forward with your candidacy.", "The position has been filled.",
		"We made the tough choice not to move forward with your candidacy.",
	}
	for _, p := range hard {
		s := mustSignal(t, classify(t, atsMail("Application update", "Hello Alex. "+p+" Best wishes.")))
		if s.Type != TypeRejection {
			t.Errorf("%q -> %s", p, s.Type)
		}
	}
	// A soft rejection needs a hiring noun in the body and no receipt in the body.
	soft := classify(t, atsMail("Application update", "Unfortunately we think another candidate is a more aligned with this role."))
	if s := soft.Signal; s == nil || s.Type != TypeRejection {
		t.Fatalf("soft rejection = %+v", soft)
	}
	noNoun := classify(t, atsMail("Application update", "Unfortunately our schedule is full."))
	if s := noNoun.Signal; s != nil && s.Type == TypeRejection {
		t.Fatal("soft rejection without a hiring noun")
	}
	withReceipt := classify(t, atsMail("Application update", "We have received your application. Unfortunately we receive many applications from other candidates."))
	if s := withReceipt.Signal; s == nil || s.Type != TypeApplied {
		t.Fatalf("receipt in the body must block a soft rejection: %+v", withReceipt.Signal)
	}
	// A soft rejection that states the decision after the usual thanks is a rejection.
	for _, body := range []string{
		"Thank you for your application for the Backend Engineer role. Unfortunately, we have decided not to take your application further.",
		"Thanks for applying to Example Corp. Unfortunately, the role is no longer available.",
		"Thanks for applying to Example Corp. Unfortunately, the role is no longer available. Unsubscribe.",
	} {
		r := classify(t, atsMail("Application update", body))
		if s := r.Signal; s == nil || s.Type != TypeRejection {
			t.Errorf("%q = %+v; candidates %+v", body, s, r.Candidates)
		}
	}
}

func TestGmail_Classify_ReceiptBlocksWeakInterviewClaims(t *testing.T) {
	process := atsMail("Thank you for applying", "We have received your application. Our process includes a phone screen and a panel interview. We will review your application and get back to you. Find a time that works on our portal calendar.")
	if s := mustSignal(t, classify(t, process)); s.Type != TypeApplied {
		t.Fatalf("receipt describing its process = %s", s.Type)
	}
	invite := atsMail("Thank you for applying", "We have received your application. We would like to invite you to a 15-minute intro call. Please self-schedule here: https://calendly.com/sam/15min")
	if s := mustSignal(t, classify(t, invite)); s.Type != TypeInterview {
		t.Fatalf("strong invitation = %s", s.Type)
	}
	passive := atsMail("Application received", "We have received your application and our team will review it. Self-schedule an interview at https://calendly.com/portal/info once selected.")
	if s := mustSignal(t, classify(t, passive)); s.Type == TypeInterview {
		t.Fatalf("a strong verb beside a passive review line must not override the receipt")
	}
}

func TestGmail_Classify_NamedInterviewNeedsArtifact(t *testing.T) {
	desc := atsMail("We got your application", "Our typical interview process involves phone interviews, a technical interview and a panel interview with the team. We have received your application.")
	if s := mustSignal(t, classify(t, desc)); s.Type != TypeApplied {
		t.Fatalf("process description = %s", s.Type)
	}
	subj := Input{From: from("Sam Example Recruiting", "sam", corpHost), Subject: "Final round interview with Example Corp", Text: "Looking forward to it. Example Corp engineering."}
	if s := mustSignal(t, classify(t, subj)); s.Type != TypeInterview {
		t.Fatalf("subject-named interview = %s", s.Type)
	}
	dated := Input{From: from("Sam Example Recruiting", "sam", corpHost), Subject: "Next week", Text: "The panel interview is at 10:30 am on Tuesday. I will be interviewing you with the team."}
	if s := mustSignal(t, classify(t, dated)); s.Type != TypeInterview {
		t.Fatalf("interview with a stated time = %s", s.Type)
	}
}

func TestGmail_Classify_HumanSchedulingOwnWordsOnly(t *testing.T) {
	scheduled := Input{From: from("Sam Example Recruiting", "sam", corpHost), Subject: "Re: Intro chat",
		Text: "I've sent a Google Calendar invite for Tuesday. See you then. On Mon, Mar 8, 2027 at 9:48 AM Alex Example <" + addr("alex", "gmail.com") + "> wrote: could you share a few time slots?"}
	s := mustSignal(t, classify(t, scheduled))
	if s.Type != TypeInterview {
		t.Fatalf("quoted text turned a scheduled interview into %s", s.Type)
	}
	reply := Input{From: from("Sam Example Recruiting", "sam", corpHost), Subject: "Intro chat", Text: "Hi Alex, I'd love to hop on a quick call. Could you share your availability this week? I've sent you an invite placeholder."}
	if s := mustSignal(t, classify(t, reply)); s.Type != TypeReplyNeeded {
		t.Fatalf("reply-needed must win over scheduled, got %s", s.Type)
	}
	if got := ownWords("above On Sat, Sep 5, 2026 at 9:48 AM Someone <x> wrote: below"); got != "above " {
		t.Fatalf("ownWords = %q", got)
	}
}

func TestGmail_Classify_InterviewGate(t *testing.T) {
	stranger := Input{From: from("", "pat", "gmail.com"), Subject: "Interview", Text: "Would like to speak about the position. Please reply with your availability for an interview."}
	s := mustSignal(t, classify(t, stranger))
	if s.Type != TypeReview || s.Evidence.Event.RuleID != "review.untrusted_interview_claim" {
		t.Fatalf("stranger = %s/%s", s.Type, s.Evidence.Event.RuleID)
	}
	withLink := stranger
	withLink.Text += " Book here: https://calendly.com/pat/30min"
	if s := mustSignal(t, classify(t, withLink)); s.Type == TypeReview {
		t.Fatalf("a scheduling artifact must corroborate the claim: %s", s.Type)
	}
	newsletter := Input{From: from("", "news", otherHost), Subject: "Interview tips", Text: "Career tips and a newsletter about how to book time for an interview. Unsubscribe."}
	if s := classify(t, newsletter).Signal; s != nil && s.Type == TypeReview {
		t.Fatalf("newsletter template reached review")
	}
}

func TestGmail_Classify_UntrustedInterviewClaimGoesToReview(t *testing.T) {
	r := classify(t, Input{
		From: from("", "pat", "gmail.com"), Subject: "Quick question about the role",
		Text: "Hi Alex, I saw your application for the Backend Engineer role at Example Corp. When are you available to chat about the interview process?",
	})
	rule := ""
	for _, c := range r.Candidates {
		if c.BlockedBy == "interview_untrusted_no_artifact" {
			rule = c.BlockedBy
		}
	}
	if rule == "" {
		t.Fatalf("candidates = %+v", r.Candidates)
	}
	s := mustSignal(t, r)
	if s.Type != TypeReview || s.Tier != TierReview {
		t.Fatalf("signal = %s/%s", s.Type, s.Tier)
	}
}

func TestGmail_Classify_NoJobVocabularyIgnored(t *testing.T) {
	r := classify(t, Input{From: from("Leasing Office", "office", otherHost), Subject: "Your apartment", Text: "Please share your availability this week to view the unit. Let me know what works."})
	if r.Signal != nil {
		t.Fatalf("a leasing office became %s", r.Signal.Type)
	}
	if got := blockedBy(r, "review.untrusted_interview_claim"); got != "missing_hiring_context" && got != "<absent>" {
		t.Fatalf("review blocked by %q", got)
	}
}

func TestGmail_Classify_UntrustedClaimsGoToReview(t *testing.T) {
	for name, in := range map[string]Input{
		"rejection": {From: from("", "pat", "gmail.com"), Subject: "Hello", Text: "We are not moving forward with you."},
		"receipt":   {From: from("", "pat", "gmail.com"), Subject: "Hello", Text: "Thanks for considering us."},
	} {
		s := mustSignal(t, classify(t, in))
		if s.Type != TypeReview {
			t.Errorf("%s from a stranger = %s", name, s.Type)
		}
	}
	// Hiring context in the message makes the same claim trusted.
	ctx := Input{From: from("", "pat", otherHost), Subject: "Your application", Text: "We have decided not to proceed with your application for the Backend Engineer role."}
	if s := mustSignal(t, classify(t, ctx)); s.Type != TypeRejection {
		t.Fatalf("with context = %s", s.Type)
	}
}

func TestGmail_Classify_AssessmentNeedsHiringContext(t *testing.T) {
	vendor := Input{From: from("HackerRank", "no-reply", "hackerrank.com"), Subject: "Assessment invitation", Text: "Please complete the coding test within the next 7 days."}
	if s := classify(t, vendor).Signal; s != nil && s.Type == TypeAssessment {
		t.Fatalf("a vendor mail naming no employer = %s", s.Type)
	}
	named := vendor
	named.Subject = "Assessment invitation from Example Corp"
	if s := mustSignal(t, classify(t, named)); s.Type != TypeAssessment || s.Company != "Example Corp" {
		t.Fatalf("named employer: %s company=%q", s.Type, s.Company)
	}
	plain := Input{From: from("", "sam", otherHost), Subject: "Take this", Text: "Please complete the skills assessment."}
	if s := classify(t, plain).Signal; s != nil && s.Type == TypeAssessment {
		t.Fatalf("no hiring context = %s", s.Type)
	}
	newsletter := Input{From: from("", "news", otherHost), Subject: "Weekly digest", Text: "Your weekly digest. Action required: read the full post. Unsubscribe."}
	if s := classify(t, newsletter).Signal; s != nil && s.Type == TypeActionNeeded {
		t.Fatal("a pure newsletter blocks action-required")
	}
}

func TestGmail_Classify_ColdSourcingLowPriority(t *testing.T) {
	r := classify(t, Input{From: from("", "pat", "gmail.com"), Subject: "Opportunity",
		Text: "Hi, I came across your profile. Our client is in need of a senior engineer. Explore your interest and availability for an interview."})
	s := mustSignal(t, r)
	if s.Type != TypeLowPriority || s.Evidence.Event.RuleID != "low_priority.cold_sourcing_template" {
		t.Fatalf("signal = %s via %s", s.Type, s.Evidence.Event.RuleID)
	}
	// A trusted recruiter with a scheduling link is not demoted.
	real := Input{From: from("Dana Recruiter", "dana", corpHost), Subject: "Interview with Example Corp", Text: "I came across your profile and would like to interview you. Book: https://calendly.com/dana/30"}
	if s := mustSignal(t, classify(t, real)); s.Type == TypeLowPriority {
		t.Fatal("a real recruiter with a booking link was demoted")
	}
}

func TestGmail_Classify_LowPriorityFamilies(t *testing.T) {
	for name, in := range map[string]Input{
		"job alert subject": {From: from("", "alerts", otherHost), Subject: "Job alert: new roles", Text: "New roles for you."},
		"survey":            atsMail("How was your experience?", "Please take our candidate experience survey. We value your feedback."),
		"practice tool":     {From: from("", "hello", otherHost), Subject: "Practice", Text: "Try our AI interview buddy for practice interview answers."},
		"identity":          {From: from("", "no-reply", otherHost), Subject: "Verify", Text: "Your verification code is 123456. Enter this code to continue."},
		"hackathon":         {From: from("", "hello", "devpost.com"), Subject: "Hackathon update", Text: "A hackathon submission to Build Week started."},
	} {
		s := mustSignal(t, classify(t, in))
		if s.Type != TypeLowPriority || s.Tier != TierLow {
			t.Errorf("%s = %s/%s via %s", name, s.Type, s.Tier, s.Evidence.Event.RuleID)
		}
	}
	// Identity mail with a job-board screening subject is not low priority.
	screen := Input{From: from("", "x", "jobmail.io"), Subject: "Action needed: next steps for Backend Engineer role", Text: "Please verify your identity to continue. Book a time: https://calendly.com/x/y"}
	if s := classify(t, screen).Signal; s != nil && s.Evidence.Event.RuleID == "low_priority.identity_verification" {
		t.Fatal("job-board screening subject was treated as identity verification")
	}
}

func TestGmail_Classify_PlatformRejectionAllowed(t *testing.T) {
	in := Input{
		From: "LinkedIn Jobs <" + addr("jobs-noreply", "linkedin.com") + ">", Subject: "Your update from Example Corp",
		Text: "Your update from Example Corp. Unfortunately, we will not be moving forward with your application. Applied on March 3.",
	}
	s := mustSignal(t, classify(t, in))
	if s.Type != TypeRejection || s.Tier == TierSuppressed {
		t.Fatalf("platform rejection = %s/%s", s.Type, s.Tier)
	}
	// Any other claim from the restricted sender is suppressed.
	other := Input{From: "LinkedIn <" + addr("jobs-noreply", "linkedin.com") + ">", Subject: "Interview request",
		Text: "I would like to invite you to an interview. Book a time: https://calendly.com/a/b"}
	s = mustSignal(t, classify(t, other))
	if s.Tier != TierSuppressed {
		t.Fatalf("restricted sender interview claim = %s/%s", s.Type, s.Tier)
	}
	if !contains(s.ReviewReasons, "sender_restricted") {
		t.Fatalf("reasons = %v", s.ReviewReasons)
	}
}

func TestGmail_Classify_TierAndReviewReasons(t *testing.T) {
	// Low confidence: untrusted, no employer, no role.
	s := mustSignal(t, classify(t, Input{From: from("", "pat", "gmail.com"), Subject: "Hello", Text: "I would like to schedule a call about the opening. Please reply with your availability."}))
	if s.Tier != TierReview {
		t.Fatalf("tier = %s", s.Tier)
	}
	if !contains(s.ReviewReasons, ReviewLowConfidence) || !contains(s.ReviewReasons, ReviewCompanyUnknown) {
		t.Fatalf("reasons = %v", s.ReviewReasons)
	}
	// Two strong sources that disagree.
	conflict := Input{From: from("Alpha Industries", "no-reply", atsHost), Subject: "Thank you for applying to Beta Robotics", Text: "We have received your application for the Backend Engineer position at Beta Robotics."}
	c := mustSignal(t, classify(t, conflict))
	if c.Tier != TierReview || !contains(c.ReviewReasons, ReviewCompanyConflict) {
		t.Fatalf("conflict signal = %s %v (%+v)", c.Tier, c.ReviewReasons, c.Evidence.Company)
	}
	// Low-priority events are never review.
	lp := mustSignal(t, classify(t, Input{From: from("", "a", otherHost), Subject: "Job alert", Text: "Jobs."}))
	if lp.Tier != TierLow || len(lp.ReviewReasons) != 0 {
		t.Fatalf("low priority = %s %v", lp.Tier, lp.ReviewReasons)
	}
}

func TestGmail_Confidence_Weights(t *testing.T) {
	base := confidenceFeatures{companyStrategy: StratBody, eventType: TypeInterview}
	for _, c := range []struct {
		name string
		f    confidenceFeatures
		want float64
	}{
		{"baseline", base, 0.20},
		{"trusted", with(base, func(f *confidenceFeatures) { f.trustedSender = true }), 0.50},
		{"hard phrase", with(base, func(f *confidenceFeatures) { f.hardEventPhrase = true }), 0.35},
		{"sender domain only", with(base, func(f *confidenceFeatures) { f.companyStrategy = StratSenderDomain }), 0.05},
		{"unknown on receipt", with(base, func(f *confidenceFeatures) { f.companyStrategy, f.eventType = StratUnknown, TypeApplied }), 0.00},
		{"everything", confidenceFeatures{trustedSender: true, explicitCompany: true, explicitRole: true, hardEventPhrase: true, subjectMatch: true, companyStrategy: StratSubject, eventType: TypeApplied}, 1.00},
	} {
		if got := c.f.score(); got != c.want {
			t.Errorf("%s = %.2f, want %.2f", c.name, got, c.want)
		}
	}
}

func with(f confidenceFeatures, mut func(*confidenceFeatures)) confidenceFeatures {
	mut(&f)
	return f
}

func TestGmail_Confidence_BodyCorroborationNeverMakesAHumanTrusted(t *testing.T) {
	body := "Let's schedule an interview for Tuesday at 3:00 pm. Join https://meet.google.com/abc-defg-hij"
	// The employer is named by the message and is the sender's own domain.
	anchored := Input{From: from("Sam Example", "sam", corpHost), Subject: "Interview with Example Corp", Text: body}
	if s := mustSignal(t, classify(t, anchored)); s.Tier != TierActionable || s.Evidence.Confidence.SenderTrust < 0.9 {
		t.Fatalf("employer-domain match = %s trust %.2f conf %.2f", s.Tier, s.Evidence.Confidence.SenderTrust, s.Confidence)
	}
	// The same words from a domain nothing vouches for are only corroboration.
	for name, in := range map[string]Input{
		"no employer match":      {From: from("Sam Example", "sam", corpHost), Subject: "Interview", Text: body},
		"another domain":         {From: from("Sam Example", "sam", "evil.test"), Subject: "Interview with Example Corp", Text: body},
		"cold sourcing template": {From: from("Sam Example", "sam", corpHost), Subject: "Interview", Text: "I came across your profile. " + body},
	} {
		s := mustSignal(t, classify(t, in))
		if s.Tier == TierActionable || s.Evidence.Confidence.SenderTrust >= 0.9 {
			t.Errorf("%s: tier %s trust %.2f", name, s.Tier, s.Evidence.Confidence.SenderTrust)
		}
	}
}

func TestGmail_Identifiers_HostAnchored(t *testing.T) {
	cases := []struct {
		name, subject, body, raw string
		want                     *Identifiers
	}{
		{"greenhouse board", "", "", `<a href="https://boards.greenhouse.io/exampleco/jobs/4567890?gh_jid=4567890">View</a>`, &Identifiers{JobID: "4567890", ATS: "greenhouse", Tenant: "exampleco"}},
		{"gh_jid only", "", "", "https://example.com/careers?gh_jid=1234567", &Identifiers{JobID: "1234567", ATS: "greenhouse"}},
		{"lever", "", "", "https://jobs.lever.co/exampleco/123e4567-e89b-12d3-a456-426614174000", &Identifiers{JobID: "123e4567-e89b-12d3-a456-426614174000", ATS: "lever", Tenant: "exampleco"}},
		{"ashby encoded tenant", "", "", "https://jobs.ashbyhq.com/example%20co/123e4567-e89b-12d3-a456-426614174000", &Identifiers{JobID: "123e4567-e89b-12d3-a456-426614174000", ATS: "ashby", Tenant: "example co"}},
		{"workday request id", "", "", "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/X?jobReqId=JR-10234", &Identifiers{ReqID: "JR-10234", ATS: "workday"}},
		{"bare requisition needs a workday url", "", "Your reference R-1463799 was recorded", "", nil},
		{"bare requisition beside a workday url", "", "Reference R-1463799 at https://example.wd5.myworkdayjobs.com/Careers", "", &Identifiers{ReqID: "R-1463799", ATS: "workday"}},
		{"icims strips prefix", "", "", "https://careers-example.icims.com/jobs/12345/engineer/job", &Identifiers{JobID: "12345", ATS: "icims", Tenant: "example"}},
		{"smartrecruiters", "", "", "https://jobs.smartrecruiters.com/ExampleCo/743999876543", &Identifiers{JobID: "743999876543", ATS: "smartrecruiters", Tenant: "exampleco"}},
		{"jobvite", "", "", "https://jobs.jobvite.com/exampleco/job/oAbCd12", &Identifiers{JobID: "oAbCd12", ATS: "jobvite", Tenant: "exampleco"}},
		{"successfactors", "", "", `https://career5.successfactors.com/career?career_job_req_id=98765&amp;x=1`, &Identifiers{ReqID: "98765", ATS: "successfactors"}},
		{"labeled", "", "Job ID: A1234 and Application ID: 55-77", "", &Identifiers{JobID: "A1234", ApplicationID: "55-77"}},
		{"label needs a digit", "", "Requisition ID: needed soon", "", nil},
		{"unknown host ignored", "", "", "https://evil.example.org/jobs/1234567", nil},
		{"url wins over label", "", "Job ID: 99999", "https://boards.greenhouse.io/exampleco/jobs/4567890", &Identifiers{JobID: "4567890", ATS: "greenhouse", Tenant: "exampleco"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractIdentifiers(c.subject, c.body, c.raw)
			if c.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if got == nil || got.JobID != c.want.JobID || got.ReqID != c.want.ReqID || got.ApplicationID != c.want.ApplicationID || got.ATS != c.want.ATS || got.Tenant != c.want.Tenant {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
	s := mustSignal(t, classify(t, Input{From: from("Example Corp", "no-reply", atsHost), Subject: "Thank you for applying", Text: "We have received your application.",
		Raw: `<a href="https://boards.greenhouse.io/exampleco/jobs/4567890">x</a>`}))
	if s.Evidence.Identifiers == nil || s.Evidence.Identifiers.JobID != "4567890" {
		t.Fatalf("the raw pass must keep hrefs: %+v", s.Evidence.Identifiers)
	}
}

// An ATS mail about the applicant's own application keeps its lifecycle type
// when it ends in a list footer; bulk mail with the same footer stays low.
func TestGmail_Classify_LifecycleMailKeepsItsTypeUnderAListFooter(t *testing.T) {
	const footer = "\n\nSent by Example Corp Talent, 1 Main St. Unsubscribe | Manage preferences"
	cases := []struct {
		name, subject, text, want string
	}{
		{"interview invite with a scheduling link", "Interview invitation: Backend Engineer at Example Corp",
			"Hi Alex,\n\nThanks for your interest in the Backend Engineer role at Example Corp. We are excited to invite you to interview with our team. Please schedule your interview here: https://calendly.com/example-corp/45min" + footer,
			TypeInterview},
		{"interview invite that names the application", "Next steps for your application to Example Corp",
			"Hi Alex,\n\nThank you for your application for the Backend Engineer role. Please schedule your interview here: https://calendly.com/example-corp/45min" + footer,
			TypeInterview},
		{"offer letter", "Your offer from Example Corp",
			"Hi Alex,\n\nCongratulations! We are delighted to extend you an offer for the Backend Engineer position. Your offer letter is attached; please accept the offer by Friday, March 19." + footer,
			TypeOffer},
		{"action required", "Action required for your application",
			"Hi Alex,\n\nAction required: please complete the work authorization form for your application to the Backend Engineer role by March 19." + footer,
			TypeActionNeeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := classify(t, atsMail(c.subject, c.text))
			s := mustSignal(t, r)
			if s.Type != c.want {
				t.Fatalf("type = %s (rule %s), want %s; candidates %+v", s.Type, s.Evidence.Event.RuleID, c.want, r.Candidates)
			}
		})
	}
	for name, in := range map[string]Input{
		"job-alert digest": atsMail("New jobs picked for you", "Recommended jobs picked for you: Backend Engineer at Example Corp. Thank you for applying through our site. View all jobs."+footer),
		"newsletter with interview tips": {From: from("Career Weekly", "news", otherHost), Subject: "Interview tips and offers",
			Text: "This week's newsletter: five interview tips, how to schedule an interview, and how to accept the offer that is right for you. Action required: read the full post." + footer},
		"recruiter mass mail": {From: from("Dana Recruiter", "dana", otherHost), Subject: "New roles this week",
			Text: "Hi there, I came across your profile. Our client is in need of engineers. Schedule a call: https://calendly.com/dana/30." + footer},
	} {
		r := classify(t, in)
		if s := r.Signal; s == nil || s.Type != TypeLowPriority {
			t.Errorf("%s = %+v; candidates %+v", name, s, r.Candidates)
		}
	}
}
