package match

import "testing"

func abFixed(id, answer string) Entry {
	return Entry{ID: id, Kind: KindFixed, Body: Body{Answer: answer}, Meta: Meta{Category: CategoryOther}}
}

func abOverrideOn(host string, os ...Override) map[string][]Override {
	return map[string][]Override{host: os}
}

func TestOverrides_LongestSuffixWins(t *testing.T) {
	b := Bank{Entries: []Entry{abFixed("x", "base")}, Overrides: map[string][]Override{
		"example.test":        {{ID: "x", Body: Body{Answer: "short"}, Fields: []string{"answer"}}},
		"boards.example.test": {{ID: "x", Body: Body{Answer: "long"}, Fields: []string{"answer"}}},
	}}
	tests := []struct{ host, want string }{
		{"eu.boards.example.test", "long"},
		{"boards.example.test", "long"},
		{"jobs.example.test", "short"},
		{"example.test", "short"},
		{"notexample.test", "base"},
		{"other.example.org", "base"},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			got := b.EntriesForHost(tc.host)
			if len(got) != 1 || got[0].Answer != tc.want {
				t.Fatalf("answer = %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestOverrides_HostKeyCaseInsensitive(t *testing.T) {
	b := Bank{Entries: []Entry{abFixed("x", "base")}, Overrides: abOverrideOn("example.test", Override{ID: "x", Body: Body{Answer: "site"}, Fields: []string{"answer"}})}
	if got := b.EntriesForHost("Jobs.Example.TEST")[0].Answer; got != "site" {
		t.Fatalf("answer = %q", got)
	}
}

func TestOverrides_KindChangeKeepsBaseMetadata(t *testing.T) {
	base := abFixed("x", "base")
	base.AnswerKey, base.Examples, base.InputTypes, base.Placeholder = "k", []string{"q"}, []string{InputText}, true
	o := Override{ID: "x", Kind: KindPickOption, Body: Body{Preferred: []string{"A"}, FallbackText: "A"}, Fields: []string{"preferred", "fallback_text"}}
	got, err := o.Apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPickOption || got.Preferred[0] != "A" || got.Answer != "" {
		t.Fatalf("body = %+v", got.Body)
	}
	if got.AnswerKey != "k" || got.Examples[0] != "q" || got.InputTypes[0] != InputText || !got.Placeholder || got.ID != "x" {
		t.Fatalf("metadata lost: %+v", got.Meta)
	}
}

func TestOverrides_InvalidKindChangeLeavesBase(t *testing.T) {
	b := Bank{Entries: []Entry{abFixed("x", "base")}, Overrides: abOverrideOn("example.test", Override{ID: "x", Kind: KindPickOption, Body: Body{}, Fields: nil})}
	got := b.EntriesForHost("example.test")
	if got[0].Kind != KindFixed || got[0].Answer != "base" {
		t.Fatalf("base changed: %+v", got[0])
	}
}

func TestEntriesForHost_RemovesArchivedAndLeavesBankUntouched(t *testing.T) {
	archived := abFixed("old", "x")
	archived.State = StateArchived
	b := Bank{Entries: []Entry{abFixed("a", "1"), archived}}
	got := b.EntriesForHost("example.test")
	if len(got) != 1 || got[0].ID != "a" || len(b.Entries) != 2 {
		t.Fatalf("got %d entries, bank has %d", len(got), len(b.Entries))
	}
}

func TestBank_OwnAnswer_OnlyAStoredEntryTheUserWrote(t *testing.T) {
	ack := Entry{ID: "privacy-ack", Kind: KindAutoAcknowledge, Meta: Meta{Category: CategoryOther}}
	stamped := abFixed("stamped", "x")
	stamped.StampedBody = true
	runCtx := abFixed("run-ctx", "Yes")
	runCtx.RunContext = true
	b := Bank{Entries: []Entry{abFixed("data-retention", "Yes"), ack, abFixed("terms-and-conditions-consent", "Yes"),
		abFixed(DerivedIDs[0], "x"), stamped, runCtx}}
	filled := func(id string, src Source, o Outcome) Result {
		return Result{Selector: "#q", Outcome: o, Answer: "Yes", EntryID: id, Source: src}
	}
	tests := []struct {
		name string
		r    Result
		want bool
	}{
		{"the user's own entry, filled", filled("data-retention", SourceEntry, OutcomeFilled), true},
		{"the user's own entry, filled for review", filled("data-retention", SourceEntry, OutcomeFilledReview), true},
		{"a model fill naming the entry", filled("data-retention", SourceEntry, OutcomeFilledModel), false},
		{"an abstain", filled("data-retention", SourceEntry, OutcomeAbstained), false},
		{"a composed answer", filled("data-retention", SourceComposed, OutcomeFilled), false},
		{"a form-text rule", filled("data-retention", SourceFormText, OutcomeFilled), false},
		{"a derived answer", filled("data-retention", SourceDerived, OutcomeFilled), false},
		{"an auto-acknowledge entry", filled("privacy-ack", SourceEntry, OutcomeFilled), false},
		{"a consent entry resolved as an acknowledgement", filled("terms-and-conditions-consent", SourceEntry, OutcomeFilled), false},
		{"a derived entry", filled(DerivedIDs[0], SourceEntry, OutcomeFilled), false},
		{"a body stamped at load time", filled("stamped", SourceEntry, OutcomeFilled), false},
		{"a per-run observation entry", filled("run-ctx", SourceEntry, OutcomeFilled), false},
		{"an entry the bank does not hold", filled("missing", SourceEntry, OutcomeFilled), false},
		{"no entry", filled("", SourceEntry, OutcomeFilled), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := b.OwnAnswer(tc.r); got != tc.want {
				t.Fatalf("OwnAnswer = %v, want %v", got, tc.want)
			}
		})
	}
}
