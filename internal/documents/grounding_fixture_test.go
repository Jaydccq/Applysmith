package documents

import (
	"testing"

	"applysmith/internal/workspace"
)

// fixtureCV is the synthetic CV of the persona. Every legitimate bullet below
// is a restatement of one of its lines.
const fixtureCV = `# Alex Example

## Summary

Backend engineer working across Go, C++ and C# services.

## Experience

### Senior Engineer, Example Systems (2021-03 to present)

- Led the platform migration and cut incidents by 40% across 12 services
- Reduced p99 latency from 800 ms to 120 ms for the checkout API
- Built a C++ pricing engine and a C# billing service on .NET 6 used by 3 product teams
- Grew the Node.js gateway to handle 2.5M requests per day
- Mentored four engineers in code review and design
- Doubled deployment frequency by moving CI to GitHub Actions
- Saved $1.2M per year in cloud spend by rightsizing AWS clusters
- Migrated 14 Kafka consumers from Python 3.8 to Go, cutting CPU use by 35%
- Co-led a team of 6 engineers through the PostgreSQL 14 upgrade
- Reduced on-call pages by half after adding Prometheus alerts and runbooks

### Engineer, Sample Works (2018-01 to 2021-02)

- Built the ingestion service in Golang used by twelve teams
- Owned on-call for a 99.9% availability target
- Cut report time from 5 h to 20 min using a 3-step pipeline in Go & SQL
- Processed 5K+ survey files per day with a Redis-backed queue
- Shrank Docker image size by 60% and build time from 12 min to 4 min
- Introduced Terraform modules that provisioned 30 environments in under 10 minutes
- Wrote 200+ integration tests, lifting coverage from 55% to 85%
- Onboarded 5 new hires and ran weekly design reviews

## Projects

### Reef Survey Tool

- Published the tool as open source with 300 GitHub stars
- Tuned SQL queries to run 3x faster on a 50 GB dataset
- Added Grafana dashboards tracking 15 metrics across 4 services
`

const fixtureJD = `Staff Backend Engineer at Example Corp

Requirements:
- 5+ years of experience building backend services in Go or C++
- Experience with Kafka, PostgreSQL and Kubernetes
- Ability to mentor engineers and lead design reviews

Nice to have:
- Experience with ClickHouse and Terraform
`

// fixtureManifest selects every line, so a bullet is judged on its wording and
// not on what the page budget kept.
func fixtureManifest(t *testing.T, cv, jd, role string) Manifest {
	t.Helper()
	p, rep := workspace.ParseProfile([]byte(testProfile))
	if !rep.OK() {
		t.Fatal(rep)
	}
	m, err := buildManifest(sourceSet{Profile: p, ProfileBytes: []byte(testProfile), CV: cv, Posting: jd, Company: "Example Corp", Role: role}, ModeTailored, 2)
	if err != nil {
		t.Fatal(err)
	}
	m.Selected = nil
	for _, e := range m.Evidence {
		m.Selected = append(m.Selected, e.ID)
	}
	must(t, m.Freeze())
	return m
}

// fixtureBullet is a bullet and the evidence line it restates.
type fixtureBullet struct{ entry, from, text string }

