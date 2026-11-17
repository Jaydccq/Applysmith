// Package link scores a Gmail signal against the tracker's candidacies and
// decides whether it links automatically, needs review or links to nothing.
// It is pure: the caller supplies the candidacies and the links a thread
// already holds.
package link

import (
	"slices"
	"strings"
	"unicode"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/identity"
)

// Version is stored with every decision.
const Version = "link-1"

// Decisions.
const (
	Auto     = "auto"
	Review   = "review"
	Unlinked = "unlinked"
)

// Thresholds and weights of the score.
const (
	AutoScore     = 0.5
	AutoLead      = 0.25
	ReviewFloor   = 0.2
	companyWeight = 0.5
	roleWeight    = 0.4
	subjectWeight = 0.2
	senderWeight  = 0.15
	mismatch      = 0.3
	// coverageFloor is the role-token coverage below which two roles conflict.
	coverageFloor = 0.6
	// eps absorbs float rounding at the thresholds.
	eps = 1e-9
)

// Reasons recorded with a decision.
const (
	ReasonClearMatch        = "clear_single_match"
	ReasonAmbiguous         = "ambiguous_runner_up"
	ReasonBelowAuto         = "below_auto_threshold"
	ReasonNoCandidate       = "no_viable_candidate"
	ReasonThreadContinuity  = "thread_continuity"
	ReasonThreadMultiLinked = "thread_multi_linked"
	ReasonDuplicateCluster  = "duplicate_cluster"
	ReasonRoleQualifier     = "role_qualifier_mismatch"
)

// Candidacy is the part of a tracker row the linker reads.
type Candidacy struct {
	ID            int64
	Company, Role string
}

// Signal is the part of a stored signal the linker reads.
type Signal struct {
	Company, Role string
	Subject       string
	// Sender is the raw From header.
	Sender        string
	ReviewReasons []string
}

// Decision is the outcome for one signal.
type Decision struct {
	Kind string
	// CandidacyID is zero unless the decision is auto.
	CandidacyID int64
	Score       float64
	RunnerUp    float64
	Reasons     []string
}

var stopwords = toSet("and", "the", "of", "for", "a", "an", "in", "at", "to", "with", "remote", "hybrid", "onsite", "new", "us", "usa", "ii", "iii", "iv", "i")

// generic role nouns and levels carry no information about which role is meant.
var generic = toSet("engineer", "engineers", "engineering", "developer", "developers", "manager", "managers", "analyst", "analysts",
	"specialist", "associate", "senior", "junior", "staff", "principal", "lead", "intern", "sr", "jr", "software", "full", "time", "part")

func toSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

