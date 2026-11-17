// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/llm"

	"applysmith/internal/gates"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs from the golden file (run with -update to accept):\n%s", name, got)
	}
}

func readHTML(t *testing.T, e *tenv, number int64) string {
	t.Helper()
	var p string
	if err := e.st.Read().QueryRow(`SELECT html_path FROM report_index WHERE report_number = ?`, number).Scan(&p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReport_GoldenFinalMarkdownAndHTML(t *testing.T) {
	e := newTenv(t)
	res := e.run(Request{URL: "https://jobs.example.test/p/golden", Text: postingText("Golden marker."), Company: "Example Systems", Title: "Software Engineer"})
	golden(t, "final.golden.md", e.readMarkdown(res.ReportNumber))
	golden(t, "final.golden.html", readHTML(t, e, res.ReportNumber))
}

func TestReport_ParseableScorecardRecomputesTheHeaderScore(t *testing.T) {
	e := newTenv(t)
	for _, m := range []string{"sc1", "sc2", "sc3", "sc4"} {
		res := e.run(req(m))
		md := e.readMarkdown(res.ReportNumber)
		in, card, err := ParseScorecard(md)
		if err != nil {
			t.Fatal(err)
		}
		if got := Compute(in); got.Score != card.Score || got.Raw != card.Raw {
			t.Fatalf("recomputed %+v, stored %+v", got, card)
		}
		if h := ParseHeader(md); h["score"] != formatScore(card.Score) {
			t.Fatalf("header score %s, scorecard %v", h["score"], card.Score)
		}
	}
}

func formatScore(v float64) string { return fmtFloat(v) }

func TestReport_ApplicationAnswersOnlyAtOrAbove4_5(t *testing.T) {
	high := deepReply(deepOpts{must: []float64{1, 1, 1}, nice: []float64{1}, hidden: []float64{1}, align: []float64{1, 1}})
	e := newTenv(t, deepFixture("Marker hi.", high, answersReply),
		deepFixture("Marker lo.", deepReply(deepOpts{must: []float64{1, 1, 1}, nice: []float64{0}, align: []float64{1}})))
	hi := e.run(req("hi"))
	md := e.readMarkdown(hi.ReportNumber)
	if hi.Recommendation != RecStrong || !strings.Contains(md, "## Application answers") || !strings.Contains(md, "Because of the clinics.") || !strings.Contains(md, "Unanswered: the profile gives no basis") {
		t.Fatalf("strong report:\n%s", md)
	}
	if _, ok := ParseHeader(md)["application_answers"]; ok {
		t.Fatal("the header must not say answers are omitted")
	}
	var eligible int
	if err := e.st.Read().QueryRow(`SELECT documents_eligible FROM report_index WHERE report_number = ?`, hi.ReportNumber).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible != 1 || e.ledgerCalls("deep-eval") != 2 {
		t.Fatalf("eligible %d, deep calls %d", eligible, e.ledgerCalls("deep-eval"))
	}

	lo := e.run(req("lo"))
	mdLo := e.readMarkdown(lo.ReportNumber)
	if lo.Score == nil || *lo.Score >= 4.5 || *lo.Score < 4.0 {
		t.Fatalf("the low fixture should land just under 4.5: %v", lo.Score)
	}
	if strings.Contains(mdLo, "## Application answers") || !strings.Contains(ParseHeader(mdLo)["application_answers"], "omitted") {
		t.Fatalf("report:\n%s", mdLo)
	}
	_ = e.st.Read().QueryRow(`SELECT documents_eligible FROM report_index WHERE report_number = ?`, lo.ReportNumber).Scan(&eligible)
	if eligible != 0 {
		t.Fatal("documents are not eligible below 4.5")
	}
}

func TestReport_FailedAnswersCallDoesNotFailTheJob(t *testing.T) {
	high := deepReply(deepOpts{must: []float64{1, 1, 1}, nice: []float64{1}, hidden: []float64{1}, align: []float64{1, 1}})
	e := newTenv(t, llm.Fixture{Task: "deep-eval", Contains: "Marker fa.", Steps: []llm.Step{{Text: high}, {Fail: llm.KindRateLimited, Status: 429}}})
	res := e.run(req("fa"))
	if res.Outcome != OutcomeReported {
		t.Fatalf("result = %+v", res)
	}
	if md := e.readMarkdown(res.ReportNumber); strings.Contains(md, "## Application answers") || !strings.Contains(ParseHeader(md)["application_answers"], "unavailable") {
		t.Fatalf("report:\n%s", md)
	}
}

func TestReport_NumbersAreUniqueUnderConcurrencyAndNeverReused(t *testing.T) {
	e := newTenv(t)
	const n = 16
	numbers := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := req("c" + string(rune('a'+i)))
			r.Company = "Company " + string(rune('A'+i))
			numbers[i] = e.run(r).ReportNumber
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, num := range numbers {
		if num == 0 || seen[num] {
			t.Fatalf("numbers = %v", numbers)
		}
		seen[num] = true
	}
	var top int64
	for num := range seen {
		top = max(top, num)
	}
	// Deleting the newest report from disk and from the index never frees its number.
	var md string
	if err := e.st.Read().QueryRow(`SELECT markdown_path FROM report_index WHERE report_number = ?`, top).Scan(&md); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.root, filepath.FromSlash(md))); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM report_index WHERE report_number = ?`, `DELETE FROM report_artifacts WHERE report_number = ?`} {
			if _, err := tx.Exec(q, top); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := e.run(req("after"))
	if next.ReportNumber <= top {
		t.Fatalf("next = %d, top = %d", next.ReportNumber, top)
	}
	if got := filepath.Base(md); !regexp.MustCompile(`^\d{4}-company-[a-p]-2026-10-11\.md$`).MatchString(got) {
		t.Fatalf("file name = %s", got)
	}
}

func TestReport_FailedFileWriteLeavesNoIndexOrTrackerRow(t *testing.T) {
	e := newTenv(t)
	if err := os.WriteFile(filepath.Join(e.root, "reports"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.run(req("fw"))
	if res.Outcome != OutcomeFailed || res.Reason != ReasonWrite {
		t.Fatalf("result = %+v", res)
	}
	if e.count("report_index") != 0 || len(e.entries()) != 0 {
		t.Fatal("no index row or tracker row may remain")
	}
	if n := e.count("report_artifacts"); n != 0 {
		t.Fatalf("no artifact row may remain: %d", n)
	}
	if err := os.Remove(filepath.Join(e.root, "reports")); err != nil {
		t.Fatal(err)
	}
	if next := e.run(req("fw2")); next.ReportNumber != 1 {
		t.Fatalf("the failed write must not consume a number, next is %d", next.ReportNumber)
	}
}

func reportFiles(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "reports", "*"))
	j, _ := filepath.Glob(filepath.Join(root, ".applysmith", "publish", "*"))
	return append(m, j...)
}

func TestReport_HTMLWriteFailureCommitsNothingAndLeavesNoFiles(t *testing.T) {
	e := newTenv(t)
	blocker := filepath.Join(e.root, "reports", "0001-example-systems-2026-10-11.html")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.run(req("hf"))
	if res.Outcome != OutcomeFailed || res.Reason != ReasonWrite {
		t.Fatalf("result = %+v", res)
	}
	if left := reportFiles(t, e.root); len(left) != 1 || left[0] != blocker {
		t.Fatalf("files left: %v", left)
	}
	if e.count("report_index") != 0 || e.count("report_artifacts") != 0 || len(e.entries()) != 0 {
		t.Fatal("nothing may be committed")
	}
	_ = os.Remove(blocker)
	if next := e.run(req("hf2")); next.ReportNumber != 1 {
		t.Fatalf("next report is %d, want 1", next.ReportNumber)
	}
}

func TestReport_IndexFailureCommitsNothingAndLeavesNoFiles(t *testing.T) {
	e := newTenv(t)
	err := e.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_index BEFORE INSERT ON report_index BEGIN SELECT RAISE(ABORT, 'index insert refused'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	res := e.run(req("if"))
	if res.Outcome != OutcomeFailed || res.Reason != ReasonWrite {
		t.Fatalf("result = %+v", res)
	}
	if left := reportFiles(t, e.root); len(left) != 0 {
		t.Fatalf("files left: %v", left)
	}
	if e.count("report_artifacts") != 0 || len(e.entries()) != 0 {
		t.Fatal("nothing may be committed")
	}
	if err := e.st.Write(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec(`DROP TRIGGER fail_index`); return err }); err != nil {
		t.Fatal(err)
	}
	if next := e.run(req("if2")); next.ReportNumber != 1 {
		t.Fatalf("next report is %d, want 1", next.ReportNumber)
	}
}

func TestReport_TrackerFailureCommitsNothing(t *testing.T) {
	e := newTenv(t)
	err := e.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_tracker BEFORE INSERT ON tracker_entries BEGIN SELECT RAISE(ABORT, 'tracker insert refused'); END`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	res := e.run(req("tf"))
	if res.Outcome != OutcomeFailed || e.count("report_index") != 0 || e.count("report_artifacts") != 0 || len(reportFiles(t, e.root)) != 0 {
		t.Fatalf("result %+v", res)
	}
}

