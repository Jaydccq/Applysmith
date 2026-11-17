// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"applysmith/internal/llm"
)

// NewFake is the fake provider the product installs, so the "no key yet" setup
// choice can evaluate a posting and report it as simulated.
func NewFake() *llm.Fake {
	f := llm.NewFake()
	f.SetGenerator(FakeGenerator)
	return f
}

// FakeGenerator is the evaluation fixture generator of the fake provider. For
// a given prompt it returns the same schema-valid reply every time; the hit
// levels, legitimacy and prose derive from a hash of the posting text, so
// different postings give different but stable results. Install it with
// llm.Fake.SetGenerator.
func FakeGenerator(task, prompt string) (string, bool) {
	kind, _, _ := strings.Cut(prompt, "\n")
	posting := between(prompt, "<job_description>", "</job_description>")
	sum := sha256.Sum256([]byte(posting))
	var out any
	switch {
	case task == "quick-screen" && kind == "Task: quick-screen":
		out = fakeScreen(sum)
	case task == "deep-eval" && kind == "Task: deep-eval":
		out = fakeDeep(sum, posting)
	case task == "deep-eval" && kind == "Task: deep-answers":
		out = fakeAnswers(sum)
	case task == "jd-decode" && kind == "Task: jd-decode":
		out = fakeDecode(sum, posting)
	case task == "jd-extract" && kind == "Task: jd-extract":
		// The fixture states what a labeled line of the text says, and nothing
		// it was not given.
		out = map[string]any{"title": field(posting, "Title"), "company": field(posting, "Company"), "location": field(posting, "Location")}
	default:
		return "", false
	}
	b, _ := json.Marshal(out)
	return string(b), true
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, close)
	return body
}

func field(posting, name string) string {
	for _, l := range strings.Split(posting, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), name+": "); ok {
			return v
		}
	}
	return ""
}

func fakeScreen(sum [32]byte) map[string]any {
	score := 2.5 + float64(sum[0]%26)/10
	decision := DecisionAdvance
	if score < SkipBelow {
		decision = DecisionDrop
	}
	return map[string]any{
		"score": score, "decision": decision, "legitimacy": "credible", "blockers": []string{},
		"reasons": []string{"derived from the fixture hash"}, "concerns": []string{}, "summary": "Fixture screening summary.",
	}
}

func fakeItems(sum [32]byte, offset int, prefix string, n int, gate bool) []map[string]any {
	items := make([]map[string]any, n)
	for i := range items {
		hit := hitLevelsFixture[int(sum[(offset+i)%32])%3]
		evidence := ""
		if hit > 0 {
			evidence = "CV: fixture evidence for " + prefix
		}
		it := map[string]any{"id": fmt.Sprintf("%s%d", prefix, i+1), "label": fmt.Sprintf("%s requirement %d", prefix, i+1),
			"source": "posting", "hit_level": hit, "evidence": evidence}
		if gate {
			it["gate_class"] = GateNone
		}
		items[i] = it
	}
	return items
}

var hitLevelsFixture = [3]float64{0, 0.5, 1}

// fakeAlignment is the four alignment dimensions with hash-derived hits.
func fakeAlignment(sum [32]byte) []map[string]any {
	items := fakeItems(sum, 8, "align", len(AlignmentIDs), false)
	for i, it := range items {
		it["id"] = AlignmentIDs[i]
	}
	return items
}

// fakeDeep names the posting as the prompt states it: the title and company
// the request or the extraction gave, else a labeled line of the text. It
// invents none, so a posting that names no employer is filed as unknown and
// two different postings never share a made-up name.
func fakeDeep(sum [32]byte, posting string) map[string]any {
	return map[string]any{
		"posting": map[string]any{
			"title": field(posting, "Title"), "company": field(posting, "Company"), "location": field(posting, "Location"), "work_model": "unknown",
			"compensation":    map[string]any{"text": field(posting, "Compensation"), "minimum": nil, "maximum": nil, "currency": "", "period": ""},
			"employment_type": "full-time", "company_size": "unknown", "role_family": "software engineering",
		},
		"must_have":         fakeItems(sum, 0, "must", 4, true),
		"nice_to_have":      fakeItems(sum, 4, "nice", 2, false),
		"hidden_signals":    fakeItems(sum, 6, "hidden", 2, false),
		"profile_alignment": fakeAlignment(sum),
		"red_flags":         []map[string]any{{"id": "flag1", "label": "fixture flag", "level": hitLevelsFixture[int(sum[12])%3], "evidence": "fixture"}},
		"legitimacy":        map[string]any{"level": []string{"credible", "unclear"}[sum[13]%2], "signals": []string{"fixture signal"}},
		"sections": map[string]any{
			"snapshot":         fmt.Sprintf("Fixture snapshot %x.", sum[:4]),
			"level_trajectory": "Fixture level and trajectory.", "pay_notes": "Fixture pay notes.",
			"tailoring_plan": []string{"Fixture tailoring plan."}, "interview_prep": []string{"Fixture interview preparation."},
			"keywords": []string{"fixture", "go", "fixture"},
		},
	}
}

func fakeAnswers(sum [32]byte) map[string]any {
	return map[string]any{"answers": []map[string]any{
		{"question": "Why this company?", "answer": fmt.Sprintf("Fixture answer %x.", sum[:3]), "status": "answered"},
		{"question": "Why this role?", "answer": "", "status": "unanswered"},
	}}
}

// fakeDecode quotes the first sentence of the posting's first line of eight
// or more words for every source, so the quote check passes.
func fakeDecode(sum [32]byte, posting string) map[string]any {
	quote := ""
	for _, l := range strings.Split(posting, "\n") {
		l = strings.TrimSpace(l)
		if len(strings.Fields(l)) < 8 {
			continue
		}
		quote, _, _ = strings.Cut(l, ".")
		break
	}
	sourced := func(items []map[string]any) []map[string]any {
		for _, it := range items {
			it["source"] = quote
		}
		return items
	}
	return map[string]any{
		"posting":        map[string]any{"title": field(posting, "Title"), "company": field(posting, "Company")},
		"needs":          []map[string]any{{"id": "need1", "need": "Fixture need.", "source": quote}},
		"must_have":      sourced(fakeItems(sum, 0, "must", 3, true)),
		"nice_to_have":   sourced(fakeItems(sum, 3, "nice", 2, false)),
		"hidden_signals": sourced(fakeItems(sum, 5, "hidden", 2, false)),
		"level":          map[string]any{"seniority": "mid", "team_stage": "growing", "ownership": "fixture ownership", "source": quote},
		"wording":        []map[string]any{{"phrase": "fixture phrase", "reading": "fixture reading"}},
		"red_flags":      []map[string]any{{"id": "flag1", "label": "fixture flag", "level": hitLevelsFixture[int(sum[12])%3], "evidence": "fixture"}},
	}
}
