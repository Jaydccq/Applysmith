// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// GateDesktopAction decides every action the Ask Agent takes through the
// computer-use service (owner decisions AC-2 and AC-5).
const GateDesktopAction Gate = "desktop_action"

// Codes of the desktop action gate.
const (
	CodeDesktopPaused        = "desktop_paused"
	CodeDesktopAppRefused    = "desktop_app_refused"
	CodeDesktopAppNotNamed   = "desktop_app_not_named"
	CodeDesktopNoControl     = "desktop_no_control"
	CodeDesktopControl       = "desktop_control_refused"
	CodeDesktopWalkOnly      = "desktop_walk_only"
	CodeDesktopTypingRefused = "desktop_typing_refused"
	CodeDesktopNotTextField  = "desktop_not_text_field"
	CodeDesktopKeyRefused    = "desktop_key_refused"
	CodeDesktopTextTooLong   = "desktop_text_too_long"
	CodeDesktopUnknownAction = "desktop_unknown_action"
)

// DesktopTool is the kind of one desktop action.
type DesktopTool string

const (
	DesktopClick  DesktopTool = "click"
	DesktopType   DesktopTool = "type"
	DesktopKeys   DesktopTool = "keys"
	DesktopScroll DesktopTool = "scroll"
)

// MaxDesktopTextRunes is the longest text one desktop action types.
const MaxDesktopTextRunes = 2000

// DesktopActionInput holds the facts of one action, read for the window the
// call names just before it is forwarded.
type DesktopActionInput struct {
	Tool DesktopTool
	// Bundle and App are the bundle id and the name of the window's app, as
	// the service reports them. An empty bundle id is an app whose identity
	// could not be read.
	Bundle, App string
	// ManagedBrowser is true for a window of the Chrome process the product
	// launched for its apply and scan lanes.
	ManagedBrowser bool
	// NamedByUser is true when the user's own message of the turn names the
	// app (NamesApp).
	NamedByUser bool
	// Paused is the user's Pause on the Agent control page.
	Paused bool
	// Found is false when the index or point matched no control the latest
	// look of the window read.
	Found bool
	// Role and Labels are the control's accessibility role and its title,
	// description and value as the look read them.
	Role   string
	Labels []string
	// Keys is the chord of a keys action, such as "shift+tab".
	Keys string
	// Text is the text a type action would type.
	Text string
	// FocusRole is the role of the control the keyboard focus was last put in
	// by the turn's own click or typing in this window. It is empty when the
	// turn made no such action there, or pressed a key that can move the
	// focus since (MovesFocus): no look reads which control holds the focus.
	FocusRole string
}

// DesktopAction decides one desktop action. It refuses the apps the agent
// never acts in, apps the user did not name, a press of a control that reads
// as Submit, Withdraw, Discard, Delete, Pay, Buy or Send, any Return, pressed
// or inside typed text, and in the managed browser everything but a press the
// walk may make. No setting or caller lifts these rules.
func DesktopAction(in DesktopActionInput) Decision {
	const g = GateDesktopAction
	if in.Paused {
		return decide(g, Deny, CodeDesktopPaused, "desktop control is paused on the Agent control page")
	}
	if d := DesktopTarget(in); !d.Allowed() {
		return d
	}
	switch in.Tool {
	case DesktopScroll:
		return decide(g, Allow, CodeOK, "a scroll changes nothing")
	case DesktopKeys:
		return keysDecision(in)
	case DesktopClick:
		return desktopPress(in)
	case DesktopType:
		switch {
		case in.ManagedBrowser:
			return decide(g, Deny, CodeDesktopTypingRefused, "the agent types nothing in the apply browser; answers reach the form through the run's own questions")
		case !in.Found:
			return decide(g, Deny, CodeDesktopNoControl, "the text field is not one the latest look read; look again")
		case !textRoles[in.Role]:
			return decide(g, Deny, CodeDesktopNotTextField, "the control is a %s, not a text field", orUnknown(in.Role))
		case utf8.RuneCountInString(in.Text) > MaxDesktopTextRunes:
			return decide(g, Deny, CodeDesktopTextTooLong, "the text is over %d characters", MaxDesktopTextRunes)
		case typesAKey(in.Text):
			return decide(g, Deny, CodeDesktopKeyRefused, "the text carries a control character such as Return, Tab or Escape, which the field takes as that key; type plain text and press keys by name")
		}
		return decide(g, Allow, CodeOK, "typing into a text field of %s", appName(in))
	}
	return decide(g, Deny, CodeDesktopUnknownAction, "the action %q is not a desktop action", string(in.Tool))
}