// legitBullets are restatements an honest model draft makes: reordered, with a
// synonym verb, a spelling of the unit or an extra modifier the CV uses
// elsewhere. Each is grounded in the CV and must pass.
var legitBullets = []fixtureBullet{
	{"X1", "platform migration", "Led the platform migration and cut incidents by 40% across 12 services"},
	{"X1", "platform migration", "Cut incidents by 40% across 12 services by leading the platform migration"},
	{"X1", "platform migration", "Led the platform migration, cutting platform incidents by 40% across 12 services"},
	{"X1", "platform migration", "Reduced incidents by 40% across twelve services"},
	{"X1", "platform migration", "Co-led the platform migration that cut incidents by 40% across 12 services"},
	{"X1", "platform migration", "Responsible for the platform migration that cut incidents by 40% across 12 services"},
	{"X1", "platform migration", "Re-architected the platform and cut incidents by forty percent across 12 services"},
	{"X1", "p99 latency", "Reduced p99 latency from 800 ms to 120 ms for the checkout API"},
	{"X1", "p99 latency", "Cut checkout API p99 latency from 800ms to 120ms"},
	{"X1", "p99 latency", "Lowered p99 latency from 0.8 s to 0.12 s on the checkout API"},
	{"X1", "pricing engine", "Built a C++ pricing engine and a C# billing service on .NET 6 used by 3 product teams"},
	{"X1", "pricing engine", "Developed a C# billing service on .NET 6 and a C++ pricing engine for 3 product teams"},
	{"X1", "gateway", "Grew the Node.js gateway to handle 2.5M requests per day"},
	{"X1", "gateway", "Scaled the Node.js gateway to 2.5M daily requests"},
	{"X1", "gateway", "Scaled the Node.js gateway to serve 2,500,000 requests a day"},
	{"X1", "Mentored four", "Mentored 4 engineers in code review and design"},
	{"X1", "Mentored four", "Coached four engineers in design and code review"},
	{"X1", "Doubled", "Doubled deployment frequency by moving CI to GitHub Actions"},
	{"X1", "Doubled", "Increased deployment frequency 2x by moving CI to GitHub Actions"},
	{"X1", "cloud spend", "Saved $1.2M a year in cloud spend by rightsizing AWS clusters"},
	{"X1", "cloud spend", "Rightsized AWS clusters, saving $1.2M per year in cloud spend"},
	{"X1", "cloud spend", "Cut annual cloud spend by $1.2M through AWS cluster rightsizing"},
	{"X1", "Kafka consumers", "Migrated 14 Kafka consumers from Python 3.8 to Go, cutting CPU use by 35%"},
	{"X1", "Kafka consumers", "Cut CPU use by 35% by migrating 14 Kafka consumers from Python 3.8 to Go"},
	{"X1", "PostgreSQL 14", "Co-led a team of 6 engineers through the PostgreSQL 14 upgrade"},
	{"X1", "PostgreSQL 14", "Led six engineers through the PostgreSQL 14 upgrade"},
	{"X1", "on-call pages", "Halved on-call pages after adding Prometheus alerts and runbooks"},
	{"X1", "on-call pages", "Reduced on-call pages by half with Prometheus alerts and runbooks"},
	{"X2", "ingestion service", "Built the ingestion service in Go used by 12 teams"},
	{"X2", "ingestion service", "Developed the Golang ingestion service used by twelve teams"},
	{"X2", "99.9%", "Owned on-call for a 99.9% availability target"},
	{"X2", "99.9%", "Owned on-call to meet a 99.9 percent availability target"},
	{"X2", "report time", "Cut report time from 5 hours to 20 minutes using a 3-step pipeline in Go and SQL"},
	{"X2", "report time", "Reduced report time from 5 h to 20 min with a 3-step Go & SQL pipeline"},
	{"X2", "survey files", "Processed 5K+ survey files per day with a Redis-backed queue"},
	{"X2", "survey files", "Processed 5,000+ survey files a day through a Redis-backed queue"},
	{"X2", "Docker image", "Shrank Docker image size by 60% and build time from 12 min to 4 min"},
	{"X2", "Docker image", "Cut Docker image size by 60 percent and build time from 12 to 4 minutes"},
	{"X2", "Terraform modules", "Introduced Terraform modules that provisioned 30 environments in under 10 minutes"},
	{"X2", "integration tests", "Wrote 200+ integration tests, lifting coverage from 55% to 85%"},
	{"X2", "integration tests", "Raised test coverage from 55% to 85% with 200+ integration tests"},
	{"X2", "Onboarded", "Onboarded 5 new hires and ran weekly design reviews"},
}

