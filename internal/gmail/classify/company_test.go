package classify

import (
	"testing"
)

func resolve(t *testing.T, tax *Taxonomy, fromHeader, subject, body string) CompanyEvidence {
	t.Helper()
	f := parseAddress(fromHeader)
	return tax.resolveCompany(subject, body, f, tax.senderPolicy(f, body))
}

func TestGmail_Company_DeskLabelStripping(t *testing.T) {
	for in, want := range map[string]string{
		"Example Corp Talent Acquisition": "Example Corp",
		"Example HR":                      "Example",
		"Talent Acquisition - Example":    "Example",
		// Agencies are named this way and keep their names.
		"Example Recruitment":              "Example Recruitment",
		"Recruiting from Scratch":          "Recruiting from Scratch",
		"DC Department of Human Resources": "DC Department of Human Resources",
	} {
		if got := cleanCompany(in); got != want {
			t.Errorf("cleanCompany(%q) = %q, want %q", in, got, want)
		}
	}
	// A value that is only a desk label is generic, so resolution falls through.
	tax := Defaults()
	for _, label := range []string{"HR", "Talent Acquisition"} {
		if !tax.isGeneric(label) {
			t.Errorf("%q should be generic", label)
		}
	}
	got := resolve(t, tax, from("HR System", "hrsystem", "example.com"), "Hello", "Hello there")
	if got.Value != "Example" || got.Strategy != StratSenderDomain {
		t.Fatalf("desk-only display name = %+v", got)
	}
}

func TestGmail_Company_AdministrativeOpenersNeverEmployer(t *testing.T) {
	tax := Defaults()
	for _, subject := range []string{
		"Update on Your Backend Engineer Application",
		"Action Required: complete your profile",
		"Re: Your recent job application",
	} {
		got := resolve(t, tax, from("", "no-reply", "example.com"), subject, "Hello")
		if tax.isGeneric(got.Value) && got.Value != UnknownCompany {
			t.Errorf("subject %q produced the employer %q", subject, got.Value)
		}
		for _, bad := range []string{"Update on Your", "Action Required", "Recent", "Re"} {
			if got.Value == bad {
				t.Errorf("subject %q produced the employer %q", subject, got.Value)
			}
		}
	}
	for _, v := range []string{"Update on Your", "Action Required", "Reminder", "Re", "Your recent", "The Robotics Software Engineer role"} {
		if !tax.isGeneric(v) {
			t.Errorf("%q should be generic", v)
		}
	}
	if tax.isGeneric("The Trade Desk") || tax.isGeneric("xAI") || tax.isGeneric("fal") {
		t.Error("real names with a leading article or lowercase brand were rejected")
	}
}

