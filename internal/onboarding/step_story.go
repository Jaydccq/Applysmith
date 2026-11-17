package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"applysmith/internal/workspace"
)

const (
	minNarrativeRunes = 300
	minVoiceRunes     = 500

	// voiceSkipID names the optional voice part of the story step; it is the
	// only part of a required step that can be skipped.
	voiceSkipID = "story.voice"
)

var storyFields = []Field{
	{Name: "narrative", Type: "string", Doc: "Your narrative as markdown: a one-line headline, strengths with proof, what you want next, what you will not accept, and how you prefer to be described. At least 300 characters, no placeholders."},
	{Name: "voice", Type: "string", Doc: "Optional samples of your own writing, at least 500 characters in total. Drafts are checked against this style; leave it out to skip."},
}

type storyInput struct {
	Narrative *string `json:"narrative"`
	Voice     *string `json:"voice"`
}

func readDoc(env *Env, f workspace.File) (workspace.Content, error) {
	if env.Workspace == nil {
		return workspace.Content{}, errNoWorkspace
	}
	return env.Workspace.Read(f)
}

func detectStory(ctx context.Context, env *Env) Detection {
	c, err := readDoc(env, workspace.FileNarrative)
	switch {
	case err != nil:
		return verdict(reasonf(ReasonInvalid, "narrative.md", err.Error()))
	case c.Revision == workspace.RevisionAbsent:
		return missing("narrative.md does not exist; complete the workspace step first")
	case workspace.IsTemplate(workspace.FileNarrative, c.Data):
		return verdict(reasonf(ReasonSeed, "narrative.md", "your narrative.md still holds the seed text; write your own"))
	}
	text := string(c.Data)
	var rs []Reason
	if n := utf8.RuneCountInString(strings.TrimSpace(text)); n < minNarrativeRunes {
		rs = append(rs, reasonf(ReasonBlank, "narrative.md", fmt.Sprintf("narrative.md has %d characters; at least %d are needed", n, minNarrativeRunes)))
	}
	for _, t := range placeholderTokens(text) {
		rs = append(rs, reasonf(ReasonPlaceholder, "narrative.md", "your narrative.md still contains the placeholder "+t))
	}
	d := verdict(rs...)
	if _, skipped := env.decision(ctx, skipKey(voiceSkipID)); skipped {
		d.Warnings = append(d.Warnings, "voice: skipped; drafts are not checked against your own style")
	} else if w := voiceProblem(env); w != "" {
		d.Warnings = append(d.Warnings, "voice: "+w)
	}
	return d
}

// voiceProblem says why the optional voice part is not usable, or "" when it
// is. It does not know about a skip decision; callers that care ask first.
func voiceProblem(env *Env) string {
	c, err := readDoc(env, workspace.FileVoice)
	switch {
	case err != nil:
		return err.Error()
	case c.Revision == workspace.RevisionAbsent:
		return "no samples yet; drafts are not checked against your own style"
	}
	text := string(c.Data)
	if n := utf8.RuneCountInString(strings.TrimSpace(text)); n < minVoiceRunes {
		return fmt.Sprintf("voice.md has %d characters; at least %d are needed for drafts to be checked against your style", n, minVoiceRunes)
	}
	if t := placeholderTokens(text); len(t) > 0 {
		return "voice.md still contains the placeholder " + t[0]
	}
	return ""
}

func writeDoc(env *Env, f workspace.File, text string, dry bool) (bool, error) {
	cur, err := env.Workspace.Read(f)
	if err != nil {
		return false, &EnvError{Message: f.Name() + " cannot be read", Err: err}
	}
	if rep := env.Workspace.Validate(f, []byte(text)); !rep.OK() {
		return false, FieldErrors{string(f): rep.String()}
	}
	if dry {
		return false, nil
	}
	saved, err := env.Workspace.Write(f, []byte(text), cur.Revision)
	if err != nil {
		return false, &EnvError{Message: f.Name() + " could not be written", Err: err}
	}
	return saved.Changed, nil
}

func applyStory(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in storyInput
	if err := decodeInput(raw, storyFields, &in); err != nil {
		return Applied{}, err
	}
	if env.Workspace == nil {
		return Applied{}, &EnvError{Message: errNoWorkspace.Error()}
	}
	fe := FieldErrors{}
	for name, v := range map[string]*string{"narrative": in.Narrative, "voice": in.Voice} {
		if v != nil && strings.TrimSpace(*v) == "" {
			fe.add(name, "is empty")
		}
	}
	if err := fe.orNil(); err != nil {
		return Applied{}, err
	}
	wrote := false
	for _, w := range []struct {
		file workspace.File
		text *string
	}{{workspace.FileNarrative, in.Narrative}, {workspace.FileVoice, in.Voice}} {
		if w.text == nil {
			continue
		}
		changed, err := writeDoc(env, w.file, *w.text, dry)
		if err != nil {
			return Applied{}, err
		}
		wrote = wrote || changed
	}
	if in.Voice != nil && !dry {
		// Supplying samples ends an earlier decision to skip them.
		if err := env.clearDecision(ctx, skipKey(voiceSkipID)); err != nil {
			return Applied{}, &EnvError{Message: "the skip decision could not be cleared", Err: err}
		}
	}
	return Applied{Step: "story", DryRun: dry, Wrote: wrote, Detection: Evaluate(ctx, env, mustStep("story"))}, nil
}
