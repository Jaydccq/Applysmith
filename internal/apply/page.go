package apply

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// fillOptions builds one page fill's settings.
func (s *session) fillOptions(st *PageState) engine.FillOptions {
	run := s.e.Run
	p := engine.PostingContext{URL: redactURL(run.Input.URL), JDText: s.jd, Company: firstNonEmpty(s.companyName(), s.statedCompany())}
	p.Title = run.Input.Role
	if run.Report != nil {
		p.Title = run.Report.Role
	}
	// The text's location line is left to the matcher, which reads it from
	// JDText after the engine has looked at the form's own JobPosting data.
	p.Location = s.placedLoc()
	o := engine.FillOptions{
		Applicant: s.r.d.Page.Applicant, Posting: p, MainWorld: s.r.d.Page.MainWorld, Timing: s.r.d.Page.Engine,
		OnUnreached: func(d engine.FrameDecision) {},
		// The controls a resumed run found changed by hand stay as the user left them.
		Edited: s.keep.edited(),
	}
	if s.r.d.Page.Settings != nil {
		o.AcceptLegalDeclarations = s.r.d.Page.Settings().AutoAcceptLegalDeclarations
	}
	return o
}

// boardCompany is the employer a public board's address names: Greenhouse,
// Lever and Ashby put the employer's slug in the path. A run given only an
// address has no other company, and the matcher's reading of "to work for
// Affirm" needs the name to tell it from a second clause.
func boardCompany(ats, tenant string) string {
	switch ats {
	case ATSGreenhouse, ATSLever, ATSAshby:
		return strings.Join(strings.FieldsFunc(tenant, func(r rune) bool { return r == '-' || r == '_' }), " ")
	}
	return ""
}

// statedCompany is the employer when the run was given none: the name the
// posting page's JobPosting data states, else the board's slug, else what the
// page states otherwise (its site name), else the tenant's slug on a
// per-employer host. A slug can squash the name ("northwoodspace" for
// Northwood Space), so a question naming "Northwood" named no employer.
func (s *session) statedCompany() string {
	return firstNonEmpty(s.e.Run.Stated.Company, boardCompany(s.e.Run.ATS, s.e.Run.Tenant), s.pageCompany, tenantSlug(s.e.Run.Input.URL))
}

// tenantSlug is the employer's slug a per-employer ATS address carries.
func tenantSlug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host, segs := strings.ToLower(u.Hostname()), segments(u.Path)
	switch {
	case anyHost(host, []string{"teamtailor.com", "recruitee.com", "bamboohr.com", "breezy.hr"}):
		if l := strings.Split(host, "."); len(l) > 2 && l[0] != "www" && l[0] != "app" {
			return l[0]
		}
	case (host == "jobs.jobvite.com" || host == "apply.workable.com" || host == "jobs.smartrecruiters.com") && len(segs) > 0:
		// A Workable short link (/j/{code}) names no account.
		if host == "apply.workable.com" && segs[0] == "j" {
			return ""
		}
		return segs[0]
	}
	return ""
}

// PostingRead is a posting as its public board states it.
type PostingRead struct{ Title, Location, Text string }

// boardReadWait bounds the one board request of a run; a slow board leaves the
// run with the page alone.
const boardReadWait = 10 * time.Second

