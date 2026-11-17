package match

import (
	"errors"
	"fmt"
)

// Request bounds. A page can render 268-option degree lists and a 240-entry
// country list, so the option bound is far above a typical form.
const (
	MaxQuestions      = 300
	MaxOptionsPerItem = 500
)

// ControlKind is the kind of control the engine found.
type ControlKind string

const (
	KindSelect        ControlKind = "select"
	KindRadioGroup    ControlKind = "radio-group"
	KindCheckbox      ControlKind = "checkbox"
	KindText          ControlKind = "text"
	KindCustomListbox ControlKind = "custom-listbox"
)

// Valid reports whether k is one of the five control kinds.
func (k ControlKind) Valid() bool {
	switch k {
	case KindSelect, KindRadioGroup, KindCheckbox, KindText, KindCustomListbox:
		return true
	}
	return false
}

// InputType is the shape of the value a control takes.
type InputType string

// Valid reports whether t is one of the thirteen input types (InputTypes, in
// vocab.go, is the closed set).
func (t InputType) Valid() bool { return ValidInputType(string(t)) }

// Option is one rendered choice of a control.
type Option struct {
	Value string
	Label string
}

// Question is one control of the page.
type Question struct {
	Selector    string
	Text        string
	SectionPath []string
	Kind        ControlKind
	InputType   InputType // empty: inferred from the sanitized text and options
	Options     []Option
	Required    bool
	MaxLength   int
	// Description is the text under the question that Text does not hold
	// (Oklo: what the options A, B and C stand for).
	Description string
	// Hidden is set by the engine for a control it found hidden or off-screen.
	Hidden bool
}

// Posting is the optional posting context of a request.
type Posting struct {
	Company     string
	Title       string
	Description string
	Location    string
}

// Date is a calendar date in the applicant's time zone.
type Date struct {
	Year, Month, Day int
}

// Request is one match call.
type Request struct {
	Host      string
	Posting   Posting
	Questions []Question
	// CoverLetterAttached and ResumeAttached are observations of this run:
	// nil means not observed.
	CoverLetterAttached *bool
	ResumeAttached      *bool
	// Today is the injected calendar date; the package never reads a clock.
	Today Date
	// Facts are the profile values the profile-grounded resolvers read.
	Facts Facts
	// AutoAcceptLegalDeclarations is the user's runtime setting: a required
	// privacy or terms acknowledgement, truth certification, at-will
	// acknowledgement or e-signature consent is ticked only when it is true. An
	// absent setting reads as false.
	AutoAcceptLegalDeclarations bool

	// matcher is the running matcher, set only while the post-passes run.
	matcher *Matcher
	// bud is the request's budget, set by MatchContext.
	bud *budget
	// labels holds the option profiles every control of the request shares, by
	// label and question text: a page repeats one long option list on several
	// controls. Set by MatchContext.
	labels map[labelKey]*labelProfile
	// ownFacts are the facts as the own-experience search reads them, built
	// by the first question that searches (Step.ownMentions).
	ownFacts []ownSource
	// postingLoc is postingLocation of the posting, read by the first guard
	// that needs it.
	postingLoc *string
}

// postingLocation is the posting's location, worked out once per request.
func (r *Request) postingLocation() string {
	if r.postingLoc == nil {
		l := postingLocation(r.Posting)
		r.postingLoc = &l
	}
	return *r.postingLoc
}

// Rematch matches one question on its own with the entry set and the stages of
// the running match, for a post-pass that rewrites a question. The post-passes
// do not run again. It reports an error outside a post-pass.
func (r Request) Rematch(q Question) (Result, error) {
	if r.matcher == nil {
		return Result{}, errors.New("answerbank: Rematch is available only inside a post-pass")
	}
	m := r.matcher
	r.Questions, r.matcher = []Question{q}, nil
	resp, _, err := m.matchQuestions(r)
	if err != nil {
		return Result{}, err
	}
	return resp.Results[0], nil
}

// Entries are the bank entries of the request's host, host overrides applied and
// archived entries removed, in bank order; available only inside a post-pass.
// They are the matcher's cached host entries: callers must not modify them.
func (r Request) Entries() []Entry {
	if r.matcher == nil {
		return nil
	}
	b := r.matcher.baseFor(r).entries
	n := len(b) - len(runContextEntries(r))
	return b[:n:n]
}

// ErrValidation marks a request that is rejected before any question is
// processed.
var ErrValidation = errors.New("validation_failed")

// Validate enforces the request bounds. A request beyond them is rejected whole.
func (r Request) Validate() error {
	if len(r.Questions) > MaxQuestions {
		return fmt.Errorf("%w: %d questions, at most %d", ErrValidation, len(r.Questions), MaxQuestions)
	}
	for i, q := range r.Questions {
		if len(q.Options) > MaxOptionsPerItem {
			return fmt.Errorf("%w: question %d has %d options, at most %d", ErrValidation, i, len(q.Options), MaxOptionsPerItem)
		}
		if q.Selector == "" {
			return fmt.Errorf("%w: question %d has no selector", ErrValidation, i)
		}
		if !q.Kind.Valid() {
			return fmt.Errorf("%w: question %d has control kind %q", ErrValidation, i, q.Kind)
		}
		if q.InputType != "" && !q.InputType.Valid() {
			return fmt.Errorf("%w: question %d has input type %q", ErrValidation, i, q.InputType)
		}
		if q.MaxLength < 0 {
			return fmt.Errorf("%w: question %d has a negative maxLength", ErrValidation, i)
		}
	}
	return nil
}

// IsRequired applies the required definition every rule uses: the flag is true
// and the label does not itself say it is optional.
func (q Question) IsRequired() bool {
	if !q.Required {
		return false
	}
	return !labelSaysOptional(q.Text)
}
