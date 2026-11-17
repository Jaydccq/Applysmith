package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/lazyregexp"
)

// Bounds of the independent review. The review and the one revision are model
// calls inside the run's own deadline: each is skipped when what is left would
// starve the compile and publish steps behind it.
const (
	ReviewTimeout   = 60 * time.Second
	RevisionTimeout = 120 * time.Second
	publishMargin   = 30 * time.Second
)

// Statuses of document-review.json. A review that did not run is unavailable,
// never approved.
const (
	ContentUnavailable = "unavailable"
	ContentReviewed    = "reviewed"
	ContentRevised     = "revised"
)

const reviewRecordSchema = 1

// reviewFinding is a factual or wording finding: a draft span, a category and
// the manifest ids that settle it. The edit is a hint for the one revision.
type reviewFinding struct {
	Span     string   `json:"span"`
	Category string   `json:"category"`
	Issue    string   `json:"issue"`
	Evidence []string `json:"evidence"`
	Edit     string   `json:"edit,omitempty"`
}

// narrativeNote is a broader suggestion. It is recorded and never applied.
type narrativeNote struct {
	Span       string `json:"span,omitempty"`
	Category   string `json:"category"`
	Suggestion string `json:"suggestion"`
}

type reviewReply struct {
	Factual   []reviewFinding `json:"factual"`
	Narrative []narrativeNote `json:"narrative"`
}

