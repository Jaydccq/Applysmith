// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	_ "embed"
	"strings"
	"unicode"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// Tier is the kind of employer behind a posting.
type Tier string

const (
	TierDirect      Tier = "direct"
	TierStaffing    Tier = "staffing"
	TierConsultancy Tier = "consultancy"
	TierAggregator  Tier = "aggregator"
	TierUnknown     Tier = "unknown"
)

// Tiers lists every tier.
var Tiers = []Tier{TierDirect, TierStaffing, TierConsultancy, TierAggregator, TierUnknown}

//go:embed roster.tsv
var rosterData string

var tierRoster = func() map[string]Tier {
	m := map[string]Tier{}
	for _, line := range strings.Split(rosterData, "\n") {
		tier, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if key := NormalizeCompany(name); key != "" {
			m[key] = Tier(tier)
		}
	}
	return m
}()

var legalSuffix = map[string]bool{
	"inc": true, "incorporated": true, "llc": true, "llp": true, "lp": true, "ltd": true, "limited": true,
	"corp": true, "corporation": true, "co": true, "company": true, "plc": true, "gmbh": true, "ag": true,
	"sa": true, "srl": true, "bv": true, "nv": true, "pty": true, "pte": true,
}

var trailingParen = lazyregexp.New(`\s*\([^()]*\)\s*$`)

// NormalizeCompany folds case and accents, drops one trailing parenthetical,
// turns punctuation into separators and drops trailing legal suffixes while
// keeping at least one token. Matching on the result is exact.
func NormalizeCompany(name string) string {
	name = trailingParen.ReplaceAllString(strings.TrimSpace(name), "")
	name = strings.ReplaceAll(identity.Fold(name), "&", " and ")
	tokens := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for len(tokens) > 1 && legalSuffix[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " ")
}

// Text markers state placement at a client, a contract engagement, or the
// firm's own kind. The plural "our clients", the possessive "our client's"
// and a direct employer's "no third-party recruiters" notice are not markers.
var staffingMarkers = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\bour client\b[^'\w]`),
	lazyregexp.New(`(?i)\bon behalf of (our|a|one of our) (direct )?client\b`),
	lazyregexp.New(`(?i)\bcontract[- ]to[- ](hire|perm)\b`),
	lazyregexp.New(`(?i)\b(corp[- ]to[- ]corp|c2c)\b`),
	lazyregexp.New(`(?i)\bw-?2 (only|candidates only|employees only)\b`),
	lazyregexp.New(`(?i)\b(is|are) a (leading |global |national )?(staffing|recruiting|recruitment) (firm|agency|company)\b`),
	lazyregexp.New(`(?i)\b(we|this) (is|are) (a )?(staffing|recruiting) (firm|agency)\b`),
}

// ClassifyTier returns the employer tier. The roster takes priority over
// text; a rostered direct employer is never reclassified by text.
func ClassifyTier(company, text string) Tier {
	if t, ok := tierRoster[NormalizeCompany(company)]; ok {
		return t
	}
	for _, re := range staffingMarkers {
		if re.MatchString(text + " ") {
			return TierStaffing
		}
	}
	return TierUnknown
}