func TestReport_HTMLEscapesModelTextAndIsSelfContained(t *testing.T) {
	reply := deepReply(deepOpts{snapshot: "Hello <script>alert(1)</script> and <img src=x onerror=alert(2)> [bad](javascript:alert(3))"})
	e := newTenv(t, deepFixture("Marker xs.", reply))
	res := e.run(req("xs"))
	page := readHTML(t, e, res.ReportNumber)
	for _, bad := range []string{"<script", "<img", "onerror=alert(2)>", "javascript:", "src=\"http", "href=\"http", "<link"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("the script text must be visible as text:\n%s", page)
	}
	if !strings.Contains(page, `http-equiv="Content-Security-Policy"`) || !strings.Contains(page, "default-src 'none'") {
		t.Error("the page must carry a restrictive Content-Security-Policy")
	}
	if !strings.HasPrefix(page, "<!doctype html>") || !strings.Contains(page, "<style>") {
		t.Error("the page must be self-contained")
	}
	// The markdown file keeps the model's text as written.
	if !strings.Contains(e.readMarkdown(res.ReportNumber), "<script>alert(1)</script>") {
		t.Error("the markdown keeps the text as written")
	}
}

func penalizing(e *tenv, delta float64) {
	e.prof.EligibilityGate.EmployerTiers = map[string]workspaceTier{"staffing": {Policy: "penalize", Delta: ptr(delta)}}
}

