package match

import "strings"

// pickOptionText is the text a pick-option entry types into a control with no
// options: its fallback_text, else its one preferred value. An empty answer
// is not one: returning it would claim the control and leave the page blank,
// and of several preferred values none is the typed one.
func pickOptionText(e *Entry) (string, bool) {
	if e == nil || e.Kind != KindPickOption {
		return "", false
	}
	if strings.TrimSpace(e.FallbackText) != "" {
		return e.FallbackText, true
	}
	if len(e.Preferred) == 1 && strings.TrimSpace(e.Preferred[0]) != "" {
		return e.Preferred[0], true
	}
	return "", false
}
