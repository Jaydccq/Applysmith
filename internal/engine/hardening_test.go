package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
)

// An embed that moved to another origin between its admission and the start
// must not be handed the applicant data, whatever admitted it.
func TestFrames_FrameMovedToAnotherOriginBeforeStartIsNotStarted(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.addFrame("probed", "https://forms.example.com/apply", probeResult{Controls: 4})
	d.onEval = func(frame, expr string) error {
		if (frame == "gh" || frame == "probed") && strings.Contains(expr, "__applysmithEngine.start(") {
			d.origins[frame] = "https://sso.example.org" // the embed redirected itself in-frame
		}
		return nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	for _, id := range []string{"gh", "probed"} {
		if d.startCount(id) != 0 {
			t.Errorf("%s: the engine started on a document at another origin", id)
		}
		if by[id].Outcome != FailedInject || !strings.Contains(by[id].Reason, reasonOriginChanged) {
			t.Errorf("%s: decision %+v, want a refusal with reason %s", id, by[id], reasonOriginChanged)
		}
	}
	if res.Top.State != StateDone {
		t.Fatalf("top %+v", res.Top)
	}
}

func TestFrames_StartGuardIsPartOfTheStartEvaluation(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	var exprs []string
	d.onEval = func(frame, expr string) error {
		if frame == "gh" && strings.Contains(expr, "__applysmithEngine.start(") {
			exprs = append(exprs, expr)
		}
		return nil
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	if len(exprs) != 1 || !strings.HasPrefix(exprs[0], `location.origin!=="https://boards.greenhouse.io"?`) {
		t.Fatalf("the sub-frame start was not guarded by its admitted origin in one evaluation: %q", exprs)
	}
}

func TestFrames_RegistryFrameReplacedOntoAnotherSiteIsClassifiedAgain(t *testing.T) {
	for name, c := range map[string]struct {
		url     string
		probe   probeResult
		starts  int
		outcome string
	}{
		"denylisted site":        {"https://ad.doubleclick.net/x", probeResult{}, 1, SkippedDenylist},
		"page without controls":  {"https://login.example.org/sso", probeResult{}, 1, SkippedZeroControl},
		"sign-in form":           {"https://login.example.org/sso", probeResult{Controls: 3}, 1, FailedInject},
		"another registry embed": {"https://job-boards.greenhouse.io/embed/next", probeResult{}, 2, AdmittedRegistry},
	} {
		t.Run(name, func(t *testing.T) {
			d := newFakeDriver()
			d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
			d.status = func(frame string, _ int) (string, error) {
				if frame == "gh" && d.starts[frame] == 1 {
					d.urls["gh"], d.probes["gh"] = c.url, c.probe
					return "", replacedErr()
				}
				return doneStatus("RUN"), nil
			}
			res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
			if err != nil {
				t.Fatal(err)
			}
			by := decisionsByID(res)["gh"]
			if d.startCount("gh") != c.starts || by.Outcome != c.outcome {
				t.Fatalf("starts=%d decision=%+v, want %d starts and %s", d.startCount("gh"), by, c.starts, c.outcome)
			}
		})
	}
}

func TestBindings_CallFromAFrameAtAnotherOriginThanAdmittedIsRejected(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	got := map[string]string{}
	d.status = func(frame string, n int) (string, error) {
		if frame == "top" && n == 3 {
			call := func(origin string) string {
				_, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{
					Frame: browser.Frame{ID: "gh", Origin: origin}, Payload: `{"runId":"` + d.runIDs["top"] + `","questions":[]}`})
				if err != nil {
					return err.Error()
				}
				return "served"
			}
			got["same"] = call("https://boards.greenhouse.io")
			got["moved"] = call("https://sso.example.org")
		}
		if frame == "top" && n < 5 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	if got["same"] != "served" || got["moved"] != errOriginChanged.Error() {
		t.Fatalf("calls %v", got)
	}
}

func TestProtocol_PageDeadlineIsCountedFromTheStartOfTheFill(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) {
		time.Sleep(40 * time.Millisecond)
		return "", replacedErr()
	}
	o := quickOptions()
	o.Timing.PageDeadline = 5 * time.Second
	_, err := newFakePage(t, d).Fill(context.Background(), o)
	wantCode(t, err, CodeNavigationLimit)
	args := d.startArg["top"]
	if len(args) != 1+MaxReinjections {
		t.Fatalf("%d starts", len(args))
	}
	first, last := args[0].PageDeadlineMs, args[len(args)-1].PageDeadlineMs
	if first-last < int64(MaxReinjections)*30 {
		t.Fatalf("deadline handed to the engine went from %d ms to %d ms: each reinjection got a fresh page deadline", first, last)
	}
}

func TestProtocol_StatusOfAnotherRunIsNotThisFillsResult(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return doneStatus("some-other-run"), nil }
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodePollFailed)
}

const nativeRow = `{"selector":"[id=\"cv\"]","label":"Resume","section":[],"lane":"profile","kind":"file","required":false,"outcome":"skipped","review":false,"reason":"needs-native-upload","fileName":"resume-alex-example.pdf"}`

