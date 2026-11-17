package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/browser"
)

// Host-side protocol rules the first tests did not pin: the stamp comparison,
// the windowed answer loop, run scoping of the bindings, and when a frame is
// first touched.

func TestProtocol_EngineWithAnOlderStampIsReplacedBeforeTheFill(t *testing.T) {
	d := newFakeDriver()
	d.present["top"] = true
	d.oldStamp = map[string]string{"top": "older-build"}
	var bundles, starts int
	startedAfterBundle := true
	d.onEval = func(_, expr string) error {
		switch {
		case len(expr) > 10000:
			bundles++
		case strings.Contains(expr, "__applysmithEngine.start("):
			starts++
			startedAfterBundle = startedAfterBundle && bundles == 1
		}
		return nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if bundles != 1 || starts != 1 || !startedAfterBundle || res.Top.State != StateDone {
		t.Fatalf("bundles=%d starts=%d startedAfterBundle=%v state=%s", bundles, starts, startedAfterBundle, res.Top.State)
	}
}

func answerReplyOf(handled int) string {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i := 0; i < handled; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"selector":"#q","outcome":"filled"}`)
	}
	b.WriteString(`],"handled":` + string(rune('0'+handled)) + `}`)
	return b.String()
}

func resolutions(n int) []Resolution {
	out := make([]Resolution, n)
	for i := range out {
		out[i] = Resolution{Selector: "#q", Outcome: "filled", Value: "x"}
	}
	return out
}

func TestHost_AnswerInWindows_EmptyPrefixStops(t *testing.T) {
	d := newFakeDriver()
	d.present["top"] = true
	sizes := []int{}
	d.onAnswer = func(_, expr string, n int) (string, error) {
		sizes = append(sizes, strings.Count(expr, `"selector"`))
		switch n {
		case 1:
			return answerReplyOf(2), nil
		case 2:
			return answerReplyOf(1), nil
		}
		return `{"results":[],"handled":0}`, nil
	}
	got, err := newFakePage(t, d).Answer(context.Background(), "", resolutions(6))
	if err != nil {
		t.Fatal(err)
	}
	// The host re-sends the remainder after each prefix and ends the loop on a
	// reply that covers none, so the unwritten tail is read as unwritten.
	if len(sizes) != 3 || sizes[0] != 6 || sizes[1] != 4 || sizes[2] != 3 || len(got) != 3 {
		t.Fatalf("calls %v results %d", sizes, len(got))
	}
}

func TestHost_Answer_LostReplyStopsTheRun(t *testing.T) {
	d := newFakeDriver()
	d.present["top"] = true
	d.onAnswer = func(string, string, int) (string, error) { return "", timeoutErr() }
	_, err := newFakePage(t, d).Answer(context.Background(), "run-7", resolutions(2))
	if err == nil {
		t.Fatal("a lost reply was not reported")
	}
	if len(d.stops) != 1 || !strings.Contains(d.stops[0], `"run-7"`) || !strings.Contains(d.stops[0], `"cancelled"`) {
		t.Fatalf("stops %v: the run that wrote is stopped before the failure is reported", d.stops)
	}
	// A replaced document has nothing left to stop, and no run id means no run to name.
	d2 := newFakeDriver()
	d2.present["top"] = true
	d2.onAnswer = func(string, string, int) (string, error) { return "", replacedErr() }
	if _, err := newFakePage(t, d2).Answer(context.Background(), "run-7", resolutions(1)); err == nil || len(d2.stops) != 0 {
		t.Fatalf("err=%v stops=%v", err, d2.stops)
	}
	d3 := newFakeDriver()
	d3.present["top"] = true
	d3.onAnswer = func(string, string, int) (string, error) { return "", timeoutErr() }
	_, _ = newFakePage(t, d3).Answer(context.Background(), "", resolutions(1))
	if len(d3.stops) != 0 {
		t.Fatalf("stops %v without a run id", d3.stops)
	}
}

func TestHost_Binding_StaleRunIDRejected(t *testing.T) {
	d := newFakeDriver()
	m, rs := &fakeMatcher{}, &fakeResumes{}
	p, err := NewPage(d, Host{Matcher: m, Resumes: rs})
	if err != nil {
		t.Fatal(err)
	}
	call := func(binding, payload string) (string, error) {
		return d.handlers[binding](context.Background(), browser.BindingCall{Frame: browser.Frame{ID: "top", Origin: "https://jobs.example.com"}, Payload: payload})
	}
	first := newRunState("run-1")
	first.admit("top", "https://jobs.example.com")
	if err := p.begin(first); err != nil {
		t.Fatal(err)
	}
	if out, err := call(BindingResume, `{"runId":"run-1","document":"resume"}`); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("the current run: %s %v", out, err)
	}
	// A new run on the same tab replaces the old one; the old run's late
	// requests die on every binding and reach neither the matcher nor the documents.
	p.end(first)
	second := newRunState("run-2")
	second.admit("top", "https://jobs.example.com")
	if err := p.begin(second); err != nil {
		t.Fatal(err)
	}
	served := len(rs.requests)
	for binding, payload := range map[string]string{
		BindingMatch:    `{"runId":"run-1","questions":[]}`,
		BindingResume:   `{"runId":"run-1","document":"resume"}`,
		BindingTerminal: `{"runId":"run-1","state":"done"}`,
	} {
		if out, err := call(binding, payload); !errors.Is(err, errStaleRun) || out != "" {
			t.Errorf("%s: out=%q err=%v", binding, out, err)
		}
	}
	if len(m.calls()) != 0 || len(rs.requests) != served {
		t.Fatalf("matcher %d resume %d->%d: a stale call reached a provider", len(m.calls()), served, len(rs.requests))
	}
	if len(second.terminals) != 0 {
		t.Fatalf("terminals %+v: a notice for another run was recorded", second.terminals)
	}
	if _, err := call(BindingMatch, `{"questions":[]}`); !errors.Is(err, errStaleRun) {
		t.Fatalf("a call without a run id: %v", err)
	}
}

func TestFrames_NoProbeBeforeAFillStarts(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("embed", "https://forms.example.com/x", probeResult{Controls: 3})
	var exprs []string
	d.onEval = func(frame, expr string) error {
		switch {
		case strings.Contains(expr, "__applysmithEngine.probe()"):
			exprs = append(exprs, "probe:"+frame)
		case strings.Contains(expr, "__applysmithEngine.start("):
			exprs = append(exprs, "start:"+frame)
		default:
			exprs = append(exprs, "other")
		}
		return nil
	}
	p := newFakePage(t, d)
	if len(exprs) != 0 {
		t.Fatalf("a page that has not been asked to fill touched the document: %v", exprs)
	}
	if _, err := p.Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	probe, start := -1, -1
	for i, e := range exprs {
		switch {
		case e == "probe:embed" && probe < 0:
			probe = i
		case e == "start:top" && start < 0:
			start = i
		}
	}
	if probe < 0 || start < 0 || probe < start {
		t.Fatalf("the probe (%d) did not follow the fill's start (%d): %v", probe, start, exprs)
	}
}

func TestHost_Start_LegalDeclarationsReadPerPage(t *testing.T) {
	d := newFakeDriver()
	p := newFakePage(t, d)
	for _, accept := range []bool{true, false, true} {
		o := quickOptions()
		o.AcceptLegalDeclarations = accept
		if _, err := p.Fill(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	var got []bool
	for _, a := range d.startArg["top"] {
		got = append(got, a.AcceptLegalDeclarations)
	}
	// The setting travels in each start's arguments, so a toggle applies to the
	// next page; a fill that does not carry it starts with the setting off.
	if len(got) != 4 || !got[0] || got[1] || !got[2] || got[3] {
		t.Fatalf("start arguments %v", got)
	}
}

func TestHost_Start_TheControlsTheUserChangedAreNamedToTheirOwnDocument(t *testing.T) {
	d := newFakeDriver()
	d.addFrame("embed", "https://forms.example.com/x", probeResult{Controls: 3})
	o := quickOptions()
	o.Edited = map[string][]string{"": {`[id="phone"]`}, "embed": {`[id="city"]`}}
	if _, err := newFakePage(t, d).Fill(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// A selector names a control of one document: the embedded form's own
	// phone box is not the one the user changed in the page, and the page's
	// City is not the embedded form's.
	top, embed := d.startArg["top"], d.startArg["embed"]
	if len(top) != 1 || strings.Join(top[0].Edited, ",") != `[id="phone"]` {
		t.Fatalf("top start arguments %+v", top)
	}
	if len(embed) != 1 || strings.Join(embed[0].Edited, ",") != `[id="city"]` {
		t.Fatalf("embedded start arguments %+v", embed)
	}
}

// A second Page on a document another Page already served finds a helper of
// the current build that listens on the first Page's nonce: it installs its
// own, where the stamp alone would have left the engine talking to nobody.
func TestHost_HelperIsInstalledAgainUnderThisPagesNonce(t *testing.T) {
	d := newFakeDriver()
	o := quickOptions()
	o.MainWorld = true
	first, second := newFakePage(t, d), newFakePage(t, d)
	for _, p := range []*Page{first, first, second} {
		if _, err := p.Fill(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if d.helperInstalls["top"] != 2 {
		t.Fatalf("the helper was installed %d times, want once per page", d.helperInstalls["top"])
	}
	if got, want := d.usedHelper["top"], []string{first.nonce, first.nonce, second.nonce}; !slices.Equal(got, want) {
		t.Fatalf("the engine was told %v, want %v", got, want)
	}
	if d.helperNonce["top"] != second.nonce {
		t.Fatalf("the helper listens on %q, want the second page's nonce", d.helperNonce["top"])
	}
}
