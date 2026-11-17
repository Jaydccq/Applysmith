package bind

import (
	"context"
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/cvparse"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

// Bounds on the evidence a request carries.
const (
	maxCVLines          = 400
	maxEvidenceQueries  = 8
	evidencePerQuestion = 4
)

// FactsFromProfile copies the profile values the profile-grounded resolvers
// read. A blank value stays blank; nothing is defaulted.
func FactsFromProfile(p workspace.Profile) match.Facts {
	a := p.Candidate.Address
	f := match.Facts{
		DateOfBirth:               p.Candidate.DateOfBirth,
		VisaStatus:                p.WorkAuthorization.VisaStatus,
		RequiresSponsorshipNow:    p.WorkAuthorization.RequiresSponsorshipNow,
		RequiresSponsorshipFuture: p.WorkAuthorization.RequiresSponsorshipFuture,
		AuthorizedCountries:       p.WorkAuthorization.AuthorizedCountries,
		Citizenships:              p.WorkAuthorization.Citizenships,
		WorkModes:                 p.Preferences.WorkModes,
		Address: match.AddressFact{Line1: a.Line1, Line2: a.Line2, City: a.City, County: a.County,
			Region: a.Region, PostalCode: a.PostalCode, Country: a.Country},
	}
	for _, e := range p.History.Education {
		f.Education = append(f.Education, match.EducationFact{School: e.School, Degree: e.Degree, Field: e.Field,
			Start: e.Start, End: e.End, GradeAverage: e.GradeAverage})
	}
	for _, e := range p.History.Employment {
		f.Employment = append(f.Employment, match.EmploymentFact{Employer: e.Employer, Title: e.Title,
			Start: e.Start, End: e.End, Location: e.Location, Summary: e.Summary})
	}
	for _, s := range p.Skills {
		years := s.Years
		if years == nil {
			d := defaultSkillYears
			years = &d
		}
		f.Skills = append(f.Skills, match.SkillFact{Name: s.Name, Years: years, Level: s.Level})
	}
	text := slices.Concat(p.Targets.Locations, p.Preferences.WorkModes, p.Preferences.Relocation.Places, p.Preferences.Relocation.Excluded,
		[]string{p.Links.LinkedIn, p.Links.GitHub, p.Links.Portfolio}, p.Links.Other)
	for _, r := range p.Targets.Roles {
		text = append(text, r.Title)
	}
	for _, r := range p.Resumes {
		text = append(append(text, r.Label, r.Description, r.Instructions), r.Tags...)
	}
	for _, t := range text {
		if strings.TrimSpace(t) != "" {
			f.ProfileText = append(f.ProfileText, t)
		}
	}
	for _, r := range p.CompanyRelationships.Relationships {
		f.Relationships = append(f.Relationships, match.RelationshipFact{Family: match.RelationshipFamily(r.Family), Company: r.Company,
			Aliases: r.Aliases, Current: r.Current, Person: r.PersonName, Relation: r.Relation, Recorded: r.Recorded})
	}
	if len(p.CompanyRelationships.Complete) > 0 {
		f.RelationshipsComplete = map[match.RelationshipFamily]string{}
		for family, asOf := range p.CompanyRelationships.Complete {
			f.RelationshipsComplete[match.RelationshipFamily(family)] = asOf
		}
	}
	return f
}

// defaultSkillYears is what a skill the user lists without its years counts
// as, in the profile or the CV's skills section: one to two years, the owner's
// default, until the user records the years.
const defaultSkillYears = 1.5

// withCVSkills adds the skills the CV's skills section names that the profile
// does not record, at defaultSkillYears and no level: the CV is the user's own word
// on what they work with, and a profile need not repeat it. A slash pair
// ("C/C++") also names its two skills. The seeded template CV names none.
func withCVSkills(skills []match.SkillFact, cv string) []match.SkillFact {
	if strings.TrimSpace(cv) == "" || workspace.IsTemplate(workspace.FileCV, []byte(cv)) {
		return skills
	}
	have := map[string]bool{}
	for _, s := range skills {
		have[strings.ToLower(strings.TrimSpace(s.Name))] = true
	}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if k := strings.ToLower(name); k != "" && !have[k] {
			have[k] = true
			years := defaultSkillYears
			skills = append(skills, match.SkillFact{Name: name, Years: &years})
		}
	}
	for _, name := range cvparse.Parse(cv).Skills {
		add(name)
		if parts := strings.Split(name, "/"); len(parts) > 1 {
			for _, p := range parts {
				add(p)
			}
		}
	}
	return skills
}

