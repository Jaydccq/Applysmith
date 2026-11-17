package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
)

// Digest bounds.
const (
	MaxDigestThreads = 30
	MaxDrafts        = 3
	DraftBudget      = 45 * time.Second
	MaxWeeklyList    = 10
	RatingExamples   = 12
	weeklyQuietDays  = 7
	headlineChars    = 160
	lineChars        = 200
)

var laneTitle = map[string]string{
	lifecycle.LaneDoNow:   "Do now",
	lifecycle.LanePrepare: "Prepare",
	lifecycle.LaneReview:  "Needs review",
	lifecycle.LaneWaiting: "Waiting",
	lifecycle.LaneClosed:  "Closed",
	SectionWaitingOnYou:   "Waiting on you",
	SectionFollowUp:       "Waiting on the company, quiet for over 7 days",
}

// itemOf is thread t listed in section at now. A due time already past on a
// thread that waits on the company is no deadline of anyone's (the interview
// it was set by is over), so it is left out; a thread waiting on the company
// says when the company last wrote, which tells a follow-up worth sending from
// a thread long gone quiet.
func itemOf(t Thread, section string, now time.Time) DigestItem {
	it := DigestItem{Key: t.ID, Section: section, Company: employerName(t.Employer), Role: roleName(t.Role), Lane: t.View.Lane,
		State: t.View.State, Owner: t.View.Owner, NextAction: t.View.NextAction, DraftKind: t.View.DraftKind}
	if !t.View.Due.IsZero() && !(t.View.Owner == lifecycle.OwnerCompany && t.View.Due.Before(now)) {
		it.Due = t.View.Due.UTC().Format(time.RFC3339)
	}
	it.Line = deterministicLine(it, time.Local)
	if section == SectionFollowUp && !t.LastIncoming.IsZero() {
		it.Line += " (last heard " + t.LastIncoming.Local().Format("Mon Jan 2") + ")"
	}
	return it
}

// dueText words the item's due time in loc.
func dueText(it DigestItem, loc *time.Location) string {
	if it.Due == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, it.Due)
	if err != nil {
		return ""
	}
	return DueWords(t, loc)
}

func deterministicLine(it DigestItem, loc *time.Location) string {
	s := it.Company
	if it.Role != "" {
		s += ", " + it.Role
	}
	s += ": " + it.NextAction
	if d := dueText(it, loc); d != "" {
		s += " (due " + d + ")"
	}
	return s
}

// collected is the deterministic part of a digest.
type collected struct {
	items    []DigestItem
	threads  []Thread
	muted    []DigestItem
	omitted  int
	receipts int
}

// collect keeps the threads updated in the window, ranks them by lane, due
// time and recency, counts receipts and muted threads without listing them and
// lists at most 30.
func collect(threads []Thread, verdicts map[string]Feedback, from, to time.Time) collected {
	var c collected
	var keep []Thread
	for _, t := range threads {
		if t.LastUpdate.Before(from) || t.LastUpdate.After(to) {
			continue
		}
		switch {
		case t.View.Lane == lifecycle.LaneReceiptOnly:
			c.receipts++
		case muted(verdicts[t.ID], t):
			c.muted = append(c.muted, itemOf(t, t.View.Lane, to))
		default:
			keep = append(keep, t)
		}
	}
	slices.SortStableFunc(keep, func(a, b Thread) int {
		if d := lifecycle.LaneOrder(a.View.Lane) - lifecycle.LaneOrder(b.View.Lane); d != 0 {
			return d
		}
		switch {
		case a.View.Due.IsZero() != b.View.Due.IsZero():
			if a.View.Due.IsZero() {
				return 1
			}
			return -1
		case !a.View.Due.IsZero() && !a.View.Due.Equal(b.View.Due):
			return a.View.Due.Compare(b.View.Due)
		}
		if c := b.LastUpdate.Compare(a.LastUpdate); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(keep) > MaxDigestThreads {
		c.omitted = len(keep) - MaxDigestThreads
		keep = keep[:MaxDigestThreads]
	}
	c.threads = keep
	for _, t := range keep {
		c.items = append(c.items, itemOf(t, t.View.Lane, to))
	}
	return c
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func deterministicHeadline(c collected) string {
	if len(c.items) == 0 && c.receipts == 0 && len(c.muted) == 0 {
		return "Nothing new in your job mail."
	}
	counts := map[string]int{}
	for _, it := range c.items {
		counts[it.Lane]++
	}
	var parts []string
	for _, lane := range []string{lifecycle.LaneDoNow, lifecycle.LanePrepare, lifecycle.LaneReview, lifecycle.LaneWaiting, lifecycle.LaneClosed} {
		if n := counts[lane]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(laneTitle[lane])))
		}
	}
	out := plural(len(c.items), "thread", "threads")
	if len(parts) > 0 {
		out += " (" + strings.Join(parts, ", ") + ")"
	}
	return out + "."
}

