//go:build browser

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
)

func fastTiming() Timing {
	return Timing{Poll: 200 * time.Millisecond, PollTimeout: 10 * time.Second, PageDeadline: 60 * time.Second, DocumentWait: 2 * time.Second}
}

func fillOptions() FillOptions {
	return FillOptions{Applicant: fixtureApplicant, Posting: PostingContext{Company: "Example Corp", Title: "Engineer"}, Timing: fastTiming()}
}

// mainEval evaluates in the page's main world of a frame, as a page script would.
func (f *tabFixture) mainEval(frame, expr string, out any) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := f.hooks.Eval(ctx, frame, "", expr)
	if err != nil {
		f.t.Fatalf("main-world eval %.60q: %v", expr, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		f.t.Fatalf("decode %s: %v", raw, err)
	}
}

func (f *tabFixture) frames() map[string]browser.Frame {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	list, err := f.hooks.Frames(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]browser.Frame{}
	for _, fr := range list {
		switch {
		case fr.IsTop:
			out["top"] = fr
		case strings.Contains(fr.URL, "/embed-same"):
			out["same"] = fr
		case strings.Contains(fr.URL, "/embed-cross"):
			out["cross"] = fr
		case strings.Contains(fr.URL, "/embed-reg"):
			out["reg"] = fr
		default:
			out["empty"] = fr
		}
	}
	return out
}

