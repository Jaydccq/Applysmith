package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// Candidate is one lane row the list may run.
type Candidate struct {
	URL, Company, Role string
	TrackerID          int64
	ReportNumber       *int64
}

// SelectOptions is the request a list is selected for.
type SelectOptions struct {
	// Submit is true for a list that presses Submit: unreadable duplicate
	// evidence then stops the selection instead of reading as none.
	Submit bool
	// RetryToday attempts rows whose last run today stopped on a judgement. It
	// never lifts a duplicate fact, the skip list, a challenge, a credential
	// hold, a user-data stop or a fact about the posting.
	RetryToday bool
	// Named restricts the list to the rows of NamedCompanies; a request that
	// names companies never runs the list's top rows. Rows matching
	// NamedCompanies are flagged Named whether or not the restriction is set.
	Named          bool
	NamedCompanies []string
	Now            time.Time
	// Build is the identity of the running build; a judgement stop recorded by
	// the same clean build keeps holding.
	Build string
	// SkipList is the resolved batch skip list (gates.ResolveSkipList).
	SkipList []string
	// Limit bounds the runnable rows; zero reads as 5. A named request ignores it.
	Limit int
}

// Skipped is a row the selection left out, with the rule that did and who has
// the next step (a wire.Owner* value, empty when nobody does). Closed says the
// posting itself said it is closed, so nothing offers it again.
type Skipped struct {
	Company, Role, URL string
	Rule, Reason       string
	Owner              string
	Closed             bool
}

// SelectResult is the runnable rows in list order and everything left out.
type SelectResult struct {
	Rows    []ListRow
	Skipped []Skipped
	// Warnings are bookkeeping failures that changed nothing about the rows.
	Warnings []string
}

// Skip rules, named for what they read. The order of the constants is the
// order they are applied in.
const (
	RuleApplied     = "applied"      // a submissions row or applied tracker evidence on the URL
	RuleCompanyRole = "company-role" // the same candidacy under another URL
	RuleSharedLink  = "shared-link"  // one URL carried by evaluations of different companies
	RuleInterrupted = "interrupted"  // a batch began the row and never ended it
	RuleToday       = "today"        // the last run of the day is a verdict
	RuleHold        = "hold"         // terminal posting, challenge or credential cooldown
	RuleSkipList    = "skip-list"    // the company is left to the user
	RuleStaleLink   = "stale-link"   // the URL's report is for another role
	RulePrecheck    = "precheck"     // a mirror or liveness check ended the row
	defaultLimit    = 5
	mirrorResolvers = 4
)

// ErrEvidenceUnreadable stops a submitting selection whose duplicate evidence
// could not be read: missing evidence is not read as no evidence.
var ErrEvidenceUnreadable = errors.New("apply list: duplicate evidence is unreadable")

// Selector decides which lane rows a list runs. Resolve, Probe and Record are
// optional: without them mirrors run on their own address, nothing is probed
// and no precheck record is written.
type Selector struct {
	Evidence Evidence
	Resolve  ResolveFunc
	Probe    Prober
	Record   PrecheckRecorder
}

// NewSelector builds a selector over the evidence.
func NewSelector(ev Evidence) *Selector { return &Selector{Evidence: ev} }

// Select picks the rows to run. Rows of one posting scanned from several
// mirror sites are grouped first; a group is skipped by the first rule that
// holds for any of its URLs.
func (s *Selector) Select(ctx context.Context, cands []Candidate, opts SelectOptions) (SelectResult, error) {
	opts = withDefaults(opts)
	sel, err := s.selection(ctx, opts)
	if err != nil {
		return SelectResult{}, err
	}
	var rows []Candidate
	seen := map[string]bool{}
	for _, c := range cands {
		k := postingKey(c.URL)
		if strings.TrimSpace(c.URL) == "" || seen[k] || (opts.Named && !NamedCompany(opts.NamedCompanies, c.Company)) {
			continue
		}
		seen[k] = true
		rows = append(rows, c)
	}
	groups := GroupMirrors(rows)
	limit := opts.Limit
	if opts.Named {
		limit = len(groups)
	}
	var pf *Prefetch
	if s.Resolve != nil {
		pf = StartPrefetch(ctx, s.aheadOf(groups, opts, limit), s.Resolve, mirrorResolvers)
		defer pf.Stop()
	}
	var res SelectResult
	for _, g := range groups {
		if len(res.Rows) >= limit {
			break
		}
		named := NamedCompany(opts.NamedCompanies, g.Primary.Company)
		sk := sel.decide(g, named)
		row := ListRow{URL: g.Primary.URL, Company: g.Primary.Company, Role: g.Primary.Role, Named: named, Mirrors: len(g.Mirrors)}
		if sk == nil && pf != nil {
			if sk, err = s.prepare(ctx, sel, pf, g, named, &row, &res); err != nil {
				return SelectResult{}, err
			}
		}
		if sk != nil {
			res.Skipped = append(res.Skipped, *sk)
			continue
		}
		res.Rows = append(res.Rows, row)
	}
	s.probeRows(ctx, &res, opts)
	return res, ctx.Err()
}