// postingText is the posting's own words for the answers and drafts. A Lever
// /apply or Ashby /application page holds the form without the description,
// so the board's text stands in for it there; elsewhere the page's text
// stands (see pageText). The board's location is kept on every board, because
// a page shows it without the "Location:" line the text is read for.
// The note says where the text came from when the board was asked.
func (s *session) postingText(ctx context.Context, st *PageState, ats string) (string, string) {
	page, note := s.pageText(st)
	board := s.e.Run.Input.URL
	if b := greenhouseEmbedBoard(st, board); b != "" {
		board, ats = b, ATSGreenhouse
	}
	if !asksBoard(ats, board) {
		// The page can name the board where the run's address does not: a
		// Workable short link (apply.workable.com/j/{code}) names no account,
		// and Workable answers it with a 301 to the account's address.
		board = st.URL
	}
	if read := s.r.d.Page.Posting; read == nil || s.boardRead || !asksBoard(ats, board) {
		return page, note
	}
	s.boardRead = true
	rctx, cancel := context.WithTimeout(ctx, boardReadWait)
	defer cancel()
	b, err := s.r.d.Page.Posting(rctx, board)
	if err != nil {
		why := "the posting's board could not be read (" + err.Error() + "); the page's text stands for the posting"
		if note != "" {
			why = note + "; " + why
		}
		return page, why
	}
	s.board = b
	if (ats != ATSLever && ats != ATSAshby) || strings.TrimSpace(b.Text) == "" {
		return page, note
	}
	head := strings.TrimSpace(b.Title)
	if loc := strings.TrimSpace(b.Location); loc != "" {
		head += "\nLocation: " + loc
	}
	return clipText(strings.TrimSpace(head+"\n\n"+strings.TrimSpace(b.Text)), 8000), "the application page holds only the form: the posting's text was read from its board"
}

// asksBoard reports whether a run reads its posting's public board: by the
// chain's system for Greenhouse, Lever and Ashby, and by its address for a
// SmartRecruiters or Workable posting, which the chain fills as a generic site
// and whose page states no place the run reads (Workable's) or not on every
// page the walk starts from (SmartRecruiters' one-click form).
func asksBoard(ats, posting string) bool {
	switch ats {
	case ATSGreenhouse, ATSLever, ATSAshby:
		return true
	}
	key, _ := identity.ATSKey(posting)
	name, _, _ := strings.Cut(key, ":")
	return name == identity.SmartRecruiters || name == identity.Workable
}

// pageText is the page's text, after the walk's first page without a form
// when the form is shown without the description (Workable's /apply/,
// BambooHR's): none of that page's paragraphs is on the form page. The description then comes first, so
// the cap keeps it and not the form.
func (s *session) pageText(st *PageState) (string, string) {
	if e := s.lead; e != nil && e != st {
		long := 0
		for _, l := range strings.Split(e.Text, "\n") {
			if l = strings.TrimSpace(l); len(l) < 120 {
				continue
			}
			if strings.Contains(st.Text, l) {
				long = 0
				break
			}
			long++
		}
		if long > 0 {
			return clipText(strings.TrimSpace(e.Text)+"\n\n"+st.Text, 8000), "the form page holds no description: the posting's text was read from the page before it"
		}
	}
	return clipText(st.Text, 8000), ""
}

// greenhouseEmbedBoard is the board address of a Greenhouse form embedded in
// a company's page: its frame names the board, the gh_jid (or the frame's
// token) the job. The embed can also be the page itself, where the run was
// handed off to it (Datadog's careers page never renders its frame), and the
// run's own address then names no board.
func greenhouseEmbedBoard(st *PageState, postingURL string) string {
	u, err := url.Parse(postingURL)
	if err != nil {
		return ""
	}
	id := u.Query().Get("gh_jid")
	srcs := []string{st.URL}
	for _, f := range st.Frames {
		srcs = append(srcs, f.Src)
	}
	for _, src := range srcs {
		fu, err := url.Parse(src)
		if err != nil || !hostIs(fu.Hostname(), "greenhouse.io") || !strings.HasPrefix(fu.Path, "/embed/job_app") {
			continue
		}
		slug := fu.Query().Get("for")
		if id == "" {
			id = fu.Query().Get("token")
		}
		if slug != "" && ghJobIDRE.MatchString(id) {
			return "https://job-boards.greenhouse.io/" + url.PathEscape(slug) + "/jobs/" + id
		}
	}
	return ""
}

var ghJobIDRE = lazyregexp.New(`^\d{4,}$`)

