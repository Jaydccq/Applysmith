package match

import "testing"

func TestEntryDefaults(t *testing.T) {
	e := Entry{}
	if e.EffectiveState() != StateActive || e.EffectiveCreatedBy() != CreatedByUser || !e.AbstainsWhenAmbiguous() {
		t.Fatalf("defaults: state %q, creator %q, abstain %v", e.EffectiveState(), e.EffectiveCreatedBy(), e.AbstainsWhenAmbiguous())
	}
	no := false
	if (Body{AbstainIfAmbiguous: &no}).AbstainsWhenAmbiguous() {
		t.Fatal("an explicit false must be kept")
	}
}

func TestClosedVocabularies(t *testing.T) {
	if len(Categories) != 20 || len(InputTypes) != 13 || len(Kinds) != 7 {
		t.Fatalf("sizes: %d categories, %d input types, %d kinds", len(Categories), len(InputTypes), len(Kinds))
	}
	if !ValidCategory("self-identification") || ValidCategory("hobbies") {
		t.Fatal("category membership")
	}
	if !ValidInputType("long_text") || ValidInputType("hologram") {
		t.Fatal("input type membership")
	}
	for _, k := range Kinds {
		if len(RequiredKeys(k)) > len(BodyKeys(k)) {
			t.Fatalf("%s requires more keys than it has", k)
		}
	}
}
