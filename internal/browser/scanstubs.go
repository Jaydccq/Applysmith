package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Chrome activates the tab of a script dialog and of a script-opened window
// before DevTools can answer, which brings the browser over the user's
// application. A scan tab is invisible to the user, so its pages get stubs that
// answer as the dialog policy would and record a popup URL instead of opening
// the window: the address the window would have loaded, resolved against the
// document as the browser resolves it. Apply tabs keep real dialogs and
// popups: that window is the user's visible lane.
const scanStubScript = `(()=>{
const n=%q;const send=globalThis[n];try{delete globalThis[n]}catch(e){}
const def=(k,v)=>Object.defineProperty(window,k,{value:v,configurable:true,writable:true});
def('open',function open(u){try{let s=String(u===undefined?'':u);if(s)try{s=new URL(s,document.baseURI).href}catch(e){}send(s)}catch(e){}return null});
def('alert',function alert(){});
def('confirm',function confirm(){return false});
def('prompt',function prompt(){return null});
Object.defineProperty(window,'onbeforeunload',{get(){return null},set(){},configurable:true});
const add=window.addEventListener;
window.addEventListener=function addEventListener(t){if(t==='beforeunload')return;return add.apply(this,arguments)};
})()`

const maxPopupURLs = 50

// installScanStubs registers the stub script for every document and frame of
// the tab that shares its process. The popup URL reaches Go through a binding
// whose random name the script removes from the page before page code runs.
func (i *Instance) installScanStubs(ctx context.Context, t *tab) error {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "no randomness for the popup binding")
	}
	name := "aj_" + hex.EncodeToString(raw[:])
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := chromedp.Call(sctx, runtime.AddBinding, runtime.AddBindingParams{Name: name}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot add the popup binding")
	}
	if _, err := chromedp.Call(sctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
		Source: fmt.Sprintf(scanStubScript, name), RunImmediately: ptr(true),
	}); err != nil {
		return wrapError(CodeBrowserDisconnected, err, "cannot install the scan stubs")
	}
	t.popupBinding.Store(&name)
	watch(ctx, runtime.BindingCalled, func(ev runtime.EventBindingCalled) {
		if ev.Name != name {
			return
		}
		u := ev.Payload
		if len(u) > 400 {
			u = u[:400]
		}
		t.mu.Lock()
		if len(t.popupURLs) < maxPopupURLs {
			t.popupURLs = append(t.popupURLs, u)
		}
		t.mu.Unlock()
		i.logEvent("popup_captured", t.lane, u)
	})
	return nil
}

// restorePopupBinding registers a scan tab's popup binding afresh on a session
// that dropped its bindings. A tab without scan stubs has none.
func (t *tab) restorePopupBinding(ctx context.Context) error {
	name := t.popupBinding.Load()
	if name == nil {
		return nil
	}
	if err := removeBinding(ctx, *name); err != nil {
		return err
	}
	return addBinding(ctx, runtime.AddBindingParams{Name: *name})
}

// hidePopupBinding takes the popup binding's name back out of the main worlds
// of the documents that are loaded, where registering it afresh put it within
// reach of page code. Each document's stub holds its own reference.
func (t *tab) hidePopupBinding(ctx context.Context, s *hookSession) {
	name := t.popupBinding.Load()
	if name == nil {
		return
	}
	s.mu.Lock()
	mains := []runtime.ExecutionContextID{0}
	for _, id := range s.defaults {
		mains = append(mains, id)
	}
	s.mu.Unlock()
	expr := fmt.Sprintf("try{delete globalThis[%q]}catch(e){}", *name)
	for _, id := range mains {
		_, _ = chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{Expression: expr, ContextID: id})
	}
}

// PopupURLs returns the URLs the tab's page tried to open in a new window.
func (h *LaneHandle) PopupURLs(tabID string) ([]string, error) {
	t, err := h.tab(tabID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.popupURLs...), nil
}