// trackerRow is the location and company the tracker holds for the posting,
// read once per page: pageWork forgets them, because a cover letter's
// evaluation can make the row between two pages. The match call carries the
// address without its query, so on a site that names the posting there (ADP's
// jobId) its lookup finds no row; the run's own address is read here, whole.
func (s *session) trackerRow() (loc, company string) {
	tr := s.r.d.Page.Tracker
	if tr == nil || s.trackerRead {
		return s.trackerLoc, s.trackerCo
	}
	res, err := tr.Lookup(context.Background(), s.e.Run.Input.URL)
	if err != nil {
		return "", ""
	}
	s.trackerRead, s.trackerLoc, s.trackerCo = true, "", ""
	if res.Found && res.Entry != nil {
		s.trackerLoc, s.trackerCo = strings.TrimSpace(res.Entry.Location), strings.TrimSpace(res.Entry.Company)
	}
	return s.trackerLoc, s.trackerCo
}

func (s *session) trackerLocation() string {
	loc, _ := s.trackerRow()
	return loc
}

// fillStop maps a failed fill to the stop that ends the run: the machine's
// faults are environment, everything else the system did wrong is a defect.
func fillStop(err error) *stop {
	code, _ := engine.CodeOf(err)
	switch code {
	case engine.CodeTabClosed, engine.CodeBrowserLost, engine.CodeApplicantUnavailable, engine.CodeBundleMissing:
		return &stop{class: ClassEnvironment, reason: "fill: " + string(code) + ": " + err.Error()}
	case engine.CodeTopOriginChanged:
		return &stop{class: ClassDefect, reason: "fill: " + string(code), moved: true}
	case "":
		return &stop{class: ClassDefect, reason: "fill: " + err.Error()}
	}
	return &stop{class: ClassDefect, reason: "fill: " + string(code)}
}

// addReport adds a page's roll-up to the run record. The first page a resumed
// run reads is the one its earlier leg stopped on when the address is the
// same, so the fresh report takes the place of that leg's last one.
func (s *session) addReport(rep PageReport) {
	e := s.e
	if n := e.reread; n > 0 && n == len(e.Run.Pages) && e.Run.Pages[n-1].URL == rep.URL {
		e.Run.Pages = e.Run.Pages[:n-1]
	}
	e.reread = 0
	e.Run.Pages = append(e.Run.Pages, rep)
}

