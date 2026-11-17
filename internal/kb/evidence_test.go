package kb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cvDoc = `# Alex Example

## Experience
Team size: 5 engineers

## Education
Studied computer science at Example University.
`

const profileDoc = `version: 1
candidate:
  full_name: Alex Example
  date_of_birth: "1990-04-02"
work_authorization:
  requires_sponsorship_now: false
  visa_status: citizen
targets:
  compensation:
    currency: USD
    target: 150000
history:
  employers:
    - Example Corp
scan:
  interval: daily
`

func TestFactSources_VoiceIsNotEvidence(t *testing.T) {
	f := newFixture(t)
	f.write("voice.md", "I always say that stakeholder alignment workshops energise me.")
	f.write("cv.md", cvDoc)
	res := f.search("stakeholder alignment workshops energise")
	if len(res.Facts) != 0 {
		t.Fatalf("voice.md returned as evidence: %v", sourceRefs(res))
	}
}

func TestFactSources_CVProfileNarrative(t *testing.T) {
	f := newFixture(t)
	f.write("cv.md", cvDoc)
	f.write("profile.yml", profileDoc)
	f.write("narrative.md", "# Narrative\n\nI move between platform and product work because migrations teach empathy.")
	if r := f.search("computer science education"); len(r.Facts) == 0 || r.Facts[0].SourceKind != KindCV {
		t.Fatalf("cv: %+v", r.Facts)
	}
	if r := f.search("platform product migrations empathy"); len(r.Facts) == 0 || r.Facts[0].SourceKind != KindNarrative {
		t.Fatalf("narrative: %+v", r.Facts)
	}
	r := f.search("employers history Example Corp")
	if len(r.Facts) == 0 || r.Facts[0].SourceKind != KindProfile || r.Facts[0].Trust != TrustProfile {
		t.Fatalf("profile: %+v", r.Facts)
	}
	for _, fact := range f.search("scan interval daily").Facts {
		t.Fatalf("a settings block was indexed: %+v", fact)
	}
}

func TestEvidence_FactIDsAndTrust(t *testing.T) {
	f := newFixture(t)
	f.write("cv.md", cvDoc)
	f.write("knowledge/projects/p.md", snippet("", "Rebuilt the cache layer.\n\nCache hit rate: 91 percent"))
	r := f.search("team size engineers")
	if len(r.Facts) != 1 || r.Facts[0].ID != "semantic:team size" || r.Facts[0].Trust != TrustEvidential {
		t.Fatalf("labeled fact = %+v", r.Facts)
	}
	r = f.search("rebuilt cache layer")
	if len(r.Facts) == 0 || r.Facts[0].ID != "snippet:knowledge/projects/p.md" {
		t.Fatalf("unlabeled fact id = %+v", r.Facts)
	}
	if trustRank[TrustCorrection] <= trustRank[TrustCurated] || trustRank[TrustCurated] <= trustRank[TrustProfile] || trustRank[TrustProfile] <= trustRank[TrustEvidential] {
		t.Fatal("trust tiers out of order")
	}
}

func TestEvidence_Conflict_BothNamed(t *testing.T) {
	f := newFixture(t)
	f.write("cv.md", cvDoc)
	f.write("knowledge/projects/p.md", snippet("", "## Team\nTeam size: 4 engineers"))
	r := f.search("team size engineers")
	var conflicted []Fact
	for _, fact := range r.Facts {
		if fact.ID == "semantic:team size" {
			conflicted = append(conflicted, fact)
		}
	}
	if len(conflicted) != 2 {
		t.Fatalf("facts = %+v", r.Facts)
	}
	for i, fact := range conflicted {
		if len(fact.ConflictsWith) != 1 || fact.ConflictsWith[0] == "" {
			t.Fatalf("fact %d conflictsWith = %v", i, fact.ConflictsWith)
		}
	}
}