// DesktopTarget decides whether the agent may act in a window's app at all,
// whatever the action: never in a refused app, and only in the managed browser
// or an app the user's message names. The windows the agent lists and looks at
// are the ones it passes, so it sees only what it may act in.
func DesktopTarget(in DesktopActionInput) Decision {
	const g = GateDesktopAction
	switch {
	case refusedApp(in.Bundle, in.ManagedBrowser):
		return decide(g, Deny, CodeDesktopAppRefused, "the agent never acts in %s", appName(in))
	case !in.ManagedBrowser && !in.NamedByUser:
		return decide(g, Deny, CodeDesktopAppNotNamed, "your message does not name %s, so the agent does not act in it", appName(in))
	}
	return decide(g, Allow, CodeOK, "the agent may act in %s", appName(in))
}

func desktopPress(in DesktopActionInput) Decision {
	const g = GateDesktopAction
	if !in.Found {
		return decide(g, Deny, CodeDesktopNoControl, "the point lands on no control the latest look read; nothing is pressed")
	}
	if anyMixedScript(in.Labels) {
		return decide(g, Deny, CodeMixedScriptLabel, "a label spells a word from more than one alphabet, so it cannot be trusted to say what it looks like; nothing is pressed")
	}
	kind := ClassifyControl(in.Labels...)
	switch {
	case kind == ControlSubmit || kind == ControlWithdraw || kind == ControlDiscard || destructiveLabel(in.Labels):
		return decide(g, Deny, CodeDesktopControl, "the agent never presses a control that reads as Submit, Withdraw, Discard, Delete, Pay, Buy or Send; an application is sent only by the apply chain's own Submit")
	case in.ManagedBrowser && kind != ControlNavigation:
		return decide(g, Deny, CodeDesktopWalkOnly, "in the apply browser the agent presses only Next, Continue, Review, Save and continue or an Apply opener")
	}
	return decide(g, Allow, CodeOK, "a press of a control the latest look read in %s", appName(in))
}

// Keys of a keys action. The apply browser gets only those that move focus or
// the view, so no key there picks an option, ticks a box or sends the form. A
// named app also gets the arrows, and the keys that edit only in a text field:
// on any other control Space presses it and Backspace or Delete can remove
// what is selected, and a key lands on whichever control holds the focus.
var (
	browserKeys = map[string]bool{"tab": true, "shift+tab": true, "escape": true, "pageup": true, "pagedown": true, "home": true, "end": true}
	appKeys     = map[string]bool{"up": true, "down": true, "left": true, "right": true}
	editKeys    = map[string]bool{"space": true, "backspace": true, "delete": true}
	focusKeys   = map[string]bool{"tab": true, "shift+tab": true, "escape": true}
)

// MovesFocus reports whether a chord can move the keyboard focus to another
// control, after which nothing says which control a key would land on.
func MovesFocus(keys string) bool { return focusKeys[NormalizeChord(keys)] }

func keysDecision(in DesktopActionInput) Decision {
	const g = GateDesktopAction
	chord := NormalizeChord(in.Keys)
	switch {
	case chord == "return" || chord == "enter" || strings.HasSuffix(chord, "+return") || strings.HasSuffix(chord, "+enter"):
		return decide(g, Deny, CodeDesktopKeyRefused, "the agent never presses Return; it could send a form")
	case browserKeys[chord]:
	case !in.ManagedBrowser && appKeys[chord]:
	case !in.ManagedBrowser && editKeys[chord] && textRoles[in.FocusRole]:
		return decide(g, Allow, CodeOK, "a key that edits the text field the agent last clicked or typed into")
	case !in.ManagedBrowser && editKeys[chord]:
		return decide(g, Deny, CodeDesktopKeyRefused, "the key %q acts on the focused control, and the agent's last click or typing in this window did not leave the focus in a text field; click the text field first", in.Keys)
	default:
		return decide(g, Deny, CodeDesktopKeyRefused, "the key %q is not one the agent may press here", in.Keys)
	}
	return decide(g, Allow, CodeOK, "a key that moves focus or the view")
}

// NormalizeChord lowercases a chord and spells its keys one way: "Shift+Tab",
// "shift + tab" and "SHIFT+TAB" are one chord, as are "Page Down" and
// "pagedown" or "esc" and "escape". The computer-use client sends this
// spelling, so the service presses the chord the gate decided on.
func NormalizeChord(s string) string {
	parts := strings.Split(strings.ToLower(s), "+")
	for i, p := range parts {
		p = strings.Join(strings.Fields(p), "")
		switch p {
		case "esc":
			p = "escape"
		case "arrowup", "uparrow":
			p = "up"
		case "arrowdown", "downarrow":
			p = "down"
		case "arrowleft", "leftarrow":
			p = "left"
		case "arrowright", "rightarrow":
			p = "right"
		case "spacebar":
			p = "space"
		case "pgup":
			p = "pageup"
		case "pgdn", "pgdown":
			p = "pagedown"
		}
		parts[i] = p
	}
	return strings.Join(parts, "+")
}