// Task 1.7: a synthetic multi-frame form filled end to end.
func TestFill_MultiFrameForm(t *testing.T) {
	f := openFixture(t)
	matcher := &fakeMatcher{}
	resumes := &fakeResumes{}
	page := f.page(Host{Matcher: matcher, Resumes: resumes})
	f.navigate(f.fx.A("/apply?case=multi&reloads=1"))

	var (
		logMu sync.Mutex
		logs  []string
	)
	var unreached []FrameDecision
	o := fillOptions()
	// Frames log from their own goroutines; the line is formatted, so a value in the args would show.
	o.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	o.OnUnreached = func(d FrameDecision) { unreached = append(unreached, d) }
	res, err := page.Fill(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}

	// The self-navigating step: the document was replaced once and the fill completed on the final one.
	if res.Reinjections != 1 || f.fx.hitCount("multi") != 2 {
		t.Fatalf("reinjections=%d loads=%d", res.Reinjections, f.fx.hitCount("multi"))
	}
	if res.Top.State != StateDone || res.Top.Summary == nil {
		t.Fatalf("top state %+v", res.Top)
	}
	if len(unreached) != 0 {
		t.Fatalf("unreached frames %v", unreached)
	}

	// Frame decisions: same-origin and cross-origin frames admitted by probe, the empty one skipped.
	fr := f.frames()
	if len(fr) != 4 || fr["cross"].IsTop || fr["cross"].Origin != "http://b.sitetwo.test:"+strings.Split(f.fx.B(""), ":")[2] {
		t.Fatalf("frames %+v", fr)
	}
	byID := map[string]FrameDecision{}
	for _, d := range res.Decisions {
		byID[d.FrameID] = d
	}
	if len(res.Decisions) != 3 || byID[fr["same"].ID].Outcome != AdmittedProbe || byID[fr["cross"].ID].Outcome != AdmittedProbe || byID[fr["empty"].ID].Outcome != SkippedZeroControl {
		t.Fatalf("decisions %+v", res.Decisions)
	}

	dumpRows(t, res)
	// Read-back of every field, in the page's own world.
	type check struct{ frame, expr, want string }
	checks := []check{
		{"top", `document.getElementById('fn').value`, "Alex"},
		{"top", `document.getElementById('ln').value`, "Example"},
		{"top", `document.getElementById('em').value`, "alex@example.com"},
		{"top", `document.querySelector('input[name=wa1]:checked')?.value ?? ''`, "Yes"},
		{"top", `document.getElementById('src').value`, "Job board"},
		{"top", `[...document.querySelectorAll('input[name="lang[]"]:checked')].map(e=>e.value).join(',')`, "go,rs"},
		{"top", `document.getElementById('yrs').value`, "6"},
		{"top", `document.getElementById('cv').files[0]?.name ?? ''`, "resume-alex-example.pdf"},
		{"top", `String(!!window.__trustedChange)`, "true"},
		{"same", `document.getElementById('city').value`, "Durham"},
		{"same", `document.getElementById('zip').value`, "27701"},
		{"same", `document.getElementById('ed1').value`, "Bachelor's degree"},
		{"cross", `document.getElementById('ph').value`, "+1 555 0100"},
		{"cross", `document.querySelector('input[name=sp1]:checked')?.value ?? ''`, "No"},
		{"cross", `document.getElementById('li').value`, "https://www.linkedin.com/in/alex-example"},
	}
	for _, c := range checks {
		var got string
		f.mainEval(fr[c.frame].ID, c.expr, &got)
		if got != c.want {
			t.Errorf("%s %s = %q, want %q", c.frame, c.expr, got, c.want)
		}
	}

	// The native file set replaced the engine's refused assignment.
	if len(res.NativeUploads) != 1 || !strings.HasSuffix(res.NativeUploads[0], `[id="cv"]`) {
		t.Fatalf("native uploads %v", res.NativeUploads)
	}
	var cvRow *Row
	for i, r := range res.Top.Summary.Rows {
		if r.Kind == "file" {
			cvRow = &res.Top.Summary.Rows[i]
		}
	}
	if cvRow == nil || cvRow.Outcome != "filled" || cvRow.FileName != "resume-alex-example.pdf" {
		t.Fatalf("file row %+v", cvRow)
	}

	// The run summary: rows carry the read-back values.
	rows := map[string]Row{}
	for _, r := range res.Top.Summary.Rows {
		rows[r.Label] = r
	}
	for label, want := range map[string]string{"First name": "Alex", "Email": "alex@example.com", "Years of experience with Go": "6", "How did you hear about us?": "Job board"} {
		if r := rows[label]; r.Outcome != "filled" || r.Value != want {
			t.Errorf("row %q = %+v, want filled %q", label, r, want)
		}
	}
	if res.Top.Summary.Counts.Failed != 0 || res.Top.Summary.MatchUnavailable {
		t.Errorf("counts %+v", res.Top.Summary.Counts)
	}
	for _, id := range []string{fr["same"].ID, fr["cross"].ID} {
		var st *Status
		for _, r := range res.Frames {
			if r.Frame.ID == id {
				st = r.Status
			}
		}
		if st == nil || st.State != StateDone || st.Summary.Counts.Filled < 2 {
			t.Errorf("frame %s status %+v", id, st)
		}
	}

	// Stamps: every engine reports the bundle's stamp.
	if res.Stamp != Stamp() || res.Top.Stamp != Stamp() {
		t.Errorf("stamps %q %q want %q", res.Stamp, res.Top.Stamp, Stamp())
	}
	for _, name := range []string{"top", "same", "cross"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		raw, err := f.hooks.Eval(ctx, fr[name].ID, World, `__applysmithEngine.status().stamp`)
		cancel()
		if err != nil || string(raw) != `"`+Stamp()+`"` {
			t.Errorf("%s engine stamp %s %v", name, raw, err)
		}
	}

	// Every match request names a run: the replaced document asked under its own attempt's, the final one under the
	// result's. The terminal binding fired once per frame.
	current := 0
	for _, req := range matcher.calls() {
		if req.RunID == "" {
			t.Errorf("match request without a run id")
		}
		if req.RunID == res.RunID {
			current++
		}
	}
	if current == 0 {
		t.Errorf("no match request under the result's run %q", res.RunID)
	}
	if len(res.Terminals) != 3 {
		t.Errorf("terminal notices %+v", res.Terminals)
	}
	if !strings.Contains(res.Notice, "Filled") || strings.Contains(res.Notice, "could not be reached") {
		t.Errorf("notice %q", res.Notice)
	}

	// Nothing leaked into any page's main world.
	for name, frame := range fr {
		if name == "empty" {
			continue
		}
		var leaks []string
		f.mainEval(frame.ID, `(()=>{const bad=/applysmith|__aj|applicant/i;const out=[];
for(const n of Object.getOwnPropertyNames(globalThis))if(bad.test(n))out.push('global:'+n);
for(const n of Object.getOwnPropertyNames(document))if(bad.test(n))out.push('expando:'+n);
if(typeof __ajMatch!=='undefined')out.push('binding');
if(window.name)out.push('window.name');
for(const s of [localStorage,sessionStorage])for(let i=0;i<s.length;i++)out.push('storage:'+s.key(i));
if(document.cookie)out.push('cookie');
for(const el of document.querySelectorAll('*'))for(const a of el.attributes)
  if(bad.test(a.name)||(!/^(INPUT|TEXTAREA|SELECT|OPTION)$/.test(el.tagName)&&a.value.includes('alex@example.com')))out.push('attr:'+el.tagName+'.'+a.name);
return out})()`, &leaks)
		if len(leaks) != 0 {
			t.Errorf("%s main world leaks %v", name, leaks)
		}
	}
	logMu.Lock()
	joined := strings.Join(logs, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, "frame-decision") || strings.Contains(joined, "alex@example.com") {
		t.Errorf("host log %q", joined)
	}
}

func dumpRows(t *testing.T, res *PageResult) {
	for _, fr := range res.Frames {
		if fr.Status != nil && fr.Status.Summary != nil {
			for _, r := range fr.Status.Summary.Rows {
				t.Logf("ROW %s | %s %s %q %s", r.Label, r.Outcome, r.Reason, r.Value, r.Lane)
			}
		}
	}
}