// pageWork fills one page, checks it and fixes what the check found. It stops
// the run on a failed fill or an identity mismatch; required leftovers are
// left to the final verdict, because a later page of the same application can
// reveal the rest.
func (s *session) pageWork(ctx context.Context, w *walker, st *PageState) *stop {
	s.frame, s.covered, s.trackerRead = "", nil, false
	ps := &pageState{report: PageReport{URL: redactURL(st.URL), Step: len(s.pages) + 1}}
	if s.jd == "" {
		var note string
		if s.jd, note = s.postingText(ctx, st, s.e.Run.ATS); note != "" {
			ps.report.Notes = append(ps.report.Notes, note)
		}
	}
	if st.Oracle != nil {
		s.oracleHistory(ctx, st, ps)
	}
	ps.timeline = st.Oracle != nil
	// A cover-letter slot is filled only with a letter that exists before the fill.
	if note, made := s.prepareLetter(ctx, st); note != "" {
		ps.report.Notes = append(ps.report.Notes, note)
		if !made {
			s.e.Run.Warnings = append(s.e.Run.Warnings, note)
		}
	}
	start := s.r.d.Now()
	res, err := s.page.Fill(ctx, s.fillOptions(st))
	if err == nil && w != nil && (readNoRow(res) || writeMissed(res)) {
		// A cookie dialog with a backdrop hides the form from the fill
		// (Workable's), or takes the clicks a write needs (Toast's phone
		// country picker): close it, then fill once more.
		cleared, stp := w.closeConsent(ctx, st.URL)
		if stp != nil {
			return stp
		}
		if cleared {
			res, err = s.page.Fill(ctx, s.fillOptions(st))
		}
	}
	if err == nil && w != nil && unreadForm(res, st) > 0 {
		// The walk's inventory, read from the engine a moment before, holds a
		// form the fill found none of: a page still drawing its fields, or a
		// layer over them. The fill looks once more after a poll. A tab that
		// has left the inventory's address is read again by the walk: Taleo's
		// Next posts the résumé while its form stays on screen, then lands on a
		// wait page that reloads itself until the résumé is parsed.
		if !w.sleep(ctx, w.tm.Poll) {
			return cancelledStop(StageFill)
		}
		if again, perr := Probe(ctx, s.tab); perr == nil && again.URL != st.URL {
			return &stop{class: ClassDefect, reason: "fill: the tab left the page the fill was given", moved: true}
		}
		res, err = s.page.Fill(ctx, s.fillOptions(st))
	}
	ps.report.FillMs = s.r.d.Now().Sub(start).Milliseconds()
	if err != nil {
		return fillStop(err)
	}
	s.covered = coveredFrames(res)
	// The résumé the fill attached is named once, in this page's report and in
	// the fill stage's detail.
	if line := s.docs.resumeLine(); line != "" {
		ps.report.Notes = append(ps.report.Notes, line)
		s.walkNotes = append(s.walkNotes, line)
	}
	// Why a document the fill asked for was not attached.
	ps.report.Notes = append(ps.report.Notes, s.docs.takeNotes()...)
	ps.runID = res.RunID
	ps.report.EngineReinjects = res.Reinjections
	if len(s.pages) == 0 && res.Top.Summary != nil && !res.Top.Summary.NotAnApplication {
		s.r.signalFirstFill(s.e, res.Top.Summary.Rows)
	}
	switch res.Top.State {
	case engine.StateDone:
	case engine.StateStopped, engine.StateError:
		reason := firstNonEmpty(res.Top.Reason, res.Top.Message, res.Top.State)
		return &stop{class: ClassDefect, reason: "fill: " + reason}
	}
	if res.Top.Summary != nil && res.Top.Summary.NotAnApplication {
		// A page whose fields the fill never read is never walked past: Next
		// would send what the site put there unchecked (Oracle's contact step
		// held "JordanAvery" as the first name).
		if n := unreadForm(res, st); n > 0 {
			return &stop{class: ClassDefect, reason: fmt.Sprintf("fill: the page shows %d fields, but the fill read no application form on it; nothing on it was written or checked, so the walk does not go past it", n)}
		}
		fr := applicationFrame(res)
		if fr == "" {
			ps.report.Notes = append(ps.report.Notes, "the engine reads this form as not an application")
			s.pages = append(s.pages, ps)
			s.addReport(ps.report)
			return nil
		}
		// The top frame is a shell around an embedded application: the check,
		// the fix and the answers go to the frame the engine filled.
		s.frame = fr
		ps.report.Notes = append(ps.report.Notes, "the application is embedded in a frame: the check reads the frame")
	}
	if blocked, berr := s.blocked(ctx); berr == nil && len(blocked) > 0 {
		ps.report.BlockedSubmits = len(blocked)
		ps.report.Notes = append(ps.report.Notes, fmt.Sprintf("%d native submission(s) were held while the engine wrote", len(blocked)))
	}
	s.pages = append(s.pages, ps)
	s.check(ctx, res, ps)
	if len(ps.leftovers) > 0 {
		if after, perr := Probe(ctx, s.tab); perr == nil {
			s.fillCodeBoxes(ctx, after, ps, time.Time{})
		}
	}
	s.fix(ctx, res, ps)
	s.collectEvents(res, ps, ps.answered)
	// An identity mismatch or an unreadable page ends the walk before the next
	// step; the other findings wait for the final verdict, and a value the
	// review doubts is marked for the user's review while the fill goes on.
	pr := ps.gatesReport(len(s.pages))
	verdict := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{pr}, Final: false, PostingLocation: s.postingLoc(), ReviewAdvisory: true})
	ps.report.Readiness = string(verdict.Verdict)
	ps.recordLeftovers()
	s.addReport(ps.report)
	if !verdict.Allowed() {
		_ = s.r.audit(s.e, verdict.Decision, st.URL, false)
		return &stop{class: Class(verdict.Class), reason: "check: " + verdict.Reason, at: StageCheck}
	}
	return nil
}

