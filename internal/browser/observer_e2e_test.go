//go:build browser

package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Requirement: Kept tabs are detached from automation.

// keptPage is a page with a field the user has typed into.
const keptPage = `<!doctype html><title>kept</title><input id=f value="typed by the user"><script>window.__init=window.__init||0</script>`

func keptServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(keptPage))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/kept"
}

func attached(t *testing.T, inst *Instance, id string) bool {
	t.Helper()
	infos, err := chromedp.Targets(inst.root)
	ok(t, err)
	for _, i := range infos {
		if string(i.TargetID) == id {
			return i.Attached
		}
	}
	t.Fatalf("target %s not listed", id)
	return false
}

func readValue(o *Observer, expr string) (string, error) {
	raw, err := o.Evaluate(context.Background(), expr)
	return string(raw), err
}

func TestBrowser_Observer_QuietReadOnlyAndReleased(t *testing.T) {
	inst, _ := sharedInstance(t)
	url := keptServer(t)
	h := inst.Lane(LaneApply)

	tab := openTab(t, inst, LaneApply, "run-observer")
	navigate(t, inst, LaneApply, tab, url)
	ok(t, h.Keep(tab))
	eventually(t, 2*time.Second, "the kept tab to hold no session", func() bool { return !attached(t, inst, tab) })

	const (
		readField = "document.getElementById('f').value"
		readArmed = "[sessionStorage.getItem('armed'),typeof window.__armed].join('|')"
		arm       = "sessionStorage.setItem('armed','1'),window.__armed=true"
	)
	scripts := ObserverScripts{IsArm: func(s string) bool { return s == arm }, IsRead: func(e string) bool { return e == readField || e == readArmed }}
	obs, err := h.Observe(ctx30(t), tab, scripts)
	ok(t, err)
	released := false
	defer func() {
		if !released {
			obs.Release()
		}
	}()

	if _, err := h.Observe(ctx30(t), tab, scripts); !isNotAllowed(err) {
		t.Fatalf("a second observer: %v", err)
	}

	t.Run("Arming is the only write", func(t *testing.T) {
		if err := obs.Install(ctx30(t), arm); err != nil {
			t.Fatalf("arming: %v", err)
		}
		if got, err := readValue(obs, readField); err != nil || got != `"typed by the user"` {
			t.Fatalf("read %s %v", got, err)
		}
		for _, w := range []string{
			"document.getElementById('f').value='chain'",
			"location.href='about:blank'",
			readField + " ",
			arm,
		} {
			if _, err := obs.Evaluate(ctx30(t), w); !isNotAllowed(err) {
				t.Errorf("%q: %v, want not-allowed", w, err)
			}
		}
		if got, _ := readValue(obs, readField); got != `"typed by the user"` {
			t.Fatalf("the field changed to %s", got)
		}
	})

	t.Run("a lane operation on a kept tab is refused", func(t *testing.T) {
		if _, err := h.Evaluate(ctx30(t), tab, "1"); !isNotAllowed(err) {
			t.Fatalf("evaluate: %v", err)
		}
		if _, err := h.Navigate(ctx30(t), tab, "https://example.org/"); !isNotAllowed(err) {
			t.Fatalf("navigate: %v", err)
		}
	})

	t.Run("Quiet observer", func(t *testing.T) {
		// The armed script ran in a world of the observer's own: what it wrote
		// to the page's storage is there, and its global is not the page's.
		if got, err := readValue(obs, readArmed); err != nil || got != `"1|undefined"` {
			t.Fatalf("armed %s %v", got, err)
		}
		if !attached(t, inst, tab) {
			t.Fatal("the observer's session is not listed")
		}
	})

	t.Run("Observer released", func(t *testing.T) {
		obs.Release()
		released = true
		eventually(t, 2*time.Second, "the tab to hold no session", func() bool { return !attached(t, inst, tab) })
		if _, err := obs.Evaluate(ctx30(t), readField); err == nil {
			t.Fatal("a released observer evaluated")
		}
		again, err := h.Observe(ctx30(t), tab, scripts)
		ok(t, err)
		again.Release()
	})
}

func TestBrowser_Observer_TabClosedEndsIt(t *testing.T) {
	inst, _ := sharedInstance(t)
	url := keptServer(t)
	h := inst.Lane(LaneApply)
	tab := openTab(t, inst, LaneApply, "run-observer-close")
	navigate(t, inst, LaneApply, tab, url)
	ok(t, h.Keep(tab))
	read := "document.title"
	obs, err := h.Observe(ctx30(t), tab, ObserverScripts{IsRead: func(e string) bool { return e == read }})
	ok(t, err)
	defer obs.Release()
	if _, err := obs.Evaluate(ctx30(t), read); err != nil {
		t.Fatal(err)
	}
	ok(t, h.CloseTab(ctx30(t), tab))
	if _, err := obs.Evaluate(ctx30(t), read); err == nil {
		t.Fatal("evaluated on a closed tab")
	} else if code, _ := CodeOf(err); code != CodeTabClosed {
		t.Fatalf("code %q err %v, want tab-closed", code, err)
	}
	if _, err := h.Observe(ctx30(t), tab, ObserverScripts{}); err == nil {
		t.Fatal("observed a closed tab")
	}
}

