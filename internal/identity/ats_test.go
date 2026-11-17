package identity

import "testing"

func TestATSKey_SmartRecruitersOneClickJobQuery(t *testing.T) {
	a, okA := ATSKey("https://jobs.smartrecruiters.com/oneclick-ui/company/Acme/job?id=744000012345")
	b, okB := ATSKey("https://jobs.smartrecruiters.com/oneclick-ui/company/acme/job?id=744000012346")
	if !okA || !okB || a == b {
		t.Fatalf("two ids must give two keys: %q (%v), %q (%v)", a, okA, b, okB)
	}
	if a != "smartrecruiters:acme/744000012345" {
		t.Errorf("key = %q", a)
	}
}

func TestATSKey_SmartRecruitersSamePostingAcrossShapes(t *testing.T) {
	want := "smartrecruiters:acme/744000012345"
	for _, in := range []string{
		"https://jobs.smartrecruiters.com/oneclick-ui/company/acme/publication/744000012345",
		"https://jobs.smartrecruiters.com/acme/744000012345-software-engineer",
		"https://jobs.smartrecruiters.com/oneclick-ui/company/acme/job?id=744000012345",
		"https://jobs.smartrecruiters.com/oneclick-ui/company/acme/job?id=744000012345&utm_source=x",
		"https://api.smartrecruiters.com/v1/companies/acme/postings/744000012345",
	} {
		if got, ok := ATSKey(in); !ok || got != want {
			t.Errorf("ATSKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if k, ok := ATSKey("https://jobs.smartrecruiters.com/oneclick-ui/company/acme/job"); ok {
		t.Errorf("a one-click URL with no id names no posting, got %q", k)
	}
}

func TestATSKey_AshbyApplicationSuffix(t *testing.T) {
	const uuid = "0a1b2c3d-1111-2222-3333-444455556666"
	if k, ok := ATSKey("https://jobs.ashbyhq.com/example-co/application"); ok {
		t.Errorf("a board application URL names no posting, got %q", k)
	}
	if k, ok := ATSKey("https://jobs.ashbyhq.com/example-co/application/"); ok {
		t.Errorf("a board application URL names no posting, got %q", k)
	}
	want := "ashby:example-co/" + uuid
	for _, in := range []string{
		"https://jobs.ashbyhq.com/example-co/" + uuid + "/application",
		"https://jobs.ashbyhq.com/example-co/application?ashby_jid=" + uuid,
	} {
		if got, ok := ATSKey(in); !ok || got != want {
			t.Errorf("ATSKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestIsATSNative_OnlyRealATSKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"greenhouse:example-co/1001":                 true,
		"lever:example-co/0a1b2c3d-1111-2222-3333-4": true,
		"smartrecruiters:acme/744000012345":          true,
		"greenhouse:1001":                            false, // {source}:{id}
		"lever:abc123":                               false,
		"ashby:":                                     false,
		"workable:/12":                               false,
		"linkedin:3912345678":                        false,
		"https://example.com/jobs/1":                 false,
		"example corp|engineer":                      false,
		"":                                           false,
	} {
		if got := IsATSNative(key); got != want {
			t.Errorf("IsATSNative(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSameOpening_SourceIDKeyIsNotATSNative(t *testing.T) {
	a := Posting{Source: "greenhouse", SourceJobID: "1001", Company: "Example Corp", Role: "Software Engineer"}
	b := Posting{Source: "greenhouse", SourceJobID: "2002", Company: "Example Corp", Role: "Software Engineer"}
	if !SameOpening(a, b) {
		t.Error("source-id keys are not requisition keys; the company and role predicate still decides")
	}
}
