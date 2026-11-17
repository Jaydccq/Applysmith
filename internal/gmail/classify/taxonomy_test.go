package classify

import (
	"regexp"
	"strings"
	"testing"
)

func TestGmail_Taxonomy_DefaultsAreGeneric(t *testing.T) {
	tax := Defaults()
	if len(tax.TenantOverrides) != 0 {
		t.Fatalf("the shipped tenant overrides name employers: %v", tax.TenantOverrides)
	}
	domain := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	for name, set := range map[string]map[string]bool{
		"trusted_ats": tax.TrustedATS, "multi_tenant_ats": tax.MultiTenantATS, "vendor_ats": tax.VendorATS,
		"scheduling_tools": tax.SchedulingTools, "webmail": tax.Webmail,
	} {
		for d := range set {
			if !domain.MatchString(d) {
				t.Errorf("%s entry %q is not a bare domain", name, d)
			}
		}
	}
	for d := range tax.Restricted {
		if !domain.MatchString(d) {
			t.Errorf("restricted sender %q is not a bare domain", d)
		}
	}
	// No personal address or employer-looking entry anywhere in the source file.
	if strings.Contains(string(defaultSenders), "@") {
		t.Error("the shipped file contains an address")
	}
}

func TestGmail_Taxonomy_OverrideExtendsDefaults(t *testing.T) {
	base := Defaults()
	tax, err := WithOverride([]byte(`
trusted_ats: [hiring.example.net, lever.co]
tenant_overrides:
  mycorp: My Corp
restricted_senders:
  jobs.example.net: [applied_confirmation]
`))
	if err != nil {
		t.Fatal(err)
	}
	if !tax.TrustedATS["hiring.example.net"] || !tax.TrustedATS["greenhouse.io"] {
		t.Fatal("merged list lost an entry")
	}
	if len(tax.TrustedATS) != len(base.TrustedATS)+1 {
		t.Fatalf("lists are concatenated without duplicates: %d vs %d", len(tax.TrustedATS), len(base.TrustedATS))
	}
	if tax.TenantOverrides["mycorp"] != "My Corp" {
		t.Fatalf("overrides = %v", tax.TenantOverrides)
	}
	if !tax.Restricted["jobs.example.net"]["applied_confirmation"] || tax.Restricted["linkedin.com"] == nil {
		t.Fatal("maps are overlaid, not replaced")
	}
	// The override wins on a clashing map key.
	win, err := WithOverride([]byte("restricted_senders:\n  linkedin.com: [rejection]\n"))
	if err != nil || !win.Restricted["linkedin.com"]["rejection"] || win.Restricted["linkedin.com"]["applied_confirmation"] {
		t.Fatalf("override did not win: %v %v", win.Restricted["linkedin.com"], err)
	}
}

func TestGmail_Taxonomy_MalformedOverrideIgnored(t *testing.T) {
	for name, text := range map[string]string{
		"not yaml":    "trusted_ats: [unclosed",
		"wrong type":  "trusted_ats: yes please",
		"unknown key": "trusted_atss: [x.example.net]",
	} {
		tax, err := WithOverride([]byte(text))
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if tax == nil || len(tax.TrustedATS) != len(Defaults().TrustedATS) {
			t.Errorf("%s: the defaults were not kept", name)
		}
	}
	// An empty override file is fine.
	if _, err := WithOverride(nil); err != nil {
		t.Fatal(err)
	}
}

func TestGmail_Policy_RestrictedSender(t *testing.T) {
	tax := Defaults()
	p := tax.senderPolicy(parseAddress(from("LinkedIn", "jobs-noreply", "linkedin.com")), "Recruiter, Talent Acquisition team")
	if !p.Restricted || p.Trusted || p.TrustedBySignature {
		t.Fatalf("policy = %+v", p)
	}
	if !p.allows(TypeApplied) || p.allows(TypeInterview) || p.allows(TypeOffer) {
		t.Fatalf("allowed = %v", p.Allowed)
	}
	sub := tax.senderPolicy(parseAddress(from("", "x", "mail.linkedin.com")), "")
	if !sub.Restricted {
		t.Fatal("a subdomain of a restricted sender is restricted")
	}
	if open := tax.senderPolicy(parseAddress(from("", "x", "example.com")), ""); open.Restricted || !open.allows(TypeInterview) {
		t.Fatal("an ordinary sender must not be restricted")
	}
}

func TestGmail_Policy_TrustNameVersusSignature(t *testing.T) {
	tax := Defaults()
	trusted := func(f, body string) Policy { return tax.senderPolicy(parseAddress(f), body) }
	if p := trusted(from("Sam Example, Technical Recruiter", "sam", otherHost), ""); !p.Trusted || p.TrustedBySignature {
		t.Errorf("recruiting identity in the display name: %+v", p)
	}
	if p := trusted(from("Hiring", "x", atsHost), ""); !p.Trusted {
		t.Errorf("trusted ATS domain: %+v", p)
	}
	sig := "Thanks for your time.\n--\nSam Example\nTechnical Recruiter at Example Corp"
	if p := trusted(from("Sam Example", "sam", otherHost), sig); !p.Trusted || !p.TrustedBySignature {
		t.Errorf("signature block: %+v", p)
	}
	tail := strings.Repeat("filler text here. ", 40) + "Sam Example, Talent Acquisition Partner"
	if p := trusted(from("Sam Example", "sam", otherHost), tail); !p.TrustedBySignature {
		t.Errorf("message tail without a marker: %+v", p)
	}
	prose := "Our recruiting team will reach out. " + strings.Repeat("filler text here. ", 40) + "Regards, Sam"
	if p := trusted(from("Sam Example", "sam", otherHost), prose); p.Trusted {
		t.Errorf("prose outside the signature conferred trust: %+v", p)
	}
	if p := trusted(from("Sam Example", "sam", "gmail.com"), sig); p.Trusted {
		t.Errorf("webmail earned trust from a signature: %+v", p)
	}
	// Department words are accepted in a signature, never in a display name.
	if p := trusted(from("Example Corp Human Resources", "x", otherHost), ""); p.Trusted {
		t.Errorf("department word in a display name: %+v", p)
	}
	if p := trusted(from("Sam Example", "sam", otherHost), "--\nSam\nHuman Resources, Example Corp"); !p.TrustedBySignature {
		t.Errorf("department word in a signature: %+v", p)
	}
}

func TestGmail_Policy_BroadIdentityOnlyCorroborates(t *testing.T) {
	tax := Defaults()
	f := parseAddress(from("Talent Desk", "desk", otherHost))
	p := tax.senderPolicy(f, "")
	if p.Trusted {
		t.Fatal("a broad identity word made the sender trusted")
	}
	// It cannot rename a company.
	if c := tax.companyFromATSName(f, p); c != nil {
		t.Fatalf("broad identity supplied the employer %+v", c)
	}
	// It does corroborate an interview claim.
	in := eventInput{subject: "Interview", body: "Would like to speak about the position. Please reply with your availability for an interview.", from: f}
	if fail := tax.interviewGateFailure(in, p, tax.schedulingArtifact(), ""); fail != "" {
		t.Fatalf("gate failed with %q", fail)
	}
}