func TestEvidence_SameFactTwoSourcesMerges(t *testing.T) {
	f := newFixture(t)
	f.write("cv.md", cvDoc)
	f.write("knowledge/projects/p.md", snippet("", "## Team\nTeam size: 5 engineers"))
	r := f.search("team size engineers")
	n := 0
	for _, fact := range r.Facts {
		if fact.ID == "semantic:team size" {
			n++
			if len(fact.Provenance) != 2 || len(fact.ConflictsWith) != 0 {
				t.Fatalf("merged fact = %+v", fact)
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected one merged fact, got %d", n)
	}
}

func TestEvidence_SensitiveGatedByQueryCategory(t *testing.T) {
	f := newFixture(t)
	f.write("profile.yml", profileDoc)
	f.write("cv.md", "## Strengths\nGreatest strength is steady delivery under pressure.")
	r := f.search("Describe your greatest strength")
	for _, fact := range r.Facts {
		if fact.SourceKind == KindProfile {
			t.Fatalf("profile fact leaked into a general question: %+v", fact)
		}
	}
	if r := f.search("Do you require visa sponsorship now?"); len(r.Facts) == 0 || !strings.Contains(r.Facts[0].Text, "sponsorship") {
		t.Fatalf("work authorization query: %+v", r.Facts)
	}
	if r := f.search("What compensation target do you expect?"); len(r.Facts) == 0 {
		t.Fatal("compensation query returned nothing")
	}
	if r := f.search("What is your date of birth?"); len(r.Facts) == 0 {
		t.Fatal("date of birth query returned nothing")
	}
	// Compensation asked about must not surface the work authorization section.
	for _, fact := range f.search("salary compensation target currency").Facts {
		if strings.Contains(fact.Text, "sponsorship") {
			t.Fatalf("cross-category leak: %+v", fact)
		}
	}
}

func TestGaps_RecordedOnEmptyAndFolded(t *testing.T) {
	f := newFixture(t)
	f.search("Why are you interested in Example Corp?")
	f.search("Why does Sample Labs interest you?")
	f.search("Tell me about quantum lattice research")
	gaps, err := f.svc.Gaps(f.ctx)
	if err != nil || len(gaps) != 2 {
		t.Fatalf("gaps = %+v, %v", gaps, err)
	}
	if gaps[0].Count != 2 {
		t.Fatalf("first row = %+v", gaps[0])
	}
}

func TestGaps_NotRecordedWhenFound(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Built the harbor scheduling algorithm."))
	f.search("harbor scheduling algorithm")
	if gaps, _ := f.svc.Gaps(f.ctx); len(gaps) != 0 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestEvidence_UnreadableSourceNotNoMatch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Built the harbor scheduling algorithm."))
	dir := filepath.Join(f.ws.Root, "knowledge")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := f.search("harbor scheduling algorithm")
	if r.Status != StatusSourcesUnavailable || len(r.Failed) == 0 || r.Failed[0].Source != "knowledge" {
		t.Fatalf("result = %+v", r)
	}
	if gaps, _ := f.svc.Gaps(f.ctx); len(gaps) != 0 {
		t.Fatalf("an unreadable source recorded a gap: %+v", gaps)
	}
}

func TestEvidence_PartialWhenOneSourceFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Built the harbor scheduling algorithm."))
	f.write("cv.md", cvDoc)
	cv := filepath.Join(f.ws.Root, "cv.md")
	if err := os.Chmod(cv, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cv, 0o600) })
	r := f.search("harbor scheduling algorithm")
	if r.Status != StatusPartial || len(r.Facts) == 0 || r.Failed[0].Source != KindCV {
		t.Fatalf("result = %+v", r)
	}
}

// An unreadable briefs folder is a failure of the briefs, and their rows stay
// in the index, as for any source that cannot be read.
func TestEvidence_UnreadableBriefsFolderFailsTheBriefs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	f := newFixture(t)
	f.write("knowledge/projects/a.md", snippet("", "Built the harbor scheduling algorithm."))
	f.write("knowledge/companies/example.test.md", "---\ntitle: Example Test\n---\n\nHand written note: Example Test makes reef sensors.\n")
	f.search("harbor scheduling algorithm")
	dir := filepath.Join(f.ws.Root, "knowledge", "companies")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := f.search("harbor scheduling algorithm")
	if len(r.Failed) != 1 || r.Failed[0].Source != "briefs" || len(r.Facts) == 0 {
		t.Fatalf("result = %+v", r)
	}
	var n int
	if err := f.st.Read().QueryRowContext(f.ctx, `SELECT count(*) FROM kb_docs WHERE path = 'knowledge/companies/example.test.md'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("brief rows = %d, %v", n, err)
	}
}

type stubSource struct {
	kind  string
	facts []Fact
	err   error
}

func (s stubSource) Kind() string                           { return s.kind }
func (s stubSource) Search(_ ctxT, _ Query) ([]Fact, error) { return s.facts, s.err }

func TestEvidence_ExternalSourceFailureIsNamed(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Sources = []Source{stubSource{kind: KindAnswerBank, err: os.ErrPermission}}
	})
	r := f.search("anything at all here")
	if r.Status != StatusSourcesUnavailable || r.Failed[len(r.Failed)-1].Source != KindAnswerBank {
		t.Fatalf("result = %+v", r)
	}
}

func TestEvidence_ExternalSourceTrustOrder(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Sources = []Source{stubSource{kind: KindCorrection, facts: []Fact{{ID: "semantic:city", Text: "City: Springfield", SourceKind: KindCorrection, SourceRef: "corrections#1", Score: 1, Trust: TrustCorrection}}}}
	})
	f.write("cv.md", "## Location\nCity: Shelbyville and relocation plans")
	r := f.search("city relocation plans")
	if len(r.Facts) < 2 || r.Status != StatusGrounded {
		t.Fatalf("result = %+v", r)
	}
	var corr, cv *Fact
	for i := range r.Facts {
		switch r.Facts[i].SourceKind {
		case KindCorrection:
			corr = &r.Facts[i]
		case KindCV:
			cv = &r.Facts[i]
		}
	}
	if corr == nil || cv == nil || len(corr.ConflictsWith) == 0 || corr.Trust != TrustCorrection {
		t.Fatalf("facts = %+v", r.Facts)
	}
}
