package browser

import (
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

func TestBrowser_SchemeAllowlist(t *testing.T) {
	allowed := []string{
		"", "about:blank", "http://example.com/", "https://example.com/a?b=1#c", "data:text/html,<p>x</p>",
		"blob:https://example.com/3f1c", "blob:http://example.com/3f1c",
	}
	blocked := []string{
		"file:///etc/hosts", "chrome://version", "devtools://devtools/bundled/x.html", "chrome-extension://abc/x.html",
		"view-source:https://example.com", "javascript:alert(1)", "ftp://example.com/x", "about:srcdoc", "blob:file:///x",
		"blob:null/1", "http://", "chrome-untrusted://x", "ws://example.com",
	}
	for _, u := range allowed {
		if !schemeAllowed(u) {
			t.Errorf("%q should be allowed", u)
		}
		if err := checkNavigationURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	for _, u := range blocked {
		if schemeAllowed(u) {
			t.Errorf("%q should be blocked", u)
		}
		if code, _ := CodeOf(checkNavigationURL(u)); code != CodeSchemeNotAllowed {
			t.Errorf("%q: code %q, want scheme-not-allowed", u, code)
		}
	}
}

func TestBrowser_OnlyTheMainFrameLeavesTheAllowlist(t *testing.T) {
	const tab = "TAB1"
	for _, c := range []struct {
		name, frame, url string
		want             bool
	}{
		{"main frame to a local scheme", tab, "chrome://version/", true},
		{"main frame to a web page", tab, "https://example.com/", false},
		{"subframe srcdoc", "SUBFRAME", "about:srcdoc", false},
		{"subframe to a local scheme", "SUBFRAME", "file:///etc/hosts", false},
	} {
		ev := page.EventFrameRequestedNavigation{FrameID: cdp.FrameID(c.frame), URL: c.url}
		if got := leavesAllowlist(tab, ev); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
