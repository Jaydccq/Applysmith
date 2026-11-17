package answerbank

import (
	_ "embed"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// An entry's shape is what its answer can be: the page offers exactly that,
// and a hand edit or an Ask Agent save outside it is refused (CheckAnswers).
// What the matcher needs beyond the answer the user picks (the other wordings
// of a choice on forms) comes with the shape, so the user never writes it.

// The shapes.
const (
	ShapeYesNo    = "yes-no"
	ShapeChoice   = "choice"
	ShapeChoices  = "choices"
	ShapeDate     = "date"
	ShapeURL      = "url"
	ShapeNumber   = "number"
	ShapeEmail    = "email"
	ShapeText     = "text"
	ShapeLongText = "long-text"
	ShapeList     = "list"
	ShapeRange    = "range"
	ShapeEmployer = "employer"
	ShapeLanguage = "language"
	ShapeNone     = "none"
)

// Choice is one answer of a choice shape: the label the page shows and a
// written question gets, and the option labels it picks on a form, best first
// (the label first).
type Choice struct {
	Label   string
	Matches []string
}

// Shape is what an entry's answer can be. Choices are the answers of a choice,
// a choices (several) or a yes or no shape, and the levels of a language.
type Shape struct {
	Type    string
	Choices []Choice
}

//go:embed defaults/answer-choices.yml
var choicesYAML []byte

// declineWordings are the shipped decline answer's wordings, best first.
var declineWordings = []string{"Decline to self-identify", "Decline to answer", "I prefer not to answer", "I prefer not to say",
	"I do not wish to answer", "I do not wish to self-identify", "Prefer not to answer", "Prefer not to say"}

var yesNoChoices = []Choice{{Label: "Yes", Matches: []string{"Yes"}}, {Label: "No", Matches: []string{"No"}}}

type catalog struct {
	choices map[string][]Choice
	levels  []Choice
}

var loadCatalog = sync.OnceValue(func() catalog {
	var raw struct {
		Choices map[string][]string `yaml:"choices"`
		Levels  []string            `yaml:"levels"`
	}
	if err := yaml.Unmarshal(choicesYAML, &raw); err != nil {
		panic(fmt.Sprintf("answerbank: the shipped answer choices do not parse: %v", err))
	}
	c := catalog{choices: map[string][]Choice{}, levels: parseChoices(raw.Levels)}
	for id, list := range raw.Choices {
		c.choices[id] = parseChoices(list)
	}
	return c
})

func parseChoices(list []string) []Choice {
	out := make([]Choice, 0, len(list))
	for _, s := range list {
		if s == "decline" {
			out = append(out, Choice{Label: declineWordings[0], Matches: slices.Clone(declineWordings)})
			continue
		}
		parts := strings.Split(s, "|")
		out = append(out, Choice{Label: parts[0], Matches: parts})
	}
	return out
}

// ShapeOf is what e's answer can be: its listed choices, else the shape its
// first input type names, else a free answer.
func ShapeOf(e match.Entry) Shape {
	cat := loadCatalog()
	first := ""
	if len(e.InputTypes) > 0 {
		first = e.InputTypes[0]
	}
	yes := slices.Contains(e.InputTypes, match.InputYesNo)
	switch e.Kind {
	case match.KindFixed:
		switch {
		case first == match.InputYesNo:
			return Shape{Type: ShapeYesNo, Choices: yesNoChoices}
		case first == match.InputDate:
			return Shape{Type: ShapeDate}
		case first == match.InputURL:
			return Shape{Type: ShapeURL}
		case first == match.InputNumber:
			return Shape{Type: ShapeNumber}
		case strings.Contains(e.ID, "email"):
			return Shape{Type: ShapeEmail}
		case first == match.InputLongText:
			return Shape{Type: ShapeLongText}
		}
		return Shape{Type: ShapeText}
	case match.KindPickOption:
		if list, ok := cat.choices[e.ID]; ok {
			return Shape{Type: ShapeChoice, Choices: list}
		}
		if yes {
			return Shape{Type: ShapeChoice, Choices: yesNoChoices}
		}
		return Shape{Type: ShapeText}
	case match.KindMultiSelect:
		if list, ok := cat.choices[e.ID]; ok {
			return Shape{Type: ShapeChoices, Choices: list}
		}
		return Shape{Type: ShapeList}
	case match.KindCompensationRange:
		return Shape{Type: ShapeRange}
	case match.KindEmployerHistory:
		return Shape{Type: ShapeEmployer, Choices: yesNoChoices}
	case match.KindLanguageProficiency:
		return Shape{Type: ShapeLanguage, Choices: cat.levels}
	}
	return Shape{Type: ShapeNone}
}

// ChoiceFor is the choice of s an answer names: its label or one of its
// wordings, ignoring case.
func (s Shape) ChoiceFor(answer string) (Choice, bool) {
	answer = strings.TrimSpace(answer)
	for _, c := range s.Choices {
		if slices.ContainsFunc(c.Matches, func(m string) bool { return strings.EqualFold(m, answer) }) {
			return c, true
		}
	}
	return Choice{}, false
}

// Labels lists the choices' labels.
func (s Shape) Labels() []string {
	out := make([]string, len(s.Choices))
	for i, c := range s.Choices {
		out[i] = c.Label
	}
	return out
}

var (
	currencyRE = lazyregexp.New(`^[A-Z]{3}$`)
	emailRE    = lazyregexp.New(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// AnswerProblem says why e's answer does not fit its shape, empty when it
// does. A blank answer and the ask-me decision fit every shape.
func AnswerProblem(e match.Entry) string {
	if e.AsksMe() {
		return ""
	}
	s := ShapeOf(e)
	blank := func(v string) bool { return strings.TrimSpace(v) == "" }
	oneOf := func(v string) string {
		if blank(v) {
			return ""
		}
		if _, ok := s.ChoiceFor(v); ok {
			return ""
		}
		return "the answer must be one of: " + strings.Join(s.Labels(), ", ")
	}
	switch s.Type {
	case ShapeYesNo:
		return oneOf(e.Answer)
	case ShapeChoice:
		if len(e.Preferred) == 0 {
			return oneOf(e.FallbackText)
		}
		c, ok := s.ChoiceFor(e.Preferred[0])
		if !ok || !slices.Equal(e.Preferred, c.Matches) {
			return "the answer must be one of: " + strings.Join(s.Labels(), ", ")
		}
		return ""
	case ShapeChoices:
		for _, v := range e.Selected {
			if p := oneOf(v); p != "" {
				return p
			}
		}
	case ShapeDate:
		if _, err := time.Parse(time.DateOnly, strings.TrimSpace(e.Answer)); !blank(e.Answer) && err != nil {
			return "the answer must be a date (YYYY-MM-DD)"
		}
	case ShapeURL:
		u, err := url.Parse(strings.TrimSpace(e.Answer))
		if !blank(e.Answer) && (err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "") {
			return "the answer must be a link that starts with https://"
		}
	case ShapeNumber:
		if _, err := strconv.ParseFloat(strings.TrimSpace(e.Answer), 64); !blank(e.Answer) && err != nil {
			return "the answer must be a number"
		}
	case ShapeEmail:
		if !blank(e.Answer) && !emailRE.MatchString(strings.TrimSpace(e.Answer)) {
			return "the answer must be an e-mail address"
		}
	case ShapeRange:
		for _, v := range []string{e.Minimum, e.Maximum} {
			if _, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v), ",", ""), 64); !blank(v) && err != nil {
				return "the minimum and the maximum must be numbers"
			}
		}
		if !blank(e.Currency) && !currencyRE.MatchString(e.Currency) {
			return "the currency must be a three-letter code such as USD"
		}
	case ShapeEmployer:
		if p := oneOf(e.AnswerIfMatch); p != "" {
			return p
		}
		return oneOf(e.AnswerIfNoMatch)
	case ShapeLanguage:
		for _, v := range []string{e.Speaking, e.Listening, e.Reading, e.Writing} {
			if p := oneOf(v); p != "" {
				return p
			}
		}
	}
	return ""
}

