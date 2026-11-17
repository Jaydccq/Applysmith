package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/agent/memory"
	"applysmith/internal/kb"
)

// StoryMiner is the story-mining service of the knowledge base.
type StoryMiner interface {
	SuggestQuadrantsIndexed(ctx context.Context) ([]kb.QuadrantSuggestion, error)
	ListMining(ctx context.Context) ([]kb.MiningSession, error)
	StartMining(ctx context.Context, in kb.MiningStart) (kb.MiningSession, error)
	GetMining(ctx context.Context, id string) (kb.MiningSession, error)
	MiningReply(ctx context.Context, id, text string, expected int) (kb.MiningSession, error)
	SaveMining(ctx context.Context, id string) (kb.Draft, error)
	AbandonMining(ctx context.Context, id string) (kb.MiningSession, error)
	FollowUps(ctx context.Context, draftID int64) ([]kb.FollowUp, error)
}

func storyMining(m StoryMiner) Tool {
	return Tool{Name: "story_mining", Label: "Mining a story", Class: ClassDraft, PII: PIILow, Timeout: draftTimeout,
		Description: "Guide the user through writing down one behavioral story, one question at a time (situation, task, tension, action, result). " +
			"Actions: suggest (which quadrant to mine next, from the coverage gaps), list (open sessions), start (a quadrant of project, team, customer or failure; optionally the user's pasted notes or a notes file in the workspace), " +
			"or start with a target instead: an application question that asks for a story (a security finding, work with an external customer, a partnership with business teams, a metric owned, a model built, or proud or hardest work); the session then asks that story's parts, takes \"skip\" for a part the user cannot answer, and asks once for a missing figure. " +
			"reply (the user's answer to the current question), save (turn a finished session into a story draft) and abandon. " +
			"A reply, and any notes, must be the user's own words copied from their message: never write, summarize or infer a story for them. " +
			"A saved draft is never published and its facts stay unconfirmed until the user confirms them; follow-up questions about it are answered through record_story_answer.",
		Schema: `{"type":"object","additionalProperties":false,"required":["action"],"properties":{
			"action":{"type":"string","enum":["suggest","list","start","reply","save","abandon"]},
			"quadrant":{"type":"string","enum":["project","team","customer","failure"]},
			"target":{"type":"string","maxLength":4000,"description":"The application question the story should answer, copied word for word."},
			"notes":{"type":"string","maxLength":8000,"description":"The user's pasted notes, copied from their message."},
			"notes_file":{"type":"string","maxLength":300,"description":"A path inside the workspace that the user named."},
			"session_id":{"type":"string","maxLength":80},
			"text":{"type":"string","maxLength":4000,"description":"The user's answer, copied from their message."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Action, Quadrant, Notes string
				Target                  string
				NotesFile               string `json:"notes_file"`
				SessionID               string `json:"session_id"`
				Text                    string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			res, err := runMining(ctx, m, c.Turn, miningArgs{a.Action, a.Quadrant, a.Target, a.Notes, a.NotesFile, a.SessionID, a.Text})
			if err != nil {
				return miningRefusal(err)
			}
			return res, nil
		}}
}

type miningArgs struct{ action, quadrant, target, notes, notesFile, sessionID, text string }

func runMining(ctx context.Context, m StoryMiner, t *Turn, a miningArgs) (ToolResult, error) {
	action, quadrant, notes, notesFile, sessionID, text := a.action, a.quadrant, a.notes, a.notesFile, a.sessionID, a.text
	readOnly := action == "suggest" || action == "list"
	if !readOnly && t != nil && (t.Channel == ChannelScheduled || t.Channel == ChannelDigest) {
		return ToolResult{Text: "Refused: a story is written down with the user present, so a scheduled turn can only suggest or list."}, nil
	}
	switch action {
	case "suggest":
		sug, err := m.SuggestQuadrantsIndexed(ctx)
		if err != nil {
			return ToolResult{}, err
		}
		var b strings.Builder
		b.WriteString("Quadrants to mine, most useful first:\n")
		for _, s := range sug {
			fmt.Fprintf(&b, "- %s (score %d): %s\n", s.Quadrant, s.Score, cutRunes(strings.Join(s.Reasons, "; "), 200))
		}
		return ToolResult{Text: b.String()}, nil
	case "list":
		list, err := m.ListMining(ctx)
		if err != nil {
			return ToolResult{}, err
		}
		var b strings.Builder
		for _, s := range list {
			if s.State == kb.MineCollecting || s.State == kb.MineReady {
				fmt.Fprintf(&b, "- %s\n", miningLine(s))
			}
		}
		if b.Len() == 0 {
			return ToolResult{Text: "No story-mining session is open."}, nil
		}
		return ToolResult{Text: "Open sessions:\n" + b.String()}, nil
	case "start":
		if quadrant == "" && a.target == "" {
			return ToolResult{Text: "Say which quadrant to mine (project, team, customer or failure), or give the application question as the target; suggest ranks the quadrants."}, nil
		}
		if memory.InjectionShaped(a.target) {
			return ToolResult{Text: "Refused: the target reads as an instruction to the assistant; give the application question itself."}, nil
		}
		in := kb.MiningStart{Quadrant: quadrant, Target: a.target}
		switch {
		case notes != "" && notesFile != "":
			return ToolResult{Text: "Give pasted notes or a notes file, not both."}, nil
		case notes != "":
			if !userSaid(t, notes) {
				return ToolResult{Text: "Refused: the notes must be copied from the user's own message."}, nil
			}
			in.Notes = notes
		case notesFile != "":
			if !userSaid(t, notesFile) {
				return ToolResult{Text: "Refused: the user must name the notes file in their message."}, nil
			}
			in.NotesFile = notesFile
		}
		s, err := m.StartMining(ctx, in)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Text: miningText(s)}, nil
	case "reply":
		if sessionID == "" || strings.TrimSpace(text) == "" {
			return ToolResult{Text: "A reply needs the session id and the user's answer."}, nil
		}
		if memory.InjectionShaped(text) {
			return ToolResult{Text: "Refused: the text reads as an instruction to the assistant, so nothing was recorded."}, nil
		}
		if !userSaid(t, text) {
			return ToolResult{Text: "Refused: a reply must be the user's own words copied from their message, not a paraphrase or a guess."}, nil
		}
		cur, err := m.GetMining(ctx, sessionID)
		if err != nil {
			return ToolResult{}, err
		}
		s, err := m.MiningReply(ctx, sessionID, text, len(cur.Log))
		if err != nil {
			return ToolResult{}, err
		}
		if s.Ignored {
			return ToolResult{Text: "That reply was not recorded: the session no longer takes replies. " + miningText(s)}, nil
		}
		return ToolResult{Text: miningText(s)}, nil
	case "save":
		d, err := m.SaveMining(ctx, sessionID)
		if err != nil {
			return ToolResult{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Saved story draft %d (revision %d). It is not published and its facts are unconfirmed.\n", d.ID, d.Revision)
		if fu, err := m.FollowUps(ctx, d.ID); err == nil {
			for _, q := range fu {
				fmt.Fprintf(&b, "- follow-up %s (%s): %s\n", q.ID, q.Dimension, q.Question)
			}
		}
		return ToolResult{Text: b.String(), NextAction: "Ask the user the follow-up questions; record each answer with record_story_answer."}, nil
	case "abandon":
		s, err := m.AbandonMining(ctx, sessionID)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Text: fmt.Sprintf("Session %s is abandoned.", s.ID)}, nil
	}
	return ToolResult{Text: "Unknown action."}, nil
}

func miningLine(s kb.MiningSession) string {
	out := fmt.Sprintf("%s (%s, %s)", s.ID, s.Quadrant, s.State)
	if s.Next != nil {
		out += ", next: " + s.Next.Field
	}
	return out
}

func miningText(s kb.MiningSession) string {
	switch s.State {
	case kb.MineCollecting:
		if s.Target != "" {
			return fmt.Sprintf("Session %s collects a %s story for the question %q. Ask the user, and wait for their answer (they may say skip): %s", s.ID, s.Essay, cutRunes(s.Target, 300), s.Next.Text)
		}
		return fmt.Sprintf("Session %s (%s) is collecting. Ask the user, and wait for their answer: %s", s.ID, s.Quadrant, s.Next.Text)
	case kb.MineReady:
		return fmt.Sprintf("Session %s (%s) has every field. Call story_mining with action save to create the draft.", s.ID, s.Quadrant)
	}
	return miningLine(s)
}

// userSaid reports whether the text is part of the user's message of the
// turn, ignoring case and spacing, so the user's own words are the only ones a
// session records.
func userSaid(t *Turn, text string) bool {
	if t == nil {
		return false
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	want := norm(text)
	return want != "" && strings.Contains(norm(t.UserMessage), want)
}

func miningRefusal(err error) (ToolResult, error) {
	var secret *kb.SecretError
	switch {
	case errors.As(err, &secret):
		return ToolResult{Text: "Refused: the text looks like it holds a secret, so nothing was recorded."}, nil
	case errors.Is(err, kb.ErrNotFound):
		return ToolResult{Text: "There is no such session; use list to see the open ones."}, nil
	case errors.Is(err, kb.ErrInvalid), errors.Is(err, kb.ErrConflict), errors.Is(err, kb.ErrUnavailable):
		return ToolResult{Text: "Not done: " + strings.TrimPrefix(err.Error(), "kb: ") + "."}, nil
	}
	return ToolResult{}, err
}
