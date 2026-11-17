package scheduler

import (
	"strings"
	"testing"
	"time"
)

const ny = "America/New_York"

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func recurrence(t *testing.T, p Preset, dtstart string) *Recurrence {
	t.Helper()
	r, err := NewRecurrence(p, mustTime(t, dtstart))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func local(t *testing.T, zone string, at time.Time) string {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return at.In(loc).Format("2006-01-02 15:04 MST")
}

func TestScheduler_Recurrence_SpringForwardAndRepeatedHour(t *testing.T) {
	tests := []struct {
		name, clock, from, upto string
		want                    []string
	}{
		{"gap fires once after the gap", "02:30", "2026-03-07T00:00:00Z", "2026-03-10T00:00:00Z",
			[]string{"2026-03-07 02:30 EST", "2026-03-08 03:00 EDT", "2026-03-09 02:30 EDT"}},
		{"repeated hour fires once at the first 01:30", "01:30", "2026-10-31T12:00:00Z", "2026-11-03T00:00:00Z",
			[]string{"2026-11-01 01:30 EDT", "2026-11-02 01:30 EST"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := recurrence(t, Preset{Kind: PresetDaily, Time: tt.clock, Timezone: ny}, "2026-03-01T00:00:00Z")
			var got []string
			for _, at := range r.Between(mustTime(t, tt.from), mustTime(t, tt.upto), 20) {
				got = append(got, local(t, ny, at))
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScheduler_Recurrence_LocalTimeInZoneNotUTC(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetDaily, Time: "09:20", Timezone: ny}, "2026-07-01T00:00:00Z")
	next, ok := r.Next(mustTime(t, "2026-07-12T00:00:00Z"))
	if !ok || local(t, ny, next) != "2026-07-12 09:20 EDT" {
		t.Fatalf("next = %v %v", next, ok)
	}
	if next.UTC().Hour() != 13 {
		t.Fatalf("fired at %s UTC, want 13:20", next.UTC().Format("15:04"))
	}
}

func TestScheduler_Recurrence_FireAtDtstartIsReachable(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetDaily, Time: "07:30", Timezone: "UTC"}, "2026-05-04T07:30:00Z")
	got := r.Between(mustTime(t, "2026-05-04T07:29:59Z"), mustTime(t, "2026-05-04T07:30:00Z"), 5)
	if len(got) != 1 || !got[0].Equal(mustTime(t, "2026-05-04T07:30:00Z")) {
		t.Fatalf("got %v", got)
	}
	if before := r.Between(mustTime(t, "2026-05-01T00:00:00Z"), mustTime(t, "2026-05-04T07:29:00Z"), 5); len(before) != 0 {
		t.Fatalf("an occurrence before dtstart: %v", before)
	}
}

func TestScheduler_Recurrence_WeeklyWeekdays(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetWeekly, Time: "09:00", Weekdays: []string{"mon", "FRI"}, Timezone: ny}, "2026-06-01T00:00:00Z")
	var got []string
	for _, at := range r.Between(mustTime(t, "2026-06-01T00:00:00Z"), mustTime(t, "2026-06-15T00:00:00Z"), 20) {
		got = append(got, local(t, ny, at))
	}
	want := "2026-06-01 09:00 EDT|2026-06-05 09:00 EDT|2026-06-08 09:00 EDT|2026-06-12 09:00 EDT"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %v", got)
	}
}

func TestScheduler_Recurrence_RRuleMonthEnds(t *testing.T) {
	tests := []struct {
		name, rule, from, upto string
		want                   string
	}{
		{"last day", "FREQ=MONTHLY;BYMONTHDAY=-1;BYHOUR=18;BYMINUTE=0;BYSECOND=0", "2026-01-01T00:00:00Z", "2026-05-01T00:00:00Z",
			"2026-01-31 18:00|2026-02-28 18:00|2026-03-31 18:00|2026-04-30 18:00"},
		{"the 31st skips short months", "FREQ=MONTHLY;BYMONTHDAY=31;BYHOUR=6;BYMINUTE=0;BYSECOND=0", "2026-01-01T00:00:00Z", "2026-06-02T00:00:00Z",
			"2026-01-31 06:00|2026-03-31 06:00|2026-05-31 06:00"},
		{"leap day in a leap year", "FREQ=MONTHLY;BYMONTHDAY=29;BYHOUR=6;BYMINUTE=0;BYSECOND=0", "2028-01-01T00:00:00Z", "2028-04-01T00:00:00Z",
			"2028-01-29 06:00|2028-02-29 06:00|2028-03-29 06:00"},
		{"last weekday of the month", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;BYHOUR=17;BYMINUTE=0;BYSECOND=0", "2026-01-01T00:00:00Z", "2026-04-01T00:00:00Z",
			"2026-01-30 17:00|2026-02-27 17:00|2026-03-31 17:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := recurrence(t, Preset{Kind: PresetRRule, RRule: tt.rule, Timezone: "UTC"}, tt.from)
			var got []string
			for _, at := range r.Between(mustTime(t, tt.from).Add(-time.Second), mustTime(t, tt.upto), 20) {
				got = append(got, at.Format("2006-01-02 15:04"))
			}
			if strings.Join(got, "|") != tt.want {
				t.Fatalf("got %v, want %s", got, tt.want)
			}
		})
	}
}