// CheckAnswers refuses a change whose new or changed answers do not fit their
// shapes. Only the answers the change writes are read: an answer a form taught
// earlier (a kept edit, an approved suggestion) holds the form's own label and
// stays until it is changed.
func CheckAnswers(before, after match.Bank) error {
	var issues []Issue
	for _, e := range after.Entries {
		if old, ok := before.Find(e.ID); ok && sameBody(old.Body, e.Body) {
			continue
		}
		if p := AnswerProblem(e); p != "" {
			issues = append(issues, Issue{EntryID: e.ID, Path: "entries." + e.ID, Message: p})
		}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

// sameBody compares two answers; an absent list and an empty one are the
// same answer here, as a save may carry either.
func sameBody(a, b match.Body) bool {
	strs := func(x match.Body) []string {
		return []string{x.Answer, x.FallbackText, x.Minimum, x.Maximum, x.DefaultAnswer, x.Currency, x.Language, x.Speaking, x.Listening, x.Reading, x.Writing,
			x.AnswerIfMatch, x.AnswerIfNoMatch, strconv.FormatBool(x.FallbackAnyOption), strconv.FormatBool(x.AbstainsWhenAmbiguous())}
	}
	return slices.Equal(strs(a), strs(b)) && slices.Equal(a.Preferred, b.Preferred) && slices.Equal(a.Selected, b.Selected) && slices.Equal(a.ProficiencyOptions, b.ProficiencyOptions)
}
