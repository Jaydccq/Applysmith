//go:build browser

package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// A page's own failure is not a lost browser (errors.go): a script that throws
// is eval-failed and a navigation that replaces the document under a running
// script is document-replaced. An evaluation read the null body of a document
// in transition and was reported as browser-disconnected.
func TestE2E_Browser_APageScriptsFailureIsThePagesNotTheBrowsers(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-page-failure")
	h := inst.Lane(LaneScan)
	for _, c := range []struct {
		name, expr string
		want       Code
	}{
		{"throws", `document.querySelector('#no-such-element').cloneNode(true)`, CodeEvalFailed},
		{"navigates away", fmt.Sprintf(`new Promise(()=>{location.href=%q})`, fx.Loopback("/probe")), CodeDocumentReplaced},
	} {
		navigate(t, inst, LaneScan, tab, fx.Loopback("/probe"))
		_, err := h.Evaluate(ctx30(t), tab, c.expr)
		if code, _ := CodeOf(err); code != c.want {
			t.Errorf("%s: code %q err %v, want %s", c.name, code, err, c.want)
		}
	}
	if got := eval[int](t, h, tab, "1+1"); got != 2 {
		t.Fatal("the tab did not survive the page's failures")
	}
}

func TestE2E_Browser_RedirectThen404ReturnsStatusFinalURLAndTitle(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-redirect")
	res, err := inst.Lane(LaneScan).Navigate(ctx30(t), tab, fx.Loopback("/redirect"))
	ok(t, err)
	if res.Status != 404 || res.URL != fx.Loopback("/gone") || res.Title != "Gone" || res.Error != "" {
		t.Fatalf("result %+v", res)
	}
}

func TestE2E_Browser_FailedLoadIsAResultNotAFault(t *testing.T) {
	inst, _ := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-refused")
	closed := freePortT(t)
	res, err := inst.Lane(LaneScan).Navigate(ctx30(t), tab, fmt.Sprintf("http://127.0.0.1:%d/", closed))
	ok(t, err)
	if res.Error == "" || !strings.Contains(res.Error, "ERR_CONNECTION_REFUSED") || res.LoadState != "failed" {
		t.Fatalf("result %+v", res)
	}
}

func TestE2E_Browser_NoNetworkIsNetworkOffline(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-offline")
	h := inst.Lane(LaneScan)
	navigate(t, inst, LaneScan, tab, fx.Loopback("/probe"))
	sess := tabSession(t, inst, LaneScan, tab)
	call(t, sess, network.Enable, network.EnableParams{})
	call(t, sess, network.EmulateNetworkConditionsByRule, network.EmulateNetworkConditionsByRuleParams{
		MatchedNetworkConditions: []*network.Conditions{{Offline: true, DownloadThroughput: -1, UploadThroughput: -1}}})
	t.Cleanup(func() {
		_, _ = chromedp.Call(sess, network.EmulateNetworkConditionsByRule, network.EmulateNetworkConditionsByRuleParams{
			MatchedNetworkConditions: []*network.Conditions{{DownloadThroughput: -1, UploadThroughput: -1}}})
	})
	_, err := h.Navigate(ctx30(t), tab, fx.Loopback("/probe"))
	if code, _ := CodeOf(err); code != CodeNetworkOffline {
		t.Fatalf("code %q err %v, want network-offline", code, err)
	}
}

