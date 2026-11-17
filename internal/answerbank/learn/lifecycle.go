package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// CountedCorrections maps an entry id to the corrections that count toward
// its stale rule: the ones that carry the id of the entry that produced the
// corrected value, and that changed the answer rather than how the control was
// driven. Nothing about an application's outcome enters here.
func (s *Service) CountedCorrections(ctx context.Context) (map[string]int, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT entry_id, count(*) FROM answer_events
		WHERE kind = 'user-corrected' AND counted = 1 AND entry_id <> '' GROUP BY entry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Evolve moves every active, unpinned entry with enough counted corrections to
// stale. Without apply it only reports the ids. It runs from the CLI and the
// maintenance task, never as a side effect of a read, and nothing here archives
// an entry.
func (s *Service) Evolve(ctx context.Context, apply bool) ([]string, error) {
	counts, err := s.CountedCorrections(ctx)
	if err != nil {
		return nil, err
	}
	if apply {
		return s.bank.Evolve(counts)
	}
	l, err := s.bank.File()
	if err != nil {
		return nil, err
	}
	_, changed := l.Bank.Stale(counts, stamp(s.now()))
	return changed, nil
}

// Edit is a value a person changed by hand that an answer-bank entry had
// written there: a counted correction of that entry, which the keep press can
// make the entry's answer (flow round 9, K-1).
type Edit struct {
	FillID   int64
	EntryID  string
	Host     string
	Question string
	// Before is the value the entry wrote, After the person's: the picked
	// option's label for a choice.
	Before, After string
	options       []string
}

// editsSQL reads the manual fills that replaced a value with another while
// the correction they made (insertFill) still counts. A fill of a
// value-withheld control keeps neither value, nor does one that emptied its
// control, so neither is among them.
const editsSQL = `SELECT f.id, f.entry_id, f.host, f.question, f.options_json, f.prior_value, coalesce(f.option_label, f.value)
	FROM manual_fills f JOIN answer_events e ON e.fill_id = f.id
	WHERE e.kind = 'user-corrected' AND e.counted = 1 AND f.prior_value IS NOT NULL AND coalesce(f.option_label, f.value) IS NOT NULL`

func (s *Service) loadEdits(ctx context.Context, where string, args ...any) ([]Edit, error) {
	rows, err := s.st.Read().QueryContext(ctx, editsSQL+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edit
	for rows.Next() {
		var e Edit
		var opts string
		if err := rows.Scan(&e.FillID, &e.EntryID, &e.Host, &e.Question, &opts, &e.Before, &e.After); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(opts), &e.options)
		out = append(out, e)
	}
	return out, rows.Err()
}

// A read of Edits stops at maxOffers offers or maxRouted routed edits,
// whichever comes first: a routed edit costs up to two matcher routes, about
// 7 ms over the shipped bank, so a read stays near 100 ms and 300 ms at most,
// and newer edits the press never keeps (a derived entry's) hide an older
// offer only past 40. An edit of an entry the press never changes
// (Router.entryAt) is not routed (flow round 10, K2-3).
const (
	maxOffers = 12
	maxRouted = 40
)

// Edits are the edits the keep press is offered for (Router.keeps), the newest
// first and once per entry and value; a run id keeps that run's own.
func (s *Service) Edits(ctx context.Context, runID string) ([]Edit, error) {
	all, err := s.loadEdits(ctx, ` AND (?1 = '' OR f.run_id = ?1) ORDER BY f.at DESC, f.id DESC LIMIT 100`, runID)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	route, err := s.Routes()
	if err != nil {
		return nil, err
	}
	out := []Edit{}
	seen := map[string]bool{}
	routed := 0
	for _, e := range all {
		key := e.EntryID + "\x00" + foldAnswer(e.After)
		if seen[key] || route.entryAt(e) < 0 {
			continue
		}
		if len(out) == maxOffers || routed == maxRouted {
			break
		}
		routed++
		if route.keeps(e) {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// ReasonNotKept refuses the keep press on an edit the bank cannot keep.
const ReasonNotKept = "not-kept"

// KeepEdit is the keep press on the edit of manual fill fillID: its value
// becomes its entry's answer, saved through the bank service as the user's own
// (kept), and the entry's counted corrections stop counting, so neither the
// stale rule nor the edits count them again. It changes that one entry, and
// only for an edit the bank can keep (Router.keeps).
func (s *Service) KeepEdit(ctx context.Context, fillID int64) (workspace.Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.countedEdit(ctx, fillID)
	if err != nil {
		return workspace.Saved{}, err
	}
	route, err := s.Routes()
	if err != nil {
		return workspace.Saved{}, err
	}
	if !route.keeps(e) {
		return workspace.Saved{}, refuse(ReasonNotKept, "entry %q cannot make this value its answer to the question: it answers with it already, or would not answer with it as an answer you wrote", e.EntryID)
	}
	l, err := s.bank.File()
	if err != nil {
		return workspace.Saved{}, err
	}
	at := slices.IndexFunc(l.Bank.Entries, func(x match.Entry) bool { return x.ID == e.EntryID })
	if at < 0 {
		return workspace.Saved{}, fmt.Errorf("%w: entry %q is not in the bank", ErrNotFound, e.EntryID)
	}
	before := l.Bank.Entries[at]
	after := l.Bank.Clone()
	after.Entries[at] = kept(after.Entries[at], e.After)
	saved, err := s.bank.SaveChange(l.Bank, after, l.Revision)
	if err != nil {
		return workspace.Saved{}, err
	}
	events, err := s.uncount(ctx, `entry_id = ?`, e.EntryID)
	if err == nil {
		s.remember(fillID, e.EntryID, &before, events)
	}
	return saved, err
}

// LeaveEdit is the keep press's other answer: the entry keeps its answer, and
// its counted corrections to manual fill fillID's value, the one offer Edits
// makes of them, stop counting; those to other values still count. A stale
// entry left with fewer than the stale rule needs is active again, saved as
// the user's own change and otherwise unchanged (flow round 10, K2-1).
func (s *Service) LeaveEdit(ctx context.Context, fillID int64) (Edit, workspace.Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.countedEdit(ctx, fillID)
	if err != nil {
		return Edit{}, workspace.Saved{}, err
	}
	same, err := s.loadEdits(ctx, ` AND f.entry_id = ?`, e.EntryID)
	if err != nil {
		return Edit{}, workspace.Saved{}, err
	}
	var ids []any
	for _, x := range same {
		if sameAnswer(x.After, e.After) {
			ids = append(ids, x.FillID)
		}
	}
	counts, err := s.CountedCorrections(ctx)
	if err != nil {
		return Edit{}, workspace.Saved{}, err
	}
	// The bank goes first: when its save fails, the corrections still count and
	// the press can be made again.
	var saved workspace.Saved
	var before *match.Entry
	if counts[e.EntryID]-len(ids) < match.StaleAfterCorrections {
		if saved, before, err = s.activate(e.EntryID); err != nil {
			return Edit{}, workspace.Saved{}, err
		}
	}
	events, err := s.uncount(ctx, `fill_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
	if err == nil {
		s.remember(fillID, e.EntryID, before, events)
	}
	return e, saved, err
}

// activate makes the stale entry id active again through the bank service and
// returns the entry it changed; it saves nothing for an entry that is not stale.
func (s *Service) activate(id string) (workspace.Saved, *match.Entry, error) {
	l, err := s.bank.File()
	if err != nil {
		return workspace.Saved{}, nil, err
	}
	at := slices.IndexFunc(l.Bank.Entries, func(x match.Entry) bool { return x.ID == id })
	if at < 0 || l.Bank.Entries[at].State != match.StateStale {
		return workspace.Saved{}, nil, nil
	}
	after := l.Bank.Clone()
	after.Entries[at] = active(after.Entries[at])
	saved, err := s.bank.SaveChange(l.Bank, after, l.Revision)
	return saved, &l.Bank.Entries[at], err
}

// uncount stops the corrections where names counting; it returns their ids.
func (s *Service) uncount(ctx context.Context, where string, args ...any) ([]any, error) {
	var ids []any
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		ids = nil
		rows, err := tx.QueryContext(ctx, `UPDATE answer_events SET counted = 0 WHERE kind = 'user-corrected' AND counted = 1 AND `+where+` RETURNING id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// press is what a keep or leave press changed: the entry before and after
// (none when it saved no entry) and the corrections it stopped counting.
type press struct {
	before, after *match.Entry
	events        []any
}

// remember keeps the press for UndoEdit in memory: Undo is offered right
// after a press.
func (s *Service) remember(fillID int64, id string, before *match.Entry, events []any) {
	p := press{events: events}
	if before != nil {
		l, err := s.bank.File()
		at := slices.IndexFunc(l.Bank.Entries, func(x match.Entry) bool { return x.ID == id })
		if err != nil || at < 0 {
			return
		}
		p.before, p.after = before, &l.Bank.Entries[at]
	}
	if s.presses == nil {
		s.presses = map[int64]press{}
	}
	s.presses[fillID] = p
}

// ReasonNotUndoable refuses an undo of a press the server no longer holds.
const ReasonNotUndoable = "not-undoable"

// UndoEdit takes back the keep or leave press on manual fill fillID: the
// entry as it was, the settled corrections counting again. It is refused for
// an entry changed since and for a press it does not hold.
func (s *Service) UndoEdit(ctx context.Context, fillID int64) (workspace.Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.presses[fillID]
	if !ok {
		return workspace.Saved{}, refuse(ReasonNotUndoable, "this press can no longer be undone: it was undone already, or the app restarted since")
	}
	var saved workspace.Saved
	if p.after != nil {
		l, err := s.bank.File()
		if err != nil {
			return workspace.Saved{}, err
		}
		at := slices.IndexFunc(l.Bank.Entries, func(x match.Entry) bool { return x.ID == p.after.ID })
		if at < 0 || !reflect.DeepEqual(l.Bank.Entries[at], *p.after) {
			return workspace.Saved{}, refuse(ReasonEntryChanged, "the entry %q changed after the press, so it keeps what it holds now", p.after.ID)
		}
		next := l.Bank.Clone()
		next.Entries[at] = *p.before
		if saved, err = s.bank.SaveChange(l.Bank, next, l.Revision); err != nil {
			return workspace.Saved{}, err
		}
	}
	delete(s.presses, fillID)
	if len(p.events) == 0 {
		return saved, nil
	}
	return saved, s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE answer_events SET counted = 1 WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(p.events)), ",")+`)`, p.events...)
		return err
	})
}

// countedEdit is the edit of manual fill fillID while its correction counts.
func (s *Service) countedEdit(ctx context.Context, fillID int64) (Edit, error) {
	found, err := s.loadEdits(ctx, ` AND f.id = ?`, fillID)
	if err != nil {
		return Edit{}, err
	}
	if len(found) == 0 {
		return Edit{}, fmt.Errorf("%w: manual fill %d holds no counted correction of an entry", ErrNotFound, fillID)
	}
	return found[0], nil
}

// kept is the entry as the keep press leaves it: value is its answer, the
// picked option's label for a pick-option entry, confirmed as the user's own,
// so it answers without review and is active again.
func kept(e match.Entry, value string) match.Entry {
	if e.Kind == match.KindPickOption {
		e.Preferred, e.FallbackText = []string{value}, value
	} else {
		e.Answer = value
	}
	e.NeedsReview = false
	return active(e)
}

// active is e with the stale transition undone, and nothing else of it changed.
func active(e match.Entry) match.Entry {
	if e.State == match.StateStale {
		e.State, e.StaleAt, e.StaleCorrectionCount = "", "", 0
	}
	return e
}

// MaintenanceResult reports one maintenance pass.
type MaintenanceResult struct {
	Generated GenerateResult
	Stale     []string
	Pruned    int64
}

// Maintain is the scheduled maintenance task: it reconciles approvals the bank
// already holds, refreshes the candidates and starts their review,
// applies the stale transition and prunes events past their retention. The
// review runs in the background (ReviewLater): the daily sweep runs every
// retention rule under one short limit, and a review waits on a model once per
// candidate.
func (s *Service) Maintain(ctx context.Context) (MaintenanceResult, error) {
	var out MaintenanceResult
	if _, err := s.Reconcile(ctx); err != nil {
		return out, err
	}
	var err error
	if out.Generated, err = s.Generate(ctx, GenerateOptions{}); err != nil {
		return out, err
	}
	s.ReviewLater()
	if out.Stale, err = s.Evolve(ctx, true); err != nil {
		return out, err
	}
	out.Pruned, err = s.Prune(ctx)
	return out, err
}

// AddEntryRequest is an agent's request to add an entry for a question it
// captured from a live form.
type AddEntryRequest struct {
	Question string
	Answer   string
	// Options are the option labels the form rendered; with options the entry
	// picks one.
	Options []string
	// Host is the page host; the entry carries a captured scope and is eligible
	// only for that host, question and option set. Without one nothing is
	// written.
	Host string
	// Everywhere leaves the entry without a captured scope, so it answers the
	// question on every site.
	Everywhere bool
}

var employerRE = lazyregexp.New(`(?i)\b(?:(?:this|our|the) (?:company|organi[sz]ation|employer|firm|business|team|role|position|job|opportunity)|here|join us|work(?:ing)? (?:for|with|at) us|employees?|refer(?:red|rer))\b`)

// NamesCompany reports a question about this employer or this posting: the
// first word of the company's name, "this company", "here", "join us", an
// employee or a referrer. The user's answer to any other question a stopped
// run left answers it on every site (F-4).
func NamesCompany(question, company string) bool {
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	}
	name := ""
	for i, w := range words(company) {
		if i > 0 || w != "the" {
			name = w
			break
		}
	}
	return employerRE.MatchString(question) || len([]rune(name)) >= 3 && slices.Contains(words(question), name)
}