// Check decides the rows of the Apply Next page over one read of the
// evidence: for each candidate, the zero-cost rule that leaves it out of a
// list, or nil when a list would run it.
func (s *Selector) Check(ctx context.Context, cands []Candidate, opts SelectOptions) ([]*Skipped, error) {
	opts = withDefaults(opts)
	sel, err := s.selection(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*Skipped, len(cands))
	for i, c := range cands {
		out[i] = sel.decide(MirrorGroup{Primary: c}, NamedCompany(opts.NamedCompanies, c.Company))
	}
	return out, nil
}

func withDefaults(o SelectOptions) SelectOptions {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Limit <= 0 {
		o.Limit = defaultLimit
	}
	return o
}

func (s *Selector) selection(ctx context.Context, opts SelectOptions) (*selection, error) {
	f, err := s.Evidence.Facts(ctx, opts.Now)
	if err != nil {
		return nil, err
	}
	if opts.Submit && len(f.Unreadable) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrEvidenceUnreadable, strings.Join(f.Unreadable, ", "))
	}
	return newSelection(f, opts), nil
}

// aheadOf lists the mirror rows whose resolutions start before the walk
// reaches them: the first limit mirror groups not on the skip list.
func (s *Selector) aheadOf(groups []MirrorGroup, opts SelectOptions, limit int) []Candidate {
	var out []Candidate
	for _, g := range groups {
		if len(out) >= limit {
			break
		}
		if IsAggregatorURL(g.Primary.URL) && !skipListed(opts.SkipList, g.Primary.Company) {
			out = append(out, g.Primary)
		}
	}
	return out
}

func skipListed(list []string, company string) bool {
	return gates.SkipCompany(gates.SkipInput{Company: company, Entries: list}).Entry != ""
}

// postingKey is the identity key a URL is compared by: tracking parameters and
// the apply path of one posting do not make a second posting.
func postingKey(raw string) string {
	if k, err := identity.URLKey(raw); err == nil && k != "" {
		return k
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// sameCompany is the company word-set match of same-candidacy-strict.
func sameCompany(a, b string) bool {
	return identity.SameCandidacyStrict(identity.Posting{Company: a, Role: "x"}, identity.Posting{Company: b, Role: "x"})
}

// NamedCompany reports whether a company is one the user named: the names are
// matched as whole words or phrases, ignoring case, in either direction (the
// request may carry the company's short name or the company a longer one). A
// name with non-ASCII characters is matched as a substring, because word
// boundaries of other scripts are not spaces.
func NamedCompany(names []string, company string) bool {
	company = strings.TrimSpace(company)
	if company == "" {
		return false
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" && (occursAsWords(n, company) || occursAsWords(company, n)) {
			return true
		}
	}
	return false
}

func occursAsWords(needle, hay string) bool {
	needle, hay = strings.ToLower(needle), strings.ToLower(hay)
	if !isASCII(needle) {
		return strings.Contains(hay, needle)
	}
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(needle)
		if (start == 0 || !wordRune(hay, start-1, true)) && (end == len(hay) || !wordRune(hay, end, false)) {
			return true
		}
		from = start + 1
	}
}