func TestBrowser_Observer_ARunNotKeptIsNotObserved(t *testing.T) {
	inst, _ := sharedInstance(t)
	tab := openTab(t, inst, LaneApply, "run-observer-open")
	if _, err := inst.Lane(LaneApply).Observe(ctx30(t), tab, ObserverScripts{}); !isNotAllowed(err) {
		t.Fatalf("an open tab: %v", err)
	}
}

// Handoff: within 2 s of Keep the tab holds no session, init script or
// binding, and its page is unchanged.
func TestBrowser_Observer_HandoffLeavesNoSessionInitScriptOrBinding(t *testing.T) {
	inst, _ := sharedInstance(t)
	url := keptServer(t)
	h := inst.Lane(LaneApply)
	tab := openTab(t, inst, LaneApply, "run-handoff")
	hk, err := h.Hooks(ctx30(t), tab)
	ok(t, err)
	ok(t, hk.AddInitScript(ctx30(t), "window.__init=7"))
	ok(t, hk.AddBinding("applysmith-handoff-test", "handoffBinding", func(context.Context, BindingCall) (string, error) { return "", nil }))
	navigate(t, inst, LaneApply, tab, url)
	if got := eval[int](t, h, tab, "window.__init"); got != 7 {
		t.Fatalf("the init script did not run before the hand-off: %d", got)
	}
	ok(t, h.Keep(tab))
	eventually(t, 2*time.Second, "the kept tab to hold no session", func() bool { return !attached(t, inst, tab) })

	// A reload is the user's own action here; the observer is only the means of
	// asking the page afterwards.
	const reload = "sessionStorage.getItem('r')||(sessionStorage.setItem('r','1'),setTimeout(function(){location.reload()},0))"
	const probe = "[window.__init,typeof handoffBinding,document.getElementById('f').value].join('|')"
	obs, err := h.Observe(ctx30(t), tab, ObserverScripts{IsArm: func(s string) bool { return s == reload }, IsRead: func(e string) bool { return e == probe }})
	ok(t, err)
	defer obs.Release()
	if got, err := readValue(obs, probe); err != nil || got != `"7|undefined|typed by the user"` {
		t.Fatalf("page state %s %v", got, err)
	}
	_ = obs.Install(ctx30(t), reload)
	eventually(t, 5*time.Second, "the reloaded page", func() bool {
		got, err := readValue(obs, probe)
		return err == nil && got == `"0|undefined|typed by the user"`
	})
}

// Requirement: Kept tabs are detached from automation, "Reclaimed by Done".
func TestBrowser_Reclaim_ATabKeptForTheUserTakesCommandsAgainAndKeepsItsValues(t *testing.T) {
	inst, _ := sharedInstance(t)
	url := keptServer(t)
	h := inst.Lane(LaneApply)
	tab := openTab(t, inst, LaneApply, "run-reclaim")
	navigate(t, inst, LaneApply, tab, url)
	ok(t, h.Keep(tab))
	eventually(t, 2*time.Second, "the kept tab to hold no session", func() bool { return !attached(t, inst, tab) })

	const read = "document.getElementById('f').value"
	if _, err := h.Evaluate(ctx30(t), tab, read); !isNotAllowed(err) {
		t.Fatalf("a command on a kept tab: %v", err)
	}
	ok(t, h.Reclaim(tab))
	raw, err := h.Evaluate(ctx30(t), tab, read)
	ok(t, err)
	if string(raw) != `"typed by the user"` {
		t.Fatalf("the field reads %s after the tab was taken back", raw)
	}

	// Keeping it again detaches it again, and a closed tab cannot be reclaimed.
	ok(t, h.Keep(tab))
	eventually(t, 2*time.Second, "the kept tab to hold no session again", func() bool { return !attached(t, inst, tab) })
	if _, err := h.Evaluate(ctx30(t), tab, read); !isNotAllowed(err) {
		t.Fatalf("a command after the tab was kept again: %v", err)
	}
	ok(t, h.CloseTab(ctx30(t), tab))
	if err := h.Reclaim(tab); err == nil {
		t.Fatal("a closed tab was reclaimed")
	} else if code, _ := CodeOf(err); code != CodeTabClosed {
		t.Fatalf("reclaiming a closed tab: %v", err)
	}
}