// factsJSON is what the digest model may draw names and numbers from.
func factsJSON(c collected, examples []Feedback) string {
	type fact struct {
		N                                int
		Company, Role, Lane, State, Next string
		Due                              string `json:",omitempty"`
	}
	facts := struct {
		Threads  []fact
		Receipts int
		Omitted  int
		Rated    []map[string]string `json:",omitempty"`
	}{Receipts: c.receipts, Omitted: c.omitted}
	for i, it := range c.items {
		facts.Threads = append(facts.Threads, fact{i + 1, it.Company, it.Role, it.Lane, it.State, it.NextAction, dueText(it, time.Local)})
	}
	for _, e := range examples {
		facts.Rated = append(facts.Rated, map[string]string{"company": e.Company, "lane": e.Lane, "state": e.State, "verdict": e.Verdict})
	}
	raw, _ := json.Marshal(facts)
	return string(raw)
}

// modelText is the headline and the per-thread lines the digest model wrote.
type modelText struct {
	Headline string
	Lines    map[int]string
}

// composeModel asks the gmail-digest task for the headline and one line per
// thread as strict JSON. Any failure, unparsable reply, leak or number that is
// not in the facts returns ok false and the caller keeps the deterministic
// text. Thread numbers the model invents are dropped.
func (s *Stack) composeModel(ctx context.Context, c collected, examples []Feedback) (modelText, bool) {
	if s.Model == nil || len(c.items) == 0 {
		return modelText{}, false
	}
	facts := factsJSON(c, examples)
	system := "You write the text of a daily digest of the owner's job-search email. Reply with one JSON object only: " +
		`{"headline": "<one sentence, at most 160 characters>", "lines": {"1": "<one line, at most 200 characters>", ...}}` +
		" with one line per thread number in the facts. Use only the facts. Do not add numbers that are not in the facts. " +
		"Rated examples show what the owner liked or disliked; let them shape tone and emphasis. " + untrustedNote
	res, err := s.Model.Complete(ctx, TaskDigest, system, "<<<MAIL\n"+facts+"\nMAIL>>>")
	if err != nil || res.Finish == "length" {
		return modelText{}, false
	}
	raw := res.Text
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		raw = raw[i : j+1]
	}
	var parsed struct {
		Headline string
		Lines    map[string]string
	}
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return modelText{}, false
	}
	out := modelText{Headline: oneLine(parsed.Headline), Lines: map[int]string{}}
	all := out.Headline
	for k, v := range parsed.Lines {
		n, err := strconv.Atoi(k)
		v = oneLine(v)
		if err != nil || n < 1 || n > len(c.items) || v == "" {
			continue
		}
		out.Lines[n] = v
		all += " " + v
	}
	if leaks(all) || !numbersSubset(all, facts) || utf8.RuneCountInString(out.Headline) > headlineChars {
		return modelText{}, false
	}
	for n, v := range out.Lines {
		if utf8.RuneCountInString(v) > lineChars {
			delete(out.Lines, n)
		}
	}
	if out.Headline == "" {
		return modelText{}, false
	}
	return out, true
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// draftsFor produces reply drafts for at most three threads, in digest order,
// within the context's budget, and returns them by item index. A draft that
// fails or misses the budget is absent. It reads the collected threads only, so
// it can run beside the digest text.
func (s *Stack) draftsFor(ctx context.Context, threads []Thread) map[int]string {
	type res struct {
		i    int
		text string
	}
	ch := make(chan res, MaxDrafts)
	n := 0
	for i, t := range threads {
		if n == MaxDrafts {
			break
		}
		if !draftable(t) {
			continue
		}
		n++
		go func() {
			if d, ok := s.DraftFor(ctx, t); ok {
				ch <- res{i, d.Text}
				return
			}
			ch <- res{-1, ""}
		}()
	}
	out := map[int]string{}
	for ; n > 0; n-- {
		select {
		case r := <-ch:
			if r.i >= 0 {
				out[r.i] = r.text
			}
		case <-ctx.Done():
			return out
		}
	}
	return out
}

