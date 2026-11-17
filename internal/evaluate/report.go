// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Report kinds (the kind of the report file, not of the score).
const (
	KindFinal     = "final"
	KindScreening = "screening"
	KindGated     = "gated"
)

// Legitimacy of a report that was never assessed.
const LegitUnassessed = "unassessed"

// FakeNote labels a report produced with the fake provider (the offline
// choice), in the words of the reader it misleads otherwise.
const FakeNote = "this score is simulated, because no model read the posting; choose a model under Settings, Models and evaluate it again for a real one"

// ReportMeta is the header and title of a report.
type ReportMeta struct {
	Number       int64
	Date         string
	Kind         string
	Score        *float64
	ScoreKind    string
	Rec          string
	URL          string
	Verification string
	CompanySize  string
	Legitimacy   string
	RoleFamily   string
	Company      string
	Role         string
	// Caps is the caps line: each cap, demotion and tier adjustment.
	Caps string
	// AnswersNote is the header's statement when the answers are absent.
	AnswersNote string
	Fake        bool
}

var singleLine = strings.NewReplacer("\r", " ", "\n", " ")

func line(s string) string { return strings.TrimSpace(singleLine.Replace(s)) }

// list renders the model's items one per line, each flattened by line, with
// marker "1." numbering them or "-" as bullets; no item renders "unknown".
func list(items []string, marker string) string {
	var out []string
	for _, it := range items {
		if it = line(it); it != "" {
			m := marker
			if marker == "1." {
				m = strconv.Itoa(len(out)+1) + "."
			}
			out = append(out, m+" "+it)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, "\n")
}

func orDash(s string) string {
	if s = line(s); s != "" {
		return s
	}
	return "unknown"
}

// header writes the key: value lines in the fixed order.
func (m ReportMeta) header() string {
	score := "n/a"
	if m.Score != nil {
		score = fmt.Sprintf("%.1f", *m.Score)
	}
	caps := line(m.Caps)
	if caps == "" {
		caps = "none"
	}
	var b strings.Builder
	w := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, v) }
	w("report", fmt.Sprintf("%04d", m.Number))
	w("date", m.Date)
	w("score", score)
	w("score_kind", m.ScoreKind)
	w("recommendation", m.Rec)
	w("url", orDash(m.URL))
	w("verification", m.Verification)
	w("company_size", orDash(m.CompanySize))
	w("legitimacy", m.Legitimacy)
	w("role_family", orDash(m.RoleFamily))
	w("documents", fmt.Sprintf("report %d", m.Number))
	w("caps", caps)
	if m.AnswersNote != "" {
		w("application_answers", m.AnswersNote)
	}
	return b.String()
}

func (m ReportMeta) title() string {
	return fmt.Sprintf("# %s - %s\n\n", orDash(m.Company), orDash(m.Role))
}

func section(b *strings.Builder, name, body string) {
	fmt.Fprintf(b, "## %s\n\n%s\n\n", name, strings.TrimSpace(body))
}

func cell(s string) string {
	return strings.ReplaceAll(line(s), "|", "\\|")
}

// FinalBody is the content of a final report.
type FinalBody struct {
	Out     DeepOutput
	Checked Checked
	Input   ScoreInput
	Card    Scorecard
	Pay     string
	// StatedPay is the Snapshot's pay line: the posting's pay as extracted,
	// or the range read from its text when the extraction gave none (the
	// range Pay scores), so the two never disagree.
	StatedPay string
	Sponsor   string
	Signals   []string
	Answers   []Answer
	Doubtful  bool
	// Years is the profile's years of experience, nil when unknown; Targets
	// its target roles with their seniority.
	Years   *float64
	Targets string
}

type scorecardDoc struct {
	Items struct {
		Must      []Item `json:"must_have"`
		Nice      []Item `json:"nice_to_have"`
		Hidden    []Item `json:"hidden_signals"`
		Alignment []Item `json:"profile_alignment"`
		RedFlags  []Flag `json:"red_flags"`
	} `json:"items"`
	Input       ScoreInput `json:"input"`
	Computed    Scorecard  `json:"computed"`
	Unsupported []string   `json:"unsupported_hits"`
}