// unreadForm is how many fields the walk's inventory holds on a page the fill
// read as not an application, outside any frame it filled; zero when the two
// agree. Two fields are a form by the engine's own rule.
func unreadForm(res *engine.PageResult, st *PageState) int {
	if res == nil || res.Top.Summary == nil || !res.Top.Summary.NotAnApplication || applicationFrame(res) != "" {
		return 0
	}
	if fields, _ := st.FormFields(); fields >= 2 {
		return fields
	}
	return 0
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// finalVerdict computes the readiness over every page of the walk, with the
// required leftovers counted. A value the review doubts blocks only a run
// that presses Submit; a fill-only run names it for review (reviewMarks).
func (s *session) finalVerdict() gates.ReadinessResult {
	var prs []gates.PageReport
	for i, ps := range s.pages {
		prs = append(prs, ps.gatesReport(i+1))
	}
	clearRevisited(s.pages, prs)
	return gates.SubmitReadiness(gates.ReadinessInput{Pages: prs, Final: true, PostingLocation: s.postingLoc(), ReviewAdvisory: !s.e.Run.Input.Submit})
}

// clearRevisited drops an earlier page's leftover when a later page of the
// same URL holds a row under the same selector with a label that equals or
// extends it (a collapsed-sections application the walk revealed), or still
// reports the same leftover itself (a page the walk read again after a Next
// the page refused), so each open row is named once, on its latest page.
func clearRevisited(pages []*pageState, prs []gates.PageReport) {
	for i, early := range pages {
		for j := i + 1; j < len(pages); j++ {
			late := pages[j]
			if late.report.URL != early.report.URL {
				continue
			}
			filled := map[string]string{}
			for _, r := range late.rows {
				if (r.Outcome == "filled" || r.Outcome == "prefilled") && r.Value != "" {
					filled[r.Selector] = strings.ToLower(r.Label)
				}
			}
			open := map[string]bool{}
			for _, l := range late.leftovers {
				open[l.Selector+"\x00"+l.Label] = true
			}
			keep := prs[i].RequiredUnanswered[:0]
			for _, it := range prs[i].RequiredUnanswered {
				cleared := false
				for _, l := range early.leftovers {
					if l.Label == it.Label {
						if lab, ok := filled[l.Selector]; ok && strings.HasPrefix(lab, strings.ToLower(l.Label)) {
							cleared = true
						}
						if open[l.Selector+"\x00"+l.Label] {
							cleared = true
						}
					}
				}
				if !cleared {
					keep = append(keep, it)
				}
			}
			prs[i].RequiredUnanswered = keep
		}
	}
}

// applicationFrame is the sub-frame whose fill wrote or read rows, when the top
// frame is only the shell around it.
func applicationFrame(res *engine.PageResult) string {
	if fr := coveredFrames(res); len(fr) > 0 {
		return fr[0]
	}
	return ""
}

// coveredFrames are the sub-frames whose fill wrote or read rows.
func coveredFrames(res *engine.PageResult) []string {
	var out []string
	for _, f := range res.Frames {
		if f.Status != nil && f.Status.Summary != nil && len(f.Status.Summary.Rows) > 0 {
			out = append(out, f.Frame.ID)
		}
	}
	return out
}
