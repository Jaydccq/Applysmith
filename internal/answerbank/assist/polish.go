package assist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
)

const (
	dashDirective    = "Rewrite the SAME answer with the same content and length, but with zero em dashes and zero en dashes. Output only the final answer."
	strictDirective  = "STRICTER GROUNDING: the previous attempt named things the facts do not state: %s. Use only names and numbers that appear in APPLICANT FACTS, SNIPPETS or COMPANY CONTEXT, and output only the final prose."
	countDirective   = "COUNT: the question asks for %d items and the previous attempt delivered fewer. Rewrite it with exactly %d distinct items, each in its own short paragraph, from the facts only."
	companyDirective = "COMPANY FACT: the previous attempt cites nothing concrete about the company. Rewrite it so it references at least one concrete fact from COMPANY CONTEXT, in the applicant's own words."
	voiceDirective   = "VOICE: the previous attempt only repeats a third-party statistic about the employer. Rewrite it in the first person about what the company builds and why it interests the applicant; cite no ratings, awards or satisfaction figures."
)

// composeCall makes the first call under the request budget and runs the answer
// through every gate. Only a failure of that call is an error; every later
// failure leaves the earlier text or abstains.
func (r *run) composeCall(ctx context.Context, p *composePlan) (composeOut, error) {
	timeout := composeTimeout(p.in.Prep, p.in.Question)
	draft, err := r.call(ctx, callSpec{task: TaskNarrative, system: p.system, user: p.user,
		counter: &r.req.compositions, limit: MaxCompositions, timeout: timeout})
	if err != nil {
		return composeOut{}, err
	}
	return r.polish(ctx, p, draft, timeout), nil
}

// retry makes one corrective call on the same prompt. It reports false when the
// budget is gone or the call failed, so the caller keeps the earlier text.
func (r *run) retry(ctx context.Context, p *composePlan, prev, directive string, timeout time.Duration) (string, bool) {
	user := p.user + "\n=== PREVIOUS ATTEMPT (data) ===\n<<<\n" + delimited(prev) + "\n>>>\n=== DIRECTIVE ===\n" + directive + "\n"
	out, err := r.call(ctx, callSpec{task: TaskNarrative, system: p.system, user: user, timeout: timeout})
	if err != nil {
		return "", false
	}
	return mechanicalClean(stripScaffolding(out)), true
}

// safeRetry is the predicate every optional retry must pass: non-empty,
// grounded (dangerous mode excepted), not a refusal and no ungrounded
// immigration claim. Otherwise the earlier text ships, so a retry never
// regresses an invariant the first draft held.
func (p *composePlan) safeRetry(text string) bool {
	if strings.TrimSpace(text) == "" || isModelRefusal(text) || ungroundedImmigrationClaim(text, p.in.Src.WorkAuthStatus) || p.holdsPlanted(text) {
		return false
	}
	return p.in.Mode == ModeDangerous || len(blocking(p.in.Mode, p.grounding.unsupportedSpans(text))) == 0
}

// holdsPlanted reports a retry that holds a planted token, which the first
// draft was checked for.
func (p *composePlan) holdsPlanted(text string) bool {
	_, bad := containsPlanted(text, p.cinfo.Planted)
	return bad
}

func spanList(spans []span) string {
	var parts []string
	for _, s := range spans {
		parts = append(parts, fmt.Sprintf("%q", s.Text))
	}
	return strings.Join(parts, ", ")
}