func TestReport_TierPenaltyIsAppliedOnceAndRecorded(t *testing.T) {
	reply := deepReply(deepOpts{must: []float64{1, 1, 1}, nice: []float64{1}, hidden: []float64{1}, align: []float64{1, 1}})
	e := newTenv(t, deepFixture("Marker tp.", reply, answersReply), deepFixture("Marker tq.", reply, answersReply))
	plain := e.run(req("tp", func(r *Request) { r.Company = "Robert Half" }))
	penalizing(e, 1.5)
	pen := e.run(req("tq", func(r *Request) { r.Company = "Robert Half" }))
	if plain.Score == nil || pen.Score == nil || *pen.Score != round1(*plain.Score-1.5) {
		t.Fatalf("plain %v penalized %v", plain.Score, pen.Score)
	}
	if md := e.readMarkdown(pen.ReportNumber); !strings.Contains(md, "employer tier adjustment: -1.5 (staffing)") {
		t.Fatalf("report:\n%s", md)
	}
}

func TestReport_PersonalBlockInsideAPenalizedTier(t *testing.T) {
	e := newTenv(t)
	penalizing(e, 1.5)
	e.blocked[NormalizeCompany("Robert Half")] = true
	res := e.run(req("pb", func(r *Request) { r.Company = "Robert Half" }))
	if res.Outcome != OutcomeGated {
		t.Fatalf("result = %+v", res)
	}
	if md := e.readMarkdown(res.ReportNumber); !strings.Contains(md, "blocked_employer") || !strings.Contains(md, "blocked-companies") {
		t.Fatalf("report:\n%s", md)
	}
	if ClassifyTier("Robert Half", "") != TierStaffing {
		t.Fatal("a personal block must not change the tier")
	}
}

func TestReport_BlockPolicyGatesTheTier(t *testing.T) {
	e := newTenv(t)
	e.prof.EligibilityGate.EmployerTiers = map[string]workspaceTier{"consultancy": {Policy: "block"}}
	res := e.run(req("bp", func(r *Request) { r.Company = "Accenture" }))
	if res.Outcome != OutcomeGated {
		t.Fatalf("result = %+v", res)
	}
}

func TestReport_DoubtfulLegitimacyForcedByAPaymentRequest(t *testing.T) {
	e := newTenv(t)
	res := e.run(req("pf", func(r *Request) {
		r.Text = postingText("Marker pf. Candidates must pay a processing fee before the offer letter.")
	}))
	md := e.readMarkdown(res.ReportNumber)
	if h := ParseHeader(md); h["legitimacy"] != "doubtful" || !strings.Contains(md, "Warning: this posting looks doubtful") {
		t.Fatalf("report:\n%s", md)
	}
}

