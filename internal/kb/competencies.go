package kb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"applysmith/internal/lazyregexp"
)

// CompetenciesFile is the user's editable competency set, below knowledge/.
const CompetenciesFile = "competencies.yml"

// Story source types and dimensions.
var (
	SourceTypes = []string{"project", "work", "team-conflict", "customer", "leadership", "failure-learning"}
	Dimensions  = []string{"metric", "risk", "conflict", "customer-impact", "tradeoff", "learning"}
)

// Competency is one principle a story can evidence.
type Competency struct {
	ID                 string   `yaml:"id" json:"id"`
	Name               string   `yaml:"name" json:"name"`
	SatisfiedBy        []string `yaml:"satisfied_by" json:"satisfiedBy"`
	RequiredDimensions []string `yaml:"required_dimensions" json:"requiredDimensions"`
	QuestionPatterns   []string `yaml:"question_patterns" json:"questionPatterns"`
}

// DefaultCompetencies is the neutral set of twelve principles.
func DefaultCompetencies() []Competency {
	c := func(id, name string, by, dims, patterns []string) Competency {
		return Competency{ID: id, Name: name, SatisfiedBy: by, RequiredDimensions: dims, QuestionPatterns: patterns}
	}
	l := func(s ...string) []string { return s }
	return []Competency{
		c("ownership", "Ownership", l("project", "work", "leadership", "failure-learning"), l("metric"),
			l("tell me about a time you took ownership", "describe a time you went beyond your role")),
		c("customer-focus", "Customer focus", l("customer", "work", "project"), l("customer-impact"),
			l("tell me about a time you put the customer first", "describe how you used customer feedback")),
		c("technical-depth", "Technical depth", l("project", "work"), l("tradeoff"),
			l("tell me about the most technically complex project", "describe a design decision you made")),
		c("delivery", "Delivery", l("project", "work", "customer"), l("metric"),
			l("tell me about a time you delivered under pressure", "describe a project you shipped end to end")),
		c("collaboration", "Collaboration", l("team-conflict", "leadership", "work"), l("conflict"),
			l("tell me about a time you worked across teams", "describe how you handle working with a difficult colleague")),
		c("disagreement", "Disagreement", l("team-conflict", "leadership"), l("conflict", "tradeoff"),
			l("tell me about a time you disagreed with a teammate", "describe a time you pushed back on a decision")),
		c("learning-from-failure", "Learning from failure", l("failure-learning", "project"), l("learning", "risk"),
			l("tell me about a time you failed", "describe a mistake and what you learned")),
		c("mentoring", "Mentoring", l("leadership", "team-conflict"), l("learning"),
			l("tell me about a time you mentored someone", "describe how you helped a colleague grow")),
		c("ambiguity", "Handling ambiguity", l("project", "work"), l("risk", "tradeoff"),
			l("tell me about a time with unclear requirements", "describe how you made progress with incomplete information")),
		c("prioritization", "Prioritization", l("project", "work"), l("tradeoff"),
			l("tell me about a time you had to prioritize", "describe how you decide what to work on first")),
		c("communication", "Communication", l("leadership", "team-conflict", "customer"), l("tradeoff"),
			l("tell me about a time you explained something complex", "describe how you handled a hard conversation")),
		c("quality", "Quality", l("project", "work", "failure-learning"), l("risk", "metric"),
			l("tell me about a time you raised the quality bar", "describe how you prevented a defect from reaching users")),
	}
}

var competencyIDRE = lazyregexp.New(`^[a-z0-9][a-z0-9-]*$`)

// Competencies returns the user's set from knowledge/competencies.yml, or the
// defaults when the file does not exist. A file that cannot be used is an
// error, never a silent fall back to the defaults.
func (s *Service) Competencies() ([]Competency, error) {
	data, err := readRegular(filepath.Join(s.knowledgeRoot(), CompetenciesFile))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultCompetencies(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return parseCompetencies(data)
}

func parseCompetencies(data []byte) ([]Competency, error) {
	var doc struct {
		Competencies []Competency `yaml:"competencies"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, CompetenciesFile, err)
	}
	if len(doc.Competencies) == 0 {
		return nil, fmt.Errorf("%w: %s lists no competencies", ErrInvalid, CompetenciesFile)
	}
	seen := map[string]bool{}
	for _, c := range doc.Competencies {
		switch {
		case !competencyIDRE.MatchString(c.ID):
			return nil, fmt.Errorf("%w: competency id %q must be lowercase letters, digits and hyphens", ErrInvalid, c.ID)
		case seen[c.ID]:
			return nil, fmt.Errorf("%w: competency %q is listed twice", ErrInvalid, c.ID)
		case strings.TrimSpace(c.Name) == "":
			return nil, fmt.Errorf("%w: competency %q has no name", ErrInvalid, c.ID)
		case len(c.SatisfiedBy) == 0:
			return nil, fmt.Errorf("%w: competency %q needs satisfied_by", ErrInvalid, c.ID)
		case len(c.QuestionPatterns) < 2 || len(c.QuestionPatterns) > 4:
			return nil, fmt.Errorf("%w: competency %q needs two to four question_patterns", ErrInvalid, c.ID)
		}
		seen[c.ID] = true
		for _, t := range c.SatisfiedBy {
			if !contains(SourceTypes, t) {
				return nil, fmt.Errorf("%w: competency %q names unknown source type %q", ErrInvalid, c.ID, t)
			}
		}
		for _, d := range c.RequiredDimensions {
			if !contains(Dimensions, d) {
				return nil, fmt.Errorf("%w: competency %q names unknown dimension %q", ErrInvalid, c.ID, d)
			}
		}
	}
	return doc.Competencies, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
