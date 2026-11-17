package documents

import (
	"testing"
)

// evidenceLine is the one line every quantity attack below is run against.
const evidenceLine = "Led the platform migration and cut incidents by 40% across 12 services"

func claimOK(claim string, evidence ...string) bool {
	c := corpusQuantities(evidence, nil)
	for _, q := range quantities(claim) {
		if !supported(q, c) {
			return false
		}
	}
	return true
}

// Scenario: Vague and relative magnitudes. Every token that states a
// magnitude, count, ratio or approximation is a quantity, and a draft cannot
// raise a figure into a word that sounds bigger.
func TestQuantity_VagueMagnitudesAreGroundedOrRejected(t *testing.T) {
	for _, c := range []struct {
		name, claim string
		ok          bool
	}{
		{"same figures", "Cut incidents by 40% across 12 services", true},
		{"half", "Led the platform migration and cut incidents by half", false},
		{"fivefold", "Led the platform migration and cut incidents fivefold", false},
		{"three times", "Cut incidents three times across 12 services", false},
		{"a dozen", "Cut incidents by 40% across a dozen teams", false},
		{"a dozen of the same subject", "Cut incidents by 40% across a dozen services", true},
		{"dozens", "Cut incidents across dozens of services", false},
		{"hundreds", "Led the platform migration for hundreds of services", false},
		{"several", "Cut incidents by 40% across several services", false},
		{"a few", "Cut incidents across a few services", false},
		{"order of magnitude", "Cut incidents by an order of magnitude", false},
		{"dramatically", "Dramatically cut incidents across 12 services", false},
		{"about", "Cut incidents by about 40% across 12 services", false},
		{"over", "Cut incidents by over 40% across 12 services", false},
		{"a plus is a weaker claim to drop", "Cut incidents by 40% across 12+ services", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := claimOK(c.claim, evidenceLine); got != c.ok {
				t.Errorf("claimOK(%q) = %v, want %v", c.claim, got, c.ok)
			}
		})
	}
}

// A word the evidence line itself uses grounds the same word, with the same
// subject, and nothing else.
func TestQuantity_TheSameWordInTheEvidenceGroundsIt(t *testing.T) {
	for _, c := range []struct {
		claim, evidence string
		ok              bool
	}{
		{"Significantly reduced incidents", "Significantly reduced incidents across 12 services", true},
		{"Dramatically reduced incidents", "Significantly reduced incidents across 12 services", false},
		{"Significantly reduced costs", "Significantly reduced incidents across 12 services", false},
		{"Supported hundreds of services", "Supported hundreds of services", true},
		{"Supported thousands of services", "Supported hundreds of services", false},
		{"Halved incidents", "Cut incidents by half", true},
		{"Cut incidents by 50%", "Cut incidents by half", true},
		{"Cut incidents by a third", "Cut incidents by half", false},
		{"Cut incidents by about 40%", "Cut incidents by about 40%", true},
		{"Cut incidents by 40%", "Cut incidents by about 40%", false},
		{"Cut incidents by over 40%", "Cut incidents by 40%+", true},
		{"Worked with a dozen teams", "Worked with twelve teams", true},
		{"Worked with a dozen teams", "Worked with 12 services", false},
		{"Cut incidents fivefold", "Cut incidents 5x", true},
		{"Cut incidents tenfold", "Cut incidents 5x", false},
	} {
		if got := claimOK(c.claim, c.evidence); got != c.ok {
			t.Errorf("claimOK(%q | %q) = %v, want %v", c.claim, c.evidence, got, c.ok)
		}
	}
}