// Nothing the instance reports names the debugging endpoint: not the event
// log, not the instance record, not the address text.
func TestE2E_Browser_EndpointIsNotDisclosed(t *testing.T) {
	inst, fx := sharedInstance(t)
	tab := openTab(t, inst, LaneScan, "run-disclose")
	navigate(t, inst, LaneScan, tab, fx.Loopback("/probe"))
	_, _ = inst.Lane(LaneScan).Navigate(ctx30(t), tab, "file:///etc/hosts")
	_, err := inst.Lane(LaneApply).Navigate(ctx30(t), tab, fx.Loopback("/probe"))
	openTab(t, inst, LaneApply, "run-disclose")
	eventually(t, 3*time.Second, "the instance record", func() bool { _, ok := ReadRecord(inst.recordPath()); return ok })
	texts := []string{err.Error()}
	for _, e := range inst.Events() {
		texts = append(texts, e.Kind+" "+e.Detail)
	}
	for _, tx := range texts {
		if strings.Contains(tx, "/devtools/") || strings.Contains(tx, "ws://") || strings.Contains(tx, fmt.Sprint(inst.proc.Port)) {
			t.Errorf("endpoint disclosed in %q", tx)
		}
	}
	// The record keeps the port for adoption (mode 0600) but never the address.
	if rec := readFile(t, inst.recordPath()); strings.Contains(rec, "/devtools/") || strings.Contains(rec, "ws://") {
		t.Errorf("record holds the WebSocket address: %s", rec)
	}
}

// Two callers that ask for a lane while no browser runs share one launch, and
// a record left by a dead server is ignored and replaced.
func TestE2E_Browser_ConcurrentFirstUseOpensOneChrome(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	ok(t, os.MkdirAll(o.StateDir, 0o700))
	canon, err := canonical(o.ProfileDir)
	ok(t, err)
	ok(t, WriteRecord(filepath.Join(o.StateDir, "instance.json"), Record{PID: deadPID(t), Start: "x", Profile: canon, Port: freePortT(t)}))
	var wg sync.WaitGroup
	insts := make([]*Instance, 2)
	ids := make([]string, 2)
	errs := make([]error, 2)
	for i, lane := range []Lane{LaneScan, LaneApply} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			insts[i], errs[i] = Open(context.Background(), o)
			if errs[i] == nil {
				ids[i], errs[i] = insts[i].Lane(lane).OpenTab(context.Background(), "run-first")
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		ok(t, err)
	}
	t.Cleanup(func() { insts[0].Stop(context.Background()) })
	if insts[0] != insts[1] {
		t.Fatal("two instances were opened")
	}
	if pids := chromeMainProcesses(insts[0].proc.Profile); len(pids) != 1 {
		t.Fatalf("%d Chrome browser processes on the profile, want 1", len(pids))
	}
	if r, _ := ReadRecord(filepath.Join(o.StateDir, "instance.json")); r.PID != insts[0].PID() {
		t.Fatalf("record names pid %d, want %d (stale record not replaced)", r.PID, insts[0].PID())
	}
	_, a := insts[0].Lane(LaneApply).tab(ids[1])
	_, s := insts[0].Lane(LaneScan).tab(ids[0])
	if a != nil || s != nil {
		t.Fatalf("each caller must receive a tab in its own lane: %v %v", a, s)
	}
}

// The instance record is written once both lane windows exist, mode 0600.
func TestE2E_Browser_RecordIsWrittenOnceBothWindowsExist(t *testing.T) {
	dir, err := os.MkdirTemp("", "applysmith-browser-test-")
	ok(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o := testOptions(t, dir)
	inst, err := Open(ctx30(t), o)
	ok(t, err)
	t.Cleanup(func() { inst.Stop(context.Background()) })
	rec := filepath.Join(o.StateDir, "instance.json")
	has := func() bool { _, err := os.Stat(rec); return err == nil }
	if has() {
		t.Fatal("a record exists before any window")
	}
	_, err = inst.Lane(LaneScan).OpenTab(ctx30(t), "run-rec")
	ok(t, err)
	if has() {
		t.Fatal("a record exists with only the scan window")
	}
	_, err = inst.Lane(LaneApply).OpenTab(ctx30(t), "run-rec")
	ok(t, err)
	if !has() {
		t.Fatal("no record once both windows exist")
	}
	if st, _ := os.Stat(rec); st.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v", st.Mode().Perm())
	}
	r, _ := ReadRecord(rec)
	if len(r.ApplyWindows) != 1 || len(r.ScanWindows) != 1 {
		t.Fatalf("record %+v", r)
	}
}
