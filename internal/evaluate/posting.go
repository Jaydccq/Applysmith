// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode"

	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
)

// MaxPostingChars caps the description text; MinPostingChars is the least text
// that counts as a readable posting; screenWindow bounds the quick screen's
// view of the text.
const (
	MaxPostingChars = 20000
	MinPostingChars = 200
	screenWindow    = 2500
)

const htmlTags = `a|abbr|article|aside|b|blockquote|body|br|button|center|code|dd|div|dl|dt|em|figure|font|footer|form|h[1-6]|head|header|hr|html|i|iframe|img|input|label|li|link|main|meta|nav|noscript|ol|p|path|pre|section|small|span|strong|style|sub|sup|svg|table|tbody|td|th|thead|tr|u|ul`

var (
	scriptBlock = lazyregexp.New(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)
	blockTag    = lazyregexp.New(`(?i)</?(p|div|br|li|ul|ol|h[1-6]|tr|section|article|header|footer|nav|aside|blockquote|table|hr)\b[^>]*>`)
	// Only known HTML tags are markup. Anything else, such as <instructions>,
	// stays in the text and is escaped before it enters a prompt.
	markupTag = lazyregexp.New(`(?i)</?(` + htmlTags + `)\b[^>]*>`)
	spaceRun  = lazyregexp.New(`[ \t\x{00a0}]+`)
	blankRun  = lazyregexp.New(`\n{3,}`)

	railHeading = lazyregexp.New(`(?i)^(similar|related|recommended|more|other|suggested) (jobs|roles|positions|opportunities)\b|^jobs you (may|might) (also )?(like|be interested in)|^people also viewed|^you may also like|^more jobs (from|at)\b|^explore (more )?(jobs|roles)\b|^featured jobs`)
	chromeLine  = lazyregexp.New(`(?i)^(privacy( policy)?|terms( of (use|service))?|cookie( settings| policy| preferences)?|accessibility|(c|\x{00a9})\s*\d{4}.*|all rights reserved.*|follow us.*|powered by .*|about us|contact us|careers|sign (in|up)|log ?in|skip to (main )?content|menu|home|search jobs|save( job)?|share|back to (all )?jobs)$`)
)

// CleanText turns a page or pasted text into plain posting text: markup
// stripped, control characters removed, navigation and footer lines dropped,
// everything from a "similar jobs" rail onward cut, whitespace collapsed, and
// the result capped at MaxPostingChars. Deterministic rules read this text, so
// another posting's requirements in a rail cannot veto this one.
func CleanText(raw string) string {
	t := scriptBlock.ReplaceAllString(raw, " ")
	t = blockTag.ReplaceAllString(t, "\n")
	t = markupTag.ReplaceAllString(t, "")
	t = html.UnescapeString(t)
	t = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, strings.ReplaceAll(t, "\r\n", "\n"))

	var kept []string
	for _, line := range strings.Split(t, "\n") {
		line = strings.TrimSpace(spaceRun.ReplaceAllString(line, " "))
		if len(kept) > 0 && len(line) <= 80 && railHeading.MatchString(line) {
			break
		}
		if len(line) <= 80 && chromeLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	out := blankRun.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")
	out = strings.TrimSpace(out)
	if r := []rune(out); len(r) > MaxPostingChars {
		out = string(r[:MaxPostingChars])
	}
	return out
}

// shortText is the failure of a posting text under MinPostingChars once it is
// cleaned. Its words say what to do next, so every front end can show them.
type shortText struct {
	chars  int
	pasted bool // the user supplied the text; otherwise a page read gave it
}

func (e shortText) Error() string {
	if e.pasted {
		return fmt.Sprintf("the posting text has %d characters; an evaluation needs at least %d, so paste the whole description", e.chars, MinPostingChars)
	}
	return fmt.Sprintf("the page gave only %d characters of text; an evaluation needs at least %d, so paste the posting's text instead", e.chars, MinPostingChars)
}

func tooShort(clean string, pasted bool) error {
	if n := len([]rune(clean)); n < MinPostingChars {
		return shortText{chars: n, pasted: pasted}
	}
	return nil
}

// CheckText is nil when the text, cleaned as an evaluation cleans it, is long
// enough to evaluate. The command line asks it before it queues anything.
func CheckText(raw string) error { return tooShort(CleanText(raw), true) }

// window bounds text to n characters on a rune boundary.
func window(text string, n int) string {
	if r := []rune(text); len(r) > n {
		return string(r[:n])
	}
	return text
}

