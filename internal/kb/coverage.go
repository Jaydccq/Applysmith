package kb

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Grades of a coverage cell.
const (
	GradeGreen  = "green"
	GradeYellow = "yellow"
	GradeRed    = "red"
)

// PrincipleGrade is one cell of the principle grid.
type PrincipleGrade struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Grade string `json:"grade"`
	// Stories are the ids of the stories that count toward the principle.
	Stories []string `json:"stories"`
	// Missing names the required dimensions no counting story carries (yellow).
	Missing []string `json:"missing,omitempty"`
	// Suggest lists the source types that could evidence the principle (red).
	Suggest []string `json:"suggest,omitempty"`
}

// ShapeGrade is one cell of the story-shape grid.
type ShapeGrade struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Need    int      `json:"need"`
	Have    int      `json:"have"`
	Grade   string   `json:"grade"`
	Stories []string `json:"stories"`
}

// Coverage is both grids.
type Coverage struct {
	Principles []PrincipleGrade `json:"principles"`
	Shapes     []ShapeGrade     `json:"shapes"`
}

type shapeDef struct {
	id, name string
	need     int
	match    func(sn *Snippet, text string) bool
}

var (
	proudWords    = lazyregexp.New(`\b(proud|shipped|launched|delivered|built|designed)\b`)
	deadlineWords = lazyregexp.New(`\b(deadline|tight|under pressure|crunch|short notice|overnight|launch date|cutover)\b`)
	multiWords    = lazyregexp.New(`\b(multitask\w*|competing priorities|several (?:projects|priorities)|juggl\w*|in parallel|at the same time|many projects)\b`)
	mistakeWords  = lazyregexp.New(`\b(mistake|failed|failure|got wrong|my error|my fault|outage i)\b`)
)

var shapeDefs = []shapeDef{
	{"proud-project", "Proud project", 2, func(sn *Snippet, text string) bool {
		return sn.StorySourceType == "project" || (contains(sn.Dimensions, "metric") && proudWords.MatchString(text))
	}},
	{"conflict", "Conflict", 1, func(sn *Snippet, text string) bool {
		return sn.StorySourceType == "team-conflict" || contains(sn.Dimensions, "conflict")
	}},
	{"mistake", "Mistake", 1, func(sn *Snippet, text string) bool {
		return sn.StorySourceType == "failure-learning" || (contains(sn.Dimensions, "learning") && mistakeWords.MatchString(text))
	}},
	{"tight-deadline", "Tight deadline", 1, func(sn *Snippet, text string) bool { return deadlineWords.MatchString(text) }},
	{"multitasking", "Multitasking", 1, func(sn *Snippet, text string) bool {
		return multiWords.MatchString(text) || contains(sn.Principles, "prioritization")
	}},
}

func storyText(sn *Snippet) string {
	var b strings.Builder
	for _, sec := range sn.Sections {
		b.WriteString(sec.Text)
		b.WriteString("\n")
	}
	return strings.ToLower(b.String())
}

// publishedStories loads the stories of the bank: behavioral snippets. It
// reconciles the index with the files first.
func (s *Service) publishedStories(ctx context.Context) ([]*Snippet, error) {
	_, docs, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return storiesOf(docs), nil
}