// Scenario: Unit synonyms. The spelling of a unit is not a difference.
func TestQuantity_UnitSynonymsAreOneUnit(t *testing.T) {
	for _, c := range []struct {
		claim, evidence string
		ok              bool
	}{
		{"Saved $1.2M a year", "Saved $1.2M per year", true},
		{"Saved $1.2M a month", "Saved $1.2M per year", false},
		{"Saved $1.2M", "Saved $1.2M per year", false},
		{"Saved $1.2M", "Saved 1.2 million dollars", true},
		{"Saved $1,200,000", "Saved $1.2M", true},
		{"Saved $1.2M", "Saved $1.3M", false},
		{"Cut time by 5 hours", "Cut time by 5 h", true},
		{"Cut time by 5 hours", "Cut time by 300 min", true},
		{"Cut time by 5 hours", "Cut time by 5 days", false},
		{"Cut time by 800 ms", "Cut time by 0.8 s", true},
		{"Cut time by 2 s", "Cut time by 2 min", false},
		{"Cut cost by 40%", "Cut cost by 40 percent", true},
		{"Cut cost by forty percent", "Cut cost by 40%", true},
		{"Cut cost by 40", "Cut cost by 40%", false},
		{"Served 5K users", "Served 5 thousand users", true},
		{"Served 5K users", "Served 5K customers", false},
		{"Served 2.5M users", "Served 2.5 million users", true},
		{"Grew users 3x", "Grew users 3-fold", true},
		{"Grew users 2x", "Doubled users", true},
		{"Handled 2.5M requests per day", "Handled 2.5M daily requests", true},
		{"Handled 2.5M requests per week", "Handled 2.5M requests per day", false},
	} {
		if got := claimOK(c.claim, c.evidence); got != c.ok {
			t.Errorf("claimOK(%q | %q) = %v, want %v", c.claim, c.evidence, got, c.ok)
		}
	}
}

// Scenario: Subject matching tolerates an extra modifier. The head noun and
// the verb must match; an added adjective is allowed only where the evidence
// line or the user's sources use it.
func TestQuantity_SubjectToleratesAnExtraModifierOnlyWhenTheSourcesUseIt(t *testing.T) {
	line := "Led the platform migration and cut incidents by 40% across 12 services"
	for _, c := range []struct {
		name, claim string
		vocab       []string
		ok          bool
	}{
		{"modifier from the same line", "Led the platform migration, cutting platform incidents by 40% across 12 services", nil, true},
		{"modifier from the same line, other order", "Cutting migration incidents by 40% across 12 services", nil, true},
		{"modifier from another source", "Cut security incidents by 40% across 12 services", []string{"Ran the security audit"}, true},
		{"modifier from no source", "Cut security incidents by 40% across 12 services", nil, false},
		{"head noun swapped", "Cut costs by 40% across 12 services", []string{"cost control"}, false},
		{"head noun is only a modifier in the evidence", "Cut platform by 40% across 12 services", nil, false},
		{"modifier turned into the head", "Cut migration by 40% across 12 services", nil, false},
		{"verb of another class", "Hired 12 services", nil, false},
		{"opposite verb", "Increased incidents by 40% across 12 services", nil, false},
		{"synonym verb", "Reduced incidents by 40% across 12 services", nil, true},
		{"verb ing form", "Cutting incidents by 40% across 12 services", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cp := corpusQuantities([]string{line}, append([]string{line}, c.vocab...))
			ok := true
			for _, q := range quantities(c.claim) {
				ok = ok && supported(q, cp)
			}
			if ok != c.ok {
				t.Errorf("claim %q = %v, want %v", c.claim, ok, c.ok)
			}
		})
	}
}

func TestQuantity_VerbClassesAndCountedNouns(t *testing.T) {
	for _, c := range []struct {
		claim, evidence string
		ok              bool
	}{
		{"Coached four engineers", "Mentored four engineers", true},
		{"Hired four engineers", "Mentored four engineers", false},
		{"Developed 5 pipelines", "Built 5 pipelines", true},
		{"Deleted 5 pipelines", "Built 5 pipelines", false},
		{"Built 5 pipelines", "Built 5 dashboards", false},
		{"With pages per on-call week falling from 31 to 9", "Pages per on-call week fell from 31 to 9", true},
		{"Pages per on-call week rose from 31 to 9", "Pages per on-call week fell from 31 to 9", false},
		// "caught" is a verb, not the subject of the figure.
		{"Catching 60 breaking changes before release", "Added contract tests, which caught 60 breaking changes before release", true},
		{"Catching 70 breaking changes before release", "Added contract tests, which caught 60 breaking changes before release", false},
	} {
		if got := claimOK(c.claim, c.evidence); got != c.ok {
			t.Errorf("claimOK(%q | %q) = %v, want %v", c.claim, c.evidence, got, c.ok)
		}
	}
}