// FinalMarkdown renders a final-kind report.
func FinalMarkdown(m ReportMeta, f FinalBody) string {
	var b strings.Builder
	b.WriteString(m.header() + "\n" + m.title())

	verdict := fmt.Sprintf("**Recommendation:** %s. Score %.1f of 5 (interval %.2f to %.2f).\n\n%s\n\n%s",
		m.Rec, *m.Score, f.Card.Low, f.Card.High, decidingFactors(f, *m.Score), reasoning(f))
	if m.Fake {
		verdict += "\n\nNote: " + FakeNote + "."
	}
	if f.Doubtful {
		verdict += "\n\nWarning: this posting looks doubtful. Ask before applying."
	}
	section(&b, "Verdict", verdict)
	section(&b, "Snapshot", orDash(f.Out.Sections.Snapshot)+"\n\n"+roleFacts(f.Out.Posting, f.StatedPay)+"\n\n"+f.Sponsor)

	var fit strings.Builder
	fit.WriteString("| Group | Requirement | Hit | Evidence |\n|---|---|---|---|\n")
	for _, g := range []struct {
		name  string
		items []Item
	}{{"must-have", f.Checked.Must}, {"nice-to-have", f.Checked.Nice}, {"hidden signal", f.Checked.Hidden}, {"alignment", f.Checked.Align}} {
		for _, it := range g.items {
			evidence := it.Evidence
			if slices.Contains(f.Checked.Unsupported, it.ID) {
				evidence = "No profile source cited; claimed match needs review. " + evidence
			}
			fmt.Fprintf(&fit, "| %s | %s | %.1f | %s |\n", g.name, cell(it.Label), it.Hit, cell(evidence))
		}
	}
	doc := scorecardDoc{Input: f.Input, Computed: f.Card, Unsupported: f.Checked.Unsupported}
	doc.Items.Must, doc.Items.Nice, doc.Items.Hidden, doc.Items.Alignment, doc.Items.RedFlags =
		f.Checked.Must, f.Checked.Nice, f.Checked.Hidden, f.Checked.Align, f.Checked.RedFlags
	js, _ := json.MarshalIndent(doc, "", "  ")
	fit.WriteString("\n```scorecard\n" + string(js) + "\n```")
	section(&b, "Fit", fit.String())

	section(&b, "Gaps", gapTriage(f))
	section(&b, "Level and trajectory", levelFacts(f)+"\n\n"+orDash(f.Out.Sections.LevelTrajectory))
	section(&b, "Pay", f.Pay+"\n\n"+f.Out.Sections.PayNotes)
	section(&b, "Tailoring plan", list(f.Out.Sections.TailoringPlan, "1."))
	section(&b, "Interview prep", list(f.Out.Sections.InterviewPrep, "-"))

	legit := "Level: " + m.Legitimacy
	for _, s := range f.Signals {
		legit += "\n- " + line(s)
	}
	section(&b, "Legitimacy", legit)
	section(&b, "Keywords", strings.Join(dedupe(f.Out.Sections.Keywords), ", "))

	if f.Answers != nil {
		var a strings.Builder
		for _, x := range f.Answers {
			if x.Status == "unanswered" || strings.TrimSpace(x.Answer) == "" {
				fmt.Fprintf(&a, "**%s**\n\nUnanswered: the profile gives no basis.\n\n", line(x.Question))
				continue
			}
			fmt.Fprintf(&a, "**%s**\n\n%s\n\n", line(x.Question), strings.TrimSpace(x.Answer))
		}
		section(&b, "Application answers", a.String())
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// decidingFactors names, from the checked items and the computed card alone,
// what decides the recommendation and what would move it.
func decidingFactors(f FinalBody, score float64) string {
	var out []string
	add := func(format string, a ...any) { out = append(out, "- "+fmt.Sprintf(format, a...)) }
	var partial []string
	for _, it := range f.Checked.Must {
		switch {
		case it.Hit == 0 && slices.Contains(f.Checked.HonoredIDs, it.ID):
			add("Eligibility requirement not met: %s.", line(it.Label))
		case it.Hit == 0:
			add("Missed must-have: %s.", line(it.Label))
		case it.Hit < 1:
			partial = append(partial, line(it.Label))
		}
	}
	if len(partial) > 0 {
		add("Partly met must-haves: %d of %d (%s).", len(partial), len(f.Checked.Must), strings.Join(partial[:min(3, len(partial))], "; "))
	}
	for _, fl := range f.Checked.RedFlags {
		if fl.Level > 0 {
			add("Red flag (level %.1f): %s.", fl.Level, line(fl.Label))
		}
	}
	switch {
	case f.Card.Comp <= 2:
		add("Pay: the stated top is under 90%% of your minimum (pay score %.0f).", f.Card.Comp)
	case f.Card.Comp >= 5:
		add("Pay: the stated top reaches your target (pay score 5).")
	}
	binding := false
	if len(f.Card.Caps) > 0 {
		low := f.Card.Caps[0]
		for _, c := range f.Card.Caps[1:] {
			if c.Value < low.Value {
				low = c
			}
		}
		if f.Card.Raw > low.Value {
			binding = true
			add("The %s cap of %.1f holds the score below its uncapped %.2f; meeting the missed must-have lifts it.", low.Rule, low.Value, f.Card.Raw)
		}
	}
	for _, band := range []struct {
		name string
		from float64
	}{{RecConsider, SkipBelow}, {RecApply, ApplyFrom}, {RecStrong, StrongFrom}} {
		if tenths(score) < tenths(band.from) {
			gap := fmt.Sprintf("Next band: %s from %.1f, %.1f above this score", band.name, band.from, band.from-score)
			if binding {
				gap += ", out of reach while the cap holds"
			}
			add("%s.", gap)
			break
		}
	}
	if len(out) == 0 {
		return "Deciding factors: every must-have is met and no red flag was found."
	}
	return "Deciding factors:\n" + strings.Join(out, "\n")
}

// roleFacts lists what the posting states about the role, "not stated" for
// what it leaves out; pay is the stated pay line (FinalBody.StatedPay).
func roleFacts(p PostingFacts, pay string) string {
	stated := func(s string) string {
		if s = line(s); s == "" || strings.EqualFold(s, "unknown") {
			return "not stated"
		}
		return s
	}
	return strings.Join([]string{
		"- Role family: " + stated(p.RoleFamily),
		"- Location: " + stated(p.Location) + "; work model: " + stated(p.WorkModel),
		"- Employment type: " + stated(p.EmploymentType),
		"- Pay as stated: " + stated(pay),
		"- Company size: " + stated(p.CompanySize),
	}, "\n")
}

// levelFacts is the profile side of the level comparison.
func levelFacts(f FinalBody) string {
	years := "unknown (no dated employment history)"
	if f.Years != nil {
		years = fmt.Sprintf("%.0f", *f.Years)
	}
	targets := line(f.Targets)
	if targets == "" {
		targets = "not stated"
	}
	return fmt.Sprintf("Profile: %s years of experience from the employment history; target roles: %s.", years, targets)
}

var gapKinds = map[string]string{GapEligibility: "eligibility", GapCore: "core skill", GapPreference: "preference"}

// gapTriage renders the model's triage of each must-have below 1.0, in the
// must-have order. The kind is eligibility only for a gate the deterministic
// check honored, and nearest evidence that names no profile source reads as
// none.
func gapTriage(f FinalBody) string {
	byID := map[string]GapNote{}
	for _, g := range f.Out.Sections.Gaps {
		if _, seen := byID[g.ID]; !seen {
			byID[g.ID] = g
		}
	}
	var out []string
	for _, it := range f.Checked.Must {
		if it.Hit >= 1 {
			continue
		}
		g := byID[it.ID]
		kind := g.Kind
		switch {
		case slices.Contains(f.Checked.HonoredIDs, it.ID):
			kind = GapEligibility
		case kind == GapEligibility || kind == "":
			kind = GapCore
		}
		nearest := line(g.Nearest)
		if !NamesProfileSource(nearest) {
			nearest = "none in the profile"
		}
		mitigation := line(g.Mitigation)
		if mitigation == "" {
			mitigation = "none suggested"
		}
		out = append(out, fmt.Sprintf("- **%s** (hit %.1f, %s). Nearest evidence: %s. Mitigation: %s",
			line(it.Label), it.Hit, gapKinds[kind], nearest, mitigation))
	}
	if len(out) == 0 {
		return "Every must-have is met."
	}
	return strings.Join(out, "\n")
}

func reasoning(f FinalBody) string {
	met := 0
	for _, it := range f.Checked.Must {
		if it.Hit >= 1 {
			met++
		}
	}
	s := fmt.Sprintf("Computed from %d must-haves (%d fully met), %d nice-to-haves, %d hidden signals and %d alignment items; compensation scored %.0f.",
		len(f.Checked.Must), met, len(f.Checked.Nice), len(f.Checked.Hidden), len(f.Checked.Align), f.Card.Comp)
	if len(f.Card.Caps) > 0 {
		s += " Caps: " + capsText(f.Card.Caps) + "."
	}
	return s
}

func capsText(caps []Cap) string {
	parts := make([]string, len(caps))
	for i, c := range caps {
		parts[i] = fmt.Sprintf("%s %.1f (%s)", c.Rule, c.Value, line(c.Reason))
	}
	return strings.Join(parts, "; ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		k := strings.ToLower(strings.TrimSpace(s))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// ScreenBody is the content of a screening report.
type ScreenBody struct {
	Decision string
	Summary  string
	Blockers []string
	Reasons  []string
	Concerns []string
	Score    float64
	// Local is true when the screen ended on a local precheck, with no model.
	Local bool
	Facts string
}

// ScreeningMarkdown renders a screening-kind report.
func ScreeningMarkdown(m ReportMeta, s ScreenBody) string {
	var b strings.Builder
	b.WriteString(m.header() + "\n" + m.title())
	verdict := fmt.Sprintf("**Recommendation:** %s. Screening score %.1f of 5; decision %s.", m.Rec, s.Score, s.Decision)
	if m.Fake {
		verdict += "\n\nNote: " + FakeNote + "."
	}
	section(&b, "Verdict", verdict)
	section(&b, "Snapshot", orDash(s.Summary)+"\n\n"+s.Facts)
	var r strings.Builder
	fmt.Fprintf(&r, "Decision: %s\n\n", s.Decision)
	if s.Local {
		r.WriteString("Ended on a local check; no model was called.\n\n")
	}
	list := func(name string, items []string) {
		if len(items) == 0 {
			fmt.Fprintf(&r, "%s: none\n\n", name)
			return
		}
		fmt.Fprintf(&r, "%s:\n", name)
		for _, x := range items {
			fmt.Fprintf(&r, "- %s\n", line(x))
		}
		r.WriteString("\n")
	}
	list("Blockers", s.Blockers)
	list("Reasons", s.Reasons)
	list("Concerns", s.Concerns)
	section(&b, "Screen result", r.String())
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// GatedMarkdown renders a gated report.
func GatedMarkdown(m ReportMeta, failures []Failure) string {
	var b strings.Builder
	b.WriteString(m.header() + "\n" + m.title())
	section(&b, "Verdict", "**Recommendation:** skip. The posting failed the eligibility gate before any model was called.")
	var g strings.Builder
	for _, f := range failures {
		fmt.Fprintf(&g, "- %s: %s\n", f.Rule, line(f.Detail))
	}
	section(&b, "Gate result", g.String())
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ParseHeader reads the key: value lines at the top of a report.
func ParseHeader(markdown string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(markdown, "\n") {
		if strings.TrimSpace(l) == "" {
			break
		}
		if k, v, ok := strings.Cut(l, ": "); ok {
			out[k] = v
		}
	}
	return out
}

var slugRE = lazyregexp.New(`[^a-z0-9]+`)

// Slug makes a file-name fragment from a company name.
func Slug(name string) string {
	s := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(NormalizeCompany(name)), "-"), "-")
	if s == "" {
		return "unknown"
	}
	return s[:min(len(s), 40)]
}