var freeMail = lazyregexp.New(`(?i)[\w.+-]+@(gmail|yahoo|outlook|hotmail|aol|proton|protonmail|icloud|mail|gmx)\.\w+`)
var paymentAsk = lazyregexp.New(`(?i)\b(processing|application|registration|training|equipment|starter kit|onboarding|background check) fee\b|\bpay (a|the|for)\b[^.\n]{0,30}\b(fee|deposit)\b|\b(send|provide|submit|upload)\b[^.\n]{0,40}\b(passport|social security number|ssn|bank account number|driver'?s licen[cs]e|copy of your id)\b|\bwire transfer\b|\bgift cards?\b|\bcryptocurrency payment\b`)
var siteLink = lazyregexp.New(`(?i)https?://|www\.`)

// LegitimacySignals are the deterministic signals handed to the model. Payment
// is the request for payment or identity documents before an offer; it forces
// the level to doubtful.
type LegitimacySignals struct {
	Payment bool
	Others  []string
}

// Texts lists every signal for a prompt or a report.
func (l LegitimacySignals) Texts() []string {
	var out []string
	if l.Payment {
		out = append(out, "asks for payment or identity documents before an offer")
	}
	return append(out, l.Others...)
}

// Signals computes the deterministic legitimacy signals.
func Signals(text, company, rawURL string, ageDays int, tier Tier, atsNative bool) LegitimacySignals {
	var s LegitimacySignals
	s.Payment = paymentAsk.MatchString(text)
	if ageDays > 60 {
		s.Others = append(s.Others, "listing is over 60 days old")
	}
	// A mirror is a listing at an address that is not the employer's ATS; a
	// posting pasted with no address is a copy of nothing.
	if strings.TrimSpace(rawURL) != "" && !atsNative && (strings.TrimSpace(company) == "" || strings.EqualFold(strings.TrimSpace(company), "unknown")) {
		s.Others = append(s.Others, "mirror listing with no named employer")
	}
	if len([]rune(text)) < 800 {
		s.Others = append(s.Others, "thin description")
	}
	if freeMail.MatchString(text) && !siteLink.MatchString(text) {
		s.Others = append(s.Others, "free-mail contact address with no company site")
	}
	if tier == TierStaffing || tier == TierAggregator {
		s.Others = append(s.Others, "employer tier is "+string(tier))
	}
	return s
}

var legitRank = map[string]int{"doubtful": 0, "unclear": 1, "credible": 2}

// ApplyLegitimacy combines the model's level with the signals. The rules only
// lower the level: a payment request forces doubtful; two or more other
// signals cap it at unclear.
func ApplyLegitimacy(model string, s LegitimacySignals) string {
	if _, ok := legitRank[model]; !ok {
		model = "unclear"
	}
	switch {
	case s.Payment:
		return "doubtful"
	case len(s.Others) >= 2 && legitRank[model] > legitRank["unclear"]:
		return "unclear"
	}
	return model
}

func identityATS(rawURL string) (string, bool) { return identity.ATSKey(rawURL) }

// A decode reads one posting layer by layer and matches each requirement
// against the profile. It stores nothing, computes no score and creates no
// tracker row: it is analysis for the user to read.

// DecodeRequest is the posting to decode.
type DecodeRequest struct {
	Text, Title, Company, Location string
}

// MinDecodeChars is the shortest posting text a decode accepts.
const MinDecodeChars = 200

// ErrDecodeInput is a posting a decode cannot work from.
var ErrDecodeInput = errors.New("evaluate: decode input")

// DecodeNeed is one problem the hire is meant to solve.
type DecodeNeed struct {
	ID     string `json:"id"`
	Need   string `json:"need"`
	Source string `json:"source"`
}

// DecodeLevel is the level and team-stage layer.
type DecodeLevel struct {
	Seniority string `json:"seniority"`
	TeamStage string `json:"team_stage"`
	Ownership string `json:"ownership"`
	Source    string `json:"source"`
}

// DecodeWording is one phrase and its plain reading.
type DecodeWording struct {
	Phrase  string `json:"phrase"`
	Reading string `json:"reading"`
}

// decodeOutput is the structured reply of the jd-decode task.
type decodeOutput struct {
	Posting struct {
		Title   string `json:"title"`
		Company string `json:"company"`
	} `json:"posting"`
	Needs    []DecodeNeed    `json:"needs"`
	Must     []Item          `json:"must_have"`
	Nice     []Item          `json:"nice_to_have"`
	Hidden   []Item          `json:"hidden_signals"`
	Level    DecodeLevel     `json:"level"`
	Wording  []DecodeWording `json:"wording"`
	RedFlags []Flag          `json:"red_flags"`
}