// typesAKey reports whether typed text carries a rune a field takes as a key
// rather than as text: a C0 or C1 control (Return, newline, Tab, Escape,
// Backspace) or a line or paragraph separator.
func typesAKey(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
}

// textRoles are the accessibility roles a type action may write into.
var textRoles = map[string]bool{"AXTextField": true, "AXTextArea": true, "AXSearchField": true, "AXComboBox": true}

// destructiveRE is the wording, beyond the application controls, of a press
// that cannot be taken back: deleting, paying, buying, sending. Han wordings
// are matched on the raw label because Chinese puts no space between words.
var (
	destructiveRE  = lazyregexp.New(`\b(?:delete|remove|trash|erase|pay|payment|purchase|buy|checkout|check out|place order|order now|send|transfer)\b`)
	destructiveHan = []string{
		"\u63d0\u4ea4", "\u64a4\u56de", "\u64a4\u9500", "\u5220\u9664", "\u79fb\u9664", // submit, withdraw, revoke, delete, remove
		"\u53d1\u9001", "\u652f\u4ed8", "\u4ed8\u6b3e", "\u8d2d\u4e70", "\u4e0b\u5355", "\u8f6c\u8d26", // send, pay, pay, buy, place order, transfer
	}
)

func destructiveLabel(labels []string) bool {
	for _, l := range labels {
		if destructiveRE.MatchString(foldText(l)) {
			return true
		}
		for _, w := range destructiveHan {
			if strings.Contains(l, w) {
				return true
			}
		}
	}
	return false
}

// refusedApps are the apps the agent never acts in, by bundle id: Applysmith,
// computer-use services, terminals and other apps that run commands or other
// agents, System Settings, and stores of passwords.
var (
	refusedApps = map[string]bool{
		"dev.applysmith.app": true, "ai.kimi.cu": true,
		"com.apple.terminal": true, "com.googlecode.iterm2": true, "dev.warp.warp-stable": true, "com.mitchellh.ghostty": true,
		"net.kovidgoyal.kitty": true, "org.alacritty": true, "io.alacritty": true, "com.github.wez.wezterm": true, "co.zeit.hyper": true,
		"com.stablyai.orca": true, "com.apple.scripteditor2": true, "com.apple.automator": true, "com.apple.shortcuts": true,
		"com.apple.activitymonitor": true, "com.apple.console": true, "com.apple.dt.xcode": true, "com.microsoft.vscode": true,
		"com.todesktop.230313mzl4w4u92": true, "dev.zed.zed": true,
		"com.anthropic.claudefordesktop": true, "com.openai.chat": true, "com.openai.codex": true,
		"com.apple.systempreferences": true, "com.apple.keychainaccess": true, "com.apple.passwords": true,
		"com.1password.1password": true, "com.agilebits.onepassword7": true, "com.bitwarden.desktop": true,
		"com.lastpass.lastpass": true, "org.keepassxc.keepassxc": true,
	}
	refusedPrefixes = []string{"com.jetbrains."}
	// PersonalCU has had more than one bundle id; every one ends so.
	refusedSuffixes = []string{".personal-cu"}
	chromeBundles   = map[string]bool{"com.google.chrome": true, "com.google.chrome.beta": true, "com.google.chrome.dev": true, "com.google.chrome.canary": true}
)

// refusedApp reports whether the agent never acts in the app. A Chrome that is
// not the managed browser is the everyday Chrome.
func refusedApp(bundle string, managed bool) bool {
	b := strings.ToLower(strings.TrimSpace(bundle))
	switch {
	case b == "":
		return true
	case chromeBundles[b]:
		return !managed
	case refusedApps[b]:
		return true
	}
	for _, p := range refusedPrefixes {
		if strings.HasPrefix(b, p) {
			return true
		}
	}
	for _, s := range refusedSuffixes {
		if strings.HasSuffix(b, s) {
			return true
		}
	}
	return false
}

func appName(in DesktopActionInput) string {
	if in.App != "" {
		return in.App
	}
	return orUnknown(in.Bundle)
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown app"
	}
	return s
}

// NamesApp reports whether a message names an app by one of its names: its
// name as the system shows it, or the last part of its bundle id. A Latin
// name must stand as a whole word; a name in Han script may sit inside a run
// of Han characters, because Chinese puts no space between words.
func NamesApp(message string, names ...string) bool {
	msg := foldText(message)
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if hasHanRune(n) {
			if strings.Contains(message, n) {
				return true
			}
			continue
		}
		if f := foldText(n); len([]rune(f)) >= 3 && ContainsWord(msg, f) {
			return true
		}
	}
	return false
}

// ContainsWord reports whether word occurs in s with no letter or digit
// directly before or after it.
func ContainsWord(s, word string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if (start == 0 || !isWordRune(before)) && (end == len(s) || !isWordRune(after)) {
			return true
		}
		from = start + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func hasHanRune(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