var legitProjectBullets = []fixtureBullet{
	{"P", "open source", "Published the tool as open source with 300 GitHub stars"},
	{"P", "3x faster", "Tuned SQL queries to run 3x faster on a 50 GB dataset"},
	{"P", "3x faster", "Made SQL queries three times faster on a 50 GB dataset"},
	{"P", "Grafana", "Added Grafana dashboards tracking 15 metrics across 4 services"},
}

// Scenario: Quantified claim restated. The false-rejection rate on realistic
// bullets is measured here; a rejection of a grounded bullet drops it from the
// draft, so every miss costs the user a line of their own CV.
func TestGrounding_RealisticBulletsFromACVPass(t *testing.T) {
	m := fixtureManifest(t, fixtureCV, fixtureJD, "Staff Backend Engineer")
	project := m.SelectedProjects[0]
	all := append(append([]fixtureBullet{}, legitBullets...), legitProjectBullets...)
	passed := 0
	for _, b := range all {
		entry := b.entry
		if entry == "P" {
			entry = project
		}
		g := checkBulletOnly(m, entry, b.text, evID(t, m, b.from))
		if g.OK() {
			passed++
			continue
		}
		t.Errorf("rejected %q: %+v", b.text, g.Errors)
	}
	t.Logf("realistic bullets: %d of %d pass (%.0f%%)", passed, len(all), 100*float64(passed)/float64(len(all)))
	if len(all) < 30 {
		t.Fatalf("the fixture set has %d bullets; at least 30 are required", len(all))
	}
}

// checkBulletOnly gates one bullet of one entry; the other entries of the
// draft are left out, which the gate reports as missing and this ignores.
func checkBulletOnly(m Manifest, entry, text string, ids ...string) Gate {
	d := ResumeDoc{Schema: docSchema, ManifestHash: m.Hash, Language: "en"}
	draft := EntryDraft{Entry: entry, Bullets: []Bullet{{Text: text, Evidence: ids}}}
	if en, _ := m.entry(entry); en.Kind == EntryProject {
		d.Projects = []EntryDraft{draft}
	} else {
		d.Experience = []EntryDraft{draft}
	}
	g := checkResume(m, d)
	g.Errors = profileErrors(g)
	return g
}

// A line of the CV always grounds itself: the deterministic builder copies
// them, so a gate that rejects its own source line would reject every draft.
func TestGrounding_EveryCVLineGroundsItself(t *testing.T) {
	m := fixtureManifest(t, fixtureCV, fixtureJD, "Staff Backend Engineer")
	for _, e := range m.Evidence {
		if e.Entry == "" {
			continue
		}
		entry := e.Entry
		g := checkBulletOnly(m, entry, e.Text, e.ID)
		if !g.OK() {
			t.Errorf("%q does not ground itself: %+v", e.Text, g.Errors)
		}
	}
	d := deterministicResume(m, "en")
	if g := checkResume(m, d); !g.OK() {
		t.Errorf("deterministic résumé: %+v", g.Errors)
	}
}