// Decoding is a checked decode. The match matrix is Must, Nice and Hidden
// after the evaluation's own checks: a hit without a profile source is 0, and
// DealBreakers names the missed must-haves whose eligibility gate the
// requirement itself triggers. Unquoted lists the ids whose quoted source is
// not in the posting, so the reader knows they are inferences.
type Decoding struct {
	Title        string          `json:"title"`
	Company      string          `json:"company"`
	Needs        []DecodeNeed    `json:"needs"`
	Must         []Item          `json:"must_have"`
	Nice         []Item          `json:"nice_to_have"`
	Hidden       []Item          `json:"hidden_signals"`
	Level        DecodeLevel     `json:"level"`
	Wording      []DecodeWording `json:"wording"`
	RedFlags     []Flag          `json:"red_flags"`
	DealBreakers []string        `json:"deal_breakers"`
	Unsupported  []string        `json:"unsupported_hits"`
	Unquoted     []string        `json:"unquoted"`
	Prompt       prompts.Info    `json:"prompt"`
	Fake         bool            `json:"fake"`
}

// DecodeSchema is the schema the jd-decode reply must satisfy.
func DecodeSchema() json.RawMessage {
	need := obj([]string{"id", "need", "source"}, `"id":`+strT+`,"need":{"type":"string","minLength":1},"source":`+strT)
	level := obj([]string{"seniority", "team_stage", "ownership"}, `"seniority":`+strT+`,"team_stage":`+strT+`,"ownership":`+strT+`,"source":`+strT)
	wording := obj([]string{"phrase", "reading"}, `"phrase":`+strT+`,"reading":`+strT)
	flag := obj([]string{"id", "label", "level", "evidence"}, `"id":`+strT+`,"label":`+strT+`,"level":`+hitEnum+`,"evidence":`+strT)
	return json.RawMessage(obj([]string{"posting", "needs", "must_have", "nice_to_have", "hidden_signals", "level", "wording", "red_flags"},
		`"posting":`+obj([]string{"title", "company"}, `"title":`+strT+`,"company":`+strT)+
			`,"needs":{"type":"array","items":`+need+`}`+
			`,"must_have":`+itemList(`,"gate_class":`+gateEnum, 1)+
			`,"nice_to_have":`+itemList("", 0)+
			`,"hidden_signals":`+itemList("", 0)+
			`,"level":`+level+
			`,"wording":{"type":"array","items":`+wording+`}`+
			`,"red_flags":{"type":"array","items":`+flag+`}`))
}

