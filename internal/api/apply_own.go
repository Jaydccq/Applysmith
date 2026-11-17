package api

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/apply"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

// Bank entries the ladder reads whole; both are values the user wrote.
const (
	entrySponsorshipDescription = "sponsorship-type-description"
)

// own is PageDeps.Own over the workspace (OwnFacts).
func (a *applyStack) own(context.Context) (apply.Own, error) {
	return OwnFacts(a.s.cfg.Workspace, a.d.bank, a.s.now()), nil
}

// OwnFacts is what the ladder may be told about the user: the CV and voice
// files, the structured sponsorship and employment rows, the relocation
// preference, two answer-bank entries the user wrote, the bank itself (of
// which the grounded rung shows a model only what apply's groundedBank
// admits), and the profile facts of profileFacts. Nothing here is composed by
// a model, and nothing is written.
func OwnFacts(ws *workspace.Workspace, bank *answerbank.Service, today time.Time) apply.Own {
	read := func(f workspace.File) string {
		c, err := ws.Read(f)
		if err != nil || c.Revision == workspace.RevisionAbsent {
			return ""
		}
		return string(c.Data)
	}
	o := apply.Own{CV: read(workspace.FileCV), Voice: read(workspace.FileVoice), Relocation: bank.Preferences().Relocation}
	if p, ok := bank.Profile(); ok {
		f := bind.FactsFromProfile(p)
		o.SponsorshipNow, o.SponsorshipFuture, o.Employment, o.VisaStatus = f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture, f.Employment, f.VisaStatus
		o.Education = f.Education
		o.Facts = profileFacts(p, today)
	}
	if l, err := bank.Bank(); err == nil {
		o.Bank = l.Bank.Entries
		notice := ""
		for _, e := range l.Bank.Entries {
			switch e.ID {
			case match.IDEarliestStartDate:
				o.EarliestStart = strings.TrimSpace(e.Answer)
			case entrySponsorshipDescription:
				o.SponsorshipDescription = strings.TrimSpace(e.Answer)
			case entryNoticePeriod:
				if len(e.Preferred) > 0 {
					notice = strings.TrimSpace(e.Preferred[0])
				}
			}
		}
		availabilityFacts(o.Facts, o.EarliestStart, notice, today)
	}
	return o
}

// knowledge is PageDeps.Knowledge: the sections of the user's own knowledge
// base the shared search returns for a question, the best one of each
// document (a section's labeled lines come back as facts of their own, hence
// the wider K): the snippets, the narrative and the résumé only. One long
// document never fills every slot a row shows: Evertune's "Describe a
// backend system you built ..." saw three framing sections of the narrative
// and no project, and its answer said the notes held no system. A company
// brief is about the company, never evidence of the user's experience, and
// the profile reaches a model only through the ladder's fact allowlist (F-10).
func (a *applyStack) knowledge(ctx context.Context, query string) ([]match.KnowledgeSection, error) {
	res, err := a.d.kb.Search(ctx, kb.Query{Query: query, Kinds: []string{kb.KindSnippet, kb.KindNarrative, kb.KindCV}, K: 8})
	if err != nil {
		return nil, err
	}
	var out []match.KnowledgeSection
	seen := map[string]bool{}
	for _, f := range res.Facts {
		doc, _, _ := strings.Cut(f.SourceRef, "#")
		if !seen[doc] && strings.TrimSpace(f.Text) != "" {
			seen[doc] = true
			out = append(out, match.KnowledgeSection{Ref: f.SourceRef, Text: f.Text})
		}
	}
	return out, nil
}

// entryNoticePeriod is the bank entry holding the user's notice period.
const entryNoticePeriod = "availability-notice-period"

// availabilityFacts add to the profile facts the first day the applicant can
// start, as every start answer reads it (a blank or past date is thirty days
// from today), and the notice period behind it: a check that saw neither read
// RobCo's correct start a month out as contradicted by the current job.
func availabilityFacts(f map[string]string, earliest, notice string, today time.Time) {
	if f == nil {
		return
	}
	if d, ok := match.StartDate(earliest, match.Date{Year: today.Year(), Month: int(today.Month()), Day: today.Day()}); ok {
		f["availability earliest start"] = d.ISO()
	}
	if notice != "" {
		f["availability notice period"] = notice
	}
}

