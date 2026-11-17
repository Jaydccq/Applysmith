package documents

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// MaxRepairRounds is the number of repair rounds that may follow the first
// render. No caller, provider or setting raises it.
const MaxRepairRounds = 2

const (
	repairSchema = 1
	repairFile   = "layout-repair.json"
)

// roundReserve is the least pair budget a round needs: an engine pass and both
// verifiers.
const roundReserve = toolchain.EngineTimeout + 2*toolchain.HelperTimeout

// ErrStaleSource refuses a repair over content that no longer hashes to what its
// PDF was rendered from.
var ErrStaleSource = fmt.Errorf("%w: the content changed after it was rendered", ErrConflict)

// Outcomes of the repair rounds of a version.
const (
	RepairRepaired   = "repaired"
	RepairUnresolved = "unresolved"
)

// Cut is one approved line a round removed, with its rank, reason and effects.
type Cut struct {
	Document Kind     `json:"document"`
	Group    string   `json:"group"`
	Entry    string   `json:"entry,omitempty"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence,omitempty"`
	Rank     Rank     `json:"rank"`
	Reason   string   `json:"reason"`
	// Uncovered lists requirements that no remaining line of the résumé backs.
	Uncovered []string `json:"uncoveredRequirements,omitempty"`
	// Letter says what the cut did to the paired letter's claims.
	Letter []string `json:"letter,omitempty"`
}

// RepairRound is one round: trigger, adjustment, cuts, gates and rebuilt states.
type RepairRound struct {
	Round       int             `json:"round"`
	Trigger     []string        `json:"trigger"`
	Adjustments []string        `json:"adjustments,omitempty"`
	Cuts        []Cut           `json:"cuts"`
	Gates       map[Kind]string `json:"gates"`
	Rebuilt     map[Kind]string `json:"rebuilt"`
}

// CoverageChange is the résumé's requirement coverage before and after.
type CoverageChange struct {
	Before []Coverage `json:"before"`
	After  []Coverage `json:"after"`
}

// RepairRecord is layout-repair.json: why the version differs from the draft.
type RepairRecord struct {
	Schema   int            `json:"schema"`
	Rounds   []RepairRound  `json:"rounds"`
	Outcome  string         `json:"outcome"`
	Stopped  string         `json:"stopped,omitempty"`
	Coverage CoverageChange `json:"coverage"`
	// Sources are the hashes of each document's structured content before the
	// first round and after the last.
	Sources map[Kind][2]string `json:"sources"`
}

// Check ids a repair can act on: the page budget and the layout checks that
// removing text or tighter spacing can clear.
var (
	overflowChecks = []string{"max-pages", "page-count"}
	thinChecks     = []string{"final-page-thin"}
	fitChecks      = []string{"early-page-end", "footer-collision", "orphaned-entry", "internal-hole", "entry-gap"}
)

// defect is what a build shows that a repair may fix.
type defect struct {
	failed   []string
	overflow bool
	thin     bool
	fit      bool
	// excessPt is the height of the text beyond the page budget, or beyond
	// the pages a thin last page would fit on; 0 when it was not measured.
	excessPt float64
	// layer is the build's text layer, one printed line per line.
	layer string
}

func (d defect) repairable() bool { return d.overflow || d.thin || d.fit }

// defectsOf reads the failed checks of a build; allowed is its page limit and
// tight says whether the build used the tight spacing.
func defectsOf(b *built, allowed int, tight bool) defect {
	var d defect
	if b == nil || len(b.out.PDF) == 0 {
		return d
	}
	for _, c := range recordedChecks(b.out, b.finals) {
		if c.Status != toolchain.Fail {
			continue
		}
		switch {
		case c.Tool == toolPDF && hasID(overflowChecks, c.ID):
			d.overflow = true
		case c.Tool == toolLayout && hasID(thinChecks, c.ID):
			d.thin = true
		case c.Tool == toolLayout && hasID(fitChecks, c.ID):
			d.fit = true
		default:
			continue
		}
		d.failed = append(d.failed, c.Tool+" "+c.ID)
	}
	d.layer = b.out.Text
	ms := b.out.Layout.Measurements
	switch {
	case d.overflow:
		d.excessPt = spillPt(ms, allowed, b.layout, tight)
	case d.thin:
		d.excessPt = spillPt(ms, len(ms)-1, b.layout, tight)
	}
	return d
}

