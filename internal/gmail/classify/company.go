package classify

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// cutAtRole keeps the leading words of a company run up to the first role word.
func cutAtRole(captured string) string {
	words := strings.Fields(captured)
	for i, w := range words {
		if roleWordRE.MatchString(strings.Trim(w, ".,;:!?")) {
			words = words[:i]
			break
		}
	}
	return strings.Join(words, " ")
}

// stripLeadingTag removes a leading administrative or urgency bracket tag so no
// subject strategy can mint it as the employer.
func stripLeadingTag(subject string) string { return adminTagRE.ReplaceAllString(subject, "") }

func looksLikePerson(name string) bool {
	if personLastFirst.MatchString(name) {
		return true
	}
	if !personRE.MatchString(name) {
		return false
	}
	toks := strings.Fields(name)
	return !nonNameTailRE.MatchString(toks[len(toks)-1])
}

var asciiLetterRE = lazyregexp.New(`[A-Za-z]`)

// stripDeptAffixes peels recruiting-desk labels off both ends. A value that is
// only a desk label is returned untouched; isGenericCompany rejects it so
// resolution falls through to the next source.
func stripDeptAffixes(value string) string {
	out := strings.TrimSpace(value)
	for {
		next := out
		if m := deskPrefixRE.FindString(next); m != "" {
			residue := next[len(m):]
			if len(residue) > 0 && (residue[0] >= 'A' && residue[0] <= 'Z' || residue[0] >= '0' && residue[0] <= '9') {
				next = residue
			}
		}
		stripped := strings.TrimSpace(deskSuffixRE.ReplaceAllString(next, ""))
		if !danglingRE.MatchString(stripped) {
			next = stripped
		}
		if next == out || !asciiLetterRE.MatchString(next) {
			break
		}
		out = next
	}
	return out
}

var jobApplicationRE = lazyregexp.New(`(?i)\bjob application\b`)

// isGeneric reports a value that is not an employer name.
func (t *Taxonomy) isGeneric(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" || vendorNameRE.MatchString(v) {
		return true
	}
	if v[0] >= 'a' && v[0] <= 'z' && !lowerCamelRE.MatchString(v) && !shortBrandRE.MatchString(v) && !lowerDomainRE.MatchString(v) {
		return true
	}
	switch {
	case applicantVerbRE.MatchString(v):
		return true
	case jobApplicationRE.MatchString(v) && !strings.HasPrefix(strings.ToLower(v), "job application"):
		return true
	case len(strings.Fields(v)) > 6, unattendedRE.MatchString(v), positionAtRE.MatchString(v),
		roleNounRE.MatchString(v), prepFragmentRE.MatchString(v), connectorRE.MatchString(v),
		theFragmentRE.MatchString(v), theRoleRE.MatchString(v), roleAtCompanyRE.MatchString(v),
		deskOnlyRE.MatchString(v), mailboxWordRE.MatchString(v), adminPhraseRE.MatchString(v), titledPersonRE.MatchString(v):
		return true
	}
	for _, re := range liveGenericRes {
		if re.MatchString(v) {
			return true
		}
	}
	lower := strings.ToLower(v)
	return t.OpaqueDisplay[lower] || t.GenericDisplay[lower]
}

var (
	theLowerRE          = lazyregexp.New(`^[Tt]he\s+[a-z]`)
	leadingTheRE        = lazyregexp.New(`^[Tt]he\s+`)
	unattendedMailboxRE = lazyregexp.New(`(?i)^(?:an?\s+)?unattended mailbox\b.*$`)
)

