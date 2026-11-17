package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
)

func decisionsByID(res *PageResult) map[string]FrameDecision {
	out := map[string]FrameDecision{}
	for _, d := range res.Decisions {
		out[d.FrameID] = d
	}
	return out
}

func TestFrames_OneDecisionPerFrame(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app?token=1", probeResult{})
	d.addFrame("ad", "https://ad.doubleclick.net/x", probeResult{})
	d.addFrame("blank", "about:blank", probeResult{})
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	if len(res.Decisions) != 3 || by["gh"].Outcome != AdmittedRegistry || by["ad"].Outcome != SkippedDenylist || by["blank"].Outcome != SkippedZeroControl {
		t.Fatalf("decisions %+v", res.Decisions)
	}
	if d.startCount("gh") != 1 || d.startCount("ad") != 0 || d.startCount("blank") != 0 {
		t.Fatalf("starts gh=%d ad=%d blank=%d", d.startCount("gh"), d.startCount("ad"), d.startCount("blank"))
	}
	// The denylisted frame is never probed or injected.
	if d.present["ad"] {
		t.Fatal("a denylisted frame was injected")
	}
}

func TestFrames_LastRequiredFieldInAFrameIsAdmittedByProbe(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("one", "https://forms.example.com/x", probeResult{Controls: 1, Required: 1})
	d.addFrame("many", "https://forms.example.com/y", probeResult{Controls: 2})
	d.addFrame("lone", "https://forms.example.com/z", probeResult{Controls: 1})
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	if by["one"].Outcome != AdmittedProbe || by["many"].Outcome != AdmittedProbe || by["lone"].Outcome != SkippedZeroControl {
		t.Fatalf("decisions %+v", res.Decisions)
	}
}

