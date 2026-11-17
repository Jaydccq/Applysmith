package classify

import (
	"testing"
	"time"
)

func window() []SignupWindow {
	return Windows([]SignupRun{{
		JobHost: "acme.wd5.myworkdayjobs.com", SignupHost: "acme.wd5.myworkdayjobs.com",
		Start: refTime.Add(-10 * time.Minute), End: refTime, AccountCreated: refTime.Add(-5 * time.Minute),
	}})
}

func selfClassifier() *Classifier {
	return &Classifier{Tax: Defaults(), Windows: window(), WindowsKnown: true}
}

func welcome(at time.Time, sender, subject string) Input {
	return Input{MessageID: "w1", From: from("Example Corp", sender, "otp.workday.com"), Subject: subject,
		Text: "Welcome. You can now apply for roles at Example Corp and track your applications.", ReceivedAt: at}
}

func TestGmail_SelfTriggered_DemotedNotDropped(t *testing.T) {
	r := selfClassifier().Classify(welcome(refTime.Add(3*time.Minute), "acme", "Welcome to the Example Corp career portal"))
	if r.Signal == nil {
		t.Fatalf("a demoted signal must stay visible as evidence, got %s", r.Reason)
	}
	s := r.Signal
	if s.Type != TypeLowPriority || s.Tier != TierLow || s.Evidence.Event.RuleID != SelfTriggeredRule || len(s.ReviewReasons) != 0 {
		t.Fatalf("signal = %s/%s/%s %v", s.Type, s.Tier, s.Evidence.Event.RuleID, s.ReviewReasons)
	}
}

func TestGmail_SelfTriggered_RealEmployerReplyOnTheSameATS(t *testing.T) {
	in := welcome(refTime.Add(3*time.Minute), "acme", "Next steps for your Backend Engineer application")
	in.Text = "We would like to schedule a call. Please find a time here: https://calendly.com/sam/30min for your Backend Engineer application."
	r := selfClassifier().Classify(in)
	if r.Signal == nil || r.Signal.Evidence.Event.RuleID == SelfTriggeredRule {
		t.Fatalf("a real reply was demoted: %+v", r.Signal)
	}
}

func TestGmail_SelfTriggered_AllThreeConditions(t *testing.T) {
	in := welcome(refTime.Add(3*time.Minute), "acme", "Welcome to the Example Corp career portal")
	for name, c := range map[string]struct {
		in   Input
		want bool
	}{
		"all hold":              {in, true},
		"before the run":        {welcome(refTime.Add(-30*time.Minute), "acme", in.Subject), false},
		"inside the grace":      {welcome(refTime.Add(110*time.Minute), "acme", in.Subject), true},
		"3 hours after the run": {welcome(refTime.Add(3*time.Hour), "acme", in.Subject), false},
		"other sender":          {Input{MessageID: "w2", From: from("Other", "x", "unrelated.example.org"), Subject: in.Subject, Text: in.Text, ReceivedAt: in.ReceivedAt}, false},
		"not lifecycle shaped":  {welcome(refTime.Add(3*time.Minute), "acme", "Interview invitation"), false},
	} {
		r := selfClassifier().Classify(c.in)
		got := r.Signal != nil && r.Signal.Evidence.Event.RuleID == SelfTriggeredRule
		if got != c.want {
			t.Errorf("%s: demoted = %v, want %v", name, got, c.want)
		}
	}
}

func TestGmail_SelfTriggered_SubjectShapes(t *testing.T) {
	for subject, want := range map[string]bool{
		"Verify your candidate account":      true,
		"Verification code":                  true,
		"Welcome to the Acme careers portal": true,
		"Welcome to Acme":                    false,
		"Your one-time passcode":             true,
		"Password reset request":             true,
		"Interview invitation":               false,
	} {
		if got := lifecycleSubjectRE.MatchString(subject); got != want {
			t.Errorf("%q = %v, want %v", subject, got, want)
		}
	}
}

func TestGmail_SelfTriggered_NoAccountNoWindow(t *testing.T) {
	w := Windows([]SignupRun{
		{JobHost: "acme.wd5.myworkdayjobs.com", Start: refTime.Add(-time.Hour), End: refTime},
		{SignupHost: "", JobHost: "", Start: refTime, AccountCreated: refTime},
	})
	if len(w) != 0 {
		t.Fatalf("windows = %+v", w)
	}
	win := Windows([]SignupRun{{JobHost: "x.career5.successfactors.com", Start: refTime.Add(-time.Hour), End: refTime, AccountCreated: refTime.Add(-time.Minute)}})
	if len(win) != 1 || len(win[0].ATSMailDomains) != 0 {
		t.Fatalf("a non-career host has no ATS mail domains: %+v", win)
	}
	sf := Windows([]SignupRun{{JobHost: "career41.sapsf.com", Start: refTime.Add(-time.Hour), End: refTime, AccountCreated: refTime.Add(-time.Minute)}})
	if len(sf) != 1 || len(sf[0].ATSMailDomains) != 2 || sf[0].ATSMailDomains[0] != "successfactors.com" {
		t.Fatalf("SuccessFactors mail domains = %+v", sf)
	}
	wd := window()
	if len(wd) != 1 || wd[0].ATSMailDomains[0] != "otp.workday.com" || wd[0].ATSMailDomains[1] != "myworkday.com" {
		t.Fatalf("Workday mail domains = %+v", wd)
	}
	// The window ends at the later of run end and account creation, plus two hours.
	late := Windows([]SignupRun{{JobHost: "a.example.com", Start: refTime, End: refTime.Add(time.Minute), AccountCreated: refTime.Add(30 * time.Minute)}})
	if want := refTime.Add(30*time.Minute + SignupGrace); !late[0].Until.Equal(want) {
		t.Fatalf("until = %v, want %v", late[0].Until, want)
	}
}

// With no window source the classifier cannot tell the product's own sign-up
// mail from an employer's, so lifecycle-shaped mail leaves the actionable tier
// and is marked so the linker never links it on its own.
func TestGmail_SelfTriggered_NoWindowSourceKeepsLifecycleMailOutOfActionable(t *testing.T) {
	in := Input{
		From: from("Example Corp", "no-reply", atsHost), Subject: "Verify your email address to confirm your application to Example Corp",
		Text: "We have received your application for the Backend Engineer position.",
	}
	known := &Classifier{Tax: Defaults(), WindowsKnown: true}
	if s := mustSignal(t, known.Classify(withID(in))); s.Tier != TierActionable {
		t.Fatalf("with an empty but known window list the mail is classified normally: %s %v", s.Tier, s.ReviewReasons)
	}
	unknown := &Classifier{Tax: Defaults()}
	s := mustSignal(t, unknown.Classify(withID(in)))
	if s.Tier != TierReview || !contains(s.ReviewReasons, ReviewSignupWindowUnknown) {
		t.Fatalf("tier %s reasons %v", s.Tier, s.ReviewReasons)
	}
	in.Subject = "Thank you for applying to Example Corp"
	if s := mustSignal(t, unknown.Classify(withID(in))); s.Tier != TierActionable {
		t.Fatalf("a mail that is not lifecycle shaped is unaffected: %s %v", s.Tier, s.ReviewReasons)
	}
}

func withID(in Input) Input {
	in.MessageID, in.ReceivedAt = "m-window", refTime
	return in
}
