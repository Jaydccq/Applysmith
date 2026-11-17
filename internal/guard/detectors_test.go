package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func hitTexts(hs []hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.text)
	}
	return out
}

func checkHits(t *testing.T, match func(string) []hit, positives, negatives []string) {
	t.Helper()
	for _, s := range positives {
		if len(match(s)) == 0 {
			t.Errorf("expected a hit in %q", s)
		}
	}
	for _, s := range negatives {
		if got := match(s); len(got) != 0 {
			t.Errorf("unexpected hit %v in %q", hitTexts(got), s)
		}
	}
}

func TestEmailDetector(t *testing.T) {
	checkHits(t, emailHits,
		[]string{
			"contact " + join("alex", "@", "gmail.com") + " now",
			join("a.b+tag", "@", "corp.io"),
			join("someone", "@", "example.com.evil.io"),
			join("user", "@", "163.com"),
			join("x", "@", "notatest.example.co"),
		},
		[]string{
			join("alex", "@", "example.com"),
			join("alex", "@", "host.test"),
			"golang.org/x/text" + "@" + "v0.3.0",
			"github.com/egoist/mygo" + "@" + "v0.4.1",
			"react" + "@" + "19.3.0",
		})
	got := emailHits("x " + join("alex", "@", "gmail.com"))[0]
	if got.off != 2 || got.text != join("alex", "@", "gmail.com") {
		t.Errorf("hit = %+v", got)
	}
}

func TestPhoneDetector(t *testing.T) {
	checkHits(t, phoneHits,
		[]string{
			join("(212) 5", "55-0223"),
			join("212-5", "55-0200"),
			join("212.867.", "5309"),
			join("+1 212 867 ", "5309"),
			join("1-800-", "123-4567"),
			join("+44 20", " 7946 0958"),
			join("+4915", "123456789"),
			"call " + join("415 ", "867 5309") + " today",
			join("+1 555 ", "0200"),
		},
		[]string{
			"+1 555 0100",
			"+1 555 0199",
			"(212) 555-0100",
			"212-555-0150",
			"1-800-555-0123",
			"5551234567",                    // bare digit run
			"123456789012",                  // identifier digits
			"2024-01-02",                    // date
			"id 20240102 120000",            // numeric id
			"+0.5 offset",                   // not a number
			"v1.2.3",                        // version
			"digest 1234 5678",              // too short
			"port 47319 and 47320",          // ports
			"time 2024-01-02T10:00:00+0530", // zone offset
			"sizes 100,000,000,000",         // grouped count
			"0.1.0.0",                       // dotted quad fragment
		})
}