// rejectedFinding is a finding the review step refused to carry forward.
type rejectedFinding struct {
	Kind   string `json:"kind"`
	Span   string `json:"span,omitempty"`
	Text   string `json:"text"`
	Edit   string `json:"edit,omitempty"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// RevisionRecord says what became of the one allowed revision.
type RevisionRecord struct {
	Attempted  bool   `json:"attempted"`
	Applied    bool   `json:"applied"`
	Reason     string `json:"reason"`
	ResumeGate *Gate  `json:"resumeGate,omitempty"`
	LetterGate *Gate  `json:"letterGate,omitempty"`
}

// ReviewDeadline is the run time left around the review, in whole seconds.
type ReviewDeadline struct {
	RunLimit       int `json:"runLimitSeconds"`
	AtReviewStart  int `json:"remainingAtReviewStartSeconds"`
	AtRevisionEnd  int `json:"remainingAtEndSeconds"`
	ReviewTimeout  int `json:"reviewTimeoutSeconds"`
	RevisionBudget int `json:"revisionTimeoutSeconds"`
}

// ReviewCalls are the ledger call ids of the calls that answered.
type ReviewCalls struct {
	Review   string `json:"review,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// ReviewRecord is the document-review.json of a version.
type ReviewRecord struct {
	Schema       int               `json:"schema"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	Revisions    int               `json:"revisions"`
	ManifestHash string            `json:"manifestHash"`
	Calls        ReviewCalls       `json:"calls"`
	Factual      []reviewFinding   `json:"factual"`
	Narrative    []narrativeNote   `json:"narrative"`
	Rejected     []rejectedFinding `json:"rejected"`
	Revision     *RevisionRecord   `json:"revision,omitempty"`
	Deadline     ReviewDeadline    `json:"deadline"`
}

// ReviewSummary is what a reader of a stored version is shown.
type ReviewSummary struct {
	Status    string
	Reason    string
	Revisions int
	Factual   int
	Narrative int
	Rejected  int
}

type reviewRun struct {
	rec      ReviewRecord
	accepted []reviewFinding
}

func (j *job) remaining(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return max(time.Until(d), 0)
	}
	return RunTimeout
}

// reserve is what the steps after the model calls need.
func (j *job) reserve() time.Duration {
	r := publishMargin
	if j.req.PDF {
		r += toolchain.PairBudget
	}
	return r
}

func (j *job) review(ctx context.Context, v *version) (*reviewRun, error) {
	rr := &reviewRun{rec: ReviewRecord{Schema: reviewRecordSchema, ManifestHash: v.m.Hash, Factual: []reviewFinding{}, Narrative: []narrativeNote{}, Rejected: []rejectedFinding{}}}
	rec := &rr.rec
	rec.Deadline = ReviewDeadline{RunLimit: int(j.s.runTimeout.Seconds()), AtReviewStart: int(j.remaining(ctx).Seconds()),
		ReviewTimeout: int(ReviewTimeout.Seconds()), RevisionBudget: int(RevisionTimeout.Seconds())}
	rec.Deadline.AtRevisionEnd = rec.Deadline.AtReviewStart
	unavailable := func(reason string) { rec.Status, rec.Reason = ContentUnavailable, reason }
	switch {
	case j.noModel:
		unavailable("no usable model route")
		return rr, nil
	case j.remaining(ctx) < ReviewTimeout+j.reserve():
		unavailable("the run deadline leaves no room for a review")
		j.warn("the independent review was skipped: the run deadline leaves no room for it")
		return rr, nil
	}
	var reply reviewReply
	err := j.callAs(ctx, reviewSystem, reviewTask, reviewPrompt(v.m, v), reviewSchema, &reply, ReviewTimeout)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, err
	case err != nil && j.noModel:
		unavailable("no usable model route")
		return rr, nil
	case err != nil:
		unavailable(fmt.Sprintf("the review call failed (%s)", errKind(err)))
		j.warn("the independent review is unavailable (%s); the deterministic checks still apply", errKind(err))
		return rr, nil
	}
	rec.Calls.Review = j.lastCall
	rec.Status = ContentReviewed
	rr.accepted, rec.Narrative, rec.Rejected = screenReply(v.m, draftSpans(v.resume, v.letter), reply)
	rec.Factual = append(rec.Factual, rr.accepted...)
	rec.Deadline.AtRevisionEnd = int(j.remaining(ctx).Seconds())
	return rr, nil
}

// traitRE catches a finding that reads a personality or behavioural trait into
// the writer; the voice is never changed on that basis.
var traitRE = lazyregexp.New(`(?i)\b(personality|introvert\w*|extrovert\w*|temperament|demeanou?r|neurotic|agreeable|conscientious\w*|emotionally)\b`)

// screenReply keeps the findings that point at a real span, cite real manifest
// ids and propose nothing the evidence does not hold. A rejected finding is
// recorded with its reason and never reaches the revision. A narrative note is
// screened for unsupported claims too, though it is never applied.
func screenReply(m Manifest, spans []span, reply reviewReply) (accepted []reviewFinding, narrative []narrativeNote, rejected []rejectedFinding) {
	byAddr := map[string]span{}
	for _, s := range spans {
		byAddr[s.addr] = s
	}
	narrative = []narrativeNote{}
	rejected = []rejectedFinding{}
	known := m.userTexts()
	var drafted []string
	for _, s := range spans {
		known = append(known, s.text)
		drafted = append(drafted, s.text)
	}
	// A note may cite what the reviewer was shown by id or address ("E13",
	// "R4", "X1.2", "competency.4"); it is advice and never printed.
	for _, e := range m.Evidence {
		drafted = append(drafted, e.ID)
	}
	for _, r := range m.Requirements {
		drafted = append(drafted, r.ID)
	}
	for _, s := range spans {
		for a, ok := s.addr, true; ok; _, a, ok = strings.Cut(a, ".") {
			drafted = append(drafted, a)
		}
	}
	vocab := m.names(drafted...)
	posting := append(append(drafted, m.Company, m.Role), m.NiceToHave...)
	for _, r := range m.Requirements {
		posting = append(posting, r.Text)
	}
	// "US" is the posting's "United States".
	for _, t := range posting {
		var ini []rune
		for _, w := range append(nameTokens(t), nameTok{text: "a"}) {
			if capitalOnly(w.text) {
				ini = append(ini, []rune(w.text)[0])
				continue
			}
			if len(ini) > 1 {
				posting = append(posting, string(ini))
			}
			ini = nil
		}
	}
	gapVocab := m.names(posting...)
	for _, f := range reply.Factual {
		g := screenFinding(m, byAddr, f)
		if g.OK() {
			accepted = append(accepted, f)
			continue
		}
		rejected = append(rejected, rejectedFinding{Kind: "factual", Span: f.Span, Text: f.Issue, Edit: f.Edit, Code: g.Errors[0].Code, Detail: gateDetail(g)})
	}
	for _, n := range reply.Narrative {
		var g Gate
		if _, ok := byAddr[n.Span]; n.Span != "" && !ok {
			g.fail("unknown-span", "%s is not a span of the draft", n.Span)
		}
		if traitRE.MatchString(n.Suggestion) {
			g.fail("trait-inference", "a personality or behavioural trait is not a basis for a change")
		}
		// Advice that asks the writer to claim a figure or a name no evidence or
		// draft line holds is a fabrication suggestion, not advice.
		checkClaims(&g, "narrative note", n.Suggestion, corpusQuantities(known, nil))
		for _, sent := range noteSentRE.Split(n.Suggestion, -1) {
			v := vocab
			if gapSentence(sent) {
				v = gapVocab
			}
			checkNames(&g, "narrative note", sent, v)
		}
		if g.OK() {
			narrative = append(narrative, n)
			continue
		}
		rejected = append(rejected, rejectedFinding{Kind: "narrative", Span: n.Span, Text: n.Suggestion, Code: g.Errors[0].Code, Detail: gateDetail(g)})
	}
	return accepted, narrative, rejected
}

func gateDetail(g Gate) string {
	parts := make([]string, len(g.Errors))
	for i, e := range g.Errors {
		parts[i] = e.Code + ": " + e.Detail
	}
	return strings.Join(parts, "; ")
}

func screenFinding(m Manifest, byAddr map[string]span, f reviewFinding) Gate {
	var g Gate
	sp, ok := byAddr[f.Span]
	if !ok {
		g.fail("unknown-span", "%s is not a span of the draft", f.Span)
		return g
	}
	if len(f.Evidence) == 0 {
		g.fail("no-evidence", "%s: a factual finding cites no manifest evidence", f.Span)
	}
	checkEvidenceIDs(&g, m, f.Span, "", f.Evidence)
	if traitRE.MatchString(f.Issue + " " + f.Edit) {
		g.fail("trait-inference", "a personality or behavioural trait is not a basis for a change")
	}
	if f.Edit == "" {
		return g
	}
	// An edit may use a figure only when a line it cites or the span it
	// replaces already holds it. A company fact is never part of an edit; a résumé span has no use for one.
	texts := []string{sp.text}
	for _, id := range f.Evidence {
		if e, ok := m.evidence(id); ok {
			texts = append(texts, e.Text)
		}
	}
	c, n := corpusQuantities(texts, append(m.userTexts(), texts...)), m.names(sp.text)
	if strings.HasPrefix(f.Span, "letter") {
		// An edit never types a company fact: the system prints the facts the
		// letter chose. Which facts the letter chose is not known here, so an
		// edit of the closing is held to every fact it could have chosen, and
		// an edit of an earlier part must not point ahead at the facts.
		m.checkCompanyFacts(&g, f.Span, nil, []string{f.Edit})
		m.checkLetterOwn(&g, f.Span, f.Edit, c, n)
		if f.Span == "letter.closing" {
			m.checkAfterFacts(&g, f.Span, f.Edit, m.slotFacts())
		} else {
			checkBeforeFacts(&g, []aheadPart{{f.Span, f.Edit, strings.HasPrefix(f.Span, "letter.bullet")}})
		}
	} else {
		checkClaims(&g, f.Span, f.Edit, c)
		checkNames(&g, f.Span, f.Edit, n)
	}
	checkShape(&g, f.Span, f.Edit)
	checkBold(&g, f.Span, f.Edit)
	if relocRE.MatchString(f.Edit) {
		g.fail("relocation-claim", "%s: the system, not an edit, states relocation", f.Span)
	}
	if authRE.MatchString(f.Edit) {
		g.fail("work-authorization-claim", "%s: an edit states the right to work, which only the user may state", f.Span)
	}
	return g
}

// revise makes the one revision the accepted factual findings ask for. The
// revised content passes the same grounding gates as a draft; if either gate
// refuses it, or the call fails, the draft stays exactly as it was.
func (j *job) revise(ctx context.Context, v *version, rr *reviewRun) error {
	rec := &rr.rec
	defer func() { rec.Deadline.AtRevisionEnd = int(j.remaining(ctx).Seconds()) }()
	if rec.Status != ContentReviewed {
		return nil
	}
	rev := &RevisionRecord{}
	rec.Revision = rev
	switch {
	case len(rr.accepted) == 0:
		rev.Reason = "no accepted factual finding asks for a change"
		return nil
	case j.remaining(ctx) < RevisionTimeout+j.reserve():
		rev.Reason = "the run deadline leaves no room for a revision"
		j.warn("the review revision was skipped: the run deadline leaves no room for it")
		return nil
	}
	rev.Attempted = true
	task := "resume-write"
	if v.resume == nil {
		task = "cover-letter"
	}
	var reply revisionReply
	err := j.callModel(ctx, task, revisionPrompt(v.m, v, rr.accepted), revisionSchema(v.resume != nil, v.letter != nil), &reply, RevisionTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		rev.Reason = fmt.Sprintf("the revision call failed (%s); the draft is unchanged", errKind(err))
		j.warn("the review revision failed (%s); the draft is unchanged", errKind(err))
		return nil
	}
	rec.Calls.Revision = j.lastCall
	var newResume *ResumeDoc
	var newLetter *LetterDoc
	resumeChanged, letterChanged := false, false
	if v.resume != nil && reply.Resume != nil {
		d := *v.resume
		d.Generation, d.ResumeDraft = GenModel, normalizeResume(*reply.Resume)
		g := checkResume(v.m, d)
		rev.ResumeGate = &g
		newResume = &d
		resumeChanged = spansView(draftSpans(&d, nil)) != spansView(draftSpans(v.resume, nil))
	}
	if v.letter != nil && reply.Letter != nil {
		d := *v.letter
		d.Generation, d.LetterDraft = GenModel, normalizeLetter(*reply.Letter)
		g := checkLetter(v.m, d)
		rev.LetterGate = &g
		newLetter = &d
		letterChanged = spansView(draftSpans(nil, &d)) != spansView(draftSpans(nil, v.letter))
	}
	switch {
	case (v.resume != nil && newResume == nil) || (v.letter != nil && newLetter == nil):
		rev.Reason = "the revision omitted a document; the draft is unchanged"
	case rev.ResumeGate != nil && !rev.ResumeGate.OK(), rev.LetterGate != nil && !rev.LetterGate.OK():
		rev.Reason = "the revised draft was rejected by the grounding gate (" + gateCodes(rev) + "); the draft is unchanged"
		if newResume != nil {
			j.keep("resume-revision-rejected.json", newResume)
		}
		if newLetter != nil {
			j.keep("cover-letter-revision-rejected.json", newLetter)
		}
	case !resumeChanged && !letterChanged:
		rev.Reason = "the revision changed nothing"
	default:
		if resumeChanged {
			v.resume, v.resumeGate = newResume, *rev.ResumeGate
		}
		if letterChanged {
			v.letter, v.letterGate = newLetter, *rev.LetterGate
		}
		j.gen = GenModel
		rev.Applied, rev.Reason = true, "the revised draft passed the grounding gate"
		rec.Status, rec.Revisions = ContentRevised, 1
		return nil
	}
	j.warn("the review revision was not applied: %s", rev.Reason)
	return nil
}

func gateCodes(r *RevisionRecord) string {
	var g Gate
	for _, x := range []*Gate{r.ResumeGate, r.LetterGate} {
		if x != nil {
			g.Errors = append(g.Errors, x.Errors...)
		}
	}
	return codes(g)
}

// Review reads the review record stored with a version.
func (s *Service) Review(ctx context.Context, d Document) (*ReviewSummary, error) {
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return nil, err
	}
	raw, err := readRegular(filepath.Join(dir, "document-review.json"), maxRecordBytes)
	if err != nil {
		return nil, nil
	}
	var rec ReviewRecord
	if json.Unmarshal(raw, &rec) != nil || rec.Schema != reviewRecordSchema {
		return nil, nil
	}
	return &ReviewSummary{Status: rec.Status, Reason: rec.Reason, Revisions: rec.Revisions, Factual: len(rec.Factual), Narrative: len(rec.Narrative), Rejected: len(rec.Rejected)}, nil
}

var (
	noteSentRE   = lazyregexp.New(`[.!?]+(?:\s+|$)`)
	reqIDRE      = lazyregexp.New(`\bR\d+\b`)
	gapWords     = stringSet("not", "no", "never", "none", "without", "missing", "lack", "lacks", "gap", "absent", "unaddressed", "uncovered", "neither", "nor", "avoid")
	suggestWords = stringSet("add", "mention", "say", "claim", "include", "cite", "write", "name", "list", "surface", "highlight", "emphasize", "describe", "feature", "insert", "put", "show", "stress")
)

// gapSentence may name the posting's terms: it reports a gap or cites a
// requirement ("R8 (Git) is not covered") and suggests nothing outside a
// negation in its clause ("do not claim it").
func gapSentence(s string) bool {
	ws := strings.Fields(strings.ToLower(s))
	neg := func(w string) bool {
		w = strings.Trim(w, ".,;:()\"'")
		return gapWords[w] || strings.HasSuffix(w, "n't")
	}
	gap := reqIDRE.MatchString(s)
	for i, w := range ws {
		gap = gap || neg(w)
		st := strings.TrimSuffix(strings.Trim(w, ".,;:()\"'"), "ing")
		if !suggestWords[st] && !suggestWords[st+"e"] {
			continue
		}
		j := i - 1
		for j > max(0, i-3) && !strings.ContainsAny(ws[j][len(ws[j])-1:], ",;:") && !neg(ws[j]) {
			j--
		}
		if j < 0 || !neg(ws[j]) {
			return false
		}
	}
	return gap
}