func TestGmail_Company_ResolutionOrder(t *testing.T) {
	tax, err := WithOverride([]byte("tenant_overrides:\n  acmeco: Acme Co\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Explicit subject beats the ATS sender display name, which beats the tenant slug.
	got := resolve(t, tax, from("Wrong Sender", "acmeco", atsHost), "Thank you for applying to Subject Corp", "Hello")
	if got.Value != "Subject Corp" || got.Strategy != StratSubject {
		t.Fatalf("subject first = %+v", got)
	}
	got = resolve(t, tax, from("Sender Name Corp Recruiting", "acmeco", atsHost), "Application update", "Hello")
	if got.Value != "Sender Name Corp" || got.Strategy != StratATSName {
		t.Fatalf("ATS sender name second = %+v", got)
	}
	got = resolve(t, tax, from("Recruiting", "acmeco", atsHost), "Application update", "Hello there")
	if got.Value != "Acme Co" || got.Strategy != StratTenantSlug {
		t.Fatalf("tenant slug from the local part = %+v", got)
	}
	got = resolve(t, tax, from("", "donotreply", "bigcorp.myworkday.com"), "Application update", "Hello")
	if got.Strategy != StratWorkdayTenant || got.Value != "Bigcorp" {
		t.Fatalf("workday tenant = %+v", got)
	}
	// A body phrase comes before the ATS sender name when the sender looks like a person.
	got = resolve(t, tax, from("Sam Example", "sam", atsHost), "Application update", "Thanks for applying to the Backend Engineer role at Body Corp.")
	if got.Value != "Body Corp" || got.Strategy != StratBody {
		t.Fatalf("personal display name = %+v", got)
	}
	got = resolve(t, tax, from("", "x", "gmail.com"), "Update", "Thanks for applying to the Backend Engineer role at Webmail Corp.")
	if got.Value != "Webmail Corp" {
		t.Fatalf("webmail = %+v", got)
	}
	// The sender domain is the last resort.
	got = resolve(t, tax, from("", "hello", "lastresort.com"), "Hi", "Hello")
	if got.Value != "Lastresort" || got.Strategy != StratSenderDomain {
		t.Fatalf("domain = %+v", got)
	}
}

func TestGmail_Company_ToolAndVendorNeverEmployer(t *testing.T) {
	tax := Defaults()
	for _, h := range []string{
		from("", "no-reply", "calendly.com"), from("", "invite", "hackerrank.com"),
		from("", "someone", "gmail.com"), from("Greenhouse", "no-reply", "greenhouse.io"),
	} {
		got := resolve(t, tax, h, "Hi", "Hello")
		if got.Strategy == StratSenderDomain || (got.Value != UnknownCompany && tax.isVendorBrand(got.Value)) {
			t.Errorf("%s supplied the employer %+v", h, got)
		}
	}
	// A scheduling assistant's mail names the first non-personal co-recipient.
	c := newClassifier()
	in := Input{MessageID: "m", From: from("Scheduler", "no-reply", "calendly.com"), To: addr("alex", "gmail.com"),
		Cc: from("Jordan Interviewer", "jordan", "bluebird.example.com"), Subject: "Interview confirmed",
		Text: "Your interview is scheduled for tomorrow at 2:00 pm. Join https://meet.google.com/abc-defg-hij", ReceivedAt: refTime,
		Mailbox: addr("alex", "gmail.com")}
	r := c.Classify(in)
	if r.Signal == nil || r.Signal.Company != "Bluebird" || r.Signal.Evidence.Company.Strategy != StratCoRecipient {
		t.Fatalf("scheduling assistant: %+v", r.Signal)
	}
}

func TestGmail_Company_StrongConflictGoesToReview(t *testing.T) {
	tax := Defaults()
	got := resolve(t, tax, from("Alpha Industries", "no-reply", atsHost), "Thank you for applying to Beta Robotics", "We received your application")
	if !got.Conflict || len(got.Conflicts) != 2 {
		t.Fatalf("evidence = %+v", got)
	}
	// Equivalent names (a legal suffix, a prefix relation, an initialism) do not conflict.
	for _, pair := range [][2]string{{"Acme Technologies", "Acme"}, {"Palantir Technologies", "Palantir"}, {"International Business Machines", "IBM"}, {"The Trade Desk", "Trade Desk"}} {
		if !companyEquivalent(pair[0], pair[1]) {
			t.Errorf("%q and %q should be equivalent", pair[0], pair[1])
		}
	}
	if companyEquivalent("Alpha Industries", "Beta Robotics") {
		t.Error("different employers were treated as equivalent")
	}
}

func TestGmail_Company_JobBoardBrandToReview(t *testing.T) {
	tax := Defaults()
	for _, b := range []string{"Jobgether", "Jobs2Web", "Nexxt", "Adzuna", "Dice", "BuiltIn", "Naukri", "eFinancialCareers", "RemoteHunter"} {
		if !tax.JobBoardBrands[slugOf(b)] {
			t.Errorf("%s is missing from the job-board brands", b)
		}
	}
	// The extraction itself is not demoted: the company stays as read.
	s := mustSignal(t, classify(t, Input{From: from("Jobgether", "no-reply", "jobgether.com"), Subject: "Your application was sent to Jobgether", Text: "Your application was sent to Jobgether."}))
	if s.Company != "Jobgether" {
		t.Fatalf("company = %q", s.Company)
	}
}

func TestGmail_Role_ResolutionAndPlaceholders(t *testing.T) {
	cases := []struct{ subject, body, company, want string }{
		{"Your application to Backend Engineer at Example Corp", "", "Example Corp", "Backend Engineer"},
		{"Application update", "Thank you for applying to the Staff Data Scientist position at Example Corp.", "Example Corp", "Staff Data Scientist"},
		{"Software Engineer - Evaluation - example.com", "", "Example", "Software Engineer - Evaluation"},
		{"Thanks", "Hello", "", UnknownRole},
		{"Hi Alex", "Dear Jordan, Thank you for your time.", "", UnknownRole},
	}
	for _, c := range cases {
		got := resolveRole(c.subject, c.body, c.company)
		if got.Value != c.want {
			t.Errorf("role(%q | %q) = %q (%s), want %q", c.subject, c.body, got.Value, got.Strategy, c.want)
		}
	}
	if !isGenericRole("Dear Jordan, Thank you") || !isGenericRole("your application was sent") || isGenericRole("Backend Engineer") {
		t.Error("isGenericRole misjudged a value")
	}
}
