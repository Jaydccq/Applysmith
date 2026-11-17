package classify

import "testing"

// A recruiting-sounding name, address or body is text anyone can write. Each
// case is a claim that would be actionable from a trusted sender and must stay
// in review from a domain nothing vouches for.
func TestGmail_Trust_ForgeableTokensNeverMakeASignalActionable(t *testing.T) {
	const forged = "evil.test"
	for _, c := range []struct {
		name string
		in   Input
	}{
		{"display name token", Input{
			From: from("Example Corp Careers", "x", forged), Subject: "Update on your application to Example Corp",
			Text: "Unfortunately we are not moving forward with your application for the Backend Engineer role.",
		}},
		{"address token", Input{
			From: from("", "recruiter", forged), Subject: "Thank you for applying to Example Corp",
			Text: "We have received your application for the Backend Engineer position.",
		}},
		{"signature and body text", Input{
			From: from("Pat Quinn", "pat", forged), Subject: "Interview with Example Corp",
			Text: "Hi Alex, we would like to schedule an interview for the Backend Engineer role on Tuesday at 3:00 pm https://meet.google.com/abc-defg-hij\n--\nPat Quinn\nTechnical Recruiter, Example Corp",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := mustSignal(t, classify(t, c.in))
			if s.Tier != TierReview || !contains(s.ReviewReasons, ReviewSenderUnverified) {
				t.Fatalf("tier %s reasons %v", s.Tier, s.ReviewReasons)
			}
			if s.Evidence.Confidence.SenderTrust >= 0.9 {
				t.Fatalf("sender trust %.2f", s.Evidence.Confidence.SenderTrust)
			}
		})
	}
}

func TestGmail_Trust_AnchorsAreTheTaxonomyAndTheEmployerDomain(t *testing.T) {
	apply := func(f string) Input {
		return Input{From: f, Subject: "Thank you for applying to Example Corp", Text: "We have received your application for the Backend Engineer position."}
	}
	for name, c := range map[string]struct {
		in   Input
		tier string
	}{
		"trusted ATS domain":            {apply(from("Example Corp", "no-reply", atsHost)), TierActionable},
		"employer domain with suffix":   {apply(from("Example Corp Careers", "careers", corpHost)), TierActionable},
		"other domain, same display":    {apply(from("Example Corp Careers", "careers", "evil.test")), TierReview},
		"lookalike domain":              {apply(from("Example Corp Careers", "careers", "example-corp-jobs.test")), TierReview},
		"webmail never anchors":         {apply(from("Example Corp Recruiting", "recruiter", "gmail.com")), TierReview},
		"domain only, employer unnamed": {Input{From: from("", "jobs", corpHost), Subject: "Thank you for applying", Text: "We have received your application for the Backend Engineer position."}, TierReview},
	} {
		t.Run(name, func(t *testing.T) {
			s := mustSignal(t, classify(t, c.in))
			if s.Tier != c.tier {
				t.Fatalf("tier %s reasons %v (company %q via %s)", s.Tier, s.ReviewReasons, s.Company, s.Evidence.Company.Strategy)
			}
		})
	}
}

func TestGmail_Trust_OutgoingMailNeedsNoAnchor(t *testing.T) {
	in := Input{
		From: from("Alex Example", "alex", "gmail.com"), To: from("Pat Quinn", "pat", corpHost), Mailbox: addr("alex", "gmail.com"),
		Subject: "Re: Interview availability", Text: "Thanks, I am available Tuesday at 3:00 pm and I confirm.",
	}
	if s := mustSignal(t, classify(t, in)); s.Direction != DirOutgoing || contains(s.ReviewReasons, ReviewSenderUnverified) {
		t.Fatalf("outgoing mail = %s %v", s.Direction, s.ReviewReasons)
	}
}
