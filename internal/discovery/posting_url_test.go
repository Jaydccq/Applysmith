package discovery

import (
	"testing"

	"applysmith/internal/identity"
)

func TestPostingURL_SharesThePageReadersHostPredicate(t *testing.T) {
	for _, raw := range []string{"http://2130706433/jobs/1", "https://0x7f000001/jobs/1", "https://169.254.169.254/latest", "https://[::ffff:10.0.0.1]/jobs/1", "https://metadata.google.internal/x"} {
		if _, err := postingURL(raw); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
	if _, err := postingURL("https://jobs.example.test/acme/jobs/44"); err != nil {
		t.Fatal(err)
	}
}

func TestPostingURL_GreenhouseApplicationFrameBecomesThePostingPage(t *testing.T) {
	for raw, want := range map[string]string{
		"https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567":         "https://job-boards.greenhouse.io/example-corp/jobs/1234567",
		"https://job-boards.greenhouse.io/embed/job_app?token=1234567&for=example-corp&utm_x=1": "https://job-boards.greenhouse.io/example-corp/jobs/1234567",
		"https://boards.greenhouse.io/embed/job_app?for=Example-Corp&gh_jid=1234567":            "https://boards.greenhouse.io/Example-Corp/jobs/1234567",
		"https://job-boards.greenhouse.io/example-corp/jobs/1234567":                            "https://job-boards.greenhouse.io/example-corp/jobs/1234567",
		"https://job-boards.greenhouse.io/embed/job_app?for=example-corp":                       "https://job-boards.greenhouse.io/embed/job_app?for=example-corp",
		"https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=abc":             "https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=abc",
		// An employer's apply link with its tracking query glued on by "&", as a
		// LinkedIn apply button hands it over.
		"http://boards.greenhouse.io/example-corp/jobs/1234567&gh_src=abc1+?gh_src=abc1+": "https://boards.greenhouse.io/example-corp/jobs/1234567",
	} {
		got, err := postingURL(raw)
		if err != nil || got != want {
			t.Errorf("postingURL(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	// The stored address and the frame name the same posting.
	a, _ := postingURL("https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567")
	ka, _ := identity.ATSKey(a)
	kb, _ := identity.ATSKey("https://job-boards.greenhouse.io/embed/job_app?for=example-corp&token=1234567")
	if ka == "" || ka != kb {
		t.Errorf("identity %q vs %q", ka, kb)
	}
}
