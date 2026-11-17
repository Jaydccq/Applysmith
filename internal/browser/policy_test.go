package browser

import (
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestBrowser_ResourcePolicy_Decisions(t *testing.T) {
	cases := []struct {
		name               string
		top, req, ref, typ string
		want               bool
	}{
		{"an image is failed", "jobs.example.test", "jobs.example.test", "", "Image", true},
		{"a script loads", "jobs.example.test", "jobs.example.test", "", "Script", false},
		{"a document is never failed", "jobs.example.test", "x.googletagmanager.com", "", "Document", false},
		{"a tracker script is failed", "jobs.example.test", "www.googletagmanager.com", "", "Script", true},
		{"a vendor's own site loads its own scripts", "www.googletagmanager.com", "www.googletagmanager.com", "", "Script", false},
		{"a guarded main frame blocks nothing", "www.indeed.com", "static.indeed.com", "", "Image", false},
		{"a guarded requested host blocks nothing", "jobs.example.test", "static.glassdoor.com", "", "Font", false},
		{"a guarded referrer blocks nothing", "jobs.example.test", "cdn.example.test", "www.linkedin.com", "Image", false},
		{"a host that merely ends in a tracker name loads", "jobs.example.test", "notdoubleclick.net", "", "Script", false},
	}
	for _, c := range cases {
		if got := shouldBlock(c.top, c.req, c.ref, network.ResourceType(c.typ)); got != c.want {
			t.Errorf("%s: blocked=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestBrowser_ResourcePolicy_PatternsPauseOnlyWhatMayBeBlocked(t *testing.T) {
	ps := resourcePatterns()
	if len(ps) != len(blockedTypes)+len(trackerHosts) {
		t.Fatalf("%d patterns", len(ps))
	}
	for _, p := range ps {
		if p.ResourceType == "" && p.URLPattern == "" {
			t.Fatal("a catch-all pattern would pause every request")
		}
		if p.RequestStage != "Request" {
			t.Fatalf("stage %q", p.RequestStage)
		}
	}
}