// pageGeometry is where the résumé template puts text on a page, measured with
// Poppler on the compiled template, in the verifier's coordinates.
type pageGeometry struct {
	// bottomPt is the lowest a line box reaches: an entry's last bullet set at
	// the bottom of the text area.
	bottomPt float64
	// entryTopPt is the top of an entry's first line at the top of a page. A
	// section heading's box starts 2.4pt higher.
	entryTopPt float64
}

var resumePages = map[assets.Paper]pageGeometry{
	assets.PaperA4:     {bottomPt: 745.8, entryTopPt: 64.5},
	assets.PaperLetter: {bottomPt: 701.9, entryTopPt: 60.5},
}

// The space a block needs above it when it moves up behind an entry, at the
// stock spacing: a section heading's, and another entry's. Tight spacing takes
// tightSavingPt off both (the entry's closing \vspace).
const (
	headingGapPt = 10.6
	entryGapPt   = 1.6
)

// lowestPt is the bottom of the lowest line box on a page.
func lowestPt(m toolchain.Measurement) float64 {
	if m.BottomSpacePt != nil {
		return m.HeightPt - *m.BottomSpacePt
	}
	if m.TextBottomPt != nil {
		return *m.TextBottomPt
	}
	return 0
}

// spillPt is the height that must go so the text of the pages after page from
// fits on page from: that text with the space above it, less the room page
// from still has. The room and the space are known only for the résumé; a
// letter counts the text alone.
func spillPt(ms []toolchain.Measurement, from int, layout toolchain.LayoutExpect, tight bool) float64 {
	geo, known := resumePages[layout.Paper]
	known = known && layout.Template == toolchain.TemplateResume
	need := 0.0
	for _, m := range ms {
		if m.TextTopPt == nil {
			continue
		}
		switch {
		case m.Page > from:
			need += math.Max(lowestPt(m)-*m.TextTopPt, 0)
			if known {
				gap := entryGapPt
				if *m.TextTopPt < geo.entryTopPt-1 {
					gap = headingGapPt
				}
				if tight {
					gap -= tightSavingPt
				}
				need += gap
			}
		case m.Page == from && known:
			need -= math.Max(geo.bottomPt-lowestPt(m), 0)
		}
	}
	return math.Max(need, 0)
}

// Height estimates measured on the compiled templates (A4, English). A round
// that falls short is topped up by the next, so they only size a round. A
// résumé line is cut at tight spacing (the adjustment comes first): a bullet
// line is 12pt, a competency line 16.3pt and each further printed line of it
// 13.5pt.
const (
	resumeLineChars  = 112
	resumeLinePt     = 12.0
	competencyItemPt = 16.3
	competencyLinePt = 13.5
	letterLineChars  = 89
	letterLinePt     = 14.4
	letterItemGapPt  = 6.6
	// tightSavingPt is what the spacing adjustment saves per bullet: the 2pt
	// between two bullets, and for an entry's last bullet the 2pt of the
	// entry's closing space.
	tightSavingPt   = 2.0
	maxCutsPerRound = 100
)

func lineCount(text string, perLine int) int {
	return max(1, (utf8.RuneCountInString(text)+perLine-1)/perLine)
}

