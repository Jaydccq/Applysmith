package apply

import (
	"testing"

	"applysmith/internal/browser"
)

func TestApplyChain_LinkedInFlowURLShapes(t *testing.T) {
	for _, c := range []struct{ in, flow, posting string }{
		{"https://www.linkedin.com/jobs/view/4000000001", "https://www.linkedin.com/jobs/view/4000000001/apply/?openSDUIApplyFlow=true", "https://www.linkedin.com/jobs/view/4000000001/"},
		{"https://www.linkedin.com/jobs/view/senior-engineer-at-example-4000000002/?trk=x", "https://www.linkedin.com/jobs/view/4000000002/apply/?openSDUIApplyFlow=true", "https://www.linkedin.com/jobs/view/4000000002/"},
		{"https://uk.linkedin.com/jobs/view/4000000003/apply/?_l=en_US", "https://www.linkedin.com/jobs/view/4000000003/apply/?openSDUIApplyFlow=true", "https://www.linkedin.com/jobs/view/4000000003/"},
		{"https://www.linkedin.com:8443/jobs/view/4000000004", "https://www.linkedin.com:8443/jobs/view/4000000004/apply/?openSDUIApplyFlow=true", "https://www.linkedin.com:8443/jobs/view/4000000004/"},
		{"https://www.linkedin.com/jobs/collections/recommended/?currentJobId=4000000005", "", ""},
		{"https://careers.example.test/jobs/4000000006", "", ""},
	} {
		if got := LinkedInFlowURL(c.in); got != c.flow {
			t.Errorf("flow of %s = %q, want %q", c.in, got, c.flow)
		}
		if got := LinkedInPostingURL(c.in); got != c.posting {
			t.Errorf("posting of %s = %q, want %q", c.in, got, c.posting)
		}
	}
}

func TestApplyChain_LinkedInSessionChallengeIsALoginRedirectOrARenderedCaptcha(t *testing.T) {
	anchor := CaptchaW{Kind: "recaptcha_v2_enterprise", Visible: true, Invisible: false, W: 0, H: 0}
	frame := CaptchaW{Kind: "recaptcha_v2", Visible: true, W: 300, H: 80}
	for name, c := range map[string]struct {
		st   PageState
		want string
	}{
		"login":                {PageState{URL: "https://www.linkedin.com/login?session_redirect=x"}, "login_redirect"},
		"uas login":            {PageState{URL: "https://www.linkedin.com/uas/login"}, "login_redirect"},
		"checkpoint":           {PageState{URL: "https://www.linkedin.com/checkpoint/challenge/abc"}, "login_redirect"},
		"the invisible anchor": {PageState{URL: "https://www.linkedin.com/jobs/view/1/", Captcha: []CaptchaW{anchor}}, ""},
		"a rendered captcha":   {PageState{URL: "https://www.linkedin.com/jobs/view/1/", Captcha: []CaptchaW{frame}}, "captcha"},
		"hcaptcha":             {PageState{URL: "https://www.linkedin.com/jobs/view/1/", Captcha: []CaptchaW{{Kind: "hcaptcha", Visible: true, W: 300, H: 80}}}, "captcha"},
		"a clean page":         {PageState{URL: "https://www.linkedin.com/jobs/view/1/apply/"}, ""},
	} {
		if got := sessionChallenge(&c.st); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestApplyChain_LinkedInConfirmsOnlyAnApplicationsEndpointAnswering200(t *testing.T) {
	post := func(url string, status int) browser.Response {
		return browser.Response{Method: "POST", URL: url, Status: status}
	}
	apps := "https://www.linkedin.com/voyager/api/voyagerJobsDashOnsiteApplyApplication?action=submit&applications=1"
	for name, c := range map[string]struct {
		rs   []browser.Response
		want bool
	}{
		"200":                    {[]browser.Response{post(apps, 200)}, true},
		"refused":                {[]browser.Response{post(apps, 422)}, false},
		"another write":          {[]browser.Response{post("https://www.linkedin.com/voyager/api/messaging", 200)}, false},
		"a read of the endpoint": {[]browser.Response{{Method: "GET", URL: apps, Status: 200}}, false},
	} {
		if got := linkedInAccepted(c.rs); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