// profileFacts are the profile values a model rung may be handed: skills,
// education, jobs, the current city, work preferences, relocation and targets. Every key opens with a
// family gates.LadderFactAllowed accepts; identity, contact, demographic,
// compensation and work-authorization values have no key here, and the ladder
// filters again before a model sees them. A job's length is counted to today.
func profileFacts(p workspace.Profile, today time.Time) map[string]string {
	f := map[string]string{}
	put := func(key string, parts ...string) {
		var kept []string
		for _, s := range parts {
			if s = strings.TrimSpace(s); s != "" {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			f[key] = strings.Join(kept, ", ")
		}
	}
	for _, s := range p.Skills {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		years := ""
		if s.Years != nil {
			years = strconv.FormatFloat(*s.Years, 'f', -1, 64) + " years"
		}
		put("skill "+name, years, s.Level)
	}
	// An education entry carries its dates: a graduation window is answered and
	// reviewed from them (RRS: a review that saw no dates flagged a correct Yes).
	// Its grade average too: a review that saw none flagged the profile's own
	// GPA as unsupported (Adobe).
	for i, e := range p.History.Education {
		gpa := ""
		if g := strings.TrimSpace(e.GradeAverage); g != "" {
			gpa = "GPA " + g
		}
		put(fmt.Sprintf("education %d", i+1), e.Degree, e.Field, e.School, entrySpan(e.Start, e.End), gpa)
	}
	// A job carries its dates too: a review that saw no job flagged a work
	// row's correct start (Symbotic). "experience" sorts before the skills, so
	// the review's first lines hold it. And its length, so no model does the
	// date arithmetic: the review read "2026-06 to present" as "over two
	// years" (BCC). And the user's summary of the work: with none, a question
	// on professional TypeScript experience saw no job's stack and declined
	// (CharterUP).
	for i, e := range p.History.Employment {
		span := entrySpan(e.Start, e.End)
		if n := monthsHeld(e.Start, e.End, today); n > 0 {
			span += " (" + monthsText(n) + ")"
		}
		put(fmt.Sprintf("experience %d", i+1), e.Title, e.Employer, e.Location, span, e.Summary)
	}
	// Where the applicant lives, to the city: a review that saw no place flagged
	// a correct "Yes" to "Are you currently based in the U.S. or Canada?"
	// (Runway). The street and postal code stay withheld.
	addr := p.Candidate.Address
	put("current location", addr.City, addr.Region, match.CountryName(addr.Country))
	put("preferred work modes", p.Preferences.WorkModes...)
	if d := p.Preferences.MaxDaysInOffice; d != nil {
		put("preferred max days in office", strconv.Itoa(*d))
	}
	if t := p.Preferences.MaxTravelPct; t != nil {
		put("preferred max travel percent", strconv.Itoa(*t))
	}
	// The relocation rule in one line: beside a separate list of places, the
	// review read an open default's places as the only ones and flagged
	// Citadel's Miami, Greenwich and Houston, twice.
	r := p.Preferences.Relocation
	places := strings.Join(r.Places, ", ")
	switch r.Default {
	case match.RelocationOpen:
		rule := "open: willing to relocate to any place not excluded"
		if places != "" {
			rule += "; preferred: " + places
		}
		put("relocation", rule)
	case match.RelocationClosed:
		put("relocation", "closed: willing to relocate only to "+cmp.Or(places, "no place"))
	}
	put("relocation excluded places", r.Excluded...)
	var roles []string
	for _, r := range p.Targets.Roles {
		roles = append(roles, strings.TrimSpace(strings.TrimSpace(r.Seniority+" "+r.Title)))
	}
	put("target roles", roles...)
	put("target locations", p.Targets.Locations...)
	return f
}

// monthsHeld is a job's length in whole months, both end months counted as a
// résumé counts them ("2023-07 to 2023-09" is 3), an open job's to today's
// month; 0 when a date does not read as a month or the end is before the start.
func monthsHeld(start, end string, today time.Time) int {
	s, err := time.Parse("2006-01", strings.TrimSpace(start))
	if err != nil {
		return 0
	}
	e := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if end = strings.TrimSpace(end); end != "present" {
		if e, err = time.Parse("2006-01", end); err != nil {
			return 0
		}
	}
	return max(0, (e.Year()-s.Year())*12+int(e.Month()-s.Month())+1)
}

// monthsText says a length in months as years and months: "6 months", "1
// year", "2 years 3 months".
func monthsText(n int) string {
	unit := func(v int, one string) string {
		if v == 1 {
			return "1 " + one
		}
		return strconv.Itoa(v) + " " + one + "s"
	}
	switch y, m := n/12, n%12; {
	case y == 0:
		return unit(m, "month")
	case m == 0:
		return unit(y, "year")
	default:
		return unit(y, "year") + " " + unit(m, "month")
	}
}

// entrySpan is a history entry's dates as a model reads them.
func entrySpan(start, end string) string {
	switch start, end = strings.TrimSpace(start), strings.TrimSpace(end); {
	case start != "" && end != "":
		return start + " to " + end
	case end != "":
		return "ended " + end
	case start != "":
		return "from " + start
	}
	return ""
}
