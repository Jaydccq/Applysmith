package documents

import "testing"

const summaryCV = `# Alex Example

## Summary

Full-stack engineer working across Go and SQL services.

## Experience

### Senior Engineer, Example Systems (2021-03 to present)

- Led the platform migration and cut incidents by 40% across 12 services
- Saved $1.2M per year in cloud spend by rightsizing AWS clusters
- Shipped the first public API for the billing service

### Engineer, Sample Works (2018-01 to 2021-02)

- Built the ingestion service used by twelve teams
- Reviewed third-party libraries and kept a record of licence terms

## Skills

- Languages: Go, SQL
`

func summaryGate(t *testing.T, profile, text string) Gate {
	t.Helper()
	m := manifestWith(t, profile, summaryCV, testPosting)
	d := docWith(m, "X1", "Led the platform migration", evID(t, m, "platform migration"))
	d.Profile = text
	return checkResume(m, d)
}

func profileErrors(g Gate) []Finding {
	var out []Finding
	for _, f := range g.Errors {
		if f.Code != "missing-project" {
			out = append(out, f)
		}
	}
	return out
}

// Scenario: A summary may name a role the user seeks. A role noun no source of
// the user holds is rejected unless the profile lists it as a target role.
func TestSummary_RoleNounComesFromTheUsersOwnSources(t *testing.T) {
	const withTarget = testProfile + "targets:\n  roles:\n    - title: Backend Engineer\n"
	const text = "Backend engineer who builds reliable services."
	if errs := profileErrors(summaryGate(t, withTarget, text)); len(errs) != 0 {
		t.Errorf("a target role title was refused: %+v", errs)
	}
	if g := summaryGate(t, testProfile, text); !hasCode(g, "unsupported-name") {
		t.Errorf("a role noun no source holds passed: %+v", g.Errors)
	}
	if g := summaryGate(t, withTarget, "Quantum engineer who builds reliable services."); !hasCode(g, "unsupported-name") {
		t.Errorf("a role that is not a target role passed: %+v", g.Errors)
	}
}

func TestSummary_ATargetRoleDoesNotGroundATechnology(t *testing.T) {
	const profile = testProfile + "targets:\n  roles:\n    - title: Kafka Engineer\n"
	m := manifestWith(t, profile, summaryCV, "Kafka Engineer\n\nRequirements:\n- Experience with Kafka and Go\n")
	d := docWith(m, "X1", "Led the platform migration", evID(t, m, "platform migration"))
	d.Profile = "Kafka engineer who builds reliable data services."
	if g := checkResume(m, d); !hasCode(g, "unsupported-name") {
		t.Errorf("a target role grounded a technology the posting watches: %+v", g.Errors)
	}
}

// Scenario: A summary states a result without its subject. One evidence line
// that holds the figure and the verb class carries it; anything else does not.
func TestSummary_AResultWithoutASubjectRestsOnOneEvidenceLine(t *testing.T) {
	cases := []struct {
		name, text string
		ok         bool
	}{
		{"same figure and verb", "Full-stack engineer who saved $1.2M per year.", true},
		{"synonym verb class", "Full-stack engineer who cut $1.2M per year.", true},
		{"another figure", "Full-stack engineer who saved $2.4M per year.", false},
		{"another rate", "Full-stack engineer who saved $1.2M per month.", false},
		{"verb the line lacks", "Full-stack engineer who mentored $1.2M per year.", false},
		{"no verb", "Full-stack engineer with $1.2M per year.", false},
		{"named wrong subject", "Full-stack engineer who saved $1.2M per year in incidents.", false},
		{"both figures from two lines", "Full-stack engineer who cut incidents by 40% and saved $1.2M per year.", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := profileErrors(summaryGate(t, testProfile, c.text))
			if c.ok && len(errs) != 0 {
				t.Fatalf("errors = %+v", errs)
			}
			if !c.ok && len(errs) == 0 {
				t.Error("passed, want rejected")
			}
		})
	}
}

// The relaxed subject belongs to the summary alone: a bullet names its subject.
func TestSummary_ABulletStillNeedsItsSubject(t *testing.T) {
	m := manifestWith(t, testProfile, summaryCV, testPosting)
	g := checkResume(m, docWith(m, "X1", "Saved $1.2M per year.", evID(t, m, "cloud spend")))
	if !hasCode(g, "unsupported-figure") {
		t.Errorf("a headless bullet figure passed: %+v", g.Errors)
	}
}

// Scenario: Firsts and superlatives are claims. "A first for the company" needs
// a source line that says it.
func TestGrounding_RankClaimsNeedEvidence(t *testing.T) {
	m := manifestWith(t, testProfile, summaryCV, testPosting)
	mig, api := evID(t, m, "platform migration"), evID(t, m, "first public API")
	cases := []struct {
		name, text, id string
		ok             bool
	}{
		{"a first for the company", "Led the platform migration and cut incidents by 40% across 12 services, a first for the company", mig, false},
		{"first-ever", "Led the first-ever platform migration", mig, false},
		{"the only", "Led the platform migration as the only engineer", mig, false},
		{"number one priority", "Led the platform migration, the #1 priority", mig, false},
		{"record-breaking", "Led the platform migration with record-breaking uptime", mig, false},
		{"company record", "Led the platform migration and set a company record", mig, false},
		{"best-ever", "Led the platform migration with best-ever uptime", mig, false},
		{"a cited first", "Shipped the first public API for the billing service", api, true},
		{"a first the line lacks", "Shipped the first public API for the billing service and the first mobile app", api, false},
		{"first-party is no claim", "Reviewed first-party and third-party libraries", evID(t, m, "third-party"), true},
		{"a record kept is no claim", "Kept a record of licence terms", evID(t, m, "third-party"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var entry = "X1"
			if c.id == evID(t, m, "third-party") {
				entry = "X2"
			}
			g := checkResume(m, docWith(m, entry, c.text, c.id))
			var errs []Finding
			for _, f := range g.Errors {
				if f.Code == "unsupported-figure" {
					errs = append(errs, f)
				}
			}
			if c.ok && len(errs) != 0 {
				t.Fatalf("errors = %+v", errs)
			}
			if !c.ok && len(errs) == 0 {
				t.Errorf("passed, want rejected: %+v", g.Errors)
			}
		})
	}
}
