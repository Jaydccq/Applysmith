package learn

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
)

// Teach, Forget and Unforget are the door the Ask Agent edits the bank
// through: the user says a question and the answer in their own words, and
// the service decides everything the matcher depends on (which entry, its id,
// kind and category, the phrasings, a site's own answer). A change is routed
// through the matcher before it is written, and a change after which the
// matcher would not answer the question with the user's answer writes nothing.

// Reasons of a refused teach or forget.
const (
	ReasonIncomplete  = "incomplete"
	ReasonOtherEntry  = "other-entry"
	ReasonFromProfile = "from-profile"
	ReasonNotAnOption = "not-an-option"
	ReasonNotTaught   = "not-taught"
	ReasonShipped     = "shipped-default"
	ReasonUnknown     = "unknown-entry"
	ReasonNothingToDo = "nothing-to-do"
)

// What a teach or forget did.
const (
	ActionAdded       = "added"
	ActionUpdated     = "updated"
	ActionSiteAnswer  = "site-answer"
	ActionUnchanged   = "unchanged"
	ActionDeleted     = "deleted"
	ActionAskMe       = "ask-me"
	ActionSiteRemoved = "site-answer-removed"
	ActionRestored    = "restored"
)

// restoreWindow is how long a deleted entry can be put back.
const restoreWindow = time.Hour

// TeachRequest is the user's answer to one application question.
type TeachRequest struct {
	// Question is the question as a form asks it.
	Question string
	// Answer is the user's answer: the exact label of one option when the
	// form offers options.
	Answer  string
	Options []string
	// Host, when set, keeps the answer to that site; elsewhere the bank
	// answers as before.
	Host string
	// EntryID names the entry the user means; empty lets the matcher decide.
	EntryID string
	// DryRun decides as for a write and writes nothing, so a refusal is known
	// before the user is asked to approve the change.
	DryRun bool
}

// TeachResult says what the bank now holds.
type TeachResult struct {
	EntryID string
	// Question is the entry's name in the list.
	Question string
	Action   string
	// Before is the answer this one replaced, empty for a new entry.
	Before string
	// Phrasing reports the question added as a new wording of the entry.
	Phrasing bool
}