// renderDaily is the plain text of a daily digest: sections in lane order,
// items numbered through the whole text.
func renderDaily(headline string, at time.Time, c collected) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Gmail digest, %s\n%s\n", at.Format("Mon Jan 2 15:04"), headline)
	n := 0
	section := ""
	for _, it := range c.items {
		n++
		if it.Section != section {
			section = it.Section
			fmt.Fprintf(&b, "\n%s\n", laneTitle[section])
		}
		fmt.Fprintf(&b, "%d. %s\n", n, it.Line)
	}
	b.WriteString("\n")
	writeTail(&b, c)
	var drafts []string
	for i, it := range c.items {
		if it.Draft != "" {
			drafts = append(drafts, strconv.Itoa(i+1))
		}
	}
	if len(drafts) > 0 {
		fmt.Fprintf(&b, "Reply drafts are ready for item %s; open them in the dashboard. Nothing was sent.\n", strings.Join(drafts, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeTail(b *strings.Builder, c collected) {
	if c.receipts > 0 {
		fmt.Fprintf(b, "%s counted, not listed.\n", plural(c.receipts, "application confirmation", "application confirmations"))
	}
	if len(c.muted) > 0 {
		fmt.Fprintf(b, "%s muted by your ratings.\n", plural(len(c.muted), "thread", "threads"))
	}
	if c.omitted > 0 {
		fmt.Fprintf(b, "%d more threads were omitted; see the dashboard.\n", c.omitted)
	}
}

// funnelStates are the lifecycle states a weekly funnel counts.
var funnelStates = []string{lifecycle.StateApplied, lifecycle.StateResponded, lifecycle.StateAssessment, lifecycle.StateInterview, lifecycle.StateOffer, lifecycle.StateRejected}

// funnel counts distinct threads per state reached in the window from
// incoming mail.
func funnel(threads []Thread, from, to time.Time) map[string]int {
	out := map[string]int{}
	for _, state := range funnelStates {
		out[state] = 0
	}
	for _, t := range threads {
		reached := map[string]bool{}
		for _, s := range t.Signals {
			at := time.UnixMilli(s.ReceivedMS)
			if s.Direction != classify.DirIncoming || s.Tier != classify.TierActionable || at.Before(from) || at.After(to) {
				continue
			}
			if st := lifecycle.StateOf(s.Type); st != "" {
				reached[st] = true
			}
		}
		for st := range reached {
			out[st]++
		}
	}
	return out
}

func funnelTotal(f map[string]int) int {
	n := 0
	for _, v := range f {
		n += v
	}
	return n
}

func funnelText(f map[string]int) string {
	var parts []string
	for _, st := range funnelStates {
		parts = append(parts, fmt.Sprintf("%s %d", st, f[st]))
	}
	return strings.Join(parts, ", ")
}

// weeklyLists builds the two lists of a weekly review, each capped at ten.
func weeklyLists(threads []Thread, now time.Time) (onYou, followUp []DigestItem, moreYou, moreFollow int) {
	var you, follow []Thread
	for _, t := range threads {
		switch {
		case rejectedLater(t, threads):
		case t.View.Lane == lifecycle.LaneDoNow && t.View.Owner == lifecycle.OwnerUser:
			you = append(you, t)
		case t.View.Lane == lifecycle.LaneWaiting && t.View.Owner == lifecycle.OwnerCompany && now.Sub(t.LastIncoming) > weeklyQuietDays*24*time.Hour:
			follow = append(follow, t)
		}
	}
	older := func(a, b Thread) int { return a.LastIncoming.Compare(b.LastIncoming) }
	slices.SortStableFunc(you, older)
	slices.SortStableFunc(follow, older)
	cap10 := func(ts []Thread, section string) (items []DigestItem, more int) {
		for i, t := range ts {
			if i == MaxWeeklyList {
				return items, len(ts) - MaxWeeklyList
			}
			items = append(items, itemOf(t, section, now))
		}
		return items, 0
	}
	onYou, moreYou = cap10(you, SectionWaitingOnYou)
	followUp, moreFollow = cap10(follow, SectionFollowUp)
	return
}

// rejectedLater reports whether a thread linked to the same candidacy as t
// holds a rejection that came after t's latest mail. The company's answer
// arrives in a thread of its own, so the interview thread alone still read as
// waiting or as a follow-up to send (a weekly review listed Lenovo as quiet a
// month after its rejection, both threads linked to the one candidacy). An
// unlinked thread is never closed this way: the employer's name alone cannot
// tell two applications at one company apart, and a follow-up lost costs more
// than one sent in vain. The weekly lists leave such a thread out; the thread
// itself keeps its own reading.
func rejectedLater(t Thread, threads []Thread) bool {
	if t.CandidacyID == 0 {
		return false
	}
	return slices.ContainsFunc(threads, func(r Thread) bool {
		return r.ID != t.ID && r.CandidacyID == t.CandidacyID && r.View.State == lifecycle.StateRejected && r.LastIncoming.After(t.LastIncoming)
	})
}

func deterministicNarrative(f map[string]int, you, follow int) string {
	return fmt.Sprintf("In the last 7 days: %d applied, %d responded, %d reached an interview, %d offers, %d rejections. %s waiting on you and %s waiting on the company.",
		f[lifecycle.StateApplied], f[lifecycle.StateResponded], f[lifecycle.StateInterview], f[lifecycle.StateOffer], f[lifecycle.StateRejected],
		plural(you, "thread is", "threads are"), plural(follow, "thread is", "threads are"))
}

// weeklyNarrative asks for two to three sentences; anything unparseable, too
// long or carrying a number that is not a fact is replaced by the deterministic
// narrative.
func (s *Stack) weeklyNarrative(ctx context.Context, f map[string]int, you, follow int) (string, bool) {
	fallback := deterministicNarrative(f, you, follow)
	if s.Model == nil {
		return fallback, false
	}
	facts := fmt.Sprintf("Funnel (distinct threads, last 7 days): %s. Threads waiting on the owner: %d. Threads waiting on the company for over 7 days: %d.", funnelText(f), you, follow)
	system := "You write the narrative of a weekly review of the owner's job search in two to three sentences, at most 400 characters. " +
		"Use only the facts given and no number that is not in them. Plain text. " + untrustedNote
	text, ok := s.generate(ctx, TaskDigest, system, "<<<MAIL\n"+facts+"\nMAIL>>>", func(x string) bool {
		return validSentence(x) && utf8.RuneCountInString(x) <= 400 && !leaks(x) && numbersSubset(x, facts)
	})
	if !ok {
		return fallback, false
	}
	return text, true
}

func renderWeekly(narrative string, at time.Time, body DigestBody) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Gmail weekly review, %s\n%s\n\nFunnel, last 7 days: %s.\n", at.Format("Mon Jan 2"), narrative, funnelText(body.Funnel))
	n := 0
	for _, sec := range []struct {
		name string
		more int
	}{{SectionWaitingOnYou, body.MoreWaiting}, {SectionFollowUp, body.MoreFollowUp}} {
		list := sectionItems(body.Items, sec.name)
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d)\n", laneTitle[sec.name], len(list)+sec.more)
		for _, it := range list {
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, it.Line)
		}
		if sec.more > 0 {
			fmt.Fprintf(&b, "and %d more\n", sec.more)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sectionItems(items []DigestItem, section string) []DigestItem {
	var out []DigestItem
	for _, it := range items {
		if it.Section == section {
			out = append(out, it)
		}
	}
	return out
}