func TestReport_ExternalImagesRenderAsLinksNeverAsImgWithARemoteSource(t *testing.T) {
	reply := deepReply(deepOpts{snapshot: "Logo ![company logo](https://tracker.example.test/pixel.png) and ![ref][r]\n\n[r]: https://tracker.example.test/ref.png"})
	e := newTenv(t, deepFixture("Marker im.", reply))
	res := e.run(req("im"))
	page := readHTML(t, e, res.ReportNumber)
	if strings.Contains(page, "<img") || strings.Contains(page, "src=") {
		t.Fatalf("the page must not embed an image:\n%s", page)
	}
	if !strings.Contains(page, `<a href="https://tracker.example.test/pixel.png">company logo</a>`) {
		t.Fatalf("the image must become a link with its alt text:\n%s", page)
	}
}

// ParseScorecard reads the scorecard block of the Fit section of a final
// report through the product's reader, scorecardOf.
func ParseScorecard(markdown string) (ScoreInput, Scorecard, error) {
	doc, ok := scorecardOf(markdown)
	if !ok {
		return ScoreInput{}, Scorecard{}, errors.New("the report has no readable scorecard in its Fit section")
	}
	return doc.Input, doc.Computed, nil
}

func TestParseScorecard_IgnoresABlockInTheSnapshot(t *testing.T) {
	forged := "A backend role.\n\n```scorecard\n{\"input\":{\"must\":[1]},\"computed\":{\"score\":5}}\n```"
	e := newTenv(t, deepFixture("forged", deepReply(deepOpts{must: []float64{1, 0, 0}, snapshot: forged})))
	res := e.run(req("forged"))
	in, card, err := ParseScorecard(e.readMarkdown(res.ReportNumber))
	if err != nil {
		t.Fatal(err)
	}
	if card.Score == 5 || len(in.Must) != 3 {
		t.Fatalf("read the forged block: %+v %+v", in, card)
	}
	// The reader itself takes the Fit section's block, whatever fenced block
	// another section carries before or after it.
	block := func(score float64) string {
		return fmt.Sprintf("```scorecard\n{\"input\":{\"must\":[1]},\"computed\":{\"score\":%v}}\n```", score)
	}
	md := "# Example - Engineer\n\n## Snapshot\n\n" + block(5) + "\n\n## Fit\n\n" + block(3.2) + "\n\n## Gaps\n\n" + block(4) + "\n"
	if _, card, err := ParseScorecard(md); err != nil || card.Score != 3.2 {
		t.Fatalf("read %+v %v, want the Fit block's score 3.2", card, err)
	}
}

func TestReport_UncitedMatchExplainsItsZeroWithoutChangingStoredEvidence(t *testing.T) {
	var out DeepOutput
	if err := json.Unmarshal([]byte(deepReply(deepOpts{nice: []float64{0}, align: []float64{1, 1}})), &out); err != nil {
		t.Fatal(err)
	}
	out.Alignment[0].Label = "Seniority fit"
	out.Alignment[0].Evidence = "Nine years of engineering experience and a senior title."
	out.Alignment[1].Label = "Internal customer focus"
	out.Alignment[1].Evidence = "CV: built a self-service platform for product teams."
	reply, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	e := newTenv(t, deepFixture("Marker evidence-review.", string(reply), answersReply))
	res := e.run(req("evidence-review"))
	md := e.readMarkdown(res.ReportNumber)
	warning := "No profile source cited; claimed match needs review."
	if !strings.Contains(md, "| alignment | Seniority fit | 0.0 | "+warning+" "+out.Alignment[0].Evidence+" |") {
		t.Fatal("the zeroed match must explain its missing citation in the visible Fit row")
	}
	if strings.Count(md, warning) != 1 || !strings.Contains(md, "| alignment | Internal customer focus | 1.0 | "+out.Alignment[1].Evidence+" |") {
		t.Fatal("supported matches and genuine misses must keep their own evidence")
	}
	if !strings.Contains(readHTML(t, e, res.ReportNumber), warning) {
		t.Fatal("the explanation must also reach the HTML report")
	}
	var stored scorecardDoc
	block := strings.Split(strings.Split(md, "```scorecard\n")[1], "\n```")[0]
	if err := json.Unmarshal([]byte(block), &stored); err != nil {
		t.Fatal(err)
	}
	it := stored.Items.Alignment[0]
	if it.Hit != 0 || it.Evidence != out.Alignment[0].Evidence || len(stored.Unsupported) != 1 || stored.Unsupported[0] != it.ID {
		t.Fatal("the stored scorecard must keep the checked zero, original evidence and unsupported id")
	}
	if got := Compute(stored.Input); res.Score == nil || got.Score != *res.Score {
		t.Fatal("the explanation must not alter the computed score")
	}
}

