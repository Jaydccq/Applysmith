//go:build browser

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fillTop(t *testing.T, f *tabFixture, p *Page, o FillOptions) (*PageResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return p.Fill(ctx, o)
}

func topValue(f *tabFixture, id string) string {
	var v string
	f.mainEval(f.frames()["top"].ID, `document.getElementById('`+id+`').value`, &v)
	return v
}

func TestHost_SelfNavigatingPageReloadsTwice(t *testing.T) {
	f := openFixture(t)
	p := f.page(Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=nav2&reloads=2&frames=none"))
	res, err := fillTop(t, f, p, fillOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Reinjections != 2 || f.fx.hitCount("nav2") != 3 || res.Top.State != StateDone {
		t.Fatalf("reinjections=%d loads=%d state=%s", res.Reinjections, f.fx.hitCount("nav2"), res.Top.State)
	}
	if topValue(f, "em") != "alex@example.com" || topValue(f, "yrs") != "6" {
		t.Fatal("the final document is not filled")
	}
}

func TestHost_NavigationLoopFailsWithNavigationLimit(t *testing.T) {
	f := openFixture(t)
	p := f.page(Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=loop&reloads=99&frames=none"))
	_, err := fillTop(t, f, p, fillOptions())
	if c, _ := CodeOf(err); c != CodeNavigationLimit {
		t.Fatalf("error %v", err)
	}
	if n := f.fx.hitCount("loop"); n != 1+MaxReinjections+1 {
		t.Fatalf("the page loaded %d times", n)
	}
}

func TestHost_ClosedTabIsNotANavigation(t *testing.T) {
	f := openFixture(t)
	m := &fakeMatcher{}
	m.before = func(ctx context.Context, _ MatchRequest) error {
		_ = f.lane.CloseTab(context.Background(), f.tab)
		<-ctx.Done()
		return ctx.Err()
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=closed&frames=none"))
	_, err := fillTop(t, f, p, fillOptions())
	if c, _ := CodeOf(err); c != CodeTabClosed {
		t.Fatalf("error %v", err)
	}
	if f.fx.hitCount("closed") != 1 || len(m.calls()) != 1 {
		t.Fatalf("loads=%d match calls=%d: the fill restarted", f.fx.hitCount("closed"), len(m.calls()))
	}
	if !f.hooks.Released() {
		t.Fatal("hooks outlived the tab")
	}
}

// countingDriver counts evaluations that timed out.
type countingDriver struct {
	Driver
	timeouts atomic.Int32
}

func (d *countingDriver) Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error) {
	raw, err := d.Driver.Eval(ctx, frame, world, expr)
	if isTimeout(err) {
		d.timeouts.Add(1)
	}
	return raw, err
}

func TestHost_StarvedPollIsRetriedAgainstTheSameRun(t *testing.T) {
	f := openFixture(t)
	cd := &countingDriver{Driver: f.hooks}
	p, err := NewPage(cd, Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}})
	if err != nil {
		t.Fatal(err)
	}
	f.navigate(f.fx.A("/apply?case=starve&block=2500&frames=none"))
	o := fillOptions()
	o.Timing.PollTimeout = 600 * time.Millisecond
	res, err := fillTop(t, f, p, o)
	if err != nil {
		t.Fatal(err)
	}
	if cd.timeouts.Load() == 0 {
		t.Fatal("no poll timed out; the page was not starved")
	}
	if res.Top.State != StateDone || res.Reinjections != 0 || topValue(f, "em") != "alex@example.com" {
		t.Fatalf("state=%s reinjections=%d", res.Top.State, res.Reinjections)
	}
}

func TestHost_UnadmittedFrameAndStaleRunBindingCallsAreRejected(t *testing.T) {
	f := openFixture(t)
	type outcome struct{ stale, unadmitted string }
	var got outcome
	m := &fakeMatcher{}
	m.before = func(ctx context.Context, req MatchRequest) error {
		if req.Host == "" || got.stale != "" {
			return nil
		}
		frames := f.frames()
		call := func(frame, runID string) string {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			raw, err := f.hooks.Eval(c, frame, World, `__ajMatch(JSON.stringify({runId:`+jsString(runID)+`,host:"x",posting:{},questions:[]})).then(()=>"resolved",e=>e.message)`)
			if err != nil {
				return "eval error: " + err.Error()
			}
			return string(raw)
		}
		got.stale = call(frames["top"].ID, "an-old-run")
		got.unadmitted = call(frames["empty"].ID, req.RunID)
		return nil
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=reject"))
	if _, err := fillTop(t, f, p, fillOptions()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.stale, "not current") || !strings.Contains(got.unadmitted, "not admitted") {
		t.Fatalf("stale=%q unadmitted=%q", got.stale, got.unadmitted)
	}
	for _, r := range m.calls() {
		if len(r.Questions) == 0 {
			t.Fatal("a rejected call reached the matcher")
		}
	}
}

func TestHost_DeadlineStopsTheFillWithAPartialSummary(t *testing.T) {
	f := openFixture(t)
	m := &fakeMatcher{}
	m.before = func(ctx context.Context, _ MatchRequest) error {
		select {
		case <-ctx.Done():
		case <-time.After(6 * time.Second):
		}
		return errors.New("matcher gave up")
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=deadline&frames=none"))
	o := fillOptions()
	o.Timing.PageDeadline = 1500 * time.Millisecond
	res, err := fillTop(t, f, p, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Top.State != StateStopped || res.Top.Reason != "deadline" || res.Top.Summary == nil || !res.Top.Summary.Partial {
		t.Fatalf("top %+v", res.Top)
	}
}

func TestHost_MatcherDownStillFillsIdentityFields(t *testing.T) {
	f := openFixture(t)
	m := &fakeMatcher{before: func(context.Context, MatchRequest) error { return errors.New("down") }}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=down&frames=none"))
	res, err := fillTop(t, f, p, fillOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Top.Summary.MatchUnavailable || topValue(f, "em") != "alex@example.com" || topValue(f, "yrs") != "" {
		t.Fatalf("summary %+v", res.Top.Summary.Counts)
	}
}

// hookDriver lets a test act just before the host evaluates an expression.
type hookDriver struct {
	Driver
	before func(frame, expr string)
}

func (d *hookDriver) Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error) {
	if d.before != nil {
		d.before(frame, expr)
	}
	return d.Driver.Eval(ctx, frame, world, expr)
}

// navigateFrame moves a frame's document in place, as an embed redirecting
// itself would, and waits until the new document has loaded.
func (f *tabFixture) navigateFrame(frame, url, wantOrigin string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = f.hooks.Eval(ctx, frame, "", `setTimeout(()=>{location.href=`+jsString(url)+`},0)`)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := f.hooks.Eval(ctx, frame, "", `location.origin+"|"+document.readyState`)
		if err == nil && string(raw) == `"`+wantOrigin+`|complete"` {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatalf("frame %s never reached %s", frame, url)
}

func originOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	j := strings.Index(rawURL[i+3:], "/")
	return rawURL[:i+3+j]
}

// frameInputs returns the values of the text inputs of a frame's document.
func (f *tabFixture) frameInputs(frame string) []string {
	var v []string
	f.mainEval(frame, `[...document.querySelectorAll('input')].map(e=>e.value)`, &v)
	return v
}

func TestHost_EmbedThatMovesToAnotherOriginBeforeStartIsNotStarted(t *testing.T) {
	f := openFixture(t)
	moved := ""
	hd := &hookDriver{Driver: f.hooks}
	hd.before = func(frame, expr string) {
		if moved == "" && strings.Contains(expr, "__applysmithEngine.start(") && frame != f.frames()["top"].ID {
			moved = frame
			f.navigateFrame(frame, f.fx.B("/sso"), originOf(f.fx.B("/x")))
		}
	}
	p, err := NewPage(hd, Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}, StageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	f.navigate(f.fx.A("/apply?case=originmove&frames=reg"))
	res, err := fillTop(t, f, p, fillOptions())
	if err != nil {
		t.Fatal(err)
	}
	if moved == "" {
		t.Fatal("the registry frame was never started, so the redirect was not exercised")
	}
	d := decisionsByID(res)[moved]
	if d.Outcome != FailedInject || !strings.Contains(d.Reason, "origin-changed") {
		t.Fatalf("decision %+v, want a refusal with reason origin-changed", d)
	}
	for _, v := range f.frameInputs(moved) {
		if v != "" {
			t.Fatalf("an applicant value was typed into the sign-in page: %q", v)
		}
	}
}

func TestHost_RegistryEmbedReplacedOntoAnotherSiteIsClassifiedAgain(t *testing.T) {
	f := openFixture(t)
	m := &fakeMatcher{}
	var once sync.Once
	m.before = func(ctx context.Context, req MatchRequest) error {
		for _, q := range req.Questions {
			if q.Text == "Desired salary" {
				once.Do(func() {
					f.navigateFrame(f.frames()["reg"].ID, f.fx.B("/sso"), originOf(f.fx.B("/x")))
				})
			}
		}
		return nil
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=regmove&frames=reg"))
	res, err := fillTop(t, f, p, fillOptions())
	if err != nil {
		t.Fatal(err)
	}
	var regID string
	for id := range decisionsByID(res) {
		regID = id
	}
	if d := decisionsByID(res)[regID]; d.Outcome != FailedInject || !strings.Contains(d.Reason, "registry does not admit") {
		t.Fatalf("decision %+v: the new document was not classified afresh", d)
	}
	for _, v := range f.frameInputs(regID) {
		if v != "" {
			t.Fatalf("an applicant value was typed into the page the embed moved to: %q", v)
		}
	}
}

func TestHost_BindingCallFromAFrameAtAnotherOriginIsRejected(t *testing.T) {
	f := openFixture(t)
	var got string
	m := &fakeMatcher{}
	var once sync.Once
	m.before = func(ctx context.Context, req MatchRequest) error {
		for _, q := range req.Questions {
			if q.Text != "Highest education level" {
				continue
			}
			once.Do(func() {
				same := f.frames()["same"].ID
				f.navigateFrame(same, f.fx.B("/blank"), originOf(f.fx.B("/x")))
				c, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				raw, err := f.hooks.Eval(c, same, World, `__ajMatch(JSON.stringify({runId:`+jsString(req.RunID)+`,host:"x",posting:{},questions:[]})).then(()=>"resolved",e=>e.message)`)
				got = string(raw)
				if err != nil {
					got = "eval error: " + err.Error()
				}
			})
		}
		return nil
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=bindorigin"))
	if _, err := fillTop(t, f, p, fillOptions()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "not at the origin it was admitted at") {
		t.Fatalf("the call from the moved frame got %q", got)
	}
}

func TestHost_NativeUploadSurvivesTheFillAndReachesANativeFormSubmit(t *testing.T) {
	f := openFixture(t)
	p := f.page(Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=upload&frames=none"))
	res, err := fillTop(t, f, p, fillOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NativeUploads) != 1 {
		t.Fatalf("native uploads %v", res.NativeUploads)
	}
	want, err := os.ReadFile("testdata/resume-alex-example.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// The page reads the file after Fill has returned.
	var n int
	f.mainEval(f.frames()["top"].ID, `document.getElementById('cv').files[0].arrayBuffer().then(b=>b.byteLength)`, &n)
	if n != len(want) {
		t.Fatalf("the page read %d bytes of the attached file, want %d", n, len(want))
	}
	// A native multipart submit carries the file part.
	var started bool
	f.mainEval(f.frames()["top"].ID, `(()=>{const cv=document.getElementById('cv');cv.name='resume';const fm=document.getElementById('f');
fm.method='post';fm.enctype='multipart/form-data';fm.action='/upload';setTimeout(()=>fm.submit(),0);return true})()`, &started)
	deadline := time.Now().Add(15 * time.Second)
	for len(f.fx.receivedUploads()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	ups := f.fx.receivedUploads()
	if len(ups) != 1 || ups[0].Field != "resume" || ups[0].Name != "resume-alex-example.pdf" || ups[0].Size != len(want) {
		t.Fatalf("the server received %+v, want the %d byte file", ups, len(want))
	}
	// The staged copy goes when the tab does.
	ents, _ := os.ReadDir(f.stage)
	if len(ents) == 0 {
		t.Fatal("nothing was staged")
	}
	if err := f.lane.CloseTab(context.Background(), f.tab); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ents, _ = os.ReadDir(f.stage); len(ents) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the staged files outlived the tab: %v", ents)
}

func TestHost_TabThatMovesToAnotherOriginMidFillIsNotFilled(t *testing.T) {
	f := openFixture(t)
	var rejected string
	var topID string
	m := &fakeMatcher{}
	var once sync.Once
	m.before = func(ctx context.Context, req MatchRequest) error {
		once.Do(func() {
			topID = f.frames()["top"].ID
			f.navigateFrame(topID, f.fx.B("/apply?case=topmoved&frames=none"), originOf(f.fx.B("/x")))
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			raw, err := f.hooks.Eval(c, topID, World, `__ajMatch(JSON.stringify({runId:`+jsString(req.RunID)+`,host:"x",posting:{},questions:[]})).then(()=>"resolved",e=>e.message)`)
			rejected = string(raw)
			if err != nil {
				rejected = "eval error: " + err.Error()
			}
		})
		return nil
	}
	p := f.page(Host{Matcher: m, Resumes: &fakeResumes{}})
	f.navigate(f.fx.A("/apply?case=topmove&frames=none"))
	_, err := fillTop(t, f, p, fillOptions())
	if c, _ := CodeOf(err); c != CodeTopOriginChanged {
		t.Fatalf("error %v, want top-origin-changed", err)
	}
	if !strings.Contains(rejected, "not at the origin it was admitted at") {
		t.Fatalf("the binding call from the moved tab got %q", rejected)
	}
	var typed []string
	f.mainEval(topID, `[...document.querySelectorAll('input:not([type=radio]):not([type=checkbox]):not([type=file])')].map(e=>e.value).concat([...document.querySelectorAll('input:checked')].map(e=>'checked:'+e.value)).concat([...document.querySelectorAll('select')].map(e=>e.value))`, &typed)
	for _, v := range typed {
		if v != "" {
			t.Fatalf("an applicant value was typed on the site the tab moved to: %q", v)
		}
	}
}
