package match

import "testing"

func abPlaceholder(e Entry) Entry { e.Placeholder = true; return e }

func abIDs(gaps []Gap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.ID)
	}
	return out
}

func abSameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestNeedsAnswer_KindBodies(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  bool
	}{
		{"fixed empty", Entry{ID: "a", Kind: KindFixed}, true},
		{"fixed answered", Entry{ID: "a", Kind: KindFixed, Body: Body{Answer: "x"}}, false},
		{"pick empty", Entry{ID: "a", Kind: KindPickOption, Body: Body{Preferred: []string{}}}, true},
		{"pick fallback text", Entry{ID: "a", Kind: KindPickOption, Body: Body{FallbackText: "x"}}, false},
		{"compensation needs default_answer only", Entry{ID: "a", Kind: KindCompensationRange, Body: Body{Minimum: "1", Maximum: "2"}}, true},
		{"compensation answered", Entry{ID: "a", Kind: KindCompensationRange, Body: Body{DefaultAnswer: "1"}}, false},
		{"multi-select absent", Entry{ID: "a", Kind: KindMultiSelect}, true},
		{"multi-select empty list is an answer", Entry{ID: "a", Kind: KindMultiSelect, Body: Body{Selected: []string{}}}, false},
		{"language proficiency never", Entry{ID: "a", Kind: KindLanguageProficiency}, false},
		{"employer history never", Entry{ID: "a", Kind: KindEmployerHistory}, false},
		{"auto-acknowledge never", Entry{ID: "a", Kind: KindAutoAcknowledge}, false},
		{"placeholder with a body is answered", abPlaceholder(Entry{ID: "a", Kind: KindFixed, Body: Body{Answer: "x"}}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := len(NeedsAnswer(Bank{Entries: []Entry{tc.entry}}).Unanswered) == 1
			if got != tc.want {
				t.Fatalf("needs answer = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNeedsAnswer_ConditionalSlot(t *testing.T) {
	slot := Entry{ID: "contract-exclusion-reason", Kind: KindFixed}
	gate := func(answer string) Entry {
		return Entry{ID: "contract-exclusion", Kind: KindFixed, Body: Body{Answer: answer}}
	}
	pickGate := Entry{ID: "contract-exclusion", Kind: KindPickOption, Body: Body{Preferred: []string{"No", "Yes"}}}
	tests := []struct {
		name string
		gate *Entry
		want bool // slot listed
	}{
		{"No gate satisfies the slot", abPtr(gate("No")), false},
		{"Yes gate makes it a gap", abPtr(gate("Yes")), true},
		{"y gate makes it a gap", abPtr(gate(" y ")), true},
		{"true gate makes it a gap", abPtr(gate("True")), true},
		{"yesterday is not affirmative", abPtr(gate("Yesterday")), false},
		{"gate without answer", abPtr(gate("")), true},
		{"gate holding the marker", abPtr(gate(AskMe)), true},
		{"no gate entry", nil, true},
		{"pick-option gate reads preferred[0]", &pickGate, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Bank{Entries: []Entry{slot}}
			if tc.gate != nil {
				b.Entries = append(b.Entries, *tc.gate)
			}
			got := false
			for _, g := range NeedsAnswer(b).Unanswered {
				got = got || g.ID == slot.ID
			}
			if got != tc.want {
				t.Fatalf("slot listed = %v, want %v", got, tc.want)
			}
		})
	}
}

func abPtr[T any](v T) *T { return &v }

func TestNeedsAnswer_SlotThatAppliesOnlyAfterAnotherAnswer(t *testing.T) {
	gated := func(id string) Entry {
		return abPlaceholder(Entry{ID: id, Kind: KindFixed, Meta: Meta{Category: CategoryEducation}})
	}
	const listed, blocks = "listed", "blocks"
	tests := []struct {
		name, slot, gate, answer, gateCategory string
		want                                   string // "", listed or blocks
	}{
		{"a graduate has no expected graduation date", "expected-graduation-date", "currently-enrolled-student", "No", CategoryEducation, ""},
		{"a student does", "expected-graduation-date", "currently-enrolled-student", "Yes", CategoryEducation, blocks},
		{"an unanswered gate is asked first, the slot after it", "degree-focus-pursuing", "currently-enrolled-student", "", CategoryEducation, listed},
		{"an unanswered gate that does not stop setup leaves the slot blocking", "degree-focus-pursuing", "currently-enrolled-student", "", CategoryExperience, blocks},
		{"no OPT, no STEM extension", "stem-opt-extension-expiry", "currently-in-opt", "No", CategoryWorkAuthorization, ""},
		{"no sponsorship, no sponsorship kind", "sponsorship-type-description", "h1b-sponsorship", "No", CategoryWorkAuthorization, ""},
		{"permanent authorization has no expiry", "work-authorization-expiry", "permanent-work-authorization", "Yes", CategoryWorkAuthorization, ""},
		{"a temporary one does", "work-authorization-expiry", "permanent-work-authorization", "No", CategoryWorkAuthorization, blocks},
		{"an ask-me gate leaves the slot to the application too", "work-authorization-expiry", "permanent-work-authorization", AskMe, CategoryWorkAuthorization, listed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := Entry{ID: tc.gate, Kind: KindFixed, Body: Body{Answer: tc.answer}, Meta: Meta{Category: tc.gateCategory}}
			got := ""
			for _, g := range NeedsAnswer(Bank{Entries: []Entry{gated(tc.slot), gate}}).Unanswered {
				if g.ID == tc.slot {
					got = map[bool]string{false: listed, true: blocks}[g.Blocking]
				}
			}
			if got != tc.want {
				t.Fatalf("slot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNeedsAnswer_AskMeIsADecisionNotABlank(t *testing.T) {
	b := Bank{Entries: []Entry{
		{ID: "address-street", Kind: KindFixed, Body: Body{Answer: AskMe}, Meta: Meta{Category: CategoryAddress}},
		{ID: "address-zip", Kind: KindFixed, Meta: Meta{Category: CategoryAddress}},
	}}
	r := NeedsAnswer(b)
	if got := abIDs(r.Unanswered); !abSameIDs(got, []string{"address-zip"}) || !abSameIDs(abIDs(r.Blocking()), []string{"address-zip"}) {
		t.Fatalf("unanswered = %v", got)
	}
	if !abSameIDs(r.AskMe, []string{"address-street"}) {
		t.Fatalf("ask-me = %v", r.AskMe)
	}
}

func TestNeedsAnswer_NeedsReviewReportedSeparately(t *testing.T) {
	e := Entry{ID: "eeo-gender", Kind: KindPickOption, Body: Body{Preferred: []string{"Decline"}}, Meta: Meta{NeedsReview: true}}
	r := NeedsAnswer(Bank{Entries: []Entry{e}})
	if len(r.Unanswered) != 0 || !abSameIDs(abIDs(r.NeedsReview), []string{"eeo-gender"}) {
		t.Fatalf("report = %+v", r)
	}
}

func TestNeedsAnswer_BlockingGroups(t *testing.T) {
	mk := func(id, cat string, types ...string) Entry {
		return Entry{ID: id, Kind: KindFixed, Meta: Meta{Category: cat, InputTypes: types}}
	}
	b := Bank{Entries: []Entry{
		mk("full-name", CategoryIdentity),
		mk("sponsor", CategoryWorkAuthorization),
		mk("salary", CategoryCompensation),
		mk("years-experience-go", CategoryEmployment),
		mk("gate-reason", CategoryEmployment),
		mk("career-goal", CategoryIdentity, InputLongText),
		mk("skill", CategoryExperience),
		mk("relocate", CategoryAvailability),
	}}
	r := NeedsAnswer(b)
	if got := abIDs(r.Blocking()); !abSameIDs(got, []string{"full-name", "sponsor", "salary"}) {
		t.Fatalf("blocking = %v", got)
	}
	if len(r.Unanswered) != 8 {
		t.Fatalf("unanswered = %d", len(r.Unanswered))
	}
}
