package classify

import (
	"strings"
	"testing"
	"time"
)

const (
	atsHost   = "greenhouse-mail.io"
	corpHost  = "example.com"
	otherHost = "example.org"
)

func TestGmail_Classify_ApplicationReceiptFromTrustedATS(t *testing.T) {
	r := classify(t, Input{
		From:    from("Example Corp", "no-reply", atsHost),
		Subject: "Thank you for applying to Example Corp",
		Text:    "Hi Alex, we have received your application for the Backend Engineer position.",
	})
	s := mustSignal(t, r)
	if s.Type != TypeApplied || s.Tier != TierActionable || s.Confidence < 0.85 {
		t.Fatalf("signal = %s/%s/%.2f", s.Type, s.Tier, s.Confidence)
	}
	if s.Company != "Example Corp" || s.Role != "Backend Engineer" {
		t.Fatalf("company=%q role=%q", s.Company, s.Role)
	}
	found := false
	for _, sp := range s.Evidence.Spans {
		if sp.Source == "subject" && strings.Contains(strings.ToLower(sp.Snippet), "thank you for applying") && len(sp.Snippet) <= 120 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no subject span in the evidence trail: %+v", s.Evidence.Spans)
	}
}

func TestGmail_Classify_TypeSetIsTheSpecs(t *testing.T) {
	want := map[string]bool{
		"applied_confirmation": true, "recruiter_reply_needed": true, "online_assessment": true, "interview_scheduled": true,
		"interview_reminder": true, "offer": true, "rejection": true, "action_required": true, "review": true, "low_priority": true,
	}
	types := []string{
		TypeApplied, TypeReplyNeeded, TypeAssessment, TypeInterview, TypeReminder,
		TypeOffer, TypeRejection, TypeActionNeeded, TypeReview, TypeLowPriority,
	}
	seen := map[string]bool{}
	for _, ty := range types {
		if !want[ty] || seen[ty] {
			t.Errorf("type constant %q is not a distinct member of the spec set", ty)
		}
		seen[ty] = true
	}
	// A completed assessment and a take-home next step each give a signal whose
	// type is in the set.
	for _, in := range []Input{
		{From: from("", "x", corpHost), Subject: "Assessment completed: Example Corp Coding Assessment", Text: "Thank you for completing the coding challenge for Example Corp."},
		{From: from("Example Recruiter", "jobs", corpHost), Subject: "Take-home", Text: "Next steps for your application: a take-home assignment."},
	} {
		if s := mustSignal(t, classify(t, in)); !want[s.Type] {
			t.Errorf("type %s is outside the set", s.Type)
		}
	}
}

