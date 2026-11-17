package bind

import (
	"testing"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
)

// The wire carries the control's real kind: a custom listbox is not a select,
// because the matcher treats the first as a sample and the second as the whole list.
func TestBind_Engine_KindIsNeverDowngradedToSelect(t *testing.T) {
	for wire, want := range map[string]match.ControlKind{
		"select": match.KindSelect, "custom-listbox": match.KindCustomListbox, "radio-group": match.KindRadioGroup,
		"checkbox": match.KindCheckbox, "text": match.KindText,
	} {
		q := questionFromWire(engine.WireQuestion{Selector: "#a", Text: "t", Kind: wire, Options: []string{"A"}})
		if q.Kind != want {
			t.Errorf("%s became %q", wire, q.Kind)
		}
	}
}