func TestFrames_ScriptBuiltFrameIsAdmittedOnceItHasControls(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("late", "about:blank", probeResult{})
	d.status = func(frame string, n int) (string, error) {
		if frame == "top" && n == 3 {
			d.probes["late"] = probeResult{Controls: 3} // the page script built its form
		}
		if frame == "top" && n < 6 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if decisionsByID(res)["late"].Outcome != AdmittedProbe || d.startCount("late") != 1 || len(res.Decisions) != 1 {
		t.Fatalf("decisions %+v starts=%d", res.Decisions, d.startCount("late"))
	}
}

func TestFrames_UnreachableFrameFailsAloneAndIsReported(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("bad", "https://forms.example.com/bad", probeResult{})
	d.addFrame("good", "https://forms.example.com/good", probeResult{Controls: 4})
	d.onEval = func(frame, expr string) error {
		if frame == "bad" && len(expr) > 10000 {
			return errors.New("injection refused")
		}
		return nil
	}
	var unreached []FrameDecision
	o := quickOptions()
	o.OnUnreached = func(dec FrameDecision) { unreached = append(unreached, dec) }
	res, err := newFakePage(t, d).Fill(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	if by["bad"].Outcome != FailedInject || !strings.Contains(by["bad"].Reason, "injection refused") || by["good"].Outcome != AdmittedProbe {
		t.Fatalf("decisions %+v", res.Decisions)
	}
	if len(unreached) != 1 || unreached[0].FrameID != "bad" || d.startCount("good") != 1 {
		t.Fatalf("unreached %+v", unreached)
	}
	if !strings.Contains(res.Notice, "could not be reached (1 frame(s))") {
		t.Fatalf("notice %q", res.Notice)
	}
	// The top frame's engine is given every decision once, and the frame that
	// could not be entered comes back as one learning event on the summary.
	if len(d.reports) != 1 || len(d.reports[0]) != 2 {
		t.Fatalf("reports %+v", d.reports)
	}
	evs := res.Top.Summary.Events
	if len(evs) != 1 || evs[0].Kind != "frame-unreachable" || evs[0].ControlKind != "iframe" || evs[0].Reason != FailedInject {
		t.Fatalf("events %+v", evs)
	}
}

func TestFrames_OnlySkipsProduceNoUnreachableMessage(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("ad", "https://ad.doubleclick.net/x", probeResult{})
	d.addFrame("blank", "about:blank", probeResult{})
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Notice, "reached") {
		t.Fatalf("notice %q", res.Notice)
	}
}

func TestFrames_ZeroFillHonesty(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("blank", "about:blank", probeResult{})
	d.addFrame("captcha", "https://ad.doubleclick.net/c", probeResult{})
	d.status = func(frame string, _ int) (string, error) {
		return `{"state":"done","runId":"RUN","stamp":"` + Stamp() + `","summary":{"rows":[],"counts":{}}}`, nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Notice != "Nothing was filled." {
		t.Fatalf("notice %q", res.Notice)
	}
	// A second pass over a complete form says so; an embedded form is claimed only when one was filled.
	r := &PageResult{Top: Status{Summary: &Summary{Counts: Counts{Prefilled: 5}}}}
	if got := r.message(); got != "The form is already filled." {
		t.Fatalf("message %q", got)
	}
	r.Decisions = []FrameDecision{{FrameID: "e", URL: "https://forms.example.com/x", Outcome: AdmittedProbe}}
	if got := r.message(); !strings.Contains(got, "1 embedded form frame(s)") {
		t.Fatalf("message %q", got)
	}
}

func TestFrames_SlowEmbedIsNotDispatchedAgain(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("slow", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.status = func(frame string, n int) (string, error) {
		if frame == "slow" && n < 40 || frame == "top" && n < 25 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if d.startCount("slow") != 1 {
		t.Fatalf("the frame was started %d times", d.startCount("slow"))
	}
	var fr *FrameResult
	for i := range res.Frames {
		fr = &res.Frames[i]
	}
	if fr == nil || fr.Status == nil || fr.Status.State != StateDone || len(res.Frames) != 1 {
		t.Fatalf("frames %+v", res.Frames)
	}
}

func TestFrames_RegistryFrameWithReplacedDocumentIsAdmittedAgain(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.addFrame("other", "https://forms.example.com/o", probeResult{Controls: 3})
	d.status = func(frame string, n int) (string, error) {
		if (frame == "gh" || frame == "other") && d.starts[frame] == 1 {
			return "", replacedErr()
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	by := decisionsByID(res)
	if d.startCount("gh") != 2 || by["gh"].Outcome != AdmittedRegistry {
		t.Fatalf("registry frame starts=%d decision=%+v", d.startCount("gh"), by["gh"])
	}
	if d.startCount("other") != 1 || by["other"].Outcome != FailedInject {
		t.Fatalf("probed frame starts=%d decision=%+v", d.startCount("other"), by["other"])
	}
}

func TestFrames_FrameFillsShareTheRunAndOnlyAdmittedFramesMayCallBindings(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.addFrame("blank", "about:blank", probeResult{})
	var results = map[string]string{}
	d.status = func(frame string, n int) (string, error) {
		if frame == "top" && n == 2 {
			rs := d.runIDs["top"]
			call := func(f string, run string) string {
				_, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: f, Origin: d.originOf(f)}, Payload: `{"runId":"` + run + `","questions":[]}`})
				if err != nil {
					return err.Error()
				}
				return "served"
			}
			results["top"] = call("top", rs)
			results["gh"] = call("gh", rs)
			results["blank"] = call("blank", rs)
			results["stale"] = call("top", "old")
		}
		if frame == "top" && n < 4 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	if results["top"] != "served" || results["blank"] != errNotAdmitted.Error() || results["stale"] != errStaleRun.Error() {
		t.Fatalf("results %v", results)
	}
	if d.runIDs["gh"] != d.runIDs["top"] {
		t.Fatalf("run ids differ: %q %q", d.runIDs["gh"], d.runIDs["top"])
	}
}

func TestBindings_RunEndsWithTheFill(t *testing.T) {
	d := newFakeDriver()
	p := newFakePage(t, d)
	if _, err := p.Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	_, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: "top", Origin: d.originOf("top")}, Payload: `{"runId":"` + d.runIDs["top"] + `"}`})
	if !errors.Is(err, errStaleRun) {
		t.Fatalf("a call after the fill: %v", err)
	}
}

func TestBindings_MatcherErrorIsAnEnvelopeNotARejection(t *testing.T) {
	d := newFakeDriver()
	m := &fakeMatcher{before: func(context.Context, MatchRequest) error { return errors.New("down") }}
	p, err := NewPage(d, Host{Matcher: m})
	if err != nil {
		t.Fatal(err)
	}
	rs := newRunState("r1")
	rs.admit("top", "https://jobs.example.com")
	_ = p.begin(rs)
	out, err := d.handlers[BindingMatch](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: "top", Origin: "https://jobs.example.com"}, Payload: `{"runId":"r1","questions":[]}`})
	if err != nil || out != `{"error":"down","ok":false}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
	out, _ = d.handlers[BindingResume](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: "top", Origin: "https://jobs.example.com"}, Payload: `{"runId":"r1","document":"resume"}`})
	if out != `{"ok":true,"result":null}` {
		t.Fatalf("resume without a provider: %s", out)
	}
}

func TestNativeUpload_AFailedSetSaysWhyOnTheRow(t *testing.T) {
	_, _, res := nativeUploadPage(t, t.TempDir(), errors.New("the locator found no file input"))
	r := res.Top.Summary.Rows[0]
	if r.Outcome != "skipped" || r.Reason != "needs-native-upload" || !strings.Contains(r.Detail, "the locator found no file input") || len(res.NativeUploads) != 0 {
		t.Fatalf("row=%+v uploads=%v", r, res.NativeUploads)
	}
}

func terminalFrames(res *PageResult) map[string]bool {
	out := map[string]bool{}
	for _, n := range res.Terminals {
		out[n.FrameID] = true
	}
	return out
}

func TestTerminal_ResultWaitsForEveryFramesNotice(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	// The engine settles its status before its notice reaches the host.
	d.terminalDelay["top"] = 80 * time.Millisecond
	d.terminalDelay["gh"] = 120 * time.Millisecond
	o := quickOptions()
	o.Timing.HungGrace = 5 * time.Second
	res, err := newFakePage(t, d).Fill(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := terminalFrames(res); len(res.Terminals) != 2 || !got["top"] || !got["gh"] {
		t.Fatalf("terminal notices %+v", res.Terminals)
	}
	if by := decisionsByID(res); by["gh"].Outcome != AdmittedRegistry || strings.Contains(res.Notice, "could not be reached") {
		t.Fatalf("decisions %+v notice %q", res.Decisions, res.Notice)
	}
}

func TestTerminal_MissingNoticeIsReportedLikeAnUnreachableFrame(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app", probeResult{})
	d.noTerminal["gh"] = true
	var unreached []FrameDecision
	o := quickOptions()
	o.OnUnreached = func(fd FrameDecision) { unreached = append(unreached, fd) }
	start := time.Now()
	res, err := newFakePage(t, d).Fill(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the wait for a notice is not bounded: %v", time.Since(start))
	}
	by := decisionsByID(res)
	if by["gh"].Outcome != FailedNoListener || !strings.Contains(by["gh"].Reason, "terminal notice") {
		t.Fatalf("decision %+v", by["gh"])
	}
	if len(unreached) != 1 || unreached[0].FrameID != "gh" || !strings.Contains(res.Notice, "could not be reached") {
		t.Fatalf("unreached %+v notice %q", unreached, res.Notice)
	}
	if got := terminalFrames(res); !got["top"] || got["gh"] {
		t.Fatalf("terminal notices %+v", res.Terminals)
	}
}

// After a fill the person may correct a box inside an embedded application
// form as well as on the page: a drain hands over the edits of every document
// that holds the engine, and says when none does any more (the person left the
// page), but not while a document that may hold it cannot be read.
func TestFrames_ADrainTakesTheEditsOfEveryDocumentAndSaysWhenNoneHoldsTheEngine(t *testing.T) {
	edit := func(q string) string {
		return mustJSON(t, []LearningEvent{{EventID: "e-" + q, Kind: "manual-fill", Question: q, ControlKind: "text", After: "Durham", TrustedInput: true}})
	}
	cases := []struct {
		name       string
		drains     map[string]string
		unreadable string
		want       []string
		wantErr    error
	}{
		{name: "the page and its embedded form", drains: map[string]string{"top": edit("City"), "gh": edit("Preferred office")}, want: []string{"City", "Preferred office"}},
		{name: "the person left the page", drains: map[string]string{}, wantErr: ErrNoEngine},
		{name: "the form's document is being replaced", drains: map[string]string{}, unreadable: "gh", wantErr: replacedErr()},
	}
	for _, c := range cases {
		d := newFakeDriver()
		d.addFrame("gh", "https://boards.greenhouse.io/embed/job_app?token=1", probeResult{})
		d.addFrame("ad", "https://ad.doubleclick.net/x", probeResult{})
		d.drains = c.drains
		d.onEval = func(frame, expr string) error {
			if frame == c.unreadable && strings.Contains(expr, "drainEvents") {
				return replacedErr()
			}
			return nil
		}
		got, err := newFakePage(t, d).DrainEvents(context.Background())
		var qs []string
		for _, ev := range got {
			qs = append(qs, ev.Question)
		}
		if strings.Join(qs, "|") != strings.Join(c.want, "|") || !errors.Is(err, c.wantErr) {
			t.Errorf("%s: events %v, error %v; want %v, %v", c.name, qs, err, c.want, c.wantErr)
		}
	}
}
