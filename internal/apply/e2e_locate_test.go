//go:build browser

package apply

import (
	"strings"
	"testing"
)

// A Submit control built as a custom element around a shadow button is the
// control itself; a banner or another component's host over it still covers it.
func TestBrowser_Click_OwnShadowHostIsNotCovered(t *testing.T) {
	const define = `<script>
customElements.define("x-button", class extends HTMLElement {
  constructor() { super(); this.attachShadow({ mode: "open" }).innerHTML = '<button type="button" style="width:140px;height:40px"><slot></slot></button>'; }
});
customElements.define("x-banner", class extends HTMLElement {
  constructor() { super(); this.attachShadow({ mode: "open" }).innerHTML = '<div style="position:fixed;inset:0;background:#fff">Cookie notice</div>'; }
});
</script>`
	locate := func(t *testing.T, markup string) Located {
		t.Helper()
		ctx, _, _, tab := htmlTab(t, "<title>Apply</title>"+define+markup)
		var st PageState
		if err := tab.Eval(ctx, probeExpr("probe", nil), &st); err != nil {
			t.Fatal(err)
		}
		for _, c := range st.Controls {
			for _, l := range c.Labels {
				if strings.Contains(l, "Submit application") {
					loc, err := Locate(ctx, tab, c.I)
					if err != nil {
						t.Fatal(err)
					}
					return loc
				}
			}
		}
		t.Fatalf("no control labelled Submit application in %+v", st.Controls)
		return Located{}
	}
	t.Run("the control's own shadow host is a hit", func(t *testing.T) {
		loc := locate(t, `<x-button role="button">Submit application</x-button>`)
		if !loc.Found || !loc.Hit || loc.Blocker != "" {
			t.Fatalf("%+v", loc)
		}
	})
	t.Run("another component's host over it covers it", func(t *testing.T) {
		loc := locate(t, `<x-button role="button">Submit application</x-button><x-banner></x-banner>`)
		if !loc.Found || loc.Hit || loc.Blocker == "" {
			t.Fatalf("%+v", loc)
		}
	})
}

// Taleo Business Edition hides the consent box's native input behind its
// theme and draws the box as an icon in a wrapper of its own; a press lands on
// the icon, and the page's own handler ticks the box. Its labels mark required
// fields by class, and the box's caption, which carries no class, stays
// required: the sign-up ticks only a required agreement.
func TestBrowser_Locate_ThemeHiddenBoxIsPressedWhereItIsDrawn(t *testing.T) {
	ctx, _, _, tab := htmlTab(t, `<title>Apply</title><form><label class="oracletaleocwsv2-required form-label" for="e">Email</label><input id="e" type="email">
<label class="oracletaleocwsv2-required form-label" for="p">Password</label><input id="p" type="password"><label class="oracletaleocwsv2-required form-label" for="p2">Re-type new password:</label><input id="p2" type="password">
<div class="checkbox" onclick="var b=this.querySelector('input'); b.checked=!b.checked"><i style="display:inline-block;width:24px;height:24px;border:1px solid #333"></i><input type="checkbox" id="agree" style="display:none"></div>
<span>I agree to <a href="#">Example Applicant Privacy Policy</a></span>
<div><input type="checkbox" id="bare" style="display:none"></div><span>Send me job alerts</span></form>`)
	st, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	var agree, bare *InputInfo
	for i := range st.Inputs {
		switch {
		case strings.HasPrefix(st.Inputs[i].Label, "I agree to"):
			agree = &st.Inputs[i]
		case strings.Contains(st.Inputs[i].Selector, "bare"):
			bare = &st.Inputs[i]
		}
	}
	if agree == nil || bare != nil || !agree.Required {
		t.Fatalf("the drawn box is read, required, under its caption and the undrawn one is not: %+v", st.Inputs)
	}
	loc, err := Locate(ctx, tab, agree.I)
	if err != nil || !loc.Found || !loc.Hit {
		t.Fatalf("locate %+v %v", loc, err)
	}
	if err := tab.Click(ctx, loc.X, loc.Y); err != nil {
		t.Fatal(err)
	}
	var on bool
	if err := tab.Eval(ctx, `document.getElementById("agree").checked`, &on); err != nil || !on {
		t.Fatalf("checked %v %v", on, err)
	}
}
