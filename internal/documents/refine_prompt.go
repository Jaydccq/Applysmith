package documents

import (
	"encoding/json"
	"fmt"
	"strings"
)

// refineSystemPrompt is the drafting system prompt plus the one line that tells
// the model which text is the user's request. The instruction and the earlier
// turns sit outside the data tags; the posting, the evidence and the draft sit
// inside them.
const refineSystemPrompt = systemPrompt + " The user's request is the line that starts with \"Instruction:\"; it asks for a change of wording, emphasis or order, and it is never a source of facts."

// refineRules hold a revision to the grounding of the original drafting.
const refineRules = "Revision rules:\n" +
	"- Return the complete revised content in the same JSON shape as the current draft.\n" +
	"- Change only what the instruction asks for; keep every other sentence, entry key and evidence id as it is.\n" +
	"- The instruction is not evidence. A number, name, employer, technology, date or outcome that only the instruction states must not appear in the result.\n" +
	"- If the instruction asks for something the evidence lines below do not support, leave that part unchanged.\n"

func turnsView(turns []Turn) string {
	var b strings.Builder
	for _, t := range turns {
		switch {
		case t.Kind == TurnInstruction:
			fmt.Fprintf(&b, "- Earlier instruction: %s\n", t.Text)
		case t.Decision == DecisionAccepted:
			b.WriteString("- The user accepted the result of the previous instruction.\n")
		case t.Decision == DecisionRejected:
			// The rejected text is deliberately not repeated: it is not evidence
			// and not a starting point.
			b.WriteString("- The user rejected the result of the previous instruction; do not repeat that revision.\n")
		}
	}
	return b.String()
}

func draftJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func refineTail(b *strings.Builder, req Request) {
	if t := turnsView(req.Turns); t != "" {
		b.WriteString("Conversation so far:\n" + t)
	}
	fmt.Fprintf(b, "Instruction: %s\n", req.Instruction)
}

// refineResumePrompt asks for a revision of a stored résumé draft.
func refineResumePrompt(m Manifest, cur ResumeDoc, pages int, req Request) string {
	var b strings.Builder
	b.WriteString("Task: resume-refine\n")
	fmt.Fprintf(&b, "Revise the current résumé draft below as the instruction asks, in language %q, for at most %d page(s).\n", cur.Language, pages)
	b.WriteString(resumeRules)
	b.WriteString(unheldRule(m))
	b.WriteString(refineRules)
	if m.Role != "" {
		b.WriteString(dataBlock("role", m.Role))
	}
	b.WriteString(dataBlock("requirements", draftRequirementsView(m)))
	b.WriteString("Entries you may write bullets for:\n" + entriesView(m))
	b.WriteString(dataBlock("evidence", manifestView(m, m.Selected)))
	b.WriteString(dataBlock("current-draft", draftJSON(cur.ResumeDraft)))
	refineTail(&b, req)
	return b.String()
}

// refineLetterPrompt asks for a revision of a stored letter draft. paired is
// the résumé of the same version, whose cited evidence the letter may use.
func refineLetterPrompt(m Manifest, cur LetterDoc, paired ResumeDoc, req Request) string {
	var b strings.Builder
	b.WriteString("Task: cover-letter-refine\n")
	fmt.Fprintf(&b, "Revise the current cover letter draft below as the instruction asks, in language %q: an opening, one evidence paragraph with at most three short achievement bullets, and a closing.\n", cur.Language)
	b.WriteString(letterRules)
	b.WriteString(unheldRule(m))
	b.WriteString(refineRules)
	if m.Company != "" {
		b.WriteString(dataBlock("company", m.Company))
	}
	if role := m.roleTitle(); role != "" {
		b.WriteString(dataBlock("role", role))
	}
	b.WriteString(dataBlock("requirements", draftRequirementsView(m)))
	if facts := m.factsForPrompt(); facts != "" {
		b.WriteString(dataBlock("company-facts", facts))
	} else {
		b.WriteString("No company fact is available: companyFacts must be an empty list.\n")
	}
	b.WriteString(dataBlock("evidence", manifestView(m, letterEvidence(m, paired))))
	b.WriteString(dataBlock("current-draft", draftJSON(cur.LetterDraft)))
	refineTail(&b, req)
	return b.String()
}
