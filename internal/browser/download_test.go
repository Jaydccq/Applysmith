package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBrowser_SafeDownloadName(t *testing.T) {
	long := strings.Repeat("a", 200)
	cases := []struct{ in, want string }{
		{"../../.ssh/config", "config"},
		{`..\..\windows\system.ini`, "system.ini"},
		{".hidden", "hidden"},
		{"...", "download"},
		{"a<b>c:d\"e|f?g*h.txt", "a_b_c_d_e_f_g_h.txt"},
		{"tab\there\x00.txt", "tab_here_.txt"},
		{long + ".pdf", strings.Repeat("a", 116) + ".pdf"},
		{"x." + strings.Repeat("e", 20), "x." + strings.Repeat("e", 20)},
	}
	for _, c := range cases {
		if got := SafeDownloadName(c.in); got != c.want {
			t.Errorf("SafeDownloadName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if n := utf8.RuneCountInString(SafeDownloadName(long + ".pdf")); n > 120 {
		t.Errorf("name has %d characters, want at most 120", n)
	}
}

func TestBrowser_DownloadNames_CollisionsBecomeNumbered(t *testing.T) {
	dir := t.TempDir()
	staged := func() string {
		p := filepath.Join(t.TempDir(), "guid")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, want := range []string{"offer-letter.pdf", "offer-letter (1).pdf", "offer-letter (2).pdf"} {
		got, err := moveStaged(staged(), dir, "offer-letter.pdf", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != want {
			t.Fatalf("got %q, want %q", filepath.Base(got), want)
		}
	}
}

func TestBrowser_DownloadNames_TimestampAfterThousandCollisions(t *testing.T) {
	dir := t.TempDir()
	for _, n := range append([]string{"f.txt"}, numbered("f", ".txt", 1000)...) {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := uniquePath(dir, "f.txt", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if !strings.Contains(filepath.Base(got), "20260102T030405") {
		t.Fatalf("got %q, want a timestamped name", got)
	}
}

func numbered(stem, ext string, n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, stem+" ("+strconv.Itoa(i)+")"+ext)
	}
	return out
}

// A browser this server launched starts with an empty staging directory: what
// an earlier browser left there is a download that will never finish. An
// adopted browser keeps its files, since its downloads go on.
func TestBrowser_DownloadStaging_AFreshLaunchRemovesWhatWasLeft(t *testing.T) {
	i, _ := bareInstance(t)
	i.opts.StateDir = t.TempDir()
	left := filepath.Join(i.stagingDir(), "guid-1.crdownload")
	leave := func() {
		t.Helper()
		if err := os.MkdirAll(i.stagingDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(left, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	leave()
	i.proc = &Process{unwatch: func() {}}
	if err := i.prepareStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("an adopted browser's staged download was removed: %v", err)
	}
	i.proc = &Process{}
	if err := i.prepareStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatalf("a launch left an earlier browser's partial download staged: %v", err)
	}
	if st, err := os.Stat(i.stagingDir()); err != nil || !st.IsDir() {
		t.Fatalf("the staging directory is gone: %v", err)
	}
}
