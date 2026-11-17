// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"testing"
)

// The cases follow the computer-use spec's desktop-tools requirement and
// policies.md "The agent at your desktop" (owner decisions AC-2, AC-5).
func TestDesktopAction_ScopeAndPresses(t *testing.T) {
	mail := DesktopActionInput{Bundle: "com.apple.mail", App: "Mail", NamedByUser: true, Found: true, Role: "AXButton"}
	browser := DesktopActionInput{Bundle: "com.google.Chrome", App: "Google Chrome", ManagedBrowser: true, Found: true, Role: "AXButton"}
	with := func(in DesktopActionInput, f func(*DesktopActionInput)) DesktopActionInput { f(&in); return in }
	click := func(in DesktopActionInput, labels ...string) DesktopActionInput {
		return with(in, func(i *DesktopActionInput) { i.Tool, i.Labels = DesktopClick, labels })
	}
	keys := func(in DesktopActionInput, chord string) DesktopActionInput {
		return with(in, func(i *DesktopActionInput) { i.Tool, i.Keys = DesktopKeys, chord })
	}
	focused := func(in DesktopActionInput, role string) DesktopActionInput {
		return with(in, func(i *DesktopActionInput) { i.FocusRole = role })
	}
	typing := func(in DesktopActionInput, role, text string) DesktopActionInput {
		return with(in, func(i *DesktopActionInput) { i.Tool, i.Role, i.Text = DesktopType, role, text })
	}
	cases := []struct {
		name string
		in   DesktopActionInput
		v    Verdict
		code string
	}{
		{"pause refuses even a scroll of the apply browser", with(browser, func(i *DesktopActionInput) { i.Tool, i.Paused = DesktopScroll, true }), Deny, CodeDesktopPaused},
		{"Applysmith itself", click(with(mail, func(i *DesktopActionInput) { i.Bundle, i.App = "dev.applysmith.app", "Applysmith" }), "Approve"), Deny, CodeDesktopAppRefused},
		{"a terminal the user named", click(with(mail, func(i *DesktopActionInput) { i.Bundle, i.App = "com.apple.Terminal", "Terminal" }), "OK"), Deny, CodeDesktopAppRefused},
		{"the everyday Chrome", click(with(browser, func(i *DesktopActionInput) { i.ManagedBrowser, i.NamedByUser = false, true }), "Next"), Deny, CodeDesktopAppRefused},
		{"a PersonalCU of any bundle id", click(with(mail, func(i *DesktopActionInput) { i.Bundle = "local.someone.personal-cu" }), "Pause"), Deny, CodeDesktopAppRefused},
		{"an IDE of a vendor family", click(with(mail, func(i *DesktopActionInput) { i.Bundle = "com.jetbrains.goland" }), "Run"), Deny, CodeDesktopAppRefused},
		{"an app whose identity was not read", click(with(mail, func(i *DesktopActionInput) { i.Bundle = "" }), "OK"), Deny, CodeDesktopAppRefused},
		{"an app the user did not name", click(with(mail, func(i *DesktopActionInput) { i.NamedByUser = false }), "Archive"), Deny, CodeDesktopAppNotNamed},
		{"a named app's ordinary button", click(mail, "Archive"), Allow, CodeOK},
		{"Send in a named app", click(mail, "Send"), Deny, CodeDesktopControl},
		{"Move to Trash in a named app", click(mail, "Move to Trash"), Deny, CodeDesktopControl},
		{"a Han Submit in a named app", click(mail, "\u63d0\u4ea4"), Deny, CodeDesktopControl},
		{"Submit in the apply browser", click(browser, "Submit application"), Deny, CodeDesktopControl},
		{"Withdraw in the apply browser", click(browser, "Withdraw application"), Deny, CodeDesktopControl},
		{"Next in the apply browser", click(browser, "Next"), Allow, CodeOK},
		{"an answer option in the apply browser", click(with(browser, func(i *DesktopActionInput) { i.Role = "AXRadioButton" }), "Yes"), Deny, CodeDesktopWalkOnly},
		{"a lookalike Continue", click(browser, "C\u043entinue"), Deny, CodeMixedScriptLabel},
		{"a point on no control", click(with(mail, func(i *DesktopActionInput) { i.Found = false }), "Archive"), Deny, CodeDesktopNoControl},
		{"typing in the apply browser", typing(browser, "AXTextField", "hello"), Deny, CodeDesktopTypingRefused},
		{"typing in a named app's text area", typing(mail, "AXTextArea", "Thanks, the 14th works for me."), Allow, CodeOK},
		{"typing into a button", typing(mail, "AXButton", "hello"), Deny, CodeDesktopNotTextField},
		{"typing into a field the look did not read", typing(with(mail, func(i *DesktopActionInput) { i.Found = false }), "AXTextField", "hello"), Deny, CodeDesktopNoControl},
		{"typing over the length limit", typing(mail, "AXTextField", strings.Repeat("\u00e9", MaxDesktopTextRunes+1)), Deny, CodeDesktopTextTooLong},
		// A control character in typed text reaches the field as that key: a
		// "\r" or "\n" is a Return that can send what the agent typed.
		{"typing a Return inside the text", typing(mail, "AXTextField", "ok\r"), Deny, CodeDesktopKeyRefused},
		{"typing a newline inside the text", typing(mail, "AXTextArea", "line one\nline two"), Deny, CodeDesktopKeyRefused},
		{"typing a Tab inside the text", typing(mail, "AXTextField", "a\tb"), Deny, CodeDesktopKeyRefused},
		{"typing an Escape inside the text", typing(mail, "AXTextField", "a\x1b"), Deny, CodeDesktopKeyRefused},
		{"typing a C1 next line inside the text", typing(mail, "AXTextField", "a\u0085"), Deny, CodeDesktopKeyRefused},
		{"typing a line separator inside the text", typing(mail, "AXTextArea", "a\u2028b"), Deny, CodeDesktopKeyRefused},
		{"typing letters, accents and emoji", typing(mail, "AXTextField", "Caf\u00e9 \u4f60\u597d \U0001F600"), Allow, CodeOK},
		{"Return in a named app", keys(mail, "Return"), Deny, CodeDesktopKeyRefused},
		{"Return under a modifier", keys(mail, "cmd+Enter"), Deny, CodeDesktopKeyRefused},
		{"a quit shortcut", keys(mail, "cmd+q"), Deny, CodeDesktopKeyRefused},
		{"Shift+Tab in the apply browser, spelled loosely", keys(browser, "Shift + Tab"), Allow, CodeOK},
		{"Space in the apply browser would tick a box", keys(browser, "space"), Deny, CodeDesktopKeyRefused},
		{"Space in a named app with no word of where the focus is", keys(mail, "Space"), Deny, CodeDesktopKeyRefused},
		{"Backspace after the agent's own click on a button", focused(keys(mail, "Backspace"), "AXButton"), Deny, CodeDesktopKeyRefused},
		{"Delete after the focus moved to a control nobody read", keys(mail, "Delete"), Deny, CodeDesktopKeyRefused},
		{"Space in the text field the agent last typed into", focused(keys(mail, "Space"), "AXTextArea"), Allow, CodeOK},
		{"Delete in the text field the agent last clicked", focused(keys(mail, "delete"), "AXTextField"), Allow, CodeOK},
		{"an arrow in a named app needs no focus fact", keys(mail, "Down"), Allow, CodeOK},
		{"Space in the apply browser even after a text field", focused(keys(browser, "space"), "AXTextField"), Deny, CodeDesktopKeyRefused},
		{"a scroll of the apply browser", with(browser, func(i *DesktopActionInput) { i.Tool = DesktopScroll }), Allow, CodeOK},
		{"an action that is not a desktop action", with(mail, func(i *DesktopActionInput) { i.Tool = "drag" }), Deny, CodeDesktopUnknownAction},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { got(t, DesktopAction(c.in), GateDesktopAction, c.v, c.code) })
	}
}

func TestNamesApp(t *testing.T) {
	cases := []struct {
		msg   string
		names []string
		want  bool
	}{
		{"open Mail and find the verification code", []string{"Mail", "mail"}, true},
		{"send me an email when it is done", []string{"Mail", "mail"}, false},
		{"\u5728\u90ae\u4ef6\u91cc\u627e\u9a8c\u8bc1\u7801", []string{"\u90ae\u4ef6", "mail"}, true}, // find the code in Mail
		{"check my notes for the start date", []string{"Notes", "notes"}, true},
		{"open \uff2dail", []string{"Mail"}, true}, // a full-width M
		{"ask QQ", []string{"QQ"}, false},          // names under three letters are too short to tell apart
		{"use the Chrome on my desk", []string{}, false},
	}
	for _, c := range cases {
		if got := NamesApp(c.msg, c.names...); got != c.want {
			t.Errorf("NamesApp(%q, %s) = %v, want %v", c.msg, strings.Join(c.names, "|"), got, c.want)
		}
	}
}
