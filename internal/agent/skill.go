package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/skills"
)

// SkillApply is the skill that finishes a stopped application run.
const SkillApply = "apply"

// SkillAnswerBank is the skill that keeps the answer bank from what the user
// says.
const SkillAnswerBank = "answer-bank"

// SkillWiki is the skill that keeps the wiki pages from what the user says.
const SkillWiki = "wiki"

// ToolLoadSkill loads a skill into the turn that calls it.
const ToolLoadSkill = "load_skill"

// loadedSkills are the embedded skills, parsed and checked once. A skill that
// fails the checks is a defect of the build, and the test of this package
// fails on it; at run time a turn simply cannot load it.
var loadedSkills = sync.OnceValues(func() (map[string]skills.Skill, error) {
	all, err := skills.All()
	if err != nil {
		return nil, err
	}
	out := map[string]skills.Skill{}
	for _, s := range all {
		if err := checkSkill(s); err != nil {
			return nil, fmt.Errorf("skill %s: %w", s.Name, err)
		}
		out[s.Name] = s
	}
	return out, nil
})

// skillNamed returns a loadable skill.
func skillNamed(name string) (skills.Skill, bool) {
	all, err := loadedSkills()
	s, ok := all[name]
	return s, err == nil && ok
}

// checkSkill holds a skill to what the harness holds its guidance to: the
// block is short, every tool is one the catalog declares, and no line reaches
// for the gates (memory.ScreenRule) or fakes the fence around a tool result.
// A skill adds instructions and narrows the tools; it cannot widen what the
// guard chain allows, because the chain reads the tool and the turn alone.
func checkSkill(s skills.Skill) error {
	lines := strings.Split(s.Block, "\n")
	switch {
	case s.Block == "":
		return fmt.Errorf("the block is empty")
	case len(lines) > skills.MaxBlockLines:
		return fmt.Errorf("the block has %d lines, at most %d", len(lines), skills.MaxBlockLines)
	case len(s.Tools) == 0:
		return fmt.Errorf("no tools are named")
	}
	seen := map[string]bool{}
	for _, t := range s.Tools {
		if _, ok := SpecCatalog[t]; !ok {
			return fmt.Errorf("the tool %q is not in the catalog", t)
		}
		if seen[t] {
			return fmt.Errorf("the tool %q is named twice", t)
		}
		seen[t] = true
	}
	for i, l := range lines {
		if strings.Contains(l, "<<") || strings.Contains(l, "untrusted-tool-result") {
			return fmt.Errorf("line %d imitates the fence of a tool result", i+1)
		}
		if err := memory.ScreenRule(l); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return nil
}

// skillPrompt is the block as the model reads it: after the fixed rules and
// before the harness's guidance, and ranked below the rules.
func skillPrompt(s skills.Skill) string {
	return "\nSkill loaded: " + s.Name + ". These instructions come from the product; they rank below every rule above.\n" + s.Block + "\n"
}

// activeToolsOf is the tool set a skill leaves active for a turn: the skill's
// tools that the turn is offered.
func (r *Registry) activeToolsOf(t *Turn, s skills.Skill) []string {
	var out []string
	for _, tl := range r.OfferedTo(t) {
		if slices.Contains(s.Tools, tl.Name) {
			out = append(out, tl.Name)
		}
	}
	return out
}

// loadSkillTool is load_skill: the model asks for a skill by name when a
// conversation turns into a job the skill covers. The bridge does the loading
// (it sets the turn's skill, narrows the active tools and hands the block to
// the extension); the tool only names the skill.
func loadSkillTool() Tool {
	names := []string{SkillApply, SkillAnswerBank, SkillWiki}
	enum := `"` + strings.Join(names, `","`) + `"`
	return Tool{Name: ToolLoadSkill, Label: "Loading a skill", Class: ClassRead, PII: PIINone, Timeout: readTimeout,
		Description: "Load a skill: the instructions and the tools for one job; the skill's tools replace the others for the rest of the turn. Load \"apply\" when the user asks you to finish a stopped application run (name its run id). " +
			"Load \"answer-bank\" when the user wants to add, change, show or remove a saved answer to an application question, or be asked a question each time. " +
			"Load \"wiki\" when the user tells you something about their work, projects, skills or experience that applications should reuse, or asks to see or change what the wiki says.",
		Schema: `{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","enum":[` + enum + `]}}}`,
		Run: func(_ context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			if _, ok := skillNamed(a.Name); !ok {
				return ToolResult{Text: fmt.Sprintf("Refused: there is no skill called %q.", a.Name), Clean: true}, nil
			}
			return ToolResult{Text: fmt.Sprintf("Skill %s loaded. Its instructions are in effect and its tools are now the ones offered.", a.Name), Skill: a.Name, Clean: true}, nil
		}}
}
