package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/browser"
)

func newFakePage(t *testing.T, d *fakeDriver) *Page {
	t.Helper()
	p, err := NewPage(d, Host{Matcher: &fakeMatcher{}, Resumes: &fakeResumes{}, StageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantCode(t *testing.T, err error, want Code) {
	t.Helper()
	if c, _ := CodeOf(err); c != want {
		t.Fatalf("error %v, want code %s", err, want)
	}
}

func TestProtocol_NormalCompletionPollsUntilDone(t *testing.T) {
	d := newFakeDriver()
	d.status = func(_ string, n int) (string, error) {
		if n < 4 {
			return runningStatus("RUN"), nil
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Top.State != StateDone || d.polls["top"] != 4 || res.Reinjections != 0 || d.startCount("top") != 1 {
		t.Fatalf("state=%s polls=%d", res.Top.State, d.polls["top"])
	}
	if res.Notice != "Filled 2 field(s)." {
		t.Fatalf("notice %q", res.Notice)
	}
}

func TestProtocol_EnginePresentWithCurrentStampIsNotInjectedAgain(t *testing.T) {
	d := newFakeDriver()
	d.present["top"] = true
	var bundles int
	d.onEval = func(_, expr string) error {
		if len(expr) > 10000 {
			bundles++
		}
		return nil
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err != nil {
		t.Fatal(err)
	}
	if bundles != 0 {
		t.Fatalf("the bundle was evaluated %d times", bundles)
	}
}

func TestProtocol_NavigationLimitAfterThreeReinjections(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return "", replacedErr() }
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeNavigationLimit)
	if d.startCount("top") != 1+MaxReinjections {
		t.Fatalf("started %d times", d.startCount("top"))
	}
}

func TestProtocol_MissingEngineAfterNavigationReinjects(t *testing.T) {
	d := newFakeDriver()
	d.status = func(_ string, n int) (string, error) {
		if d.starts["top"] == 1 {
			return `null`, nil // the new document holds no engine
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil || res.Reinjections != 1 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestProtocol_ClosedTabIsReportedAndNotReinjected(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return "", &browser.Error{Code: browser.CodeTabClosed} }
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeTabClosed)
	if d.startCount("top") != 1 {
		t.Fatalf("restarted %d times", d.startCount("top"))
	}
}

func TestProtocol_LostBrowserIsNotANavigation(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return "", &browser.Error{Code: browser.CodeBrowserDisconnected} }
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeBrowserLost)
}

func TestProtocol_EngineHungIsStoppedAfterTheGrace(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return runningStatus("RUN"), nil }
	o := quickOptions()
	o.Timing.PageDeadline = 30 * time.Millisecond
	_, err := newFakePage(t, d).Fill(context.Background(), o)
	wantCode(t, err, CodeEngineHung)
	if len(d.stops) == 0 {
		t.Fatal("the host did not call stop")
	}
}

func TestProtocol_LostStartAcknowledgementStopsTheRun(t *testing.T) {
	d := newFakeDriver()
	d.onStart = func(string, int) error { return timeoutErr() }
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeStartLost)
	if res != nil || len(d.stops) != 1 {
		t.Fatalf("res=%v stops=%v", res, d.stops)
	}
	if id := d.runIDs["top"]; id == "" || !strings.Contains(d.stops[0], jsString(id)) || !strings.Contains(d.stops[0], `"cancelled"`) {
		t.Fatalf("stop expression %q does not name the started run %q", d.stops[0], id)
	}
}

func TestProtocol_StarvedPollRetriesTheSameRun(t *testing.T) {
	d := newFakeDriver()
	d.status = func(_ string, n int) (string, error) {
		if n <= 2 {
			return "", timeoutErr()
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil || res.Top.State != StateDone || d.startCount("top") != 1 {
		t.Fatalf("err=%v starts=%d", err, d.startCount("top"))
	}
}

// A busy out-of-process frame can drop out of the tab's frame table for a
// moment while its fill runs; the poll keeps following the same run.
func TestProtocol_AFrameMissingForAMomentIsFollowedToTheEnd(t *testing.T) {
	d := newFakeDriver()
	d.status = func(_ string, n int) (string, error) {
		if n <= 3 {
			return "", frameGoneErr()
		}
		return doneStatus("RUN"), nil
	}
	res, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if err != nil || res.Top.State != StateDone || d.startCount("top") != 1 || len(d.stops) != 0 {
		t.Fatalf("err=%v starts=%d stops=%v", err, d.startCount("top"), d.stops)
	}
}

// A frame that stays missing is given up, and its run is stopped so that a
// later pass can start a fill there again.
func TestProtocol_AFrameThatStaysMissingIsGivenUpAndItsRunStopped(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return "", frameGoneErr() }
	_, err := newFakePage(t, d).Fill(context.Background(), quickOptions())
	if c, ok := browser.CodeOf(err); !ok || c != browser.CodeFrameGone {
		t.Fatalf("error %v", err)
	}
	if len(d.stops) != 1 || !strings.Contains(d.stops[0], d.runIDs["top"]) {
		t.Fatalf("stops=%v", d.stops)
	}
}

// A poll that fails for good ends the fill with its error and stops the run it was following.
func TestProtocol_OtherPollErrorIsFatal(t *testing.T) {
	d := newFakeDriver()
	d.status = func(string, int) (string, error) { return "", errors.New("boom") }
	if _, err := newFakePage(t, d).Fill(context.Background(), quickOptions()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %v", err)
	}
	if id := d.runIDs["top"]; len(d.stops) != 1 || id == "" || !strings.Contains(d.stops[0], jsString(id)) {
		t.Fatalf("stops=%v", d.stops)
	}
}

func TestProtocol_ApplicantReadThreeTimesThenAbortsVisibly(t *testing.T) {
	d := newFakeDriver()
	var n int32
	o := quickOptions()
	o.Applicant = func(context.Context) (json.RawMessage, error) {
		atomic.AddInt32(&n, 1)
		return nil, errors.New("unreachable")
	}
	_, err := newFakePage(t, d).Fill(context.Background(), o)
	wantCode(t, err, CodeApplicantUnavailable)
	if n != ApplicantAttempts || !strings.Contains(err.Error(), "nothing was filled") {
		t.Fatalf("attempts=%d err=%v", n, err)
	}
	// A later attempt that works is used.
	n = 0
	o.Applicant = func(ctx context.Context) (json.RawMessage, error) {
		if atomic.AddInt32(&n, 1) < 3 {
			return nil, errors.New("down")
		}
		return fixtureApplicant(ctx)
	}
	if _, err := newFakePage(t, d).Fill(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func TestProtocol_StartRefusedIsAnError(t *testing.T) {
	d := newFakeDriver()
	p := newFakePage(t, d)
	p.d = &refusingDriver{fakeDriver: d}
	_, err := p.Fill(context.Background(), quickOptions())
	wantCode(t, err, CodeStartRefused)
}

type refusingDriver struct{ *fakeDriver }

func (r *refusingDriver) Eval(ctx context.Context, frame, world, expr string) (json.RawMessage, error) {
	if strings.Contains(expr, "__applysmithEngine.start(") {
		return json.RawMessage(`{"started":false,"reason":"a fill is already running"}`), nil
	}
	return r.fakeDriver.Eval(ctx, frame, world, expr)
}

func TestProtocol_OneFillPerTab(t *testing.T) {
	d := newFakeDriver()
	p := newFakePage(t, d)
	rs := newRunState("x")
	if err := p.begin(rs); err != nil {
		t.Fatal(err)
	}
	wantCode(t, p.begin(newRunState("y")), CodeBusy)
}
