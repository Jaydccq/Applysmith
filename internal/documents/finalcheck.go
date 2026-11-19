package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	unorm "golang.org/x/text/unicode/norm"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/lazyregexp"
)

// The final checks read the text layer of the compiled PDF against what the
// product itself rendered. They sit beside the two verifier scripts,
// which own page counts, geometry and the text layer, and add the rules that
// need the source: contact values, word preservation, reading order, dates,
// percentages and fonts. Each is mandatory, and each is unverified rather than
// passed when its input is missing.

// RulesVersion names the revision of these rules and the font allow-lists; it
// is recorded with every verdict.
const RulesVersion = "1"

// Check ids of the final checks, in the order they are reported.
const (
	CheckContact   = "contact"
	CheckWords     = "word-coverage"
	CheckOrder     = "text-order"
	CheckGlyphs    = "glyphs"
	CheckDates     = "dates"
	CheckPercent   = "percent"
	CheckFonts     = "fonts"
	wordCoverageAt = 0.90
)

// finalCheckIDs lists the final checks in report order.
var finalCheckIDs = []string{CheckContact, CheckWords, CheckOrder, CheckGlyphs, CheckDates, CheckPercent, CheckFonts}

// fontRule holds a template to its fonts. Every letter must be set in an
// allowed family; with single set, all letters at or below bodyMaxPt share one
// font, so a list that fell back to another font stands out.
type fontRule struct {
	allowed   []string
	single    bool
	bodyMaxPt float64
}

// Expectation is what a final PDF must show. It is built from the data the
// templates print, never from the PDF.
type Expectation struct {
	Name   string
	Email  string
	Phone  string
	Others []string // other printed contact values: address, link labels
	// Order lists strings that must appear in this order.
	Order []string
	// Dates and Percents are the date ranges and percentages the source prints.
	Dates    []string
	Percents []string
	// Source is every string the document prints.
	Source string
	Fonts  fontRule
}

var (
	// resumeFonts: moderncv in its sans option sets text in Latin Modern Sans;
	// the contact icons come from Font Awesome and put their glyph names in the
	// text layer.
	resumeFonts = fontRule{allowed: []string{"LMSans", "FontAwesome"}}
	// coverFonts: the class sets the name in Lato and the body in Raleway.
	coverFonts = fontRule{allowed: []string{"Lato", "Raleway"}, single: true, bodyMaxPt: 14}

	addressRE = lazyregexp.New(`[a-z0-9._%+-]+@[a-z0-9.-]+`)
	percentRE = lazyregexp.New(`\d+(?:[.,]\d+)?\s?%`)
	// dateRE finds a year range such as 2018-2021 or 2021 - Present.
	dateRE = lazyregexp.New(`(?i)\b(?:19|20)\d{2}\s?-\s?(?:(?:19|20)\d{2}|present|current|now)\b`)
)

// unbold removes the bold markers of a rich line.
func unbold(s string) string { return strings.ReplaceAll(s, "**", "") }