// The verdict names what decides the score from the card alone, and the gap
// triage never calls an unhonored requirement an eligibility gap or accepts
// nearest evidence without a profile source.
func TestReport_DecidingFactorsAndGapTriage(t *testing.T) {
	f := FinalBody{
		Checked: Checked{
			Must: []Item{
				{ID: "m1", Label: "Go", Hit: 1},
				{ID: "m2", Label: "AI tools in the workflow", Hit: 0},
				{ID: "m3", Label: "Bachelor's degree", Hit: 0},
				{ID: "m4", Label: "Kubernetes at scale", Hit: 0.5},
			},
			RedFlags:   []Flag{{ID: "f1", Label: "Travel above your limit", Level: 0.5}},
			HonoredIDs: []string{"m3"},
		},
		Card: Scorecard{Raw: 4.1, Comp: 5, Caps: []Cap{{CapHonoredGate, 1.5, "gate"}, {CapTwoMisses, 2.7, "two"}}},
		Out: DeepOutput{Sections: Sections{Gaps: []GapNote{
			{ID: "m2", Kind: GapEligibility, Nearest: "uses scripts", Mitigation: "Name a real AI-assisted task."},
			{ID: "m4", Kind: GapCore, Nearest: "CV: moved 120 services to Kubernetes", Mitigation: "Lead with the migration."},
		}}},
	}
	got := decidingFactors(f, 1.5)
	for _, want := range []string{
		"- Missed must-have: AI tools in the workflow.",
		"- Eligibility requirement not met: Bachelor's degree.",
		"- Partly met must-haves: 1 of 4 (Kubernetes at scale).",
		"- Red flag (level 0.5): Travel above your limit.",
		"- Pay: the stated top reaches your target (pay score 5).",
		"- The eligibility_gate cap of 1.5 holds the score below its uncapped 4.10",
		"- Next band: consider from 3.5, 2.0 above this score, out of reach while the cap holds.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("deciding factors lack %q:\n%s", want, got)
		}
	}
	g := gapTriage(f)
	for _, want := range []string{
		"- **AI tools in the workflow** (hit 0.0, core skill). Nearest evidence: none in the profile. Mitigation: Name a real AI-assisted task.",
		"- **Bachelor's degree** (hit 0.0, eligibility). Nearest evidence: none in the profile. Mitigation: none suggested",
		"- **Kubernetes at scale** (hit 0.5, core skill). Nearest evidence: CV: moved 120 services to Kubernetes.",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("gap triage lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "**Go**") {
		t.Error("a met must-have is not a gap")
	}
}

// The pay anchors come from the profile alone: the target to ask for (110
// percent of the minimum when unset), the minimum as the floor, and a range
// that clears the target moves the anchor into the range.
func TestReport_PayAnchors(t *testing.T) {
	usd := CompTarget{Currency: "USD", Minimum: 120000}
	for _, c := range []struct {
		name string
		r    *gates.Range
		want []string
	}{
		{"no range", nil, []string{"ask for 132000 USD, the target; 120000 USD, the minimum, is the walk-away floor."}},
		{"range around the target", &gates.Range{Low: 110000, High: 150000, Currency: "USD"}, []string{"ask for 132000 USD", "The stated top is 114% of the target and the stated bottom 83%."}},
		{"range above the target", &gates.Range{Low: 166600, High: 208300, Currency: "USD"}, []string{"the whole stated range clears your target of 132000 USD (its bottom is 126% of it), so anchor inside the range"}},
		{"other currency", &gates.Range{Low: 166600, High: 208300, Currency: "CAD"}, []string{"ask for 132000 USD"}},
	} {
		got := payText(c.r, usd, CompensationScore(c.r, usd))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
		if c.r != nil && c.r.Currency == "CAD" && strings.Contains(got, "%") {
			t.Errorf("%s: a range in another currency is never compared: %q", c.name, got)
		}
	}
	if got := payText(nil, CompTarget{}, 3); strings.Contains(got, "anchors") {
		t.Errorf("no profile minimum gives no anchors: %q", got)
	}
}