// Decode reads a posting through the jd-decode template and the routed model,
// then applies the evaluation's checks to the match.
func (e *Evaluator) Decode(ctx context.Context, req DecodeRequest) (Decoding, error) {
	text := CleanText(req.Text)
	if len([]rune(strings.TrimSpace(text))) < MinDecodeChars {
		return Decoding{}, fmt.Errorf("%w: the posting text is shorter than %d characters", ErrDecodeInput, MinDecodeChars)
	}
	prof, err := e.d.Profile(ctx)
	if err != nil {
		return Decoding{}, err
	}
	stance, note := gates.Sponsorship(text)
	need := SponsorNeed(prof.WorkAuthorization)
	data := promptData{
		Posting: PostingView{Title: escapeText(req.Title), Company: escapeText(req.Company), Location: escapeText(req.Location), Text: escapeText(text)},
		Profile: prof.view(escapeText), Sponsor: SponsorView{Stance: string(stance), Note: note, Need: string(need)},
	}
	if e.d.Knowledge != nil {
		for _, s := range e.d.Knowledge(ctx, text) {
			data.Knowledge = append(data.Knowledge, Snippet{ID: s.ID, Text: escapeText(s.Text)})
		}
	}
	prompt, info, err := e.d.Prompts.Render("jd-decode", data)
	if err != nil {
		return Decoding{}, err
	}
	var out decodeOutput
	resp, err := e.d.Model.Structured(ctx, llm.Request{Task: "jd-decode", System: systemPrompt, Prompt: prompt}, DecodeSchema(), &out)
	if err != nil {
		return Decoding{}, err
	}
	ck := Check(DeepOutput{Must: out.Must, Nice: out.Nice, Hidden: out.Hidden}, CheckContext{Stance: stance, Need: need, ProfileYears: prof.Years(e.d.Now())})
	d := Decoding{
		Title: firstNonEmpty(req.Title, out.Posting.Title), Company: firstNonEmpty(req.Company, out.Posting.Company),
		Needs: []DecodeNeed{}, Must: ck.Must, Nice: ck.Nice, Hidden: ck.Hidden, Level: out.Level, Wording: nonNil(out.Wording), RedFlags: nonNil(out.RedFlags),
		DealBreakers: nonNil(ck.HonoredIDs), Unsupported: nonNil(ck.Unsupported), Unquoted: []string{}, Prompt: info,
		Fake: resp != nil && e.d.IsFake(ctx, resp.Provider),
	}
	posting := foldQuote(text)
	for _, n := range out.Needs {
		if !quoted(posting, n.Source) {
			d.Unquoted = append(d.Unquoted, n.ID)
		}
		d.Needs = append(d.Needs, n)
	}
	for _, group := range [][]Item{d.Must, d.Nice, d.Hidden} {
		for _, it := range group {
			if !quoted(posting, it.Source) {
				d.Unquoted = append(d.Unquoted, it.ID)
			}
		}
	}
	return d, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// foldQuote lowercases text, folds typographic quotes and dashes and keeps
// letters and digits only, one space between words, so a quote copied with
// different spacing or punctuation still matches.
func foldQuote(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// quoted reports whether source is a quote of the folded posting text. An
// empty quote, or one of fewer than two words, quotes nothing.
func quoted(foldedPosting, source string) bool {
	q := foldQuote(source)
	return strings.Count(q, " ") >= 1 && strings.Contains(foldedPosting, q)
}

// Markdown renders a decoding for reading: the five layers, the match matrix
// and what the checks changed.
func (d Decoding) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Decode: %s - %s\n\n", orDash(line(d.Company)), orDash(line(d.Title)))
	if d.Fake {
		b.WriteString("Note: " + FakeNote + ".\n\n")
	}
	b.WriteString("## What the team needs\n\n")
	for _, n := range d.Needs {
		fmt.Fprintf(&b, "- %s%s\n", line(n.Need), quoteSuffix(n.Source))
	}
	if len(d.Needs) == 0 {
		b.WriteString("- None found in the posting's own words.\n")
	}
	b.WriteString("\n## Level and team stage\n\n")
	fmt.Fprintf(&b, "- Seniority: %s\n- Team stage: %s\n- Ownership: %s\n", orDash(line(d.Level.Seniority)), orDash(line(d.Level.TeamStage)), orDash(line(d.Level.Ownership)))
	if s := strings.TrimSpace(d.Level.Source); s != "" {
		fmt.Fprintf(&b, "- From: \"%s\"\n", line(s))
	}
	b.WriteString("\n## Match matrix\n\n| Layer | Requirement | Hit | Evidence | Deal-breaker |\n|---|---|---|---|---|\n")
	breaker := map[string]bool{}
	for _, id := range d.DealBreakers {
		breaker[id] = true
	}
	for _, g := range []struct {
		name  string
		items []Item
	}{{"must-have", d.Must}, {"nice-to-have", d.Nice}, {"hidden signal", d.Hidden}} {
		for _, it := range g.items {
			mark := ""
			if breaker[it.ID] {
				mark = "yes (" + it.GateClass + ")"
			}
			fmt.Fprintf(&b, "| %s | %s | %.1f | %s | %s |\n", g.name, cell(it.Label), it.Hit, cell(it.Evidence), mark)
		}
	}
	if len(d.Wording) > 0 {
		b.WriteString("\n## Wording\n\n")
		for _, w := range d.Wording {
			fmt.Fprintf(&b, "- \"%s\": %s\n", line(w.Phrase), line(w.Reading))
		}
	}
	if len(d.RedFlags) > 0 {
		b.WriteString("\n## Red flags\n\n")
		for _, f := range d.RedFlags {
			fmt.Fprintf(&b, "- %s (level %.1f): %s\n", line(f.Label), f.Level, line(f.Evidence))
		}
	}
	var checks []string
	if len(d.Unsupported) > 0 {
		checks = append(checks, "Set to 0 because the evidence names no profile source: "+strings.Join(d.Unsupported, ", ")+".")
	}
	if len(d.Unquoted) > 0 {
		checks = append(checks, "The quoted words are not in the posting, so read these as inferences: "+strings.Join(d.Unquoted, ", ")+".")
	}
	if len(checks) > 0 {
		b.WriteString("\n## Checks\n\n")
		for _, c := range checks {
			b.WriteString("- " + c + "\n")
		}
	}
	b.WriteString("\nThis decode is not a score and creates no tracker row; evaluate the posting for a recommendation.\n")
	return b.String()
}

func quoteSuffix(source string) string {
	if s := strings.TrimSpace(source); s != "" {
		return " (\"" + line(s) + "\")"
	}
	return ""
}

// ReportMatches returns the requirements of a final report's scorecard that the
// profile meets at least in part, each with its evidence. A report without a
// scorecard returns none.
func ReportMatches(markdown string) []Item {
	doc, ok := scorecardOf(markdown)
	if !ok {
		return nil
	}
	var out []Item
	for _, group := range [][]Item{doc.Items.Must, doc.Items.Nice, doc.Items.Hidden} {
		for _, it := range group {
			if it.Hit >= 0.5 && strings.TrimSpace(it.Label) != "" && strings.TrimSpace(it.Evidence) != "" {
				out = append(out, it)
			}
		}
	}
	return out
}