// nativeUploadPage fills a page whose only row is a file the browser must set
// (setting it fails with setErr when that is not nil), and returns the page,
// its driver and the result.
func nativeUploadPage(t *testing.T, stage string, setErr error) (*Page, *fakeDriver, *PageResult) {
	t.Helper()
	d := newFakeDriver()
	d.setErr = setErr
	d.status = func(string, int) (string, error) {
		return `{"state":"done","runId":"RUN","stamp":"` + Stamp() + `","summary":{"rows":[` + nativeRow + `],"counts":{"skipped":1}}}`, nil
	}
	p, err := NewPage(d, Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}, StageDir: stage})
	if err != nil {
		t.Fatal(err)
	}
	d.onEval = func(_, expr string) error {
		if strings.Contains(expr, "e.status():null") {
			if rs := p.current(); rs != nil {
				rs.mu.Lock()
				rs.served["top"] = map[string]ResumePayload{"resume-alex-example.pdf": {FileName: "resume-alex-example.pdf", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 alex"))}}
				rs.mu.Unlock()
			}
		}
		return nil
	}
	res, err := p.Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	return p, d, res
}

func TestNativeUpload_StagedFileOutlivesTheFillAndGoesWithTheTab(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "uploads")
	_, d, res := nativeUploadPage(t, stage, nil)
	r := res.Top.Summary.Rows[0]
	if len(d.setPaths) != 1 || len(res.NativeUploads) != 1 || len(d.setFiles) != 1 || !strings.Contains(d.setFiles[0], `__applysmithEngine.locate("[id=\"cv\"]")`) ||
		r.Outcome != "filled" || r.Reason != "" || res.Top.Summary.Counts.Skipped != 0 || res.Top.Summary.Counts.Filled != 1 {
		t.Fatalf("setPaths=%v setFiles=%v uploads=%v row=%+v counts=%+v", d.setPaths, d.setFiles, res.NativeUploads, r, res.Top.Summary.Counts)
	}
	path := d.setPaths[0][0]
	// The browser reads the file when the page uses it, after the fill returned.
	if b, err := os.ReadFile(path); err != nil || string(b) != "%PDF-1.4 alex" {
		t.Fatalf("staged file after the fill: %q %v", b, err)
	}
	if rel, err := filepath.Rel(stage, path); err != nil || strings.HasPrefix(rel, "..") || len(strings.Split(rel, string(filepath.Separator))) != 3 {
		t.Fatalf("staged outside <StageDir>/<run>/<n>/: %s", path)
	}
	for _, dir := range []string{stage, filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want mode 0700", dir, fi, err)
		}
	}
	d.closeTab()
	if _, err := os.Stat(filepath.Dir(filepath.Dir(path))); !os.IsNotExist(err) {
		t.Fatalf("the run directory outlived the tab: %v", err)
	}
	d.closeTab() // a second close is harmless
}

func TestNativeUpload_PageCloseRemovesWhatItStaged(t *testing.T) {
	stage := t.TempDir()
	p, d, _ := nativeUploadPage(t, stage, nil)
	dir := filepath.Dir(filepath.Dir(d.setPaths[0][0]))
	p.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close left %s: %v", dir, err)
	}
	p.Close()
}

func TestNativeUpload_WithoutAStagingDirectoryTheRowStaysSkipped(t *testing.T) {
	_, d, res := nativeUploadPage(t, "", nil)
	if len(d.setPaths) != 0 || len(res.NativeUploads) != 0 || res.Top.Summary.Rows[0].Outcome != "skipped" {
		t.Fatalf("setPaths=%v uploads=%v row=%+v", d.setPaths, res.NativeUploads, res.Top.Summary.Rows[0])
	}
}

func TestNativeUpload_NewPageSweepsOnlyOldAbandonedRuns(t *testing.T) {
	stage := t.TempDir()
	old, fresh := filepath.Join(stage, "old-run"), filepath.Join(stage, "fresh-run")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	aged := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPage(newFakeDriver(), Host{StageDir: stage}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("an abandoned run directory survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a recent run directory was removed: %v", err)
	}
}

// The tab itself may move between attempts (SSO, a vendor portal): the first
// attempt pins the top document's origin and a later one never starts elsewhere.
func TestProtocol_TabThatMovedToAnotherOriginBetweenAttemptsIsNotStarted(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) {
		d.origins["top"] = "https://sso.example.org"
		return "", replacedErr()
	}
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeTopOriginChanged)
	if d.startCount("top") != 1 {
		t.Fatalf("the engine started %d times; the second start must have been refused", d.startCount("top"))
	}
}

func TestProtocol_TopOriginIsTheOriginNotTheSite(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) {
		d.origins["top"] = "https://careers.example.com" // same registrable domain, another host
		return "", replacedErr()
	}
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeTopOriginChanged)
}

func TestProtocol_TopWithoutAnOriginToPinIsNotFilled(t *testing.T) {
	d := newFakeDriver()
	d.origins["top"] = "null"
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeInjectFailed)
	if d.startCount("top") != 0 {
		t.Fatal("an opaque-origin page was started")
	}
}