func TestHomePathDetector(t *testing.T) {
	u, h, w := "/Us"+"ers/", "/ho"+"me/", `C:\Us`+`ers\`
	checkHits(t, homeHits,
		[]string{
			"cd " + u + "bob/src",
			h + "carol/.config",
			w + "dave\\AppData",
			"C:/Us" + "ers/erin/x",
			"\"" + u + "Zed\"",
		},
		[]string{
			u + "alex/Desktop",
			w + "name\\x",
			u + "$USER/x",
			"%USERPROFILE%\\x",
			"the Users table",
			"/usr/local/bin",
		})
}

func TestSecretDetector(t *testing.T) {
	checkHits(t, secretHits,
		[]string{
			"-----BEGIN " + "PRIVATE KEY-----",
			"-----BEGIN " + "RSA PRIVATE KEY-----",
			"-----BEGIN " + "OPENSSH PRIVATE KEY-----",
			join("token = gh", "p_", "abcdefghijklmnopqrstuvwxyz0123456789"),
			join("AK", "IA", "ABCDEFGHIJKLMNOP"),
			join("sk-", "ant-", "api03-abcdefghijklmnopqrstuvwx"),
			join("sk-", "abcdefghijklmnopqrstuvwxyz1234"),
			join("xo", "xb-", "1234567890-abcdefghijklmnop"),
			join("AI", "za", "SyA1234567890abcdefghijklmnopqrstuvwx"),
			join("ey", "Jhbgci0ijiuz.", "eyJzdWIiOiIxMjM0NTY3.", "abcdefghijklmnop"),
		},
		[]string{
			"-----BEGIN " + "CERTIFICATE-----",
			join("AK", "IA", "IOSFODNN7EXAMPLE"),
			join("gh", "p_", "FAKEabcdefghijklmnopqrstuvwxyz0123"),
			join("sk-", "ant-", "EXAMPLE-abcdefghijklmnopqrstuvwx"),
			"sk-short",
			"task-queue-worker-name-for-the-long-running-jobs",
			"risk-adjusted-return-calculation-for-portfolios",
			"BEGIN PRIVATE KEY without the dashes",
		})
}

func TestLanguageDetector(t *testing.T) {
	checkHits(t, languageHits,
		[]string{
			"comment " + string(rune(0x4e2d)) + string(rune(0x6587)),
			string(rune(0x0416)), // Cyrillic
		},
		[]string{
			"plain ascii",
			"em dash \u2014 curly \u201cquotes\u201d ellipsis \u2026",
			"accents caf\u00e9 na\u00efve \u00fc",
			"symbols \u2192 \u00a9 \u00b0",
		})
}

func TestLockFilesExemptFromEmailAndPhoneOnly(t *testing.T) {
	addr := join("dev", "@", "corp.io")
	tree := dirTree(t, map[string]string{
		"go.sum":        addr + "\n",
		"web/bun.lock":  addr + "\n",
		"docs/notes.md": addr + "\n",
	})
	fs, err := checkEmail(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(fs); len(got) != 1 || got[0] != "docs/notes.md" {
		t.Fatalf("findings = %v", fs)
	}
	// Other detectors still read lock files.
	tree = dirTree(t, map[string]string{"go.sum": join("-----BEGIN ", "PRIVATE KEY-----") + "\n"})
	if fs, _ := checkSecrets(tree); len(fs) != 1 {
		t.Fatalf("lock file escaped the secret scan: %v", fs)
	}
}

func TestDetectorOutputIsMasked(t *testing.T) {
	addr := join("alexander", "@", "gmail.com")
	tree := dirTree(t, map[string]string{"a.md": "mail " + addr + "\n"})
	fs, _ := checkEmail(tree)
	if len(fs) != 1 {
		t.Fatalf("findings = %v", fs)
	}
	f := fs[0]
	if f.Line != 1 || f.Col != 6 || f.Excerpt != "a"+strings.Repeat("*", len(addr)-1) {
		t.Errorf("finding = %+v", f)
	}
	if strings.Contains(f.String(), "alexander") || strings.Contains(f.String(), "gmail") {
		t.Errorf("output reveals the match: %s", f)
	}
}

func TestGitleaks_MissingToolSkipsOnlyDevelopment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tree := dirTree(t, map[string]string{"source.txt": "safe"})
	if _, err := checkGitleaks(tree); !IsSkipped(err) {
		t.Fatalf("development result = %v", err)
	}
	tree.Release = true
	if _, err := checkGitleaks(tree); err == nil || IsSkipped(err) {
		t.Fatalf("release result = %v", err)
	}
}

func TestGitleaks_SnapshotScopeAndReportsNeverEchoValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in uses a shell script")
	}
	bin := t.TempDir()
	writeFile(t, bin, "gitleaks", `#!/bin/sh
snapshot="$2"
if [ ! -f "$snapshot/source.txt" ] || [ -f "$snapshot/.env" ]; then exit 3; fi
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--report-path" ]; then shift; report="$1"; fi
 shift
done
printf '%s' '[{"RuleID":"fixture-key","File":"source.txt","StartLine":1,"Secret":"FAKE_DO_NOT_ECHO_THIS_VALUE"}]' > "$report"
printf '%s' 'FAKE_DO_NOT_ECHO_THIS_VALUE' >&2
exit 1
`)
	if err := os.Chmod(filepath.Join(bin, "gitleaks"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	tree := dirTree(t, map[string]string{"source.txt": "api_key = synthetic"})
	writeFile(t, tree.Root, ".env", "local ignored secret")
	found, err := checkGitleaks(tree)
	if err != nil || len(found) != 1 || found[0].Path != "source.txt" || found[0].Line != 1 {
		t.Fatalf("result = %+v, %v", found, err)
	}
	if strings.Contains(found[0].String(), "DO_NOT_ECHO") || found[0].Value != "" || found[0].Excerpt != "" {
		t.Fatal("scanner report exposed a value")
	}
	writeFile(t, bin, "gitleaks", "#!/bin/sh\nexit 2\n")
	if _, err := checkGitleaks(tree); err == nil || IsSkipped(err) {
		t.Fatalf("scanner execution failure = %v", err)
	}
}

func TestGitleaks_ActualScannerRefusesUnmarkedKeyAndInlineSuppression(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("actual gitleaks is not installed")
	}
	key := strings.Join([]string{"bd12ef34", "ab56cd78", "ef9012ab", "34cd56ef", "789012ab"}, "")
	tree := dirTree(t, map[string]string{"source.txt": "api_key = " + key + " # gitleaks:allow\n"})
	writeFile(t, tree.Root, ".env", "api_key = "+key)
	found, err := checkGitleaks(tree)
	if err != nil || len(found) != 1 || found[0].Path != "source.txt" {
		t.Fatalf("actual scan = %+v, %v", found, err)
	}
	writeFile(t, tree.Root, "source.txt", "api_key = FAKE_"+key+"\nLinkedIn preload, SuccessFactors and similar\n")
	if found, err := checkGitleaks(tree); err != nil || len(found) != 0 {
		t.Fatalf("explicit fixture/prose scan = %+v, %v", found, err)
	}
}