func TestGmail_Classify_UnknownEmployerGoesToReview(t *testing.T) {
	r := classify(t, Input{
		From:    from("", "pat", "gmail.com"),
		Subject: "Interview availability",
		Text:    "Hi Alex, I will be interviewing you next week. The interview will focus on systems design. Could you share your availability? https://calendly.com/pat/45min",
	})
	s := mustSignal(t, r)
	if s.Type != TypeReplyNeeded && s.Type != TypeInterview {
		t.Fatalf("type = %s", s.Type)
	}
	if s.Tier != TierReview || !contains(s.ReviewReasons, ReviewCompanyUnknown) {
		t.Fatalf("tier=%s reasons=%v", s.Tier, s.ReviewReasons)
	}
	if s.Company != UnknownCompany {
		t.Fatalf("company = %q", s.Company)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestGmail_Classify_JobBoardBrandAsEmployerGoesToReview(t *testing.T) {
	r := classify(t, Input{
		From:    from("Jobgether", "no-reply", "jobgether.com"),
		Subject: "Your application was sent to Jobgether",
		Text:    "Your application was sent to Jobgether. Good luck.",
	})
	s := mustSignal(t, r)
	if s.Tier != TierReview || !contains(s.ReviewReasons, ReviewCompanyBoard) {
		t.Fatalf("tier=%s reasons=%v company=%q", s.Tier, s.ReviewReasons, s.Company)
	}
}

func TestGmail_Direction_AddressThenSentLabel(t *testing.T) {
	base := Input{From: from("Alex Example", "alex", "gmail.com"), To: from("Sam Recruiter", "sam", corpHost),
		Subject: "Re: Interview availability", Text: "Hi Sam, thanks. I confirm I am available Tuesday at 10am for the interview with Example Corp."}
	cases := map[string]Input{
		"mailbox address": withMailbox(base, addr("alex", "gmail.com"), nil),
		"SENT label":      withMailbox(base, "", []string{"SENT", "INBOX"}),
	}
	for name, in := range cases {
		s := mustSignal(t, classify(t, in))
		if s.Direction != DirOutgoing {
			t.Errorf("%s: direction = %s", name, s.Direction)
		}
	}
	in := withMailbox(base, addr("someone", "example.net"), nil)
	if s := classify(t, in).Signal; s != nil && s.Direction != DirIncoming {
		t.Errorf("a stranger's mailbox made the message %s", s.Direction)
	}
}

func withMailbox(in Input, mailbox string, labels []string) Input {
	in.Mailbox, in.Labels = mailbox, labels
	return in
}

func TestGmail_Classify_OutgoingConfirmationIsOutgoing(t *testing.T) {
	s := mustSignal(t, classify(t, Input{
		From: from("Alex Example", "alex", "gmail.com"), To: from("Sam Recruiter", "sam", corpHost),
		Subject: "Re: Chat", Text: "Hi Sam, my availability works for me on Thursday.",
		Mailbox: addr("alex", "gmail.com"),
	}))
	if s.Type != TypeReplyNeeded || s.Direction != DirOutgoing || s.Evidence.Event.RuleID != "outbound.reply_confirmation" {
		t.Fatalf("signal = %s/%s/%s", s.Type, s.Direction, s.Evidence.Event.RuleID)
	}
	// The recipient's employer is used for outgoing mail.
	if s.Company != "Example" {
		t.Fatalf("company = %q", s.Company)
	}
}

func TestGmail_Deadline_Shapes(t *testing.T) {
	ref := time.Date(2027, 3, 10, 15, 0, 0, 0, time.UTC) // a Wednesday
	for _, c := range []struct {
		text string
		want time.Time
	}{
		{"please finish by Friday", time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)},
		{"complete within 3 days", time.Date(2027, 3, 13, 15, 0, 0, 0, time.UTC)},
		{"complete by April 20", time.Date(2027, 4, 20, 0, 0, 0, 0, time.UTC)},
		{"complete by January 5", time.Date(2028, 1, 5, 0, 0, 0, 0, time.UTC)},   // more than 30 days past rolls to next year
		{"respond by Friday 5 PM", time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)}, // a weekday followed by a number is not a month and a day
		{"complete by tomorrow 9 AM", time.Date(2027, 3, 11, 0, 0, 0, 0, time.UTC)},
		{"nothing stated", time.Time{}},
	} {
		if got := parseDeadline(c.text, ref); !got.Equal(c.want) {
			t.Errorf("%q = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestGmail_Classify_DeadlineExtraction(t *testing.T) {
	s := mustSignal(t, classify(t, Input{
		From:    from("Example Corp Recruiting", "no-reply", "hackerrank.com"),
		Subject: "Example Corp online assessment invitation",
		Text:    "You are invited to take the online assessment for the Backend Engineer role at Example Corp. Please complete the assessment by Friday.",
	}))
	if s.Type != TypeAssessment {
		t.Fatalf("type = %s (%v)", s.Type, s.Evidence.Event)
	}
	if want := time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC); !s.DueAt.Equal(want) {
		t.Fatalf("due = %v, want %v", s.DueAt, want)
	}
}

func TestGmail_Classify_NoPayloadGoesToReview(t *testing.T) {
	// Metadata only: the subject alone cannot be corroborated.
	r := classify(t, Input{From: from("", "pat", otherHost), Subject: "Interview availability", Text: ""})
	if s := r.Signal; s != nil && s.Tier == TierActionable {
		t.Fatalf("a metadata-only interview claim became actionable: %+v", s)
	}
}

func TestGmail_Classify_IgnoredCarriesReason(t *testing.T) {
	r := classify(t, Input{From: from("Newsletter", "news", otherHost), Subject: "Weekly gardening tips", Text: "Plant tomatoes in spring."})
	if r.Signal != nil || r.Reason != ReasonNoMatch {
		t.Fatalf("result = %+v", r)
	}
	if r := newClassifier().Classify(Input{}); r.Reason != ReasonInvalidMessage {
		t.Fatalf("empty message reason = %q", r.Reason)
	}
	// Every candidate blocked: a leasing office asks for availability and
	// nothing in the message uses job vocabulary.
	r = classify(t, Input{
		From: from("Leasing Office", "leasing", otherHost), Subject: "Viewing availability",
		Text: "Could you share your availability for a viewing of the flat on Saturday? The leasing office is open from 9 to 5.",
	})
	if r.Signal != nil || r.Reason != ReasonAllBlocked || len(r.Candidates) == 0 {
		t.Fatalf("non-job mail = %+v", r)
	}
	for _, c := range r.Candidates {
		if c.BlockedBy == "" {
			t.Errorf("candidate %s carries no blocking guard", c.RuleID)
		}
	}
}

func TestGmail_Signal_OnePerMessage(t *testing.T) {
	in := Input{MessageID: "abc123", ThreadID: "thr9", From: from("Example Corp", "no-reply", atsHost), Subject: "Thank you for applying", Text: "We have received your application for the Backend Engineer position at Example Corp."}
	r := classify(t, in)
	if r.Signal == nil || r.Signal.MessageID != "abc123" || r.Signal.ThreadID != "thr9" {
		t.Fatalf("result = %+v", r)
	}
	if got := classify(t, Input{MessageID: "x", From: in.From, Subject: in.Subject, Text: in.Text}); got.Signal.ThreadID != "x" {
		t.Fatalf("thread id falls back to the message id, got %q", got.Signal.ThreadID)
	}
}

func TestGmail_Classify_TextSanitizedAndBounded(t *testing.T) {
	zw := string(rune(0x200b))
	curly := string(rune(0x2019))
	zero := "Thank you for app" + zw + "lying. We" + curly + "ve received your application for the Backend Engineer position at Example Corp."
	s := mustSignal(t, classify(t, Input{From: from("Example Corp", "no-reply", atsHost), Subject: "Application", Text: zero}))
	if s.Type != TypeApplied {
		t.Fatalf("invisible characters defeated the phrase match: %s", s.Type)
	}
	long := strings.Repeat("filler words go here. ", 2000)
	s = mustSignal(t, classify(t, Input{
		From: from("Example Corp", "no-reply", atsHost), Subject: "Thank you for applying", Snippet: "",
		Text: "We have received your application. " + long,
	}))
	if len([]rune(s.Snippet)) > 220 {
		t.Fatalf("snippet has %d characters", len([]rune(s.Snippet)))
	}
	if got := len(compact(long, maxMatchChars)); got > maxMatchChars+2 {
		t.Fatalf("matching text is %d bytes", got)
	}
}

func TestGmail_Deadline_InvalidDatesAndDurationsAreNotInvented(t *testing.T) {
	ref := time.Date(2027, 3, 10, 15, 0, 0, 0, time.UTC)
	for _, text := range []string{
		"due 2027-02-29", "due 2028-02-30", "due 2027/04/31",
		"complete by April 31", "complete by February 29, 2027", "complete by March 00", "complete by Janu 5",
		"complete in 106752 days", "submit in 15251 weeks",
		"respond in 9223372036854775808 days", "finish in 999999999999999999999999999 weeks",
	} {
		t.Run(text, func(t *testing.T) {
			if got := parseDeadline(text, ref); !got.IsZero() {
				t.Fatalf("invented deadline %v from %q", got, text)
			}
		})
	}
	// A valid leap date in the received year becomes invalid after the existing
	// yearless-date rule rolls it forward. Validate that selected year again.
	if got := parseDeadline("complete by February 29", time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Fatalf("invented next-year leap day: %v", got)
	}
}

func TestGmail_Classify_InvalidDeadlineKeepsAssessmentWithoutDueTime(t *testing.T) {
	for _, deadline := range []string{"2027-02-29", "April 31", "106752 days"} {
		t.Run(deadline, func(t *testing.T) {
			s := mustSignal(t, classify(t, Input{
				From:    from("Example Corp Recruiting", "no-reply", "hackerrank.com"),
				Subject: "Example Corp online assessment invitation",
				Text:    "You are invited to take the online assessment for the Backend Engineer role at Example Corp. Please complete by " + deadline + ".",
			}))
			if s.Type != TypeAssessment || !s.DueAt.IsZero() {
				t.Fatalf("assessment=%s due=%v", s.Type, s.DueAt)
			}
		})
	}
}

func TestGmail_Deadline_ValidCalendarAndDurationBoundaries(t *testing.T) {
	ref := time.Date(2027, 3, 10, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		text string
		want time.Time
	}{
		{"due 2028-02-29", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"due 2028/2/29", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"complete by fEb. 29, 2028", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"complete by JANUARY 5", time.Date(2028, 1, 5, 0, 0, 0, 0, time.UTC)},
		{"complete by Sept 20", time.Date(2027, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"complete by SEPT. 20", time.Date(2027, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"complete by Sept. 20, 2028", time.Date(2028, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"complete in 106751 days", ref.Add(106751 * 24 * time.Hour)},
		{"complete in 15250 weeks", ref.Add(15250 * 7 * 24 * time.Hour)},
		{"complete in 0 days", ref},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := parseDeadline(tc.text, ref); !got.Equal(tc.want) {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}