// cleanCompany strips mailbox tokens, legal and desk suffixes and region tags.
func cleanCompany(value string) string {
	s := value
	if theLowerRE.MatchString(s) {
		s = leadingTheRE.ReplaceAllString(s, "")
	}
	s = unattendedMailboxRE.ReplaceAllString(s, "")
	s = noReplyTailRE.ReplaceAllString(s, "")
	s = strings.TrimSpace(trailPunctRE.ReplaceAllString(s, ""))
	s = stripDeptAffixes(s)
	s = strings.TrimSpace(teamSuffixRE.ReplaceAllString(s, ""))
	if m := regionGluedRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

func trimToCapitalized(captured string) string {
	toks := strings.Fields(captured)
	for len(toks) > 1 {
		last := toks[len(toks)-1]
		if last[0] >= 'A' && last[0] <= 'Z' || last[0] >= '0' && last[0] <= '9' {
			break
		}
		toks = toks[:len(toks)-1]
	}
	return strings.Join(toks, " ")
}

func trimTrail(v string) string {
	for _, re := range []*lazyregexp.Regexp{trailProseRE, trailClauseRE, trailPronounRE, trailConnectorRE, trailAndForRE, trailEarlyRE} {
		v = re.ReplaceAllString(v, "")
	}
	v = trailingNoiseRE.ReplaceAllString(v, "")
	v = dashNoiseRE.ReplaceAllString(v, "")
	v = markRE.ReplaceAllString(v, "")
	return strings.TrimSpace(trailPunctRE.ReplaceAllString(v, ""))
}

func pickCleaned(captured string) string {
	cand := captured
	if m := personAtCompany.FindStringSubmatch(captured); m != nil {
		cand = m[1]
	}
	return trimTrail(cleanCompany(trimToCapitalized(cand)))
}

func cleanBodyCapture(captured string) string {
	cand := captured
	if m := personAtCompany.FindStringSubmatch(captured); m != nil {
		cand = m[1]
	}
	return trimTrail(cleanCompany(cand))
}

// ---- strategies ----

var kiddeRE = lazyregexp.New(`(?i)^(` + tok + `)\s+[\s\S]+?\b(?:Application\s+Update|Application\s+Confirmation|Application\s+Status|Application\s+Received|Application)\b`)
var roleKeywordRE = lazyregexp.New(`(?i)^\s*(?:senior\s+|staff\s+|principal\s+|junior\s+|lead\s+|associate\s+|entry[- ]level\s+|early\s+career\s+)?(?:software|data|machine\s+learning|ml|ai|backend|frontend|full[- ]?stack|platform|systems?|security|devops|site\s+reliability|infrastructure|product|research|applied|technical|test|qa|sales|business|forward\s+deployed|cloud|application|automation|web|mobile|firmware|hardware|embedded)\s+(?:engineer|scientist|developer|manager|analyst|researcher|intern|architect|consultant|designer|operations|specialist)`)

// companyFromSubjectShape reads "<Company> <Role> Application Update" subjects.
func (t *Taxonomy) companyFromSubjectShape(subject string) *candidate {
	m := kiddeRE.FindStringSubmatchIndex(subject)
	if m == nil {
		return nil
	}
	run := subject[m[2]:m[3]]
	cut := cutAtRole(run)
	if cut == "" {
		return nil
	}
	// The role keyword must follow the company run directly.
	end := m[2] + len(cut)
	if end > len(subject) || !roleKeywordRE.MatchString(subject[end:]) {
		return nil
	}
	cleaned := pickCleaned(cut)
	if cleaned == "" || t.isGeneric(cleaned) {
		return nil
	}
	return &candidate{cleaned, StratSubject, mkSpan("subject", cut, subject)}
}

func (t *Taxonomy) companyFromSubject(subject string) *candidate {
	if subject == "" {
		return nil
	}
	for _, p := range subjectCompanyPatterns {
		m := p.re.FindStringSubmatch(subject)
		if m == nil || m[1] == "" {
			continue
		}
		capture := m[1]
		if p.noRole {
			capture = cutAtRole(capture)
		}
		if capture == "" {
			continue
		}
		cleaned := pickCleaned(capture)
		if cleaned != "" && !t.isGeneric(cleaned) && !strings.ContainsAny(cleaned, "@<>") {
			return &candidate{cleaned, StratSubject, mkSpan("subject", capture, subject)}
		}
	}
	return nil
}

func (t *Taxonomy) companyFromExplicitBody(body string) *candidate {
	if body == "" {
		return nil
	}
	for _, re := range bodyCompanyPatterns {
		if m := re.FindStringSubmatch(body); m != nil && m[1] != "" {
			cleaned := pickCleaned(m[1])
			if cleaned != "" && !t.isGeneric(cleaned) && !strings.ContainsAny(cleaned, "@<>") {
				return &candidate{cleaned, StratBody, mkSpan("body", m[1], body)}
			}
		}
	}
	return nil
}

func (t *Taxonomy) companyFromLegacyBody(text string) *candidate {
	if text == "" {
		return nil
	}
	for _, group := range [][]*lazyregexp.Regexp{legacyBodyPatterns, proseBodyPatterns} {
		for _, re := range group {
			if m := re.FindStringSubmatch(text); m != nil && m[1] != "" {
				cleaned := cleanBodyCapture(m[1])
				if cleaned != "" && !t.isGeneric(cleaned) && !strings.ContainsAny(cleaned, "@<>") {
					return &candidate{cleaned, StratBody, mkSpan("body", m[1], text)}
				}
			}
		}
	}
	return nil
}

// namesEmployer reports whether the subject or body names an employer without
// help from the sender (an assessment vendor's mail names the hiring company).
func (t *Taxonomy) namesEmployer(in eventInput) bool {
	return t.companyFromSubjectShape(in.subject) != nil || t.companyFromSubject(in.subject) != nil ||
		t.companyFromExplicitBody(in.body) != nil || t.companyFromLegacyBody(in.subject+"\n"+in.body) != nil
}

var (
	camelLowUp   = lazyregexp.New(`([a-z0-9])([A-Z])`)
	camelUpRun   = lazyregexp.New(`([A-Z]+)([A-Z][a-z])`)
	workdayDoNot = lazyregexp.New(`(?i)^donotreply[_\s.-]+([A-Za-z0-9.& '-]+?)\s+workday$`)
	workdayAdmin = lazyregexp.New(`(?i)^workday[\s.]+admin[\s.]+(.+)$`)
	workdayLead  = lazyregexp.New(`(?i)^workday\s*[@:.\s-]+\s*(.+)$`)
	workdayLead2 = lazyregexp.New(`(?i)^workday\s+(.+)$`)
	workdayTail  = lazyregexp.New(`(?i)^(.+?)(?:[\s-]+(?:hr|people|talent|recruiting|recruitment))?[\s-]+workday$`)
	noReplyLead  = lazyregexp.New(`(?i)^(?:no[\s-]?reply|noreply|donotreply|do[\s-]?not[\s-]?reply)\s+(.+)$`)
	noReplyHead  = lazyregexp.New(`(?i)^(?:do[\s-]?not[\s-]?reply|donotreply|no[\s-]?reply|noreply)[\s_.-]+`)
	noReplyEnd   = lazyregexp.New(`(?i)[\s_.-]+(?:do[\s-]?not[\s-]?reply|donotreply|no[\s-]?reply|noreply)$`)
	notifTail    = lazyregexp.New(`(?i)[\s_.-]+(?:notifications?|alerts?|admin|updates?)\s*$`)
)

func workdayDisplayTenant(raw string) string {
	name := strings.ReplaceAll(raw, "_", " ")
	name = camelLowUp.ReplaceAllString(name, "$1 $2")
	name = camelUpRun.ReplaceAllString(name, "$1 $2")
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return ""
	}
	n := noReplyHead.ReplaceAllString(name, "")
	n = noReplyEnd.ReplaceAllString(n, "")
	n = strings.TrimSpace(notifTail.ReplaceAllString(n, ""))
	for _, re := range []*lazyregexp.Regexp{workdayDoNot, workdayAdmin, workdayLead, workdayLead2, workdayTail, noReplyLead} {
		if m := re.FindStringSubmatch(n); m != nil && strings.TrimSpace(m[1]) != "" {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func (t *Taxonomy) stripSlugSuffix(slug string) string {
	cur := slug
	for changed := true; changed && len(cur) > 2; {
		changed = false
		for _, suf := range t.TenantSlugSuffixes {
			switch {
			case strings.HasSuffix(cur, "-"+suf) || strings.HasSuffix(cur, "_"+suf):
				cur = cur[:len(cur)-len(suf)-1]
				changed = true
			case strings.HasSuffix(cur, suf) && len(cur) > len(suf)+1:
				cur = cur[:len(cur)-len(suf)]
				changed = true
			}
		}
	}
	return cur
}

var nonSlugRE = lazyregexp.New(`[^a-z0-9]+`)

func slugOf(s string) string {
	return nonSlugRE.ReplaceAllString(strings.ToLower(s), "")
}

var leadingTheFoldRE = lazyregexp.New(`(?i)^The\s+`)

func (t *Taxonomy) companyFromATSName(from Sender, p Policy) *candidate {
	if !p.Trusted || p.TrustedBySignature {
		return nil
	}
	raw := strings.TrimSpace(from.Name)
	lower := strings.ToLower(raw)
	if raw == "" || t.GenericDisplay[lower] || t.OpaqueDisplay[lower] {
		return nil
	}
	var cands []string
	tenant := workdayDisplayTenant(raw)
	if tenant != "" {
		cands = append(cands, tenant)
	}
	stripped := strings.TrimSpace(noReplyTailRE.ReplaceAllString(noReplyLeadRE.ReplaceAllString(raw, ""), ""))
	if stripped != "" {
		suffixed := strings.TrimSpace(atsNameSuffixRE.ReplaceAllString(stripped, ""))
		if tenant != "" || suffixed != strings.ToLower(suffixed) {
			cands = append(cands, suffixed)
		}
	}
	for _, c := range cands {
		if c == "" || t.GenericDisplay[strings.ToLower(c)] || t.isGeneric(c) || strings.ContainsAny(c, "@<>") {
			continue
		}
		cleaned := cleanCompany(c)
		if cleaned == "" {
			continue
		}
		slug := slugOf(cleaned)
		slugs := []string{slug}
		if noThe := slugOf(leadingTheFoldRE.ReplaceAllString(cleaned, "")); noThe != slug {
			slugs = append(slugs, noThe)
		}
		for _, s := range slugs {
			if v, ok := t.TenantOverrides[s]; ok {
				return &candidate{v, StratATSName, mkSpan("sender_name", raw, raw)}
			}
			if v, ok := t.TenantOverrides[t.stripSlugSuffix(s)]; ok {
				return &candidate{v, StratATSName, mkSpan("sender_name", raw, raw)}
			}
		}
		return &candidate{cleaned, StratATSName, mkSpan("sender_name", raw, raw)}
	}
	return nil
}

func (t *Taxonomy) companyFromTenantSlug(from Sender) *candidate {
	email := strings.ToLower(strings.TrimSpace(from.Email))
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || !domainIn(domain, t.MultiTenantATS) {
		return nil
	}
	local, _, _ = strings.Cut(local, "+")
	if local == "" || t.GenericLocalParts[local] {
		return nil
	}
	if v, ok := t.TenantOverrides[local]; ok {
		return &candidate{v, StratTenantSlug, mkSpan("sender_local_part", local, email)}
	}
	stripped := t.stripSlugSuffix(local)
	if stripped == "" || t.GenericLocalParts[stripped] {
		return nil
	}
	if v, ok := t.TenantOverrides[stripped]; ok {
		return &candidate{v, StratTenantSlug, mkSpan("sender_local_part", local, email)}
	}
	name := titleCase(strings.NewReplacer("-", " ", "_", " ").Replace(stripped))
	if name == "" || t.isGeneric(name) {
		return nil
	}
	return &candidate{name, StratTenantSlug, mkSpan("sender_local_part", local, email)}
}

var workdaySubRE = lazyregexp.New(`(?i)^(?:workday\.)?([a-z0-9-]+?)(?:\.workday)?\.(?:com|net|org)$`)

var tenantTLDRE = lazyregexp.New(`\.(?:com|net|org|io|co)$`)

func (t *Taxonomy) companyFromWorkdayDomain(from Sender) *candidate {
	domain := emailDomain(from.Email)
	if domain == "" || !strings.Contains(domain, "workday") || t.MultiTenantATS[domain] {
		return nil
	}
	tenant := strings.TrimPrefix(domain, "workday.")
	tenant = strings.TrimSuffix(strings.TrimSuffix(tenant, ".workday.com"), ".myworkday.com")
	tenant = tenantTLDRE.ReplaceAllString(tenant, "")
	if tenant == domain {
		if m := workdaySubRE.FindStringSubmatch(domain); m != nil {
			tenant = m[1]
		}
	}
	for _, raw := range []string{tenant, emailLocal(from.Email)} {
		if raw == "" {
			continue
		}
		stripped := t.stripSlugSuffix(raw)
		if stripped == "" || t.GenericLocalParts[stripped] {
			continue
		}
		if v, ok := t.TenantOverrides[stripped]; ok {
			return &candidate{v, StratWorkdayTenant, mkSpan("sender_domain", raw, domain)}
		}
		name := titleCase(strings.NewReplacer("-", " ", "_", " ").Replace(stripped))
		if name != "" && !t.isGeneric(name) {
			return &candidate{name, StratWorkdayTenant, mkSpan("sender_domain", raw, domain)}
		}
	}
	return nil
}

// domainBlockedRoots are sender roots that name a vendor, never the employer.
var domainBlockedRoots = map[string]bool{
	"gmail": true, "google": true, "greenhouse-mail": true, "hire": true, "lever": true, "ashbyhq": true,
	"myworkday": true, "smartrecruiters": true, "icims": true, "workday": true, "jobvite": true, "bamboohr": true,
	"workablemail": true, "talentbankonline": true, "pageuppeople": true, "hackerrankforwork": true,
	"codesignal": true, "hackerrank": true, "codility": true, "litmus": true, "coderbyte": true,
}

func (t *Taxonomy) isVendorBrand(name string) bool {
	slug := slugOf(name)
	if slug == "" {
		return false
	}
	if domainBlockedRoots[slug] {
		return true
	}
	for host := range t.VendorATS {
		if strings.SplitN(host, ".", 2)[0] == slug {
			return true
		}
	}
	return false
}

func (t *Taxonomy) companyFromDisplayName(from Sender, p Policy) *candidate {
	raw := strings.TrimSpace(from.Name)
	lower := strings.ToLower(raw)
	switch {
	case raw == "", len(raw) > 70, strings.ContainsAny(raw, "@<>"), t.GenericDisplay[lower], t.OpaqueDisplay[lower]:
		return nil
	case noReplyLeadRE.MatchString(raw), p.Webmail, p.SchedulingTool, looksLikePerson(raw):
		return nil
	}
	stripped := strings.TrimSpace(entitySuffixRE.ReplaceAllString(atsNameSuffixRE.ReplaceAllString(raw, ""), ""))
	if stripped == "" || t.GenericDisplay[strings.ToLower(stripped)] || t.isGeneric(stripped) {
		return nil
	}
	cleaned := cleanCompany(stripped)
	if cleaned == "" || t.isGeneric(cleaned) || t.isVendorBrand(cleaned) {
		return nil
	}
	for _, w := range strings.Fields(cleaned) {
		if c := w[0]; !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '&') {
			return nil
		}
	}
	return &candidate{cleaned, StratDisplayName, mkSpan("sender_name", raw, raw)}
}

var geoPrefixes = map[string]bool{
	"us": true, "uk": true, "eu": true, "mail": true, "notify": true, "system": true, "email": true, "do-not-reply": true,
	"noreply": true, "no-reply": true, "recruiting": true, "talent": true, "careers": true, "jobs": true, "send": true,
	"inbound": true, "outbound": true, "smtp": true, "msg": true, "reply": true, "hr": true, "career": true,
	"careeralerts": true, "recruitment": true, "recruit": true, "hire": true, "hiring": true, "alert": true, "alerts": true,
	"notification": true, "notifications": true, "apply": true, "application": true, "applications": true,
	"updates": true, "portal": true, "candidate": true, "candidates": true,
}

func registrableRoot(domain string) (root, registrable string) {
	parts := strings.FieldsFunc(domain, func(r rune) bool { return r == '.' })
	if len(parts) <= 2 {
		if len(parts) == 0 {
			return "", domain
		}
		return parts[0], domain
	}
	labels := parts[:len(parts)-1]
	start := 0
	for i, l := range labels {
		if !geoPrefixes[l] {
			start = i
			break
		}
	}
	return labels[start], strings.Join(parts[start:], ".")
}

func (t *Taxonomy) companyFromDomain(from Sender, p Policy) *candidate {
	if p.VendorATS || p.Webmail || p.SchedulingTool {
		return nil
	}
	domain := emailDomain(from.Email)
	if domain == "" {
		return nil
	}
	root, registrable := registrableRoot(domain)
	if root == "" || domainBlockedRoots[root] {
		return nil
	}
	for _, set := range []map[string]bool{t.TrustedATS, t.VendorATS, t.SchedulingTools, t.MultiTenantATS} {
		if domainIn(registrable, set) {
			return nil
		}
	}
	for host := range t.VendorATS {
		if strings.SplitN(host, ".", 2)[0] == root {
			return nil
		}
	}
	if v, ok := t.TenantOverrides[root]; ok {
		return &candidate{v, StratSenderDomain, mkSpan("sender_domain", root, domain)}
	}
	name := titleCase(strings.NewReplacer("-", " ", "_", " ").Replace(root))
	if name == "" || t.isGeneric(name) {
		return nil
	}
	return &candidate{name, StratSenderDomain, mkSpan("sender_domain", root, domain)}
}

func strategyConfidence(s string) float64 {
	switch s {
	case StratSubject:
		return 0.95
	case StratATSName:
		return 0.9
	case StratBody:
		return 0.8
	case StratTenantSlug, StratWorkdayTenant:
		return 0.85
	case StratDisplayName:
		return 0.7
	case StratSenderDomain, StratCoRecipient:
		return 0.5
	}
	return 0
}

// resolveCompany picks the employer from the first source that yields a
// non-generic name. When the display name looks like a person or the mailbox is
// webmail, the body's explicit phrases come before the ATS sender name. Two
// strong sources that name different employers mark a conflict.
func (t *Taxonomy) resolveCompany(subject, body string, from Sender, p Policy) CompanyEvidence {
	subject = stripLeadingTag(subject)
	personal := p.Webmail || looksLikePerson(strings.TrimSpace(from.Name))
	steps := map[string]func() *candidate{
		"shape":   func() *candidate { return t.companyFromSubjectShape(subject) },
		"subject": func() *candidate { return t.companyFromSubject(subject) },
		"atsname": func() *candidate { return t.companyFromATSName(from, p) },
		"body":    func() *candidate { return t.companyFromExplicitBody(body) },
		"slug":    func() *candidate { return t.companyFromTenantSlug(from) },
		"workday": func() *candidate { return t.companyFromWorkdayDomain(from) },
		"legacy":  func() *candidate { return t.companyFromLegacyBody(subject + "\n" + body) },
		"display": func() *candidate { return t.companyFromDisplayName(from, p) },
		"domain":  func() *candidate { return t.companyFromDomain(from, p) },
	}
	order := []string{"shape", "subject", "atsname", "body", "slug", "workday", "legacy", "display", "domain"}
	if personal {
		order = []string{"shape", "subject", "body", "legacy", "atsname", "slug", "workday", "display", "domain"}
	}
	var found []*candidate
	for _, k := range order {
		if c := steps[k](); c != nil && !t.isGeneric(c.value) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return CompanyEvidence{Value: UnknownCompany, Strategy: StratUnknown}
	}
	first := found[0]
	var groups [][]string
	seen := map[string]bool{}
	for _, c := range found {
		if strategyConfidence(c.strategy) < 0.85 || seen[c.value] {
			continue
		}
		seen[c.value] = true
		placed := false
		for i, g := range groups {
			if companyEquivalent(g[0], c.value) {
				groups[i] = append(groups[i], c.value)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []string{c.value})
		}
	}
	ev := CompanyEvidence{Value: first.value, Strategy: first.strategy, Confidence: strategyConfidence(first.strategy), Span: first.span}
	if len(groups) > 1 {
		ev.Conflict = true
		for _, g := range groups {
			ev.Conflicts = append(ev.Conflicts, g...)
		}
	}
	return ev
}

var shortKeyRE = lazyregexp.New(`^[a-z0-9&]{2,5}$`)

// companyEquivalent compares two employer names by canonical key, allowing a
// prefix relation and an initialism.
func companyEquivalent(a, b string) bool {
	ka, kb := CanonicalCompany(a), CanonicalCompany(b)
	if ka == "" || kb == "" || ka == kb {
		return true
	}
	if strings.HasPrefix(ka, kb+" ") || strings.HasPrefix(kb, ka+" ") {
		return true
	}
	short, long := ka, kb
	if len(kb) < len(ka) {
		short, long = kb, ka
	}
	if !shortKeyRE.MatchString(short) {
		return false
	}
	var initials strings.Builder
	for _, w := range strings.Fields(long) {
		switch w {
		case "of", "and", "the", "for":
		default:
			initials.WriteByte(w[0])
		}
	}
	return initials.String() == short
}

var canonicalTrailing = lazyregexp.New(`\s+(?:talent acquisition|talent|recruiting|recruitment|careers?|hr|notifications?|alerts?|team|hiring|opportunit(?:y|ies)|family|inc|llc|ltd|corp|co|gmbh|plc|company|pbc|llp|lp|pte|pty|sarl|srl|corporation|incorporated|limited|holdings?|international|global|worldwide|group|technolog(?:y|ies)|systems|solutions|software|labs?|laboratories|studios?|industries|enterprises|ventures|partners)$`)

var (
	sentenceBreakRE = lazyregexp.New(`\.\s+`)
	parentheticalRE = lazyregexp.New(`\s*\([^)]*\)`)
	spaceRunRE      = lazyregexp.New(`\s+`)
	nonKeyRE        = lazyregexp.New(`[^a-z0-9&+]+`)
)

// CanonicalCompany is the comparison key of an employer name: lowercase, cut at
// the first sentence boundary, without parenthetical qualifiers, punctuation or
// trailing legal and recruiting words. It is not for display.
func CanonicalCompany(value string) string {
	lowered := strings.ToLower(strings.TrimSpace(value))
	if lowered == "" || lowered == "unknown" || lowered == "unknown company" {
		return ""
	}
	first := sentenceBreakRE.Split(lowered, 2)[0]
	first = parentheticalRE.ReplaceAllString(first, " ")
	key := strings.TrimSpace(spaceRunRE.ReplaceAllString(nonKeyRE.ReplaceAllString(first, " "), " "))
	for {
		next := canonicalTrailing.ReplaceAllString(key, "")
		if next == key {
			break
		}
		key = next
	}
	if rest := strings.TrimPrefix(key, "the "); rest != key && rest != "" {
		key = strings.TrimSpace(rest)
	}
	return key
}