func TestScheduler_Recurrence_RRuleAcrossDST(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetRRule, RRule: "FREQ=DAILY;BYHOUR=2;BYMINUTE=30;BYSECOND=0", Timezone: ny}, "2026-03-06T00:00:00Z")
	var got []string
	for _, at := range r.Between(mustTime(t, "2026-03-07T00:00:00Z"), mustTime(t, "2026-03-10T12:00:00Z"), 20) {
		got = append(got, local(t, ny, at))
	}
	want := "2026-03-07 02:30 EST|2026-03-08 03:00 EDT|2026-03-09 02:30 EDT|2026-03-10 02:30 EDT"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %v", got)
	}
}

func TestScheduler_Recurrence_EndedRule(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetRRule, RRule: "FREQ=DAILY;COUNT=3;BYHOUR=8;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}, "2026-05-01T00:00:00Z")
	all := r.Between(mustTime(t, "2026-04-01T00:00:00Z"), mustTime(t, "2026-12-01T00:00:00Z"), 20)
	if len(all) != 3 {
		t.Fatalf("got %d occurrences", len(all))
	}
	if r.Ended(all[1]) {
		t.Fatal("ended before the last occurrence")
	}
	if !r.Ended(all[2]) {
		t.Fatal("not ended after the last occurrence")
	}
}

func TestScheduler_Recurrence_UntilInstant(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetRRule, RRule: "FREQ=DAILY;UNTIL=20260503T120000Z;BYHOUR=8;BYMINUTE=0;BYSECOND=0", Timezone: ny}, "2026-05-01T00:00:00Z")
	got := r.Between(mustTime(t, "2026-04-30T00:00:00Z"), mustTime(t, "2026-06-01T00:00:00Z"), 20)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestScheduler_Recurrence_LongAnchoredSubDailyRule(t *testing.T) {
	r := recurrence(t, Preset{Kind: PresetRRule, RRule: "FREQ=MINUTELY;INTERVAL=10", Timezone: "UTC"}, "2024-01-01T00:03:00Z")
	next, ok := r.Next(mustTime(t, "2026-06-15T10:00:00Z"))
	if !ok || next.Format("15:04") != "10:03" {
		t.Fatalf("next = %v", next)
	}
}