// printedLines is the number of lines text takes in a text layer, or 0 when
// the layer does not hold it exactly once.
func printedLines(layer, text string) int {
	want := []rune(skeleton(text))
	if len(want) == 0 {
		return 0
	}
	var skel []rune
	var line []int
	n := 0
	for _, r := range folded(layer) {
		switch {
		case r == '\n':
			n++
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			skel, line = append(skel, r), append(line, n)
		}
	}
	at := -1
	for i := 0; i+len(want) <= len(skel); i++ {
		if slices.Equal(skel[i:i+len(want)], want) {
			if at >= 0 {
				return 0
			}
			at = i
		}
	}
	if at < 0 {
		return 0
	}
	return line[at+len(want)-1] - line[at] + 1
}

// resumeHeight is what cutting c saves at tight spacing. Its printed lines
// come from the build's text layer; a line the layer does not hold once is
// estimated from its length.
func resumeHeight(c candidate, layer string) float64 {
	lines := printedLines(layer, c.text)
	if lines == 0 {
		lines = lineCount(unbold(c.text), resumeLineChars)
	}
	if c.group == groupCompetency {
		return competencyItemPt + float64(lines-1)*competencyLinePt
	}
	return float64(lines) * resumeLinePt
}

func tightCredit(d ResumeDoc) float64 {
	n := 0
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for _, e := range list {
			n += len(e.Bullets)
		}
	}
	return float64(n) * tightSavingPt
}

func (v *version) allowedPages(k Kind) int {
	if k == KindCover {
		return 1
	}
	return v.pages
}

// docHash is the hash of the structured content of one document.
func (v *version) docHash(k Kind) string {
	if k == KindResume {
		return sha(jsonBytes(v.resume))
	}
	return sha(jsonBytes(v.letter))
}

// repair runs at most MaxRepairRounds rounds inside the pair budget of bctx. A
// round changes only approved content already in the documents or the template
// adjustment, then builds again; the gates and final checks run again. What a
// round cannot fix stays a failed PDF.
func (j *job) repair(ctx, bctx context.Context, b pdfBuilder, v *version, ts *texSet, builds map[Kind]*built, now time.Time) error {
	rec := &RepairRecord{Schema: repairSchema, Sources: map[Kind][2]string{}}
	var before []Coverage
	if v.resume != nil {
		before = coverageOf(v.m, *v.resume)
	}
	defects := func() (map[Kind]defect, []string) {
		out := map[Kind]defect{}
		var trigger []string
		for _, k := range j.kinds(v) {
			if d := defectsOf(builds[k], v.allowedPages(k), k == KindResume && v.tight); d.repairable() {
				out[k] = d
				trigger = append(trigger, string(k)+": "+strings.Join(d.failed, ", "))
			}
		}
		return out, trigger
	}
	start := map[Kind]string{}
	for _, k := range j.kinds(v) {
		start[k] = v.docHash(k)
	}
	attempted := false
	for round := 1; round <= MaxRepairRounds; round++ {
		defs, trigger := defects()
		if len(defs) == 0 {
			break
		}
		attempted = true
		if time.Until(deadlineOf(bctx)) < roundReserve {
			rec.Stopped = "the pair budget leaves no room for another round"
			break
		}
		for _, k := range j.kinds(v) {
			if bt := builds[k]; bt != nil && bt.srcHash != "" && bt.srcHash != v.docHash(k) {
				return fmt.Errorf("%w (%s)", ErrStaleSource, k)
			}
		}
		rr := RepairRound{Round: round, Trigger: trigger, Cuts: []Cut{}, Gates: map[Kind]string{}, Rebuilt: map[Kind]string{}}
		changed := j.repairRound(v, defs, &rr)
		if len(changed) == 0 {
			rec.Stopped = "no further repair is possible without inventing or dropping required content"
			break
		}
		for _, k := range changed {
			if err := render(v, ts, k, now); err != nil {
				return err
			}
			bt, err := j.buildOne(ctx, bctx, b, v, ts, k)
			if err != nil {
				return err
			}
			builds[k] = bt
			rr.Rebuilt[k] = bt.state
		}
		rec.Rounds = append(rec.Rounds, rr)
	}
	if !attempted {
		return nil
	}
	cuts := 0
	for _, rd := range rec.Rounds {
		cuts += len(rd.Cuts)
	}
	j.warn("layout repair: %d round(s), %d approved line(s) removed (layout-repair.json)", len(rec.Rounds), cuts)
	rec.Outcome = RepairRepaired
	for k := range v.repaired {
		// A rebuild that made no PDF has nothing left to measure: it repaired nothing.
		if bt := builds[k]; bt != nil && len(bt.out.PDF) == 0 {
			rec.Outcome = RepairUnresolved
		}
	}
	if rest, _ := defects(); len(rest) > 0 {
		rec.Outcome = RepairUnresolved
		for _, k := range j.kinds(v) {
			if d, ok := rest[k]; ok {
				j.warnOn(k, "layout repair left the %s unresolved after %d round(s) (%s); the PDF stays a failed preview", k, len(rec.Rounds), strings.Join(d.failed, ", "))
			}
		}
	}
	for k := range v.repaired {
		rec.Sources[k] = [2]string{start[k], v.docHash(k)}
	}
	rec.Coverage.Before = before
	if v.resume != nil {
		rec.Coverage.After = coverageOf(v.m, *v.resume)
	}
	v.repair = rec
	return nil
}