// Teach writes the user's answer where the matcher will find it: into the
// entry the question reaches (filling a blank, replacing an answer or the
// ask-me decision), into a site answer of that entry when a host is given,
// or into a new entry of the user's own. A question the profile or a setup
// step answers is refused, since the answer is changed there.
func (s *Service) Teach(ctx context.Context, req TeachRequest) (TeachResult, error) {
	question := cleanText(req.Question, maxQuestionRunes)
	answer := strings.TrimSpace(req.Answer)
	if question == "" || answer == "" {
		return TeachResult{}, refuse(ReasonIncomplete, "a saved answer needs the question and the user's answer")
	}
	if match.IsAskMe(answer) {
		return TeachResult{}, refuse(ReasonIncomplete, "to be asked each time, forget the answer with ask me instead")
	}
	host := ""
	if strings.TrimSpace(req.Host) != "" {
		if host = HostOf(req.Host); host == "" {
			return TeachResult{}, refuse(ReasonHostRequired, "%q is not a site", req.Host)
		}
	}
	q, options := captured(question, req.Options)
	if len(options) > 0 {
		at := slices.IndexFunc(options, func(o string) bool { return strings.EqualFold(o, answer) })
		if at < 0 {
			return TeachResult{}, refuse(ReasonNotAnOption, "the answer must be one of the options: %s", strings.Join(options, " | "))
		}
		answer = options[at]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped, err := s.bank.Bank()
	if err != nil {
		return TeachResult{}, err
	}
	loaded, err := s.bank.File()
	if err != nil {
		return TeachResult{}, err
	}
	facts := s.profileFacts()
	// The user names no form: the answer is for a question asked in any
	// shape, so a question that offers no options is also asked as a choice
	// that offers exactly the answer (an entry may take choices alone).
	shapesFor := func(answer string) []match.Question {
		if len(options) > 0 {
			return []match.Question{q}
		}
		c := q
		c.Kind, c.Options = match.KindSelect, []match.Option{{Value: answer, Label: answer}}
		return []match.Question{q, c}
	}
	r := s.routeBank(stamped.Bank, facts, host, match.Posting{}, q)
	if shapes := shapesFor(answer); r.EntryID == "" {
		r = s.routeBank(stamped.Bank, facts, host, match.Posting{}, shapes[len(shapes)-1])
	}
	if r.ByParent {
		r = Routed{}
	}
	target := strings.TrimSpace(req.EntryID)
	switch {
	case target == "":
		target = r.EntryID
	case r.Fills() && r.EntryID != target:
		return TeachResult{}, refuse(ReasonOtherEntry, "the bank answers this question from entry %q, not %q", r.EntryID, target)
	}
	var change func(b *match.Bank) (TeachResult, error)
	if target == "" {
		id, err := s.mintEntryID(question)
		if err != nil {
			return TeachResult{}, err
		}
		change = s.addOwn(id, question, answer, options, host)
	} else {
		e, ok := stamped.Bank.Find(target)
		switch {
		case !ok || e.Archived():
			return TeachResult{}, refuse(ReasonUnknown, "there is no entry %q", target)
		case e.DerivedFrom != "":
			return TeachResult{}, refuse(ReasonFromProfile, "entry %q follows the %s setup step; the user changes the answer there", target, e.DerivedFrom)
		case e.StampedBody || slices.Contains(match.DerivedIDs, target) || target == match.IDCurrentCompany ||
			r.Fills() && r.EntryID == target && r.Source != match.SourceEntry:
			return TeachResult{}, refuse(ReasonFromProfile, "entry %q answers from the profile; the user changes the answer there", target)
		}
		// The entry's shape says what its answer can be: an answer said
		// without a form names one of its choices, which brings the wordings
		// the matcher looks for on forms.
		var matches []string
		if shape := answerbank.ShapeOf(e); len(shape.Choices) > 0 && e.Kind != match.KindLanguageProficiency {
			c, ok := shape.ChoiceFor(answer)
			switch {
			case ok:
				answer, matches = c.Label, c.Matches
			case len(options) == 0:
				return TeachResult{}, refuse(ReasonNotAnOption, "the answer must be one of: %s", strings.Join(shape.Labels(), ", "))
			}
		}
		change = s.answerInto(target, question, answer, matches, host, r.EntryID != target)
	}
	// The change is tried on the bank the matcher reads before the file gets it.
	check := stamped.Bank.Clone()
	res, err := change(&check)
	if err != nil {
		return TeachResult{}, err
	}
	// An answer said without a form fits the entry's shape (a form's own
	// option label is the form's to decide).
	if len(options) == 0 {
		var invalid *answerbank.ValidationError
		if err := answerbank.CheckAnswers(stamped.Bank, check); errors.As(err, &invalid) {
			return TeachResult{}, refuse(ReasonNotAnOption, "%s", invalid.Issues[0].Message)
		} else if err != nil {
			return TeachResult{}, err
		}
	}
	var after Routed
	taught := slices.ContainsFunc(shapesFor(answer), func(q match.Question) bool {
		after = s.routeBank(check, facts, host, match.Posting{}, q)
		return after.Fills() && after.EntryID == res.EntryID && strings.EqualFold(strings.TrimSpace(after.Answer), answer)
	})
	if !taught {
		after = s.routeBank(check, facts, host, match.Posting{}, q)
		return TeachResult{}, refuse(ReasonNotTaught, "nothing was saved: with it the bank would still %s", describeRoute(after))
	}
	if res.Action == ActionUnchanged || req.DryRun {
		return res, nil
	}
	file := loaded.Bank.Clone()
	if _, err := change(&file); err != nil {
		return TeachResult{}, err
	}
	if _, err := s.bank.SaveChange(loaded.Bank, file, loaded.Revision); err != nil {
		return TeachResult{}, err
	}
	return res, nil
}

// addOwn adds the user's own entry: everywhere, or kept to host.
func (s *Service) addOwn(id, question, answer string, options []string, host string) func(*match.Bank) (TeachResult, error) {
	return func(b *match.Bank) (TeachResult, error) {
		e := match.Entry{ID: id, Description: clampRunes(question, 200), Kind: match.KindFixed, Answer: answer}
		e.Examples = []string{question}
		e.State, e.CreatedBy, e.CreatedAt = match.StateActive, match.CreatedByUser, s.now().UTC().Format(time.RFC3339)
		if len(options) > 0 {
			e.Kind, e.Answer, e.Preferred, e.FallbackText = match.KindPickOption, "", []string{answer}, answer
		}
		if host != "" {
			e.CapturedScope = []match.ScopeItem{{Host: host, Question: question, Options: options}}
		}
		b.Entries = append(b.Entries, e)
		return TeachResult{EntryID: id, Question: e.Description, Action: ActionAdded}, nil
	}
}

// answerInto writes answer into the entry id, or into its answer on host
// when host is set and the entry is not kept to that host already. phrase
// adds the question as a wording of the entry, for a question that does not
// reach it yet.
func (s *Service) answerInto(id, question, answer string, matches []string, host string, phrase bool) func(*match.Bank) (TeachResult, error) {
	return func(b *match.Bank) (TeachResult, error) {
		at := slices.IndexFunc(b.Entries, func(e match.Entry) bool { return e.ID == id })
		if at < 0 {
			return TeachResult{}, refuse(ReasonUnknown, "there is no entry %q", id)
		}
		e := &b.Entries[at]
		fields, ok := answerFields[e.Kind]
		if !ok {
			return TeachResult{}, refuse(ReasonTakesNoAnswer, "entry %q is a %s entry; the user changes its answer on the Answer Bank page", id, e.Kind)
		}
		res := TeachResult{EntryID: id, Question: cmpOr(e.Description, id), Action: ActionUpdated}
		if phrase {
			if err := addPhrasing(*b, e, question); err != nil {
				return TeachResult{}, err
			}
			res.Phrasing = true
		}
		if host != "" && !scopedTo(*e, host) {
			res.Action = ActionSiteAnswer
			if b.Overrides == nil {
				b.Overrides = map[string][]match.Override{}
			}
			list := b.Overrides[host]
			o := match.Override{ID: id, Fields: fields}
			setAnswer(&o.Body, e.Kind, answer, matches)
			if i := slices.IndexFunc(list, func(o match.Override) bool { return o.ID == id }); i >= 0 {
				if merged, err := list[i].Apply(*e); err == nil {
					res.Before = AnswerText(merged)
				}
				list[i] = o
			} else {
				res.Before = AnswerText(*e)
				list = append(list, o)
			}
			b.Overrides[host] = list
		} else {
			res.Before = AnswerText(*e)
			setAnswer(&e.Body, e.Kind, answer, matches)
			e.Placeholder, e.NeedsReview, e.StampedBody = false, false, false
			e.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		}
		if res.Before == answer && !res.Phrasing {
			res.Action = ActionUnchanged
		}
		return res, nil
	}
}

// answerFields are the fields a taught answer states, by the kinds it can
// answer. A compensation range takes the answer it gives a written question;
// its minimum and maximum stay as they are.
var answerFields = map[match.Kind][]string{
	match.KindFixed:             {"answer"},
	match.KindPickOption:        {"preferred", "fallback_text"},
	match.KindCompensationRange: {"default_answer"},
}

// setAnswer writes answer where k takes it; matches are the wordings of the
// choice it names, which a pick-option prefers in that order.
func setAnswer(b *match.Body, k match.Kind, answer string, matches []string) {
	switch k {
	case match.KindFixed:
		b.Answer = answer
	case match.KindCompensationRange:
		b.DefaultAnswer = answer
	default:
		b.Preferred, b.FallbackText = []string{answer}, answer
		if len(matches) > 0 {
			b.Preferred = slices.Clone(matches)
		}
	}
}

func scopedTo(e match.Entry, host string) bool {
	return slices.ContainsFunc(e.CapturedScope, func(s match.ScopeItem) bool { return strings.EqualFold(s.Host, host) })
}

// addPhrasing adds question to e's wordings unless the matcher's exact-wording
// stage would then find it on another entry too, where it decides nothing.
func addPhrasing(b match.Bank, e *match.Entry, question string) error {
	norm := Normalize(question)
	if slices.ContainsFunc(e.Examples, func(x string) bool { return Normalize(x) == norm }) {
		return nil
	}
	e.Examples = append(slices.Clone(e.Examples), question)
	for _, sh := range match.SharedPhrasings(b.Entries)[e.ID] {
		if sh.Phrasing == question && len(sh.With) > 0 {
			return refuse(ReasonExampleCollision, "entry %q already lists this wording", sh.With[0])
		}
	}
	return nil
}

func describeRoute(r Routed) string {
	switch {
	case r.Fills():
		return fmt.Sprintf("answer it %q from entry %q", r.Answer, r.EntryID)
	case r.Reason != "":
		return fmt.Sprintf("leave it to the user (%s)", r.Reason)
	}
	return "leave it to the user"
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AnswerText is an entry's answer as one line.
func AnswerText(e match.Entry) string {
	if e.AsksMe() {
		return "ask me each time"
	}
	switch e.Kind {
	case match.KindFixed:
		return e.Answer
	case match.KindPickOption:
		if len(e.Preferred) > 0 {
			return strings.Join(e.Preferred, " / ")
		}
		return e.FallbackText
	case match.KindMultiSelect:
		return strings.Join(e.Selected, ", ")
	case match.KindCompensationRange:
		if e.DefaultAnswer != "" {
			return e.DefaultAnswer
		}
		return strings.TrimSpace(strings.Trim(e.Minimum+" to "+e.Maximum, " to") + " " + e.Currency)
	case match.KindEmployerHistory:
		return "if the employer matches: " + e.AnswerIfMatch + "; otherwise: " + e.AnswerIfNoMatch
	case match.KindLanguageProficiency:
		return strings.TrimSpace(e.Language + " " + e.Speaking)
	}
	return ""
}

// ForgetRequest removes an answer: the entry, its answer on one site, or the
// answer itself in favour of asking the user each time.
type ForgetRequest struct {
	EntryID string
	// Host removes only the entry's answer on that site.
	Host string
	// AskMe keeps the entry and leaves its question to the user each time.
	AskMe bool
	// DryRun decides as for a removal and writes nothing.
	DryRun bool
}

// ForgetResult says what was removed.
type ForgetResult struct {
	EntryID, Question, Action string
	// Before is the answer removed.
	Before string
}

// forgotten is an entry Forget deleted, kept for Unforget.
type forgotten struct {
	entry     match.Entry
	at        int
	overrides map[string]match.Override
	when      time.Time
}

// Forget removes an answer. A shipped default cannot be deleted, because the
// next load puts it back; ask me is its way out. A deleted entry can be put
// back by Unforget for an hour.
func (s *Service) Forget(ctx context.Context, req ForgetRequest) (ForgetResult, error) {
	id := strings.TrimSpace(req.EntryID)
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped, err := s.bank.Bank()
	if err != nil {
		return ForgetResult{}, err
	}
	loaded, err := s.bank.File()
	if err != nil {
		return ForgetResult{}, err
	}
	at := slices.IndexFunc(loaded.Bank.Entries, func(e match.Entry) bool { return e.ID == id })
	if at < 0 || loaded.Bank.Entries[at].Archived() {
		return ForgetResult{}, refuse(ReasonUnknown, "there is no entry %q", id)
	}
	cur, _ := stamped.Bank.Find(id)
	file := loaded.Bank.Clone()
	e := &file.Entries[at]
	res := ForgetResult{EntryID: id, Question: cmpOr(e.Description, id), Before: AnswerText(cur)}
	host := ""
	if strings.TrimSpace(req.Host) != "" {
		if host = HostOf(req.Host); host == "" {
			return ForgetResult{}, refuse(ReasonHostRequired, "%q is not a site", req.Host)
		}
	}
	var kept *forgotten
	switch {
	case host != "":
		res.Action = ActionSiteRemoved
		list := file.Overrides[host]
		i := slices.IndexFunc(list, func(o match.Override) bool { return o.ID == id })
		switch {
		case i >= 0:
			if merged, err := list[i].Apply(cur); err == nil {
				res.Before = AnswerText(merged)
			}
			if list = slices.Delete(slices.Clone(list), i, i+1); len(list) == 0 {
				delete(file.Overrides, host)
			} else {
				file.Overrides[host] = list
			}
		case len(e.CapturedScope) > 0 && scopedTo(*e, host) && !slices.ContainsFunc(e.CapturedScope, func(s match.ScopeItem) bool { return !strings.EqualFold(s.Host, host) }):
			res.Action = ActionDeleted
			kept = removeEntry(&file, at)
		default:
			return ForgetResult{}, refuse(ReasonNothingToDo, "entry %q has no answer of its own on %s", id, host)
		}
	case req.AskMe:
		if cur.AsksMe() {
			return ForgetResult{}, refuse(ReasonNothingToDo, "entry %q already asks each time", id)
		}
		if !askMeInto(e) {
			return ForgetResult{}, refuse(ReasonTakesNoAnswer, "entry %q is a %s entry, which cannot ask each time", id, e.Kind)
		}
		e.Placeholder, e.NeedsReview, e.StampedBody = false, false, false
		e.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		res.Action = ActionAskMe
	default:
		if cur.DerivedFrom != "" {
			return ForgetResult{}, refuse(ReasonFromProfile, "entry %q follows the %s setup step; the user changes the answer there, or it can ask each time", id, cur.DerivedFrom)
		}
		def, err := answerbank.DefaultBank()
		if err != nil {
			return ForgetResult{}, err
		}
		if _, shipped := def.Bank.Find(id); shipped {
			return ForgetResult{}, refuse(ReasonShipped, "entry %q is shipped with the app and comes back at the next load; it can ask each time instead", id)
		}
		res.Action = ActionDeleted
		kept = removeEntry(&file, at)
	}
	if req.DryRun {
		return res, nil
	}
	if _, err := s.bank.SaveChange(loaded.Bank, file, loaded.Revision); err != nil {
		return ForgetResult{}, err
	}
	if kept != nil {
		kept.when = s.now()
		if s.forgot == nil {
			s.forgot = map[string]forgotten{}
		}
		s.forgot[id] = *kept
	}
	return res, nil
}

// askMeInto puts the ask-me marker where e's kind takes an answer.
func askMeInto(e *match.Entry) bool {
	switch e.Kind {
	case match.KindFixed:
		e.Answer = match.AskMe
	case match.KindPickOption:
		e.Preferred, e.FallbackText = []string{match.AskMe}, ""
	case match.KindCompensationRange:
		e.DefaultAnswer = match.AskMe
	case match.KindMultiSelect:
		e.Selected = []string{match.AskMe}
	default:
		return false
	}
	return true
}

// removeEntry deletes the entry at and its site answers, returning them.
func removeEntry(b *match.Bank, at int) *forgotten {
	e := b.Entries[at]
	k := &forgotten{entry: e, at: at, overrides: map[string]match.Override{}}
	b.Entries = slices.Delete(b.Entries, at, at+1)
	for host, list := range b.Overrides {
		i := slices.IndexFunc(list, func(o match.Override) bool { return o.ID == e.ID })
		if i < 0 {
			continue
		}
		k.overrides[host] = list[i]
		if list = slices.Delete(list, i, i+1); len(list) == 0 {
			delete(b.Overrides, host)
		} else {
			b.Overrides[host] = list
		}
	}
	return k
}

// Unforget puts back an entry Forget deleted within the last hour, with its
// site answers, unless an entry with its id exists again.
func (s *Service) Unforget(ctx context.Context, id string) (ForgetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.forgot[id]
	if !ok || s.now().Sub(k.when) > restoreWindow {
		return ForgetResult{}, refuse(ReasonNothingToDo, "no entry %q was deleted in the last hour", id)
	}
	loaded, err := s.bank.File()
	if err != nil {
		return ForgetResult{}, err
	}
	if _, exists := loaded.Bank.Find(id); exists {
		return ForgetResult{}, refuse(ReasonDuplicateID, "an entry with id %q exists again", id)
	}
	file := loaded.Bank.Clone()
	file.Entries = slices.Insert(file.Entries, min(k.at, len(file.Entries)), k.entry)
	for host, o := range k.overrides {
		if file.Overrides == nil {
			file.Overrides = map[string][]match.Override{}
		}
		file.Overrides[host] = append(file.Overrides[host], o)
	}
	if _, err := s.bank.SaveChange(loaded.Bank, file, loaded.Revision); err != nil {
		return ForgetResult{}, err
	}
	delete(s.forgot, id)
	return ForgetResult{EntryID: id, Question: cmpOr(k.entry.Description, id), Action: ActionRestored, Before: AnswerText(k.entry)}, nil
}

// EntryView is one entry as the Ask Agent reads it.
type EntryView struct {
	ID, Question, Answer string
	Kind                 match.Kind
	// Sites are the hosts the entry answers on alone (its captured scope);
	// SiteAnswers the hosts where it answers differently.
	Sites, SiteAnswers []string
	// FromSetup names the setup step the answer follows.
	FromSetup              string
	Blank, AskMe, ToReview bool
}

// FindEntries lists the entries whose question, wordings, answer or id share
// the most words with query, best first, at most limit, with the number that
// share any.
func (s *Service) FindEntries(ctx context.Context, query string, limit int) ([]EntryView, int, error) {
	words := strings.Fields(Normalize(query))
	if len(words) == 0 {
		return nil, 0, refuse(ReasonIncomplete, "say what to look for")
	}
	stamped, err := s.bank.Bank()
	if err != nil {
		return nil, 0, err
	}
	type scored struct {
		e     match.Entry
		score int
	}
	var hits []scored
	for _, e := range stamped.Bank.Entries {
		if e.Archived() || e.RunContext {
			continue
		}
		text := " " + Normalize(strings.Join(append([]string{e.ID, strings.ReplaceAll(e.ID, "-", " "), e.Description, AnswerText(e)}, e.Examples...), " ")) + " "
		n := 0
		for _, w := range words {
			if strings.Contains(text, " "+w) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, scored{e, n})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int { return b.score - a.score })
	sites := map[string][]string{}
	for host, list := range stamped.Bank.Overrides {
		for _, o := range list {
			sites[o.ID] = append(sites[o.ID], stamped.Bank.HostName(host))
		}
	}
	var out []EntryView
	for _, h := range hits[:min(limit, len(hits))] {
		e := h.e
		v := EntryView{ID: e.ID, Question: cmpOr(e.Description, e.ID), Answer: AnswerText(e), Kind: e.Kind, SiteAnswers: sites[e.ID],
			FromSetup: e.DerivedFrom, Blank: e.BodyEmpty(), AskMe: e.AsksMe(), ToReview: e.NeedsReview}
		for _, sc := range e.CapturedScope {
			v.Sites = append(v.Sites, sc.Host)
		}
		slices.Sort(v.SiteAnswers)
		out = append(out, v)
	}
	return out, len(hits), nil
}
