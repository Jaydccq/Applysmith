// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"math"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

// The owner workspace's profile held a three-month internship and, after
// school, a job begun five months before: the span from one to the other read
// as 3 years, so a "3+ years" must-have was never an experience gate. The
// profile's years count the time employed, never the gaps between jobs, and
// overlapping jobs count once.
func TestEvaluate_ProfileYearsCountTheTimeEmployedNotTheGaps(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	years := func(jobs ...workspace.Employment) float64 {
		var p Profile
		p.History.Employment = jobs
		y := p.Years(now)
		if y == nil {
			t.Fatalf("no years for %+v", jobs)
		}
		return *y
	}
	job := func(start, end string) workspace.Employment {
		return workspace.Employment{Employer: "E", Start: start, End: end}
	}
	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"an internship, school, then a job", years(job("2023-07", "2023-09"), job("2026-06", "present")), 7.0 / 12},
		{"one job", years(job("2022-11", "")), 4},
		{"overlapping jobs count once", years(job("2020-11", "2022-11"), job("2021-11", "2023-11")), 3},
	}
	for _, c := range cases {
		if math.Abs(c.got-c.want) > 0.02 {
			t.Errorf("%s: %.2f years, want %.2f", c.name, c.got, c.want)
		}
	}
}