func deadlineOf(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(toolchain.PairBudget)
}

// repairRound applies one round and returns the documents that changed. The
// résumé goes first: its cuts can remove letter bullets.
func (j *job) repairRound(v *version, defs map[Kind]defect, rr *RepairRound) []Kind {
	var resume, letter bool
	if d, ok := defs[KindResume]; ok && v.resume != nil {
		resume, letter = j.repairResume(v, d, rr)
	}
	if d, ok := defs[KindCover]; ok && v.letter != nil && j.repairLetter(v, d, rr) {
		letter = true
	}
	if v.repaired == nil {
		v.repaired = map[Kind]bool{}
	}
	var changed []Kind
	for k, on := range map[Kind]bool{KindResume: resume, KindCover: letter} {
		if on {
			v.repaired[k] = true
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	return changed
}

func reasonFor(d defect, k Kind, c candidate) string {
	why := "layout defect (" + strings.Join(d.failed, ", ") + ")"
	if d.excessPt > 0 {
		why = fmt.Sprintf("%s: %.0fpt of text beyond the page budget", why, d.excessPt)
	}
	if c.group == groupLetterSentence {
		return fmt.Sprintf("%s; the letter's bullets could not absorb it, so its last removable evidence sentence was cut", why)
	}
	return fmt.Sprintf("%s; this was the lowest-ranked removable line of the %s (%d requirement(s) supported, %d evidence line(s) only it carries, letter dependency %t, strength %d)",
		why, k, c.rank.Requirements, c.rank.Unique, c.rank.Dependent, c.rank.Strength)
}

// repairResume applies the template adjustment, then cuts the lowest-ranked
// lines until the estimated excess is gone; it reports what changed.
func (j *job) repairResume(v *version, d defect, rr *RepairRound) (resume, letter bool) {
	need := withMargin(d.excessPt, rr.Round, resumeLinePt)
	adjusted := false
	if !v.tight {
		v.tight, adjusted = true, true
		rr.Adjustments = append(rr.Adjustments, "tight-spacing")
		need -= tightCredit(*v.resume)
		if d.excessPt == 0 || need <= 0 {
			// Tried alone first: it loses no content.
			return true, false
		}
	}
	cur := cloneResume(*v.resume)
	var curLetter *LetterDoc
	if v.letter != nil {
		cp := cloneLetter(*v.letter)
		curLetter = &cp
	}
	removed, any := 0.0, false
	for n := 0; n < maxCutsPerRound && (n == 0 || removed < need); n++ {
		cands := resumeCandidates(v.m, cur, curLetter)
		rankOrder(cands)
		picked := false
		for _, c := range cands {
			next := removeFromResume(cur, c)
			eff := effectOnLetter(cur, next, curLetter)
			if !eff.acceptable || !checkResume(v.m, next).OK() {
				continue
			}
			if eff.letter != nil && len(eff.letter.Bullets) != len(curLetter.Bullets) && !checkLetter(v.m, *eff.letter).OK() {
				continue
			}
			rr.Cuts = append(rr.Cuts, Cut{Document: KindResume, Group: c.group, Entry: c.entry, Text: c.text, Evidence: c.evidence, Rank: c.rank,
				Reason: reasonFor(d, KindResume, c), Uncovered: uncovered(v.m, cur, next), Letter: eff.notes})
			removed += resumeHeight(c, d.layer)
			cur, picked, any = next, true, true
			if eff.letter != nil {
				curLetter = eff.letter
			}
			break
		}
		if !picked {
			break
		}
	}
	if !any {
		// Nothing could be cut. If the adjustment was applied it still counts.
		return adjusted, false
	}
	v.resume = &cur
	v.resumeGate = checkResume(v.m, cur)
	rr.Gates[KindResume] = "passed"
	if curLetter != nil && len(curLetter.Bullets) != len(v.letter.Bullets) {
		v.letter = curLetter
		v.letterGate = checkLetter(v.m, *curLetter)
		rr.Gates[KindCover] = "passed"
		letter = true
	}
	return true, letter
}

// repairLetter cuts ranked bullets, then trailing sentences, to the excess.
func (j *job) repairLetter(v *version, d defect, rr *RepairRound) bool {
	cur := cloneLetter(*v.letter)
	need := withMargin(d.excessPt, rr.Round, letterLinePt)
	removed, any := 0.0, false
	for n := 0; n < maxCutsPerRound && (n == 0 || removed < need); n++ {
		bullets, sents := letterCandidates(v.m, cur)
		picked := false
		for _, c := range append(bullets, sents...) {
			next := removeFromLetter(cur, c)
			if !checkLetter(v.m, next).OK() {
				continue
			}
			rr.Cuts = append(rr.Cuts, Cut{Document: KindCover, Group: c.group, Text: c.text, Evidence: c.evidence, Rank: c.rank, Reason: reasonFor(d, KindCover, c)})
			removed += float64(lineCount(c.text, letterLineChars)) * letterLinePt
			if c.group == groupLetterBullet {
				removed += letterItemGapPt
			}
			cur, picked, any = next, true, true
			break
		}
		if !picked {
			break
		}
	}
	if !any {
		return false
	}
	v.letter = &cur
	v.letterGate = checkLetter(v.m, cur)
	rr.Gates[KindCover] = "passed"
	return true
}

// withMargin adds a line to the last round's target: blocks the templates keep
// together (an entry, the closing) move as a unit.
func withMargin(excess float64, round int, line float64) float64 {
	if excess > 0 && round >= MaxRepairRounds {
		return excess + line
	}
	return excess
}

// lostRequirements lists the requirements the résumé backed before the repair
// and no longer does. A nil record loses none.
func (r *RepairRecord) lostRequirements() []string {
	if r == nil {
		return nil
	}
	had := map[string]bool{}
	for _, c := range r.Coverage.Before {
		had[c.Requirement] = len(c.Evidence) > 0
	}
	var out []string
	for _, c := range r.Coverage.After {
		if had[c.Requirement] && len(c.Evidence) == 0 {
			out = append(out, c.Requirement)
		}
	}
	return out
}

// repairSummary is the part of the record the bundle manifest carries.
func repairSummary(r *RepairRecord, tight bool) map[string]any {
	if r == nil {
		return map[string]any{"rounds": 0, "adjustments": []string{}}
	}
	adj := []string{}
	if tight {
		adj = append(adj, "tight-spacing")
	}
	return map[string]any{"rounds": len(r.Rounds), "outcome": r.Outcome, "adjustments": adj, "file": repairFile}
}