func TestQuantity_ExtractionOfRatiosAndFractions(t *testing.T) {
	type want struct {
		value float64
		unit  string
	}
	for _, c := range []struct {
		text string
		want []want
	}{
		{"cut incidents by half", []want{{0.5, "x"}}},
		{"cut incidents by a third", []want{{1.0 / 3, "x"}}},
		{"fivefold", []want{{5, "x"}}},
		{"a dozen teams", []want{{12, "team"}}},
		{"3 times faster", []want{{3, "x"}}},
		{"the third-party vendor", nil},
		{"p99 latency of S3 on ISO-27001", nil},
		{"from 12 to 4 minutes", []want{{720000, "time"}, {240000, "time"}}},
		{"$1.2M a year", []want{{1.2e6, "$"}}},
		{"5 h and 20 min", []want{{1.8e7, "time"}, {1.2e6, "time"}}},
	} {
		var have []want
		for _, q := range quantities(c.text) {
			have = append(have, want{q.value, q.unit})
		}
		if len(have) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.text, have, c.want)
			continue
		}
		for i := range have {
			if !near(have[i].value, c.want[i].value) || have[i].unit != c.want[i].unit {
				t.Errorf("%q: got %v, want %v", c.text, have, c.want)
			}
		}
	}
}

// Scenario: A draft's first word. A bullet opens with a verb, and a hyphenated
// verb prefix or "Responsible for" is not a name.
func TestGrounding_BulletOpeningsAreNotNames(t *testing.T) {
	m := fixtureManifest(t, fixtureCV, fixtureJD, "Staff Backend Engineer")
	mig := evID(t, m, "platform migration")
	for _, opener := range []string{"Responsible for", "Co-led", "Re-architected", "Rebuilt", "Redesigned", "Pre-empted", "De-risked", "Led", "Accountable for", "Spearheaded", "Co-designed", "Reworked"} {
		text := opener + " the platform migration that cut incidents by 40% across 12 services"
		g := checkBulletOnly(m, "X1", text, mig)
		if hasCode(g, "unsupported-name") {
			t.Errorf("%q: %+v", text, g.Errors)
		}
	}
	// A product name in the same position is still a name.
	for _, text := range []string{"Zorblax rollout: the platform migration that cut incidents by 40% across 12 services", "Co-Zorblax migration cut incidents by 40% across 12 services"} {
		if g := checkBulletOnly(m, "X1", text, mig); !hasCode(g, "unsupported-name") {
			t.Errorf("%q: %+v", text, g.Errors)
		}
	}
}

// Scenario: Counts of the letter's own parts. A vague or small count is a claim
// only when it is bound to a measurable subject.
func TestQuantity_CountsOfTheLettersOwnPartsAreNotClaims(t *testing.T) {
	for _, c := range []struct {
		name, claim string
		ok          bool
	}{
		{"a few examples", "A few examples follow.", true},
		{"two examples", "Two examples of that work are described here.", true},
		{"a few results", "A few results from that work are described here.", true},
		{"several points", "Several points matter to me.", true},
		{"the first, alone", "I will start with the first.", true},
		{"the first week", "I can contribute from the first week.", true},
		{"a few services", "Cut incidents across a few services", false},
		{"two teams", "Led two teams", false},
		{"by a few", "Cut incidents by a few", false},
		{"points as a measure", "Improved the score by 3 points", false},
		{"I was the first", "I was the first.", false},
		{"a first-ever", "I built a first-ever pipeline.", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := claimOK(c.claim, evidenceLine); got != c.ok {
				t.Errorf("claimOK(%q) = %v, want %v", c.claim, got, c.ok)
			}
		})
	}
}