func TestScheduler_Preset_DailyWeeklyValidation(t *testing.T) {
	now := mustTime(t, "2026-05-01T00:00:00Z")
	tests := []struct {
		name string
		p    Preset
		want string
	}{
		{"ok", Preset{Kind: PresetDaily, Time: "00:00", Timezone: "UTC"}, ""},
		{"hour", Preset{Kind: PresetDaily, Time: "24:00", Timezone: "UTC"}, "HH:MM"},
		{"format", Preset{Kind: PresetDaily, Time: "7:30", Timezone: "UTC"}, "HH:MM"},
		{"no weekdays", Preset{Kind: PresetWeekly, Time: "07:30", Timezone: "UTC"}, "at least one weekday"},
		{"bad weekday", Preset{Kind: PresetWeekly, Time: "07:30", Weekdays: []string{"funday"}, Timezone: "UTC"}, "unknown weekday"},
		{"kind", Preset{Kind: "monthly", Timezone: "UTC"}, "unknown preset kind"},
		{"daily with weekdays", Preset{Kind: PresetDaily, Time: "07:30", Weekdays: []string{"mon"}, Timezone: "UTC"}, "weekly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate(now, now)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestScheduler_Preset_UnknownTimezoneRejected(t *testing.T) {
	now := mustTime(t, "2026-05-01T00:00:00Z")
	for _, zone := range []string{"Mars/Base", "", "Local"} {
		err := Preset{Kind: PresetDaily, Time: "07:30", Timezone: zone}.Validate(now, now)
		if err == nil {
			t.Errorf("zone %q was accepted", zone)
		}
	}
}

func TestScheduler_Preset_TooFrequentRejected(t *testing.T) {
	now := mustTime(t, "2026-05-01T00:00:00Z")
	err := Preset{Kind: PresetRRule, RRule: "FREQ=MINUTELY;INTERVAL=2", Timezone: "UTC"}.Validate(now, now)
	if err == nil || !strings.Contains(err.Error(), "closer than 5 minutes") {
		t.Fatalf("err = %v", err)
	}
	if err := (Preset{Kind: PresetRRule, RRule: "FREQ=MINUTELY;INTERVAL=5", Timezone: "UTC"}).Validate(now, now); err != nil {
		t.Fatalf("every 5 minutes: %v", err)
	}
	if err := (Preset{Kind: PresetRRule, RRule: "FREQ=SECONDLY", Timezone: "UTC"}).Validate(now, now); err == nil {
		t.Fatal("every second was accepted")
	}
}

func TestScheduler_Preset_NoOccurrenceWithin366DaysRejected(t *testing.T) {
	now := mustTime(t, "2026-05-01T00:00:00Z")
	err := Preset{Kind: PresetRRule, RRule: "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29", Timezone: "UTC"}.Validate(now, now)
	if err == nil || !strings.Contains(err.Error(), "366 days") {
		t.Fatalf("err = %v", err)
	}
	past := Preset{Kind: PresetRRule, RRule: "FREQ=DAILY;COUNT=2", Timezone: "UTC"}
	if err := past.Validate(mustTime(t, "2026-01-01T00:00:00Z"), now); err == nil {
		t.Fatal("an ended rule was accepted")
	}
}

func TestScheduler_Preset_RruleSingleBareRuleOnly(t *testing.T) {
	now := mustTime(t, "2026-05-01T00:00:00Z")
	bad := map[string]string{
		"start line": "DTSTART:20260501T090000Z\nRRULE:FREQ=DAILY",
		"newline":    "FREQ=DAILY;\nBYHOUR=3",
		"dtstart":    "FREQ=DAILY;DTSTART=20260501T090000Z",
		"junk":       "EVERY DAY",
		"long":       "FREQ=DAILY;BYHOUR=" + strings.Repeat("1,", 3000) + "1",
	}
	for name, rule := range bad {
		if err := (Preset{Kind: PresetRRule, RRule: rule, Timezone: "UTC"}).Validate(now, now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := (Preset{Kind: PresetRRule, RRule: "RRULE:FREQ=DAILY;BYHOUR=3;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}).Validate(now, now); err != nil {
		t.Fatalf("RRULE: prefix: %v", err)
	}
}

// The input rules of a recurrence string: at most 4,096 characters after the
// surrounding space is trimmed, no line break inside, an optional RRULE:
// prefix that gives the same schedule as the bare rule.
func TestScheduler_Preset_RruleInputRules(t *testing.T) {
	loc := time.UTC
	start := mustTime(t, "2026-05-01T00:00:00Z")
	core := "FREQ=DAILY;BYHOUR=3;BYMINUTE=0;BYSECOND=0;X="
	at := func(n int) string { return core + strings.Repeat("a", n-len(core)) }
	if _, err := parseRule(at(maxRuleLength), loc, start); err == nil || strings.Contains(err.Error(), "longer than") {
		t.Errorf("4096 characters must pass the length rule (and fail on the junk part): %v", err)
	}
	if _, err := parseRule(at(maxRuleLength+1), loc, start); err == nil || !strings.Contains(err.Error(), "longer than 4096") {
		t.Errorf("4097 characters: %v", err)
	}
	if _, err := parseRule("  \t"+at(maxRuleLength)+" \n", loc, start); err == nil || strings.Contains(err.Error(), "longer than") || strings.Contains(err.Error(), "single line") {
		t.Errorf("surrounding space does not count: %v", err)
	}
	for _, bad := range []string{"FREQ=DAILY;\nBYHOUR=3", "FREQ=DAILY;\r\nBYHOUR=3", "RRULE:FREQ=DAILY\nRRULE:FREQ=WEEKLY"} {
		if _, err := parseRule(bad, loc, start); err == nil || !strings.Contains(err.Error(), "single line") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	bare := recurrence(t, Preset{Kind: PresetRRule, RRule: "FREQ=DAILY;BYHOUR=3;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}, "2026-05-01T00:00:00Z")
	prefixed := recurrence(t, Preset{Kind: PresetRRule, RRule: " RRULE:FREQ=DAILY;BYHOUR=3;BYMINUTE=0;BYSECOND=0\n", Timezone: "UTC"}, "2026-05-01T00:00:00Z")
	upto := mustTime(t, "2026-05-08T00:00:00Z")
	a, b := bare.Between(start, upto, 20), prefixed.Between(start, upto, 20)
	if len(a) != 7 || len(a) != len(b) || !a[3].Equal(b[3]) {
		t.Errorf("bare %v, prefixed %v", a, b)
	}
}
