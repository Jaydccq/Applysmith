package kb

import (
	"context"
	"slices"
	"strings"
)

// StoryMatch is a published story that evidences a piece of text.
type StoryMatch struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
}

// MatchStories ranks the published stories against text with the same lexical
// retrieval as Search, best first, at most k. A story matches when it clears
// the shared-term floor or one of its triggers fires. Unlike Search it records
// no knowledge gap: callers such as interview prep ask about many requirements
// and an unmatched one is their finding, not a missing answer.
func (s *Service) MatchStories(ctx context.Context, text string, k int) ([]StoryMatch, error) {
	if k <= 0 || len(Tokens(text)) == 0 {
		return []StoryMatch{}, nil
	}
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	hits := s.lexical(Query{Query: text, Kinds: []string{CatBehavioral}}, docs, func(*indexedDoc) bool { return false })
	out := []StoryMatch{}
	for _, h := range hits {
		if !isStory(h.doc) || !(h.clearing || slices.Contains(h.reasons, "trigger")) {
			continue
		}
		out = append(out, StoryMatch{ID: h.doc.ID, Title: h.doc.Title, Score: h.score})
		if len(out) == k {
			break
		}
	}
	return out, nil
}

// StoryText is the readable text of one published story.
type StoryText struct {
	ID    string
	Title string
	Text  string
}

// StoryTexts returns every published story with its section text, in id order.
func (s *Service) StoryTexts(ctx context.Context) ([]StoryText, error) {
	stories, err := s.publishedStories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StoryText, 0, len(stories))
	for _, sn := range stories {
		var b strings.Builder
		for _, sec := range sn.Sections {
			b.WriteString(sec.Text)
			b.WriteString("\n")
		}
		out = append(out, StoryText{ID: sn.ID, Title: sn.Title, Text: strings.TrimSpace(b.String())})
	}
	return out, nil
}
