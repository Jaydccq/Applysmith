package match

import (
	"testing"
)

func abInt(v int) *int { return &v }

func abStampBank() Bank {
	return Bank{Entries: []Entry{
		{ID: IDOfficeAttendanceOK, Kind: KindFixed, Body: Body{Answer: "Yes"}, Meta: Meta{NumericThreshold: &NumericThreshold{Extractor: ExtractorDaysPerWeekOnsite, AboveAnswer: "No"}}},
		{ID: IDWillingToTravel, Kind: KindPickOption, Body: Body{Preferred: []string{}}},
		{ID: "willing-to-relocate", Kind: KindFixed, Meta: Meta{LocationPolicy: &LocationPolicy{Predicate: PredicateWillingToWorkAt, IfTrue: "Yes", IfFalse: "No"}}},
		{ID: "plain", Kind: KindFixed},
	}}
}

func TestStamp_TravelCeilingKeepsYesNoAnswer(t *testing.T) {
	tests := []struct {
		percent int
		want    []string
	}{
		{25, []string{"Up to 25%", "Occasionally", "Yes"}},
		{50, []string{"Up to 50%", "Frequently", "Yes"}},
		{10, []string{"Up to 10%", "Rarely", "Yes"}},
		{5, []string{"Up to 5%", "Yes"}},
		{0, []string{"Up to 0%", "No"}},
	}
	for _, tc := range tests {
		got, _ := abStampBank().Stamp(Preferences{MaxTravelPercent: abInt(tc.percent)}).Find(IDWillingToTravel)
		if !abSameIDs(got.Preferred, tc.want) || !got.StampedBody {
			t.Fatalf("%d%%: preferred = %v stamped = %v", tc.percent, got.Preferred, got.StampedBody)
		}
	}
}

func TestStamp_MaxDaysOnTheThreshold(t *testing.T) {
	src := abStampBank()
	got, _ := src.Stamp(Preferences{MaxDaysInOffice: abInt(3)}).Find(IDOfficeAttendanceOK)
	if got.NumericThreshold.MaxValue == nil || *got.NumericThreshold.MaxValue != 3 {
		t.Fatalf("max value = %v", got.NumericThreshold.MaxValue)
	}
	if orig, _ := src.Find(IDOfficeAttendanceOK); orig.NumericThreshold.MaxValue != nil {
		t.Fatal("the source bank was modified")
	}
	// Without a declared threshold the entry is left alone.
	b := abStampBank()
	b.Entries[0].NumericThreshold = nil
	if e, _ := b.Stamp(Preferences{MaxDaysInOffice: abInt(3)}).Find(IDOfficeAttendanceOK); e.NumericThreshold != nil {
		t.Fatal("a threshold appeared")
	}
}

func TestStamp_RelocationPolicyAttachedToBoundEntries(t *testing.T) {
	pol := &RelocationPolicy{Default: RelocationClosed, Places: []string{"Example City"}}
	b := abStampBank().Stamp(Preferences{Relocation: pol})
	bound, _ := b.Find("willing-to-relocate")
	plain, _ := b.Find("plain")
	if bound.LocationPolicy.Resolved != pol || plain.LocationPolicy != nil {
		t.Fatal("the policy must attach to the bound entry only")
	}
}

func TestStamp_EntryLevelCareerLevel(t *testing.T) {
	prefs := Preferences{TargetSeniorities: []string{"intern", "Junior"}}
	t.Run("absent entry is created for the load only", func(t *testing.T) {
		got, ok := abStampBank().Stamp(prefs).Find(IDDesiredCareerLevel)
		if !ok || got.Preferred[0] != "Entry Level" || !got.StampedBody {
			t.Fatalf("entry = %+v, %v", got, ok)
		}
	})
	t.Run("blank entry prefers the entry-level labels", func(t *testing.T) {
		b := abStampBank()
		b.Entries = append(b.Entries, Entry{ID: IDDesiredCareerLevel, Kind: KindPickOption, Meta: Meta{Placeholder: true}})
		got, _ := b.Stamp(prefs).Find(IDDesiredCareerLevel)
		if got.Preferred[0] != "Entry Level" {
			t.Fatalf("preferred = %v", got.Preferred)
		}
		for _, g := range NeedsAnswer(b.Stamp(prefs)).Unanswered {
			if g.ID == IDDesiredCareerLevel {
				t.Fatal("a stamped career level is not a gap")
			}
		}
		if !abContains(abIDs(NeedsAnswer(b).Unanswered), IDDesiredCareerLevel) {
			t.Fatal("an unstamped blank career level is a gap")
		}
	})
	t.Run("the user's answer wins", func(t *testing.T) {
		b := abStampBank()
		b.Entries = append(b.Entries, Entry{ID: IDDesiredCareerLevel, Kind: KindPickOption, Body: Body{Preferred: []string{"Mid level"}}})
		got, _ := b.Stamp(prefs).Find(IDDesiredCareerLevel)
		if got.Preferred[0] != "Mid level" || got.StampedBody {
			t.Fatalf("preferred = %v", got.Preferred)
		}
	})
	for name, seniorities := range map[string][]string{"mixed levels": {"intern", "senior"}, "no roles": nil, "unset level": {""}} {
		t.Run(name+" derive nothing", func(t *testing.T) {
			if _, ok := abStampBank().Stamp(Preferences{TargetSeniorities: seniorities}).Find(IDDesiredCareerLevel); ok {
				t.Fatal("the career level was derived")
			}
		})
	}
}

func abContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestStamp_StatementFillsOnlyABlankEntryOfItsKind(t *testing.T) {
	yes := Statement{ID: "work-auth-us", Step: "work-authorization", Kind: KindFixed, Body: Body{Answer: "Yes"}}
	src := Bank{Entries: []Entry{
		abPlaceholder(Entry{ID: "work-auth-us", Kind: KindFixed, Meta: Meta{Category: CategoryWorkAuthorization}}),
		{ID: "mine", Kind: KindFixed, Body: Body{Answer: "No"}},
		{ID: "asked", Kind: KindFixed, Body: Body{Answer: AskMe}},
		{ID: "picked", Kind: KindPickOption},
		{ID: "gone", Kind: KindFixed, Meta: Meta{State: StateArchived}},
	}}
	stmts := []Statement{yes}
	for _, id := range []string{"mine", "asked", "picked", "gone"} {
		stmts = append(stmts, Statement{ID: id, Step: "cv", Kind: KindFixed, Body: Body{Answer: "Derived"}})
	}
	got := src.Stamp(Preferences{Statements: stmts})
	e, _ := got.Find("work-auth-us")
	if e.Answer != "Yes" || e.Placeholder || !e.StampedBody || e.DerivedFrom != "work-authorization" {
		t.Fatalf("blank entry = %+v", e)
	}
	if gaps := abIDs(NeedsAnswer(got).Unanswered); !abSameIDs(gaps, []string{"picked"}) {
		t.Fatalf("gaps = %v, want only the pick-option entry", gaps)
	}
	// The user's answer and the ask-me decision win, a pick-option entry is not
	// given a fixed answer, and an archived entry stays archived and blank.
	for id, want := range map[string]string{"mine": "No", "asked": AskMe, "picked": "", "gone": ""} {
		e, _ := got.Find(id)
		if e.Answer != want || e.StampedBody || e.DerivedFrom != "" {
			t.Fatalf("%s = %+v", id, e)
		}
	}
	if orig, _ := src.Find("work-auth-us"); orig.Answer != "" || orig.StampedBody {
		t.Fatal("the source bank was modified")
	}
}