func TestBindings_CallFromTopAtAnotherOriginIsRejected(t *testing.T) {
	d := newFakeDriver()
	got := map[string]string{}
	d.status = func(frame string, n int) (string, error) {
		if n == 2 {
			call := func(origin string) string {
				_, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{
					Frame: browser.Frame{ID: "top", Origin: origin, IsTop: true}, Payload: `{"runId":"` + d.runIDs["top"] + `","questions":[]}`})
				if err != nil {
					return err.Error()
				}
				return "served"
			}
			got["same"] = call("https://jobs.example.com")
			got["moved"] = call("https://sso.example.org")
		}
		if n < 4 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	if got["same"] != "served" || got["moved"] != errOriginChanged.Error() {
		t.Fatalf("calls %v", got)
	}
}

func TestFrames_FrameWithoutAnOriginToPinIsNeverAdmitted(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("sandboxed", "https://forms.example.com/a", probeResult{Controls: 4})
	d.addFrame("registry", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.addFrame("garbled", "https://forms.example.com/b", probeResult{Controls: 4})
	d.origins["sandboxed"], d.origins["registry"] = "null", "null"
	d.badLoc = map[string]bool{"garbled": true}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	for _, id := range []string{"sandboxed", "registry"} {
		if by[id].Outcome != FailedInject || d.startCount(id) != 0 {
			t.Errorf("%s: decision %+v starts=%d", id, by[id], d.startCount(id))
		}
	}
	if by["garbled"].Outcome != FailedProbe || d.startCount("garbled") != 0 {
		t.Errorf("garbled: decision %+v starts=%d", by["garbled"], d.startCount("garbled"))
	}
}

func TestBindings_AnOpaqueOrMissingPinNeverMatches(t *testing.T) {
	d := newFakeDriver()
	p, err := NewPage(d, Host{Matcher: &fakeMatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	rs := newRunState("r1")
	rs.admit("opaque", "null")
	rs.admit("unpinned", "")
	if err := p.begin(rs); err != nil {
		t.Fatal(err)
	}
	for frame, origin := range map[string]string{"opaque": "null", "unpinned": ""} {
		_, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: frame, Origin: origin}, Payload: `{"runId":"r1","questions":[]}`})
		if !errors.Is(err, errOriginChanged) {
			t.Errorf("%s: %v", frame, err)
		}
	}
}

func TestStage_NamesAreNormalizedAndNeverCollide(t *testing.T) {
	stage := t.TempDir()
	p, err := NewPage(newFakeDriver(), Host{StageDir: stage})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, name := range []string{"resume.pdf", "resume.pdf", "", ".", "..", "a/b.pdf", `..\..\evil`, "../../up.pdf", "x\x00y.pdf"} {
		path, err := p.stage("run1", name, []byte(fmt.Sprint(i)))
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil || strings.HasPrefix(rel, "..") || len(strings.Split(rel, string(filepath.Separator))) != 3 {
			t.Errorf("%q staged at %s", name, path)
		}
		if b, _ := os.ReadFile(path); string(b) != fmt.Sprint(i) {
			t.Errorf("%q: contents %q, want %d (another document overwrote it?)", name, b, i)
		}
		if base := filepath.Base(path); base == "." || base == ".." || strings.ContainsAny(base, "/\\\x00") {
			t.Errorf("%q became %q", name, base)
		}
		if seen[path] {
			t.Errorf("%q collided at %s", name, path)
		}
		seen[path] = true
	}
}

func TestStage_NothingIsStagedAfterCloseEvenWhenTheyRace(t *testing.T) {
	stage := t.TempDir()
	p, err := NewPage(newFakeDriver(), Host{StageDir: stage})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				_, _ = p.stage("run-race", "resume.pdf", []byte("x"))
			}
		}()
	}
	time.Sleep(2 * time.Millisecond)
	p.Close()
	wg.Wait()
	if _, err := os.Stat(filepath.Join(stage, "run-race")); !os.IsNotExist(err) {
		t.Fatalf("a run directory survived Close: %v", err)
	}
	if _, err := p.stage("run-race", "resume.pdf", []byte("x")); err == nil {
		t.Fatal("a closed page staged a file")
	}
}

func TestStage_SweepSparesTheRunsOfLivePagesWhateverTheirAge(t *testing.T) {
	stage := t.TempDir()
	kept, err := NewPage(newFakeDriver(), Host{StageDir: stage})
	if err != nil {
		t.Fatal(err)
	}
	path, err := kept.stage("kept-run", "resume.pdf", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	run := filepath.Dir(filepath.Dir(path))
	aged := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(run, aged, aged); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPage(newFakeDriver(), Host{StageDir: stage}); err != nil { // another run's page sweeps
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the sweep removed a live page's file: %v", err)
	}
	kept.Close()
	if _, err := os.Stat(run); !os.IsNotExist(err) {
		t.Fatalf("Close left the run: %v", err)
	}
}