// indexedStories loads the stories from the index as the last Sync left it,
// without reconciling and so without writing.
func (s *Service) indexedStories(ctx context.Context) ([]*Snippet, error) {
	docs, err := s.loadDocs(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return storiesOf(docs), nil
}

func storiesOf(docs []*indexedDoc) []*Snippet {
	var out []*Snippet
	for _, d := range docs {
		if isStory(d) {
			sn := d.Snippet
			out = append(out, &sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CoverageIndexed grades every principle and story shape against the
// published stories, over the index as the last Sync left it. A story counts
// toward a principle only if it lists the principle and its source type is one
// that can honestly evidence it. It never writes, so a read route can serve
// it; a file changed since the last Sync shows up after the next one.
func (s *Service) CoverageIndexed(ctx context.Context) (Coverage, error) {
	comps, err := s.Competencies()
	if err != nil {
		return Coverage{}, err
	}
	stories, err := s.indexedStories(ctx)
	if err != nil {
		return Coverage{}, err
	}
	return gradeCoverage(comps, stories), nil
}

func gradeCoverage(comps []Competency, stories []*Snippet) Coverage {
	cov := Coverage{Principles: []PrincipleGrade{}, Shapes: []ShapeGrade{}}
	for _, c := range comps {
		g := PrincipleGrade{ID: c.ID, Name: c.Name, Stories: []string{}}
		var counting []*Snippet
		for _, st := range stories {
			if contains(st.Principles, c.ID) && contains(c.SatisfiedBy, st.StorySourceType) {
				counting = append(counting, st)
				g.Stories = append(g.Stories, st.ID)
			}
		}
		switch {
		case len(counting) == 0:
			g.Grade, g.Suggest = GradeRed, append([]string{}, c.SatisfiedBy...)
		default:
			best, bestMissing := -1, []string(nil)
			for _, st := range counting {
				missing := missingDims(c.RequiredDimensions, st.Dimensions)
				if best == -1 || len(missing) < best {
					best, bestMissing = len(missing), missing
				}
			}
			if best == 0 {
				g.Grade = GradeGreen
			} else {
				g.Grade, g.Missing = GradeYellow, bestMissing
			}
		}
		cov.Principles = append(cov.Principles, g)
	}
	for _, def := range shapeDefs {
		g := ShapeGrade{ID: def.id, Name: def.name, Need: def.need, Stories: []string{}}
		for _, st := range stories {
			if def.match(st, storyText(st)) {
				g.Have++
				g.Stories = append(g.Stories, st.ID)
			}
		}
		g.Grade = GradeRed
		if g.Have >= g.Need {
			g.Grade = GradeGreen
		}
		cov.Shapes = append(cov.Shapes, g)
	}
	return cov
}

func missingDims(required, have []string) []string {
	var out []string
	for _, d := range required {
		if !contains(have, d) {
			out = append(out, d)
		}
	}
	return out
}

// StoryContext is what a new draft should know before it is written: the
// events already banked and the cells that still need a story.
type StoryContext struct {
	Banked []BankedEvent `json:"banked"`
	Gaps   StoryGaps     `json:"gaps"`
}

// BankedEvent is a published story reduced to its event fingerprint.
type BankedEvent struct {
	ID           string   `json:"id"`
	Title        string   `json:"title,omitempty"`
	MetricTokens []string `json:"metricTokens"`
	Principles   []string `json:"principles"`
	SourceType   string   `json:"sourceType"`
}

// StoryGaps lists the yellow and red cells.
type StoryGaps struct {
	Principles []PrincipleGrade `json:"principles"`
	Shapes     []ShapeGrade     `json:"shapes"`
}

// StoryContextIndexed returns the banked events and the current coverage gaps,
// read from the index as the last Sync left it, without writing.
func (s *Service) StoryContextIndexed(ctx context.Context) (StoryContext, error) {
	comps, err := s.Competencies()
	if err != nil {
		return StoryContext{}, err
	}
	stories, err := s.indexedStories(ctx)
	if err != nil {
		return StoryContext{}, err
	}
	out := StoryContext{Banked: []BankedEvent{}, Gaps: StoryGaps{Principles: []PrincipleGrade{}, Shapes: []ShapeGrade{}}}
	for _, st := range stories {
		fp := fingerprintOf(st.Facts())
		tokens := make([]string, 0, len(fp.metrics))
		for m := range fp.metrics {
			tokens = append(tokens, m)
		}
		sort.Strings(tokens)
		out.Banked = append(out.Banked, BankedEvent{ID: st.ID, Title: st.Title, MetricTokens: tokens, Principles: st.Principles, SourceType: st.StorySourceType})
	}
	cov := gradeCoverage(comps, stories)
	for _, p := range cov.Principles {
		if p.Grade != GradeGreen {
			out.Gaps.Principles = append(out.Gaps.Principles, p)
		}
	}
	for _, sh := range cov.Shapes {
		if sh.Grade != GradeGreen {
			out.Gaps.Shapes = append(out.Gaps.Shapes, sh)
		}
	}
	return out, nil
}