// Reasons specific to adding an entry.
const (
	ReasonConfirmationRequired = "confirmation-required"
	ReasonAlreadyAnswered      = "already-answered"
	ReasonHostRequired         = "host-required"
)

// AddEntryResult names the entry written.
type AddEntryResult struct{ EntryID string }

// AddEntry writes the answer the user typed into the needs-you form as their
// own entry (created_by user, no review mark) through the bank service. It asks
// the matcher first and refuses when the question is already filled outright;
// the card shows a review-band answer, which the user's answer replaces.
func (s *Service) AddEntry(ctx context.Context, req AddEntryRequest) (AddEntryResult, error) {
	question := cleanText(req.Question, maxQuestionRunes)
	answer := strings.TrimSpace(req.Answer)
	if question == "" || answer == "" {
		return AddEntryResult{}, refuse(ReasonConfirmationRequired, "an entry needs a question and an answer")
	}
	host := HostOf(req.Host)
	if host == "" {
		return AddEntryResult{}, refuse(ReasonHostRequired, "an agent-captured entry needs the host of the page it was captured on")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stamped, err := s.bank.Bank()
	if err != nil {
		return AddEntryResult{}, err
	}
	q, options := captured(question, req.Options)
	r := s.routeBank(stamped.Bank, match.Facts{}, host, match.Posting{}, q)
	if r.Fills() && r.Outcome == match.OutcomeFilled {
		return AddEntryResult{}, refuse(ReasonAlreadyAnswered, "entry %q already answers this question", r.EntryID)
	}
	loaded, err := s.bank.File()
	if err != nil {
		return AddEntryResult{}, err
	}
	// The user's every-site answer to a question the matcher left on a blank
	// entry goes into that entry: the entry then answers the question's other
	// wordings too, which a new entry beside it would not.
	if req.Everywhere {
		if after, ok := fillBlank(loaded.Bank, r, answer); ok {
			if _, err := s.bank.SaveChange(loaded.Bank, after, loaded.Revision); err != nil {
				return AddEntryResult{}, err
			}
			return AddEntryResult{EntryID: r.EntryID}, nil
		}
	}
	id, err := s.mintEntryID(question)
	if err != nil {
		return AddEntryResult{}, err
	}
	e := match.Entry{ID: id, Description: clampRunes(question, 200), Kind: match.KindFixed, Answer: answer}
	e.Examples = []string{question}
	e.State, e.CreatedBy, e.CreatedAt = match.StateActive, match.CreatedByUser, s.now().UTC().Format(time.RFC3339)
	if len(options) > 0 {
		e.Kind, e.Answer, e.Preferred, e.FallbackText = match.KindPickOption, "", []string{answer}, answer
	}
	if !req.Everywhere {
		e.CapturedScope = []match.ScopeItem{{Host: host, Question: question, Options: options}}
	}
	after := loaded.Bank.Clone()
	after.Entries = append(after.Entries, e)
	if _, err := s.bank.SaveChange(loaded.Bank, after, loaded.Revision); err != nil {
		return AddEntryResult{}, err
	}
	return AddEntryResult{EntryID: id}, nil
}

// blankAt is the index of the fixed or pick-option entry r abstained on
// because its body is empty, else -1. A skill-screening entry is never one: its
// body is the label it gives every skill the profile records, so one question's
// answer written there would answer every other skill question with it.
func blankAt(bank match.Bank, r Routed) int {
	if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonPlaceholderEmpty || r.EntryID == "" {
		return -1
	}
	at := slices.IndexFunc(bank.Entries, func(e match.Entry) bool { return e.ID == r.EntryID })
	if at < 0 {
		return -1
	}
	if e := bank.Entries[at]; !e.BodyEmpty() || e.Archived() || e.SkillMode != "" || e.Kind != match.KindFixed && e.Kind != match.KindPickOption {
		return -1
	}
	return at
}

// fillBlank writes answer into the blank entry r abstained on (blankAt); any
// other result is false.
func fillBlank(bank match.Bank, r Routed, answer string) (match.Bank, bool) {
	at := blankAt(bank, r)
	if at < 0 {
		return bank, false
	}
	out := bank.Clone()
	if e := &out.Entries[at]; e.Kind == match.KindFixed {
		e.Answer = answer
	} else {
		e.Preferred, e.FallbackText = []string{answer}, answer
	}
	return out, true
}

// captured is the matcher question for a question captured from a form, with
// its cleaned option labels: a choice control when it has any.
func captured(question string, options []string) (match.Question, []string) {
	q := match.Question{Selector: "q", Text: question, Kind: match.KindText}
	options = cleanOptions(options)
	if len(options) > 0 {
		q.Kind = match.KindSelect
		for _, o := range options {
			q.Options = append(q.Options, match.Option{Value: o, Label: o})
		}
	}
	return q, options
}

// Router routes questions captured on their hosts over one read of the bank
// and of the profile, as the fill reads them. Over that one read a question
// always routes the same way, so each is matched once: the views route the
// same wordings of many waiting runs, each more than once.
type Router struct {
	s     *Service
	bank  match.Bank
	m     *match.Matcher
	facts match.Facts
	memo  *routeMemo
}

type routeMemo struct {
	mu   sync.Mutex
	seen map[string]Routed
}

// Routes reads the bank once for many questions.
func (s *Service) Routes() (Router, error) {
	stamped, err := s.bank.Bank()
	if err != nil {
		return Router{}, err
	}
	return Router{s: s, bank: stamped.Bank, m: match.New(stamped.Bank, match.DefaultStages()), facts: s.profileFacts(), memo: &routeMemo{seen: map[string]Routed{}}}, nil
}

// Route is what the matcher does with a question captured on host in an
// application to posting, which a question about "this company" reads.
func (r Router) Route(host string, posting match.Posting, question string, options []string) Routed {
	return r.remembered(func() Routed {
		q, _ := captured(cleanText(question, maxQuestionRunes), options)
		return r.s.route(r.m, r.facts, HostOf(host), posting, q)
	}, host, posting.Company, posting.Title, question, strings.Join(options, "\x1f"))
}

// RouteAfter is Route for a question its page asked right after another, as
// the matcher reads them there: a follow-up ("If yes, please explain") takes
// its answer from the one before it. The question is required, as the ones a
// run leaves are, so a follow-up a No settles takes "N/A".
func (r Router) RouteAfter(host string, posting match.Posting, before string, beforeOptions []string, question string, options []string) Routed {
	return r.remembered(func() Routed {
		p, _ := captured(cleanText(before, maxQuestionRunes), beforeOptions)
		q, _ := captured(cleanText(question, maxQuestionRunes), options)
		p.Selector, q.Required = "p", true
		return r.s.route(r.m, r.facts, HostOf(host), posting, p, q)
	}, host, posting.Company, posting.Title, question, strings.Join(options, "\x1f"), before, strings.Join(beforeOptions, "\x1f"))
}

// Confirming is the router as it reads once the user marks family's list on
// Profile complete on day: what that mark would answer.
func (r Router) Confirming(family match.RelationshipFamily, day string) Router {
	f := r.facts
	f.RelationshipsComplete = map[match.RelationshipFamily]string{family: day}
	for k, v := range r.facts.RelationshipsComplete {
		if k != family {
			f.RelationshipsComplete[k] = v
		}
	}
	return Router{s: r.s, bank: r.bank, m: r.m, facts: f, memo: &routeMemo{seen: map[string]Routed{}}}
}

// remembered is route's answer for the question the parts name, matched on
// the first ask only.
func (r Router) remembered(route func() Routed, parts ...string) Routed {
	if r.memo == nil {
		return route()
	}
	key := strings.Join(parts, "\x00")
	r.memo.mu.Lock()
	got, ok := r.memo.seen[key]
	r.memo.mu.Unlock()
	if ok {
		return got
	}
	got = route()
	r.memo.mu.Lock()
	r.memo.seen[key] = got
	r.memo.mu.Unlock()
	return got
}

// FillsBlank reports a result whose question the user's every-site answer
// writes into the blank entry it reached, so that answer answers every wording
// that reaches the entry (AddEntry).
func (r Router) FillsBlank(res Routed) bool { return blankAt(r.bank, res) >= 0 }

// SkillGate reports an entry that declares a skill_mode: the matcher answers it
// No, given the knowledge base, for a skill none of the user's information
// mentions (F-10).
func (r Router) SkillGate(id string) bool {
	return slices.ContainsFunc(r.bank.Entries, func(e match.Entry) bool { return e.ID == id && e.SkillMode != "" })
}

// keeps reports whether the keep press can make e's value the answer of e's
// entry. Never for a skill-screening entry (skill_mode): its body is the label
// it gives every skill the profile records. Otherwise when the bank does not
// answer e's question with the value already and, with the entry as the press
// leaves it (kept), answers it with the value as an answer the user wrote into
// the bank (Routed.Own): so never for an answer the profile derives or a load
// stamps, nor where an override for the host or an exception the entry makes
// for the question would keep answering otherwise.
func (r Router) keeps(e Edit) bool {
	at := r.entryAt(e)
	if at < 0 {
		return false
	}
	q, _ := captured(e.Question, e.options)
	host := HostOf(e.Host)
	if sameAnswer(r.s.route(r.m, r.facts, host, match.Posting{}, q).Answer, e.After) {
		return false
	}
	after := r.bank.Clone()
	after.Entries[at] = kept(after.Entries[at], e.After)
	then := r.s.routeBank(after, r.facts, host, match.Posting{}, q)
	return then.Own && sameAnswer(then.Answer, e.After)
}

// entryAt is the index of e's entry in the bank, or -1 when the bank does not
// hold it or it is a skill-screening entry, which the keep press never changes.
func (r Router) entryAt(e Edit) int {
	at := slices.IndexFunc(r.bank.Entries, func(x match.Entry) bool { return x.ID == e.EntryID })
	if at >= 0 && r.bank.Entries[at].SkillMode != "" {
		return -1
	}
	return at
}

// Outright is the result when the bank answers it outright: the entry and its
// answer, else the zero value while the question is open or answered only for
// review.
func (r Routed) Outright() Routed {
	if r.Fills() && r.Outcome == match.OutcomeFilled {
		return r
	}
	return Routed{}
}