// cvText reads the résumé text; an absent or unreadable file reads as empty.
func (b *Binding) cvText() string {
	c, err := b.d.Workspace.Read(workspace.FileCV)
	if err != nil || c.Revision == workspace.RevisionAbsent {
		return ""
	}
	return string(c.Data)
}

// evidence is the text a proficiency ladder is judged against: the résumé's
// lines, and the knowledge snippets for each question that renders a ladder (at
// most maxEvidenceQueries per request, so a long form costs a bounded number
// of searches). The seeded template CV gives no line.
func (b *Binding) evidence(ctx context.Context, cv string, qs []match.Question) []string {
	if workspace.IsTemplate(workspace.FileCV, []byte(cv)) {
		cv = ""
	}
	var out []string
	for _, line := range strings.Split(cv, "\n") {
		if l := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*+ ")); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
			if len(out) == maxCVLines {
				break
			}
		}
	}
	if b.d.Evidence == nil {
		return out
	}
	queries := 0
	for _, q := range qs {
		if queries == maxEvidenceQueries {
			break
		}
		if !match.IsProficiencyLadder(q.Options) || strings.TrimSpace(q.Text) == "" {
			continue
		}
		queries++
		res, err := b.d.Evidence.Search(ctx, kb.Query{Query: q.Text, Kinds: []string{kb.KindSnippet}, K: evidencePerQuestion})
		if err != nil {
			b.d.Log.Warn("bind: knowledge search failed", "err", err)
			continue
		}
		for _, f := range res.Facts {
			if f.SourceKind == kb.KindSnippet && strings.TrimSpace(f.Text) != "" {
				out = append(out, f.Text)
			}
		}
	}
	return out
}

// knowledge is match.Facts.Knowledge for a request with a question about the
// user's own experience (match.OwnQuestion): every section of the user's own
// documents, and whether they were read whole (F-10). Nothing is read, so no
// answer is No for want of evidence, for any other request, while the user has
// no résumé of their own (absent, or the seeded template), and when the
// knowledge base cannot be read. A Binding without a knowledge base reads one
// with nothing in it.
func (b *Binding) knowledge(ctx context.Context, cv string, qs []match.Question) ([]match.KnowledgeSection, bool) {
	if strings.TrimSpace(cv) == "" || workspace.IsTemplate(workspace.FileCV, []byte(cv)) || !slices.ContainsFunc(qs, ownSearched) {
		return nil, false
	}
	if b.d.Evidence == nil {
		return nil, true
	}
	secs, err := b.d.Evidence.OwnSections(ctx)
	if err != nil {
		b.d.Log.Warn("bind: the knowledge base could not be read for a question about the user's own experience", "err", err)
		return nil, false
	}
	out := make([]match.KnowledgeSection, len(secs))
	for i, s := range secs {
		out[i] = match.KnowledgeSection(s)
	}
	return out, true
}

// ownSearched reports a question whose F-10 search reads the knowledge base:
// one about the user's own experience, or about their years with a named
// skill (Huntress's "How many years of experience do you have working with
// Ruby on Rails?").
func ownSearched(q match.Question) bool {
	t := match.Sanitize(q.Text, q.Options)
	return match.OwnQuestion(t) || match.OwnYearsQuestion(t, q.Options)
}

// near is match.Facts.KnowledgeNear, with semantic retrieval on: for each
// question about the user's own experience, the snippet sections the knowledge
// base's semantic stage ranks near it, which may mention it in other words, so
// it is never No for want of a word (F-10). A question costs one search, and
// one embedding of its text when that stage runs.
func (b *Binding) near(ctx context.Context, qs []match.Question) map[string][]string {
	out := map[string][]string{}
	for _, q := range qs {
		t := match.Sanitize(q.Text, q.Options)
		if _, done := out[t]; done || !ownSearched(q) {
			continue
		}
		res, err := b.d.Evidence.Search(ctx, kb.Query{Query: t, Kinds: []string{kb.KindSnippet}, K: evidencePerQuestion})
		if err != nil {
			b.d.Log.Warn("bind: knowledge search failed", "err", err)
		}
		out[t] = nil
		for _, f := range res.Facts {
			if f.Stage == kb.StageSemantic && f.SourceKind == kb.KindSnippet {
				out[t] = append(out[t], f.SourceRef)
			}
		}
	}
	return out
}