// Scenario: "used by" and "serving" before a counted figure say whom the
// work is for, as "for" does, and a rate word behind a new phrase is not the
// figure's rate. Quoted from refused drafts of the letter
// measurement; the counted noun still has to match and another verb still
// needs its evidence.
func TestQuantity_UsageVerbsBeforeACountedFigure(t *testing.T) {
	const runs = "Runs the internal deployment platform for forty product teams."
	const pipeline = "Replaced a hand-run release process with a self-service deployment pipeline; forty product teams now ship on their own and median lead time fell from two days to forty minutes."
	for _, c := range []struct {
		claim, evidence string
		ok              bool
	}{
		{"Replaced a hand-run release process with a self-service deployment pipeline used by forty product teams, cutting median lead time from two days to forty minutes.", pipeline, true},
		{"I bring experience across Go, Python and PostgreSQL, having run the internal deployment platform used by forty product teams at Northwind Labs.", runs, true},
		{"I have run systems that other engineering teams depend on daily, including an internal deployment platform serving forty product teams.", runs, true},
		{"Built a platform used by forty services.", runs, false},
		{"Praised by four engineers for the incident review.", "Led the weekly incident review and mentored four engineers", false},
		// "weekly" belongs to the phrase after "through", not to the count.
		{"I have mentored four engineers through weekly incident review, and I want to keep growing as an engineer.", "Led the weekly incident review and mentored four engineers; pages per on-call week fell from 31 to 9.", true},
		{"Mentored four engineers weekly.", "Led the weekly incident review and mentored four engineers; pages per on-call week fell from 31 to 9.", false},
		// A preposition after a figure is not its counted noun ("9 through").
		{"Reduced pages per on-call week from 31 to 9 through incident review and mentoring.", "Led the weekly incident review and mentored four engineers; pages per on-call week fell from 31 to 9.", true},
		{"Reduced pages per on-call week from 31 to 9 services.", "Led the weekly incident review and mentored four engineers; pages per on-call week fell from 31 to 9.", false},
	} {
		if got := claimOK(c.claim, c.evidence); got != c.ok {
			t.Errorf("%q: supported = %v, want %v", c.claim, got, c.ok)
		}
	}
}

// A letter restates a result in its own words (captured drafts the gate
// refused although every figure is in the cited line): a verb's irregular past
// is the verb, "removing" is a reduction as "eliminating" is, a noun of change
// is read like its verb so "a 22% improvement in throughput" is about
// throughput (it names no direction, so either direction restates it), and
// "backed by" carries no claim of its own. A different verb, subject or figure
// still fails.
func TestQuantity_ALetterMayRestateTheResultInItsOwnWords(t *testing.T) {
	const ingest = "Orchestrated batch jobs on Linux, achieving a 22% improvement in processing throughput across multiple regions."
	const outbox = "Architected an event-driven system with Redis write-behind caching, eliminating 70% of database write contention."
	const search = "Designed a retrieval pipeline over 400k documents with nightly re-indexing, holding p95 query latency under 180 ms."
	for _, c := range []struct {
		claim, evidence string
		ok              bool
	}{
		{"Orchestrated batch jobs on Linux, improving processing throughput by 22% across multiple regions.", ingest, true},
		{"Improved throughput by 22%.", ingest, true},
		{"Improved throughput by 25%.", ingest, false},
		{"Improved latency by 22%.", ingest, false},
		{"Increased throughput by 22%.", ingest, true},
		{"Removing 70% of database write contention.", outbox, true},
		{"Cutting 70% of database write contention.", outbox, true},
		{"Adding 70% of database write contention.", outbox, false},
		{"Removing 70% of database read contention.", outbox, false},
		{"A retrieval pipeline that held p95 query latency under 180 ms.", search, true},
		{"A retrieval pipeline that kept p95 query latency under 180 ms.", search, false},
		{"An assistant backed by a retrieval pipeline over 400k documents.", search, true},
		{"An assistant backed by a retrieval pipeline over 500k documents.", search, false},
	} {
		if got := claimOK(c.claim, c.evidence); got != c.ok {
			t.Errorf("claimOK(%q | %q) = %v, want %v", c.claim, c.evidence, got, c.ok)
		}
	}
}