// polish runs the gates in their fixed order.
func (r *run) polish(ctx context.Context, p *composePlan, draft string, timeout time.Duration) composeOut {
	in, g, mode := p.in, p.grounding, p.in.Mode
	text := stripScaffolding(draft)
	if isModelRefusal(text) {
		return abstained(match.ReasonModelAbstain, "the model declined to answer")
	}
	if tok, bad := containsPlanted(text, p.cinfo.Planted); bad {
		return abstained(match.ReasonUnsupported, fmt.Sprintf("the answer holds %q, which a sentence of the posting or the question tried to plant", tok))
	}
	if hasDash(text) {
		if rt, ok := r.retry(ctx, p, text, dashDirective, timeout); ok && rt != "" && !hasDash(rt) && !isModelRefusal(rt) && !p.holdsPlanted(rt) {
			text = rt
		}
	}
	text = mechanicalClean(text)
	if text == "" {
		return abstained(match.ReasonModelAbstain, "the answer is empty")
	}

	block := blocking(mode, g.unsupportedSpans(text))
	if len(block) > 0 {
		// A stricter retry that declines (NO_ANSWER) or holds a planted token
		// leaves the first draft to the salvage below, which keeps it only
		// without the sentences that hold the unsupported spans and only when
		// the rest is supported.
		if rt, ok := r.retry(ctx, p, text, fmt.Sprintf(strictDirective, spanList(block)), timeout); ok && rt != "" && !isModelRefusal(rt) && !p.holdsPlanted(rt) {
			text = rt
			block = blocking(mode, g.unsupportedSpans(text))
		}
	}
	if ungroundedImmigrationClaim(text, in.Src.WorkAuthStatus) {
		return abstained(match.ReasonUnsupported, "the answer asserts a work-authorization position the recorded status does not state")
	}
	if len(block) > 0 {
		kept, ok := salvage(g, mode, text, block)
		if !ok {
			return abstained(match.ReasonUnsupported, "unsupported claims: "+spanList(block))
		}
		text = kept
	}
	if mode == ModeDangerous {
		var ok bool
		if text, ok = claimClassGate(g, text); !ok {
			return abstained(match.ReasonUnsupported, "an unsupported name is paired with a duration")
		}
	}

	if n, ok := askedItemCount(in.Question); ok && deliveredItems(text) < n {
		if rt, ok := r.retry(ctx, p, text, fmt.Sprintf(countDirective, n, n), timeout); ok && p.safeRetry(rt) {
			text = rt
		}
	}
	useCompany := p.companyQ && !p.cinfo.empty()
	if useCompany && !usesAnyFact(text, p.cinfo.Facts) {
		if rt, ok := r.retry(ctx, p, text, companyDirective, timeout); ok && p.safeRetry(rt) {
			text = rt
		}
	}
	if useCompany && isThirdPartyEmployerStatistic(text) {
		rt, ok := r.retry(ctx, p, text, voiceDirective, timeout)
		if !ok || !p.safeRetry(rt) || isThirdPartyEmployerStatistic(rt) {
			return abstained(match.ReasonUnsupported, "the answer only repeats a third-party statistic about the employer")
		}
		text = rt
	}

	text = strings.TrimSpace(stripUnaskedWorkAuth(text))
	if text == "" {
		return abstained(match.ReasonModelAbstain, "nothing is left of the answer")
	}
	if looksLikeProseAsk(in.Question) && (isNarrativeShaped(in.Question) || proseVerbRE.MatchString(in.Question)) && wordCount(text) < 6 {
		return abstained(match.ReasonUnsupported, "a stub is not an answer to a request for a description")
	}
	return composeOut{Answer: text, Cited: p.citedKeys(text)}
}

// claimClassGate (dangerous mode) removes the sentences that pair an
// unsupported name with a duration, a falsifiable hands-on claim; the rest ships
// only with at least 25 words.
func claimClassGate(g *grounding, text string) (string, bool) {
	var drop []span
	for _, s := range g.unsupportedSpans(text) {
		if s.Kind == spanName {
			drop = append(drop, s)
		}
	}
	var kept []string
	changed := false
	for _, sent := range sentences(text) {
		bad := false
		if durationRE.MatchString(sent) {
			low := strings.ToLower(sent)
			for _, s := range drop {
				if wordBoundaryContains(low, strings.ToLower(s.Text)) {
					bad = true
					break
				}
			}
		}
		if bad {
			changed = true
			continue
		}
		kept = append(kept, sent)
	}
	if !changed {
		return text, true
	}
	out := joinSentences(kept)
	return out, wordCount(out) >= minSalvage
}

// citedKeys are the references of the facts the answer used.
func (p *composePlan) citedKeys(answer string) []string {
	var keys []string
	at := tokens(answer)
	for _, group := range [][]Fact{p.snippets, p.profile} {
		for _, f := range group {
			if matchCount(tokens(f.Text), at) >= 2 || (len(group) == 1 && p.snippetOnly(f)) {
				keys = append(keys, f.Ref)
			}
		}
	}
	seen := map[string]bool{}
	for _, l := range p.evidence {
		if !seen[l.Ref] && matchCount(tokens(l.Text), at) >= 3 {
			seen[l.Ref] = true
			keys = append(keys, l.Ref)
		}
	}
	if !p.cinfo.empty() && matchCount(tokens(p.cinfo.Text), at) >= 3 {
		keys = append(keys, "company-brief")
	}
	return keys
}

func (p *composePlan) snippetOnly(f Fact) bool { return f.Kind == FactSnippet }