// wordRune reports whether the rune before index i (back) or at it is a letter
// or digit.
func wordRune(s string, i int, back bool) bool {
	var r rune
	if back {
		r, _ = utf8.DecodeLastRuneInString(s[:i+1])
	} else {
		r, _ = utf8.DecodeRuneInString(s[i:])
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

var (
	mirrorIDRE  = lazyregexp.New(`\d{9,}`)
	mirrorReqRE = lazyregexp.New(`_([A-Za-z]{0,3}[-_]?\d{3,})(?:-\d+)?(?:[/?#]|$)`)
)

// atsHostSuffixes are applicant-tracking systems: the employer's own
// application lives there.
var atsHostSuffixes = []string{
	"myworkdayjobs.com", "greenhouse.io", "lever.co", "ashbyhq.com", "smartrecruiters.com", "icims.com", "workable.com",
	"oraclecloud.com", "avature.net", "successfactors.com", "ukg.net", "jobvite.com", "bamboohr.com",
}

// MirrorGroup is one posting: the row it runs on and the other rows of the
// same posting in lane order.
type MirrorGroup struct {
	Primary Candidate
	Mirrors []Candidate
}

type mirrorRow struct {
	c     Candidate
	host  string
	canon string
	ids   map[string]bool
	name  string
}

// postingIDs are the applicant-tracking job ids a URL carries: a number of
// nine or more digits, or a Workday requisition without its copy suffix.
func postingIDs(raw string) map[string]bool {
	ids := map[string]bool{}
	canon, err := identity.CanonicalURL(raw)
	if err != nil {
		canon = raw
	}
	for _, id := range mirrorIDRE.FindAllString(canon, -1) {
		ids[id] = true
	}
	if anyHost(hostOf(raw), []string{"myworkdayjobs.com"}) {
		if m := mirrorReqRE.FindStringSubmatch(canon); m != nil {
			ids["workday:"+strings.ToUpper(m[1])] = true
		}
	}
	return ids
}

func (a mirrorRow) sharesID(b mirrorRow) bool {
	for id := range a.ids {
		if b.ids[id] {
			return true
		}
	}
	return false
}

// differ reports whether the URLs of two same-title rows say they are two
// postings: two listings on one host, or two different job ids.
func (a mirrorRow) differ(b mirrorRow) bool {
	if a.sharesID(b) {
		return false
	}
	if a.host == b.host && a.canon != b.canon {
		return true
	}
	return len(a.ids) > 0 && len(b.ids) > 0
}

// rank orders the rows of one posting for running: a known ATS first, then a
// host that names the company, then any other site, an aggregator last.
func (m mirrorRow) rank() int {
	switch {
	case anyHost(m.host, atsHostSuffixes):
		return 3
	case IsAggregatorURL(m.c.URL):
		return 0
	}
	if keys := identity.CompanyKeys(m.c.Company); len(keys) > 0 {
		if k := alnum(keys[0]); len(k) >= 3 && strings.Contains(alnum(m.host), k) {
			return 2
		}
	}
	return 1
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GroupMirrors groups rows (in lane order) into postings. Two rows are one
// posting when their companies match and they share an ATS job id, or have the
// same exact company and title with nothing in the URLs saying they differ.
// Only exact titles merge: a location, level or year in a title is another
// job. Both rules lean toward splitting. Rows without a URL stay alone, and
// each group sits at its first row's position.
func GroupMirrors(rows []Candidate) []MirrorGroup {
	ms := make([]mirrorRow, len(rows))
	for i, c := range rows {
		ms[i] = mirrorRow{c: c, host: hostOf(c.URL), canon: c.URL, ids: map[string]bool{}}
		if strings.TrimSpace(c.URL) != "" {
			if canon, err := identity.CanonicalURL(c.URL); err == nil {
				ms[i].canon = canon
			}
			ms[i].ids = postingIDs(c.URL)
			ms[i].name = identity.Posting{Company: c.Company, Role: c.Role}.IdentityKey()
		}
	}
	root := make([]int, len(ms))
	for i := range root {
		root[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if root[i] != i {
			root[i] = find(root[i])
		}
		return root[i]
	}
	union := func(a, b int) {
		if ra, rb := find(a), find(b); ra != rb {
			root[max(ra, rb)] = min(ra, rb)
		}
	}
	members := func(r int) []int {
		var out []int
		for i := range ms {
			if find(i) == r {
				out = append(out, i)
			}
		}
		return out
	}
	// A shared id is the strongest evidence, and it lets a same-host pair (a
	// Workday copy suffix) through before the title rule reads hosts.
	for i := range ms {
		for j := i + 1; j < len(ms); j++ {
			if ms[i].c.URL != "" && ms[j].c.URL != "" && ms[i].sharesID(ms[j]) && sameCompany(ms[i].c.Company, ms[j].c.Company) {
				union(i, j)
			}
		}
	}
	for i := range ms {
		for j := i + 1; j < len(ms); j++ {
			if ms[i].c.URL == "" || ms[j].c.URL == "" || ms[i].name == "" || ms[i].name != ms[j].name || find(i) == find(j) {
				continue
			}
			if groupsDiffer(ms, members(find(i)), members(find(j))) {
				continue
			}
			union(i, j)
		}
	}
	var out []MirrorGroup
	for i := range ms {
		if find(i) != i {
			continue
		}
		idx := members(i)
		best := idx[0]
		for _, k := range idx[1:] {
			if ms[k].rank() > ms[best].rank() {
				best = k
			}
		}
		g := MirrorGroup{Primary: ms[best].c}
		for _, k := range idx {
			if k != best {
				g.Mirrors = append(g.Mirrors, ms[k].c)
			}
		}
		out = append(out, g)
	}
	return out
}

func groupsDiffer(ms []mirrorRow, left, right []int) bool {
	for _, x := range left {
		for _, y := range right {
			if ms[x].differ(ms[y]) {
				return true
			}
		}
	}
	return false
}

// Owners of a skipped row, from the wire vocabulary.
const (
	ownerUser   = wire.OwnerUser
	ownerPost   = wire.OwnerPosting
	ownerEnv    = wire.OwnerEnvironment
	ownerDefect = wire.OwnerDefect
)

// selection is the evidence of one selection, indexed for the rules.
type selection struct {
	opts  SelectOptions
	f     Facts
	holds *holdSet
	// ledger holds the submissions that count as an application (everything
	// but a failed row), by identity key and by address key.
	ledger map[string][]LedgerFact
	// applied is the identity keys, aliases and report addresses of tracker
	// rows in an applied status.
	applied map[string]bool
	// sources are the tracker rows that are on their own evidence of an
	// application: an applied status, or a report the chain already submitted.
	sources []trackerSource
	// submissions are the ledger rows that count as an application, with the
	// company and role (the report's when the row stored only a host) reduced
	// once for the same-candidacy test.
	submissions []submission
	reports     map[int64]ReportFact
	// byURL are the reports carrying each address key.
	byURL map[string][]ReportFact
	// companies are the distinct companies whose tracker rows' reports carry
	// each address key.
	companies   map[string][]string
	today       map[string]RunFact
	interrupted map[string]time.Time
	midnight    time.Time
	// rowOf is the tracker row of each identity key, for a candidate that
	// does not name its row.
	rowOf map[string]int64
}

type trackerSource struct {
	row     TrackerFact
	applied bool
	form    identity.Strict
}

type submission struct {
	l    LedgerFact
	form identity.Strict
}

func newSelection(f Facts, opts SelectOptions) *selection {
	s := &selection{opts: opts, f: f, holds: newHoldSet(f.Runs), ledger: map[string][]LedgerFact{}, applied: map[string]bool{},
		reports: map[int64]ReportFact{}, byURL: map[string][]ReportFact{}, companies: map[string][]string{},
		today: map[string]RunFact{}, interrupted: map[string]time.Time{}, rowOf: map[string]int64{}}
	y, m, d := opts.Now.Date()
	s.midnight = time.Date(y, m, d, 0, 0, 0, 0, opts.Now.Location())
	submitted := map[int64]bool{}
	for _, l := range f.Ledger {
		if l.State == gates.StateFailed {
			continue
		}
		for _, k := range []string{l.IdentityKey, postingKey(l.URL)} {
			if k != "" {
				s.ledger[k] = append(s.ledger[k], l)
			}
		}
		if l.Report > 0 {
			submitted[l.Report] = true
		}
	}
	for _, r := range f.Reports {
		s.reports[r.Number] = r
		if k := postingKey(r.URL); r.URL != "" {
			s.byURL[k] = append(s.byURL[k], r)
		}
	}
	for _, l := range f.Ledger {
		if l.State == gates.StateFailed {
			continue
		}
		company, role := l.Company, l.Role
		if company == "" || role == "" {
			r, ok := s.reports[l.Report]
			if !ok || l.Report <= 0 {
				continue
			}
			company, role = r.Company, r.Role
		}
		s.submissions = append(s.submissions, submission{l, identity.StrictOf(identity.Posting{Company: company, Role: role})})
	}
	for _, r := range f.Runs {
		if r.Started.Before(s.midnight) {
			continue
		}
		for _, k := range r.keys() {
			s.today[k] = r
		}
		if r.Kind == OutcomeSubmitted && r.Report > 0 {
			submitted[r.Report] = true
		}
	}
	for _, t := range f.Tracker {
		for _, k := range append([]string{t.Key, postingKey(t.PostingURL)}, t.Aliases...) {
			if _, ok := s.rowOf[k]; k != "" && !ok {
				s.rowOf[k] = t.ID
			}
		}
		isApplied := gates.AppliedOrLater(t.Status)
		if isApplied {
			for _, k := range append([]string{t.Key, postingKey(t.PostingURL), postingKey(t.ReportURL)}, t.Aliases...) {
				if k != "" {
					s.applied[k] = true
				}
			}
		}
		if isApplied || (t.Report > 0 && submitted[t.Report]) {
			s.sources = append(s.sources, trackerSource{t, isApplied, identity.StrictOf(identity.Posting{Company: t.Company, Role: t.Role})})
		}
		if t.ReportURL != "" {
			k := postingKey(t.ReportURL)
			if !hasCompany(s.companies[k], t.Company) {
				s.companies[k] = append(s.companies[k], t.Company)
			}
		}
	}
	for _, j := range f.Interrupted {
		if cur, ok := s.interrupted[j.Key]; !ok || j.StartedAt.After(cur) {
			s.interrupted[j.Key] = j.StartedAt
		}
	}
	return s
}

func hasCompany(list []string, c string) bool {
	for _, x := range list {
		if sameCompany(x, c) {
			return true
		}
	}
	return false
}

func keysOf(g MirrorGroup) []string {
	out := []string{postingKey(g.Primary.URL)}
	for _, m := range g.Mirrors {
		out = append(out, postingKey(m.URL))
	}
	return out
}

func skip(c Candidate, rule, owner, format string, a ...any) *Skipped {
	return &Skipped{Company: c.Company, Role: c.Role, URL: c.URL, Rule: rule, Reason: fmt.Sprintf(format, a...), Owner: owner}
}

// decide applies the skip rules, in order, to one posting (a group). It
// returns nil when the row may run. A duplicate fact on any URL of the group
// is a fact about the posting.
//
// Rules 1 to 4 (a duplicate, the same candidacy under another URL, a link
// shared by different companies, an interrupted batch) and the skip list hold
// whether or not the user named the company or asked to retry today.
func (s *selection) decide(g MirrorGroup, named bool) *Skipped {
	c := g.Primary
	keys := keysOf(g)
	for _, k := range keys {
		if len(s.ledger[k]) > 0 {
			return skip(c, RuleApplied, "", "already submitted")
		}
		if s.applied[k] {
			return skip(c, RuleApplied, "", "the tracker already records an application for this posting")
		}
	}
	if sk := s.sameCandidacy(c); sk != nil {
		return sk
	}
	if cos := s.companies[keys[0]]; len(cos) > 1 {
		return skip(c, RuleSharedLink, ownerUser, "this link is shared by %d evaluations of different companies (%s) - the link was captured wrong; re-find the posting", len(cos), strings.Join(cos, ", "))
	}
	if at, ok := s.interruptedAt(keys); ok {
		return skip(c, RuleInterrupted, ownerUser, "an apply-list batch began applying to it at %s and never recorded how it ended - it may have been submitted; check the posting before re-applying", at.UTC().Format(time.RFC3339))
	}
	if sk := s.todaySkip(g, keys, named); sk != nil {
		return sk
	}
	if !named {
		if h, ok := s.holds.reason(c.URL, s.opts.Build, s.opts.Now); ok {
			sk := skip(c, RuleHold, h.owner, "%s", h.reason)
			sk.Closed = h.closed
			return sk
		}
	}
	if d := gates.SkipCompany(gates.SkipInput{Company: c.Company, Entries: s.opts.SkipList}); d.Entry != "" {
		return skip(c, RuleSkipList, ownerUser, "%s", d.Reason)
	}
	return s.staleLink(c)
}

// sameCandidacy is rules 1b and 1c: the same company and role under another
// URL, as an applied (or chain-submitted) tracker row or as a ledger row, whose
// own company and role may be the report's when the chain stored only a host.
func (s *selection) sameCandidacy(c Candidate) *Skipped {
	form := identity.StrictOf(identity.Posting{Company: c.Company, Role: c.Role})
	for _, src := range s.sources {
		if form.Same(src.form) {
			verb := "submitted"
			if src.applied {
				verb = "applied"
			}
			return skip(c, RuleCompanyRole, "", "already %s as #%d (same company + role)", verb, src.row.ID)
		}
	}
	for _, sub := range s.submissions {
		if l := sub.l; form.Same(sub.form) {
			return skip(c, RuleCompanyRole, "", "already submitted as ledger row %d at %s (same company + role)", l.ID, l.At.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// interruptedAt is the start of a row a batch began and never ended, unless a
// run of the posting started since: that run's own record speaks for it.
func (s *selection) interruptedAt(keys []string) (time.Time, bool) {
	for _, k := range keys {
		at, ok := s.interrupted[k]
		if !ok {
			continue
		}
		if r, has := s.holds.latest[k]; has && r.Started.After(at) {
			continue
		}
		return at, true
	}
	return time.Time{}, false
}

// todaySkip is rule 5: the last run of the day for the posting is its verdict.
// A submit is a duplicate whatever else is asked; other stops yield to
// retry-today, except a sign-in wall and the user's own missing data, which
// yield only to the user naming the company as well. A ready document or a
// documents override of the row since the stop lifts it like a save of the
// user's data.
func (s *selection) todaySkip(g MirrorGroup, keys []string, named bool) *Skipped {
	saved := todaySaves{data: s.f.DataSavedAt, documents: s.f.DocumentsAt[s.rowID(g.Primary, keys)]}
	var verdict *todayStop
	for _, k := range keys {
		if r, ok := s.today[k]; ok {
			if t := todayVerdict(r, s.opts.Build, saved); t != nil && t.duplicate {
				verdict = t
				break
			}
		}
	}
	if r, ok := s.today[keys[0]]; ok && verdict == nil {
		verdict = todayVerdict(r, s.opts.Build, saved)
	}
	if verdict == nil {
		return nil
	}
	waitsForUser := verdict.needsUser && s.opts.RetryToday && !named
	waitsForData := verdict.waitsForData && !named
	if !verdict.duplicate && s.opts.RetryToday && !waitsForUser && !waitsForData {
		return nil
	}
	reason := verdict.reason
	switch {
	case waitsForUser:
		reason += needsUserNote
	case waitsForData:
		reason += userDataNote
	}
	return skip(g.Primary, RuleToday, verdict.owner, "%s", reason)
}

// rowID is the candidate's tracker row: its own, or the row of its address.
func (s *selection) rowID(c Candidate, keys []string) int64 {
	if c.TrackerID > 0 {
		return c.TrackerID
	}
	for _, k := range keys {
		if id, ok := s.rowOf[k]; ok {
			return id
		}
	}
	return 0
}

// staleLink refuses a candidate whose URL resolves to a report for a different
// role: the tracker link is stale. The same role with other punctuation or a
// level suffix, and a URL with no evaluated report, run.
func (s *selection) staleLink(c Candidate) *Skipped {
	reports := s.byURL[postingKey(c.URL)]
	if len(reports) == 0 {
		return nil
	}
	for _, r := range reports {
		if sameRoleNames(r.Role, c.Role) {
			return nil
		}
	}
	return skip(c, RuleStaleLink, ownerUser, "the report behind this URL is for a different role (%q) - the tracker link is stale", reports[len(reports)-1].Role)
}

// roleFiller are level markers and grammatical words that restate a role.
var roleFiller = map[string]bool{"i": true, "ii": true, "iii": true, "1": true, "2": true, "3": true, "the": true, "and": true, "of": true, "for": true, "a": true, "an": true}

func roleTokens(role string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(role), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !roleFiller[w] {
			out[w] = true
		}
	}
	return out
}

// sameRoleNames reports whether one name's words are all in the other's, in
// either direction: punctuation, a level suffix and a team suffix leave a job
// the same, while two jobs each carry words the other lacks.
func sameRoleNames(a, b string) bool {
	ta, tb := roleTokens(a), roleTokens(b)
	small, large := ta, tb
	if len(tb) < len(ta) {
		small, large = tb, ta
	}
	for w := range small {
		if !large[w] {
			return false
		}
	}
	return true
}
