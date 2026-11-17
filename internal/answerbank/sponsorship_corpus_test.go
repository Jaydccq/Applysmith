package answerbank

import (
	"bufio"
	"os"
	"testing"

	"applysmith/internal/answerbank/match"
)

// sponsorshipCorpusFillRate asks every question of the corpus under three fact
// states and counts the fills. The corpus holds the ATS wordings the project's
// tests were written from, so it measures what a stricter reading costs the
// common case.
func sponsorshipCorpusFillRate(t *testing.T) (filled, claimed, total int) {
	t.Helper()
	f, err := os.Open("testdata/sponsorship-questions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	states := []match.Facts{
		{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(true), RequiresSponsorshipFuture: boolPtr(true)},
		{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(false), RequiresSponsorshipFuture: boolPtr(false)},
		{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(false), RequiresSponsorshipFuture: boolPtr(true)},
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		q := match.Question{Selector: "#q", Text: sc.Text(), Kind: match.KindRadioGroup, Options: yesNo, Required: true}
		for _, facts := range states {
			resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts, Posting: match.Posting{Company: "Example Corp", Location: "Austin, TX"}, Questions: []match.Question{q}})
			if err != nil {
				t.Fatal(err)
			}
			total++
			r := resp.Results[0]
			if r.EntryID != "sponsorship-now-or-future" {
				continue
			}
			claimed++
			if r.Outcome == match.OutcomeFilled {
				filled++
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return filled, claimed, total
}