// holdoutBullets were written after the first set, without looking at the
// gate, to measure how it generalizes. They pass.
var holdoutBullets = []fixtureBullet{
	{"X1", "platform migration", "Led a platform migration across 12 services that cut incidents 40%"},
	{"X1", "platform migration", "Delivered a 40% reduction in incidents across 12 services through the platform migration"},
	{"X1", "p99 latency", "Brought checkout API p99 latency down from 800 ms to 120 ms"},
	{"X1", "pricing engine", "Designed and built a C++ pricing engine and a C# billing service on .NET 6 for 3 product teams"},
	{"X1", "pricing engine", "Shipped a C++ pricing engine and C# billing service (.NET 6) used by 3 product teams"},
	{"X1", "gateway", "Grew the Node.js gateway until it handled 2.5M requests per day"},
	{"X1", "gateway", "Scaled the Node.js gateway to 2.5M requests per day"},
	{"X1", "Mentored four", "Mentored four engineers on code review and design"},
	{"X1", "Mentored four", "Mentored four engineers through code review and design sessions"},
	{"X1", "Doubled", "Doubled deployment frequency after moving CI to GitHub Actions"},
	{"X1", "cloud spend", "Saved $1.2M per year on cloud spend by rightsizing AWS clusters"},
	{"X1", "cloud spend", "Rightsized AWS clusters to save $1.2M per year in cloud spend"},
	{"X1", "Kafka consumers", "Migrated 14 Kafka consumers from Python 3.8 to Go and cut CPU use by 35%"},
	{"X1", "PostgreSQL 14", "Co-led the PostgreSQL 14 upgrade with a team of 6 engineers"},
	{"X1", "on-call pages", "Cut on-call pages in half with Prometheus alerts and runbooks"},
	{"X2", "ingestion service", "Built the ingestion service in Go, used by twelve teams"},
	{"X2", "ingestion service", "Created the Go ingestion service that twelve teams depend on"},
	{"X2", "99.9%", "Owned on-call for a 99.9% availability target"},
	{"X2", "report time", "Reduced report time from 5 hours to 20 minutes"},
	{"X2", "survey files", "Processed more than 5K survey files per day with a Redis-backed queue"},
	{"X2", "Docker image", "Reduced Docker image size by 60% and build time from 12 min to 4 min"},
	{"X2", "Terraform modules", "Introduced Terraform modules that provision 30 environments in under 10 minutes"},
	{"X2", "integration tests", "Wrote over 200 integration tests and lifted coverage from 55% to 85%"},
	{"X2", "integration tests", "Raised coverage from 55% to 85% by writing 200+ integration tests"},
	{"X2", "Onboarded", "Ran weekly design reviews and onboarded five new hires"},
	{"P", "3x faster", "Tuned SQL queries to run 3x faster on a 50 GB dataset"},
	{"P", "Grafana", "Built Grafana dashboards tracking 15 metrics across 4 services"},
	{"P", "open source", "Open-sourced the tool, which earned 300 GitHub stars"},
}

// limitBullets are restatements a person would accept and the gate drops: an
// added modifier no source uses, a subject named before a comma, a head noun
// cut from its compound and a verb outside the verb classes. The gate rejects
// when unsure, so each costs a line, never an invention.
var limitBullets = []fixtureBullet{
	{"X1", "platform migration", "Drove the platform migration, reducing production incidents by 40% across 12 services"},
	{"X1", "p99 latency", "Improved p99 latency of the checkout API, from 800 ms to 120 ms"},
	{"X1", "Kafka consumers", "Rewrote 14 Kafka consumers in Go (from Python 3.8), cutting CPU use by 35%"},
	{"X2", "report time", "Cut report generation time from 5 h to 20 min with a 3-step Go and SQL pipeline"},
	{"X2", "Docker image", "Shrank the Docker image by 60% and cut build time from 12 to 4 min"},
	{"P", "3x faster", "Sped up SQL queries 3x on a 50 GB dataset"},
}

func TestGrounding_HoldoutBulletsPassAndKnownLimitsReject(t *testing.T) {
	m := fixtureManifest(t, fixtureCV, fixtureJD, "Staff Backend Engineer")
	run := func(b fixtureBullet) Gate {
		entry := b.entry
		if entry == "P" {
			entry = m.SelectedProjects[0]
		}
		return checkBulletOnly(m, entry, b.text, evID(t, m, b.from))
	}
	for _, b := range holdoutBullets {
		if g := run(b); !g.OK() {
			t.Errorf("rejected %q: %+v", b.text, g.Errors)
		}
	}
	for _, b := range limitBullets {
		if g := run(b); !hasCode(g, "unsupported-figure") {
			t.Errorf("%q now passes; move it to holdoutBullets: %+v", b.text, g.Errors)
		}
	}
	t.Logf("holdout: %d of %d pass; known limits: %d reject", len(holdoutBullets), len(holdoutBullets)+len(limitBullets), len(limitBullets))
}