func tokens(s string) []string {
	f := strings.FieldsFunc(identity.Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return f
}

func roleTokens(role string) map[string]bool {
	out := map[string]bool{}
	for _, w := range tokens(role) {
		if !stopwords[w] {
			out[w] = true
		}
	}
	return out
}

func distinctive(set map[string]bool) map[string]bool {
	out := map[string]bool{}
	for w := range set {
		if !generic[w] {
			out[w] = true
		}
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}

func placeholderRole(role string) bool {
	r := strings.TrimSpace(role)
	return r == "" || strings.EqualFold(r, classify.UnknownRole)
}

func companyKeys(name string) []string {
	if strings.EqualFold(strings.TrimSpace(name), classify.UnknownCompany) {
		return nil
	}
	return identity.CompanyKeys(name)
}

// party is a signal's or a candidacy's company and role, tokenized once: a
// pass compares every signal with every candidacy, so nothing is tokenized
// per pair.
type party struct {
	keys []string
	// keyWords are the words of each company key.
	keyWords  [][]string
	roleKnown bool
	// role holds the role's tokens without stopwords, distinct those without
	// generic nouns either, and line all of its tokens in order.
	role, distinct map[string]bool
	line           string
}

func newParty(company, role string) party {
	p := party{keys: companyKeys(company), roleKnown: !placeholderRole(role), role: roleTokens(role), line: strings.Join(tokens(role), " ")}
	p.distinct = distinctive(p.role)
	for _, k := range p.keys {
		p.keyWords = append(p.keyWords, strings.Fields(k))
	}
	return p
}

func sameCompany(a, b party) bool {
	for _, k := range a.keys {
		if slices.Contains(b.keys, k) {
			return true
		}
	}
	return false
}

// conflict reports a distinctive role conflict: both sides name a role and
// neither covers 60 percent of the other once generic nouns are removed.
func conflict(sig, cand party) bool {
	if !sig.roleKnown || !cand.roleKnown || len(sig.distinct) == 0 || len(cand.distinct) == 0 {
		return false
	}
	n := float64(overlap(sig.distinct, cand.distinct))
	return n/float64(len(sig.distinct)) < coverageFloor && n/float64(len(cand.distinct)) < coverageFloor
}

// prepared is a candidacy with its party read.
type prepared struct {
	Candidacy
	party
}

// Pool is the candidacies of one linking pass, each tokenized once.
type Pool []prepared

// NewPool reads the candidacies a pass links signals to.
func NewPool(cands []Candidacy) Pool {
	out := make(Pool, len(cands))
	for i, c := range cands {
		out[i] = prepared{c, newParty(c.Company, c.Role)}
	}
	return out
}

// signal is a signal with its party, subject and sender read.
type signal struct {
	party
	subject, sender map[string]bool
}

func newSignal(s Signal) signal {
	return signal{newParty(s.Company, s.Role), toSet(tokens(s.Subject)...), toSet(tokens(s.Sender)...)}
}

// Score adds the evidence that a signal belongs to a candidacy.
func Score(s Signal, c Candidacy) float64 {
	return score(newSignal(s), prepared{c, newParty(c.Company, c.Role)})
}

func score(s signal, c prepared) float64 {
	score := 0.0
	if sameCompany(s.party, c.party) {
		score += companyWeight
	}
	if s.roleKnown && c.roleKnown {
		if len(s.role) > 0 {
			score += roleWeight * float64(overlap(s.role, c.role)) / float64(len(s.role))
		}
		if conflict(s.party, c.party) {
			score -= mismatch
		}
	}
	if matchesCandidacy(s.subject, c) {
		score += subjectWeight
	}
	if matchesCandidacy(s.sender, c) {
		score += senderWeight
	}
	return max(score, 0)
}

// matchesCandidacy reports whether a token set holds a distinctive token of the
// candidacy's role or the whole of its company name.
func matchesCandidacy(words map[string]bool, c prepared) bool {
	if c.roleKnown && overlap(c.distinct, words) > 0 {
		return true
	}
	for _, ws := range c.keyWords {
		if len(ws) > 0 && slices.IndexFunc(ws, func(w string) bool { return !words[w] }) < 0 {
			return true
		}
	}
	return false
}

func duplicated(best prepared, pool Pool) bool {
	for _, c := range pool {
		if c.ID != best.ID && sameCompany(c.party, best.party) && c.line == best.line {
			return true
		}
	}
	return false
}

// Decide links a signal. threadLinks are the distinct candidacies the signal's
// Gmail thread is already linked to.
func Decide(s Signal, pool Pool, threadLinks []int64) Decision {
	d := decide(s, pool, threadLinks)
	if d.Kind == Auto && forcesReview(s.ReviewReasons) {
		d.Kind, d.CandidacyID = Review, 0
		d.Reasons = append(d.Reasons, s.ReviewReasons...)
	}
	return d
}

func forcesReview(reasons []string) bool {
	return slices.Contains(reasons, classify.ReviewSenderUnverified) || slices.Contains(reasons, classify.ReviewSignupWindowUnknown)
}

func decide(s Signal, pool Pool, threadLinks []int64) Decision {
	switch {
	case len(threadLinks) == 1:
		return Decision{Kind: Auto, CandidacyID: threadLinks[0], Score: 1, Reasons: []string{ReasonThreadContinuity}}
	case len(threadLinks) > 1:
		return Decision{Kind: Review, Reasons: []string{ReasonThreadMultiLinked}}
	}
	type scored struct {
		c     prepared
		score float64
	}
	sig := newSignal(s)
	var all []scored
	for _, c := range pool {
		all = append(all, scored{c, score(sig, c)})
	}
	slices.SortStableFunc(all, func(a, b scored) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	if len(all) == 0 || all[0].score < ReviewFloor-eps {
		return Decision{Kind: Unlinked, Reasons: []string{ReasonNoCandidate}}
	}
	best, runner := all[0], 0.0
	if len(all) > 1 {
		runner = all[1].score
	}
	d := Decision{Score: round2(best.score), RunnerUp: round2(runner)}
	switch {
	case best.score >= AutoScore-eps && best.score-runner >= AutoLead-eps:
		d.Kind, d.CandidacyID, d.Reasons = Auto, best.c.ID, []string{ReasonClearMatch}
		switch {
		case duplicated(best.c, pool):
			d.Kind, d.CandidacyID, d.Reasons = Review, 0, []string{ReasonDuplicateCluster}
		case conflict(sig.party, best.c.party):
			d.Kind, d.CandidacyID, d.Reasons = Review, 0, []string{ReasonRoleQualifier}
		}
	case best.score >= AutoScore-eps:
		d.Kind, d.Reasons = Review, []string{ReasonAmbiguous}
		if duplicated(best.c, pool) {
			d.Reasons = []string{ReasonDuplicateCluster}
		}
	default:
		d.Kind, d.Reasons = Review, []string{ReasonBelowAuto}
	}
	if d.Kind == Review && !slices.Contains(d.Reasons, ReasonRoleQualifier) && sameCompany(sig.party, best.c.party) && conflict(sig.party, best.c.party) {
		d.Reasons = append(d.Reasons, ReasonRoleQualifier)
	}
	return d
}

// Strength says in one word how close a stored best score is, by the
// thresholds Decide links on: close from AutoScore, weak from ReviewFloor, else
// none.
func Strength(score float64) string {
	switch {
	case score >= AutoScore-eps:
		return "close"
	case score >= ReviewFloor-eps:
		return "weak"
	}
	return "none"
}

// NearlyAsClose reports whether a runner-up is within AutoLead of the best
// score, the lead Decide asks for before it links on its own.
func NearlyAsClose(score, runnerUp float64) bool {
	return runnerUp > 0 && score-runnerUp < AutoLead-eps
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
