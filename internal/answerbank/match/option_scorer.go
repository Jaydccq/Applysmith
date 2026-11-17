package match

import "context"

// budget ends a request whose context is done. The matcher never reads a clock:
// it asks the context, and only every checkEvery calls, so the check costs
// nothing next to the scoring it guards. A nil budget never ends.
type budget struct {
	ctx     context.Context
	calls   int
	tripped bool
}

const checkEvery = 32

func newBudget(ctx context.Context) *budget {
	if ctx == nil || ctx.Done() == nil {
		return nil
	}
	return &budget{ctx: ctx}
}

// spent counts one unit of scoring work and reports whether the request is over.
func (b *budget) spent() bool {
	if b == nil {
		return false
	}
	if !b.tripped {
		b.calls++
		if b.calls%checkEvery == 0 && b.ctx.Err() != nil {
			b.tripped = true
		}
	}
	return b.tripped
}

// over reports whether the request is over, checking the context at once.
func (b *budget) over() bool {
	if b == nil {
		return false
	}
	if !b.tripped && b.ctx.Err() != nil {
		b.tripped = true
	}
	return b.tripped
}

// optionScorer scores the options of one control against answers. The profile
// of a label, the profile of an answer, the self-identification role of a label
// and every score are computed once per control, however many entries, search
// values and resolvers ask, so the cost of binding grows with options plus
// distinct answers instead of their product. Entries are keyed by label text,
// so a resolver that narrows the option list shares the same cache.
type optionScorer struct {
	text    string
	bud     *budget
	labels  map[string]*labelProfile
	answers map[string]*answerProfile
	scores  map[scoreKey]optScore
	roles   map[string]selfIDRole
	plains  map[string]string
	// shared is the request's profile cache across controls, or nil.
	shared map[labelKey]*labelProfile
}

type labelKey struct {
	label, question string
}

type scoreKey struct {
	answer, label string
}

// newOptionScorer returns the scorer of a control with this question text. The
// text is part of the cache because an option is read in the scope of its
// question.
func newOptionScorer(questionText string, bud *budget) *optionScorer {
	return &optionScorer{text: questionText, bud: bud, labels: map[string]*labelProfile{},
		answers: map[string]*answerProfile{}, scores: map[scoreKey]optScore{}, roles: map[string]selfIDRole{}, plains: map[string]string{}}
}

// label is the shared profile of an option. Callers must not modify it.
func (sc *optionScorer) label(o Option) *labelProfile {
	l, ok := sc.labels[o.Label]
	if ok {
		return l
	}
	if sc.shared == nil {
		l = newLabelProfile(o, sc.text)
	} else {
		// A profile holds the label and the question text only, so controls
		// that give the label other values share it.
		k := labelKey{o.Label, sc.text}
		if l, ok = sc.shared[k]; !ok {
			l = newLabelProfile(o, sc.text)
			sc.shared[k] = l
		}
	}
	sc.labels[o.Label] = l
	return l
}

// answer is the shared profile of an answer. Callers must not modify it.
func (sc *optionScorer) answer(text string) *answerProfile {
	a, ok := sc.answers[text]
	if !ok {
		a = newAnswerProfile(text)
		sc.answers[text] = a
	}
	return a
}

// score is the option against the answer. Once the budget is spent it scores
// zero, and the request's result for the control is replaced by the abstain.
func (sc *optionScorer) score(o Option, answer string) optScore {
	k := scoreKey{answer, o.Label}
	if s, ok := sc.scores[k]; ok {
		return s
	}
	if sc.bud.spent() {
		return optScore{0, "request budget spent"}
	}
	s := scoreProfiles(sc.label(o), sc.answer(answer))
	sc.scores[k] = s
	return s
}

// role is the self-identification role of a label; a nil scorer reads it fresh.
func (sc *optionScorer) role(label string) selfIDRole {
	if sc == nil {
		return selfIDRoleOf(label)
	}
	r, ok := sc.roles[label]
	if !ok {
		r = selfIDRoleOf(label)
		sc.roles[label] = r
	}
	return r
}

// plain is the normalized label, without the contraction expansion of its
// profile; a nil scorer normalizes fresh.
func (sc *optionScorer) plain(label string) string {
	if sc == nil {
		return normalizeLabel(label)
	}
	n, ok := sc.plains[label]
	if !ok {
		n = normalizeLabel(label)
		sc.plains[label] = n
	}
	return n
}