// folded is s with compatibility forms and accents removed and case folded, the
// form in which both sides of a comparison are put so that ligatures, composed
// or decomposed accents and typographic punctuation do not matter.
func folded(s string) string {
	var b strings.Builder
	for _, r := range unorm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r == '‘' || r == '’':
			b.WriteByte('\'')
		case r == '“' || r == '”':
			b.WriteByte('"')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// skeleton keeps the letters and digits of folded(s), so that spacing,
// hyphenation and kerning artefacts of the extractor do not matter.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range folded(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wordSet is the distinct words of s with at least two characters.
func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(folded(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 2 {
			out[w] = true
		}
	}
	return out
}

// spaced is the words of folded(s) one space apart, padded, so that a
// containment test matches whole words only.
func spaced(s string) string {
	return " " + strings.Join(strings.FieldsFunc(folded(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ") + " "
}

func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// FinalChecks runs the final checks over the text layer of a built PDF. A text
// layer that is missing, or not the one the verifier hashed, leaves every check
// unverified.
func FinalChecks(e Expectation, o toolchain.BuildOutput) []toolchain.Check {
	if reason := textProblem(o); reason != "" {
		return unverifiedFinals(reason)
	}
	text := o.Text
	return []toolchain.Check{
		contactCheck(e, text),
		wordsCheck(e, text),
		orderCheck(e, text),
		glyphsCheck(text),
		dateCheck(e, text),
		percentCheck(e, text),
		fontsCheck(e, o.Verify.FontUsage),
	}
}

func unverifiedFinals(reason string) []toolchain.Check {
	out := make([]toolchain.Check, 0, len(finalCheckIDs))
	for _, id := range finalCheckIDs {
		out = append(out, toolchain.Check{ID: id, Status: toolchain.Unverified, Reason: reason})
	}
	return out
}

// textProblem says why the extracted text cannot be trusted, or "". The dump
// is hashed with the PDF's verdict so a text file from another run cannot
// stand in for this PDF's.
func textProblem(o toolchain.BuildOutput) string {
	if o.Verify.Pages == nil || o.Verify.TextSHA256 == nil {
		return "the text layer could not be extracted"
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	// The script appends one newline to a dump that lacks it.
	if want := *o.Verify.TextSHA256; sum(o.Text) != want && sum(strings.TrimSuffix(o.Text, "\n")) != want {
		return "the text layer is not the one the verifier hashed"
	}
	return ""
}

func result(id string, problems []string, ok string) toolchain.Check {
	if len(problems) > 0 {
		return toolchain.Check{ID: id, Status: toolchain.Fail, Reason: strings.Join(problems, "; ")}
	}
	return toolchain.Check{ID: id, Status: toolchain.Pass, Reason: ok}
}

func unverified(id, reason string) toolchain.Check {
	return toolchain.Check{ID: id, Status: toolchain.Unverified, Reason: reason}
}

// contactCheck requires the name and every contact value the document prints
// to be in the text layer. The email is matched as a whole token: moderncv puts
// an icon's glyph name before it, so the address must follow a separator, but
// it must not be fused to its neighbour.
func contactCheck(e Expectation, text string) toolchain.Check {
	if e.Name == "" || e.Email == "" {
		return unverified(CheckContact, "the source has no name or email to look for")
	}
	var missing []string
	// An address often spells the name, so addresses are set aside first. The
	// name must also read as its own words: a parser that reads "JordanAvery"
	// cannot tell the first name from the last.
	cleared := addressRE.ReplaceAllString(folded(text), " ")
	switch {
	case !strings.Contains(skeleton(cleared), skeleton(e.Name)):
		missing = append(missing, "the name")
	case !strings.Contains(spaced(cleared), spaced(e.Name)):
		missing = append(missing, "the name as separate words")
	}
	if !hasToken(folded(text), folded(e.Email)) {
		missing = append(missing, "the email address")
	}
	if e.Phone != "" && !strings.Contains(noSpace(folded(text)), noSpace(folded(e.Phone))) {
		missing = append(missing, "the phone number")
	}
	skel := skeleton(text)
	for _, o := range e.Others {
		if k := skeleton(o); k != "" && !strings.Contains(skel, k) {
			missing = append(missing, fmt.Sprintf("%q", o))
		}
	}
	if len(missing) > 0 {
		return result(CheckContact, []string{"not in the text layer: " + strings.Join(missing, ", ")}, "")
	}
	return result(CheckContact, nil, "")
}

// hasToken reports whether tok occurs in text with no address character
// directly before or after it.
func hasToken(text, tok string) bool {
	addr := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._%+-@", r)
	}
	for from := 0; ; {
		i := strings.Index(text[from:], tok)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(tok)
		before, after := []rune(text[:i]), []rune(text[end:])
		if (len(before) == 0 || !addr(before[len(before)-1])) && (len(after) == 0 || !addr(after[0])) {
			return true
		}
		from = i + 1
	}
}

// wordsCheck requires at least 90 percent of the distinct source words to
// survive. A long word split by the extractor across a line still counts when
// its letters are all there.
func wordsCheck(e Expectation, text string) toolchain.Check {
	src := wordSet(e.Source)
	if len(src) == 0 {
		return unverified(CheckWords, "the source has no words to look for")
	}
	have, skel := wordSet(text), skeleton(text)
	var missing []string
	for w := range src {
		if !have[w] && !(len([]rune(w)) >= 5 && strings.Contains(skel, skeleton(w))) {
			missing = append(missing, w)
		}
	}
	// A word the layer holds only with a space inside it ("T eam") is whole
	// to a reader and broken to a program that searches the text: the letter
	// font's pair kerning once produced it.
	var split []string
	low := folded(text)
	for w := range src {
		if r := []rune(w); !have[w] && len(r) >= 3 && splitIn(low, r) {
			split = append(split, w)
		}
	}
	if len(split) > 0 {
		sort.Strings(split)
		if len(split) > 5 {
			split = split[:5]
		}
		return result(CheckWords, []string{"words are split by a space in the text layer: " + strings.Join(split, ", ")}, "")
	}
	kept := float64(len(src)-len(missing)) / float64(len(src))
	if kept < wordCoverageAt {
		sort.Strings(missing)
		if len(missing) > 5 {
			missing = missing[:5]
		}
		return result(CheckWords, []string{fmt.Sprintf("%.0f%% of %d distinct words found, at least %.0f%% required; missing include %s",
			kept*100, len(src), wordCoverageAt*100, strings.Join(missing, ", "))}, "")
	}
	return result(CheckWords, nil, fmt.Sprintf("%.0f%% of %d distinct words found", kept*100, len(src)))
}

// splitIn reports whether the folded text holds the word with one space
// inside it, on one line, as a token of its own.
func splitIn(low string, word []rune) bool {
	for i := 1; i < len(word); i++ {
		needle := string(word[:i]) + " " + string(word[i:])
		for from := 0; ; {
			at := strings.Index(low[from:], needle)
			if at < 0 {
				break
			}
			at += from
			end := at + len(needle)
			before, _ := utf8.DecodeLastRuneInString(low[:at])
			after, _ := utf8.DecodeRuneInString(low[end:])
			if !isWordRune(before) && !isWordRune(after) {
				return true
			}
			from = at + 1
		}
	}
	return false
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// orderCheck requires the anchors to appear in reading order. Each is searched
// from the end of the previous one, so only a genuine reordering fails.
func orderCheck(e Expectation, text string) toolchain.Check {
	if len(e.Order) == 0 {
		return unverified(CheckOrder, "the source names no reading order")
	}
	skel, pos := skeleton(text), 0
	prev := ""
	for _, a := range e.Order {
		k := skeleton(a)
		if k == "" {
			continue
		}
		i := strings.Index(skel[pos:], k)
		if i < 0 {
			if prev == "" {
				return result(CheckOrder, []string{fmt.Sprintf("%q is not in the text layer", clip(a))}, "")
			}
			return result(CheckOrder, []string{fmt.Sprintf("%q does not follow %q in the text layer", clip(a), clip(prev))}, "")
		}
		pos += i + len(k)
		prev = a
	}
	return result(CheckOrder, nil, "")
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40])
	}
	return s
}

// glyphsCheck fails on characters an extractor emits when a glyph has no text:
// the replacement character, an unmapped CID, a private-use code point or a
// control character.
func glyphsCheck(text string) toolchain.Check {
	var problems []string
	if strings.Contains(text, "(cid:") {
		problems = append(problems, "unmapped glyph (cid:)")
	}
	counts := map[string]int{}
	for _, r := range text {
		switch {
		case r == '�':
			counts["the replacement character U+FFFD"]++
		case r >= '' && r <= '':
			counts["private-use code points"]++
		case unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r':
			counts["control characters"]++
		}
	}
	for _, k := range []string{"the replacement character U+FFFD", "private-use code points", "control characters"} {
		if counts[k] > 0 {
			problems = append(problems, fmt.Sprintf("%d of %s", counts[k], k))
		}
	}
	return result(CheckGlyphs, problems, "")
}

// dateCheck requires every date range the source prints to be in the text
// layer exactly, with its ASCII hyphen. A range joined by another dash, or
// split, is a broken date.
func dateCheck(e Expectation, text string) toolchain.Check {
	have := noSpace(strings.ReplaceAll(text, " ", " "))
	var missing []string
	for _, d := range e.Dates {
		if !strings.Contains(have, noSpace(d)) {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		return result(CheckDates, []string{"date range not intact in the text layer: " + strings.Join(missing, ", ")}, "")
	}
	if len(e.Dates) == 0 {
		return result(CheckDates, nil, "the source prints no date range")
	}
	return result(CheckDates, nil, "")
}

// percentCheck requires every percentage the source prints to keep its sign.
func percentCheck(e Expectation, text string) toolchain.Check {
	have := noSpace(text)
	var missing []string
	for _, p := range e.Percents {
		if !strings.Contains(have, noSpace(p)) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return result(CheckPercent, []string{"percentage not in the text layer: " + strings.Join(missing, ", ")}, "")
	}
	if len(e.Percents) == 0 {
		return result(CheckPercent, nil, "the source prints no percentage")
	}
	return result(CheckPercent, nil, "")
}

// fontsCheck holds the document to its template's fonts.
func fontsCheck(e Expectation, use []toolchain.FontUse) toolchain.Check {
	if use == nil {
		return unverified(CheckFonts, "the fonts of the text runs could not be read")
	}
	if len(e.Fonts.allowed) == 0 {
		return unverified(CheckFonts, "the template names no fonts")
	}
	var problems []string
	body := map[string]int{}
	for _, u := range use {
		if u.Letters < 3 {
			continue
		}
		ok := false
		for _, p := range e.Fonts.allowed {
			if strings.HasPrefix(u.Font, p) {
				ok = true
			}
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("%d letters set in %s, not a font of the template", u.Letters, u.Font))
		}
		if e.Fonts.single && u.SizePt <= e.Fonts.bodyMaxPt {
			body[u.Font] += u.Letters
		}
	}
	if len(body) > 1 {
		var names []string
		for f := range body {
			names = append(names, f)
		}
		sort.Strings(names)
		problems = append(problems, "body text is set in more than one font: "+strings.Join(names, ", "))
	}
	return result(CheckFonts, problems, "")
}
