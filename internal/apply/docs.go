package apply

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// docResumes serves the documents the engine attaches. A version is attached
// only when its stored verification still describes its files and says it is
// ready: a stored ready row is not enough, because a replaced file reads as
// stale and unverified. The attached versions are remembered so the submit
// stage can freeze exactly those.
type docResumes struct {
	r *Runner
	e *Exec

	mu       sync.Mutex
	attached []documents.Document
	// notes say why a document was not attached, until a page report takes
	// them (takeNotes).
	notes []string
	// skipped are the uploads a demo workspace refused and the letters a text
	// box could not take, for the run's warnings.
	skipped []string
	// resume names the first résumé attached and why; named is set once the
	// page report took it (resumeLine).
	resume string
	named  bool
	// lacked is set once a transcript slot asked and none is stored.
	lacked bool
}

func newDocResumes(r *Runner, e *Exec) *docResumes { return &docResumes{r: r, e: e} }

func (d *docResumes) note(format string, a ...any) {
	d.mu.Lock()
	d.notes = append(d.notes, fmt.Sprintf(format, a...))
	d.mu.Unlock()
}

// takeNotes returns, once each, why a document was not attached since the
// last call; the page whose fill asked for it reports them.
func (d *docResumes) takeNotes() []string {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, n := range d.notes {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	d.notes = nil
	return out
}

func (d *docResumes) choose(line string) {
	d.mu.Lock()
	if d.resume == "" {
		d.resume = line
	}
	d.mu.Unlock()
}

// resumeLine returns, once, the line that names the résumé the run attached
// and why; empty before one was attached, after it was taken, and for a
// session that serves no documents.
func (d *docResumes) resumeLine() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.named || d.resume == "" {
		return ""
	}
	d.named = true
	return d.resume
}

// Resume implements engine.Resumes. In a demo workspace every upload of the
// fictional persona's document is decided and audited by the demo gate; one
// the gate or its audit refuses is skipped and the rest of the page is filled
// as usual.
func (d *docResumes) Resume(ctx context.Context, req engine.ResumeRequest) (*engine.ResumePayload, error) {
	if d.r.demo() {
		host := ""
		if u, err := url.Parse(req.Origin); err == nil {
			host = u.Hostname()
		}
		g := gates.DemoWorkspace(gates.DemoInput{Demo: true, Action: gates.DemoUpload, Host: host})
		if err := d.r.audit(d.e, g, req.Origin, false); err != nil || !g.Allowed() {
			d.mu.Lock()
			d.skipped = append(d.skipped, "upload skipped: "+g.Code+": "+g.Reason)
			d.mu.Unlock()
			return nil, nil
		}
	}
	if req.Document == "transcript" {
		return d.transcript(ctx)
	}
	kind := documents.KindResume
	if req.Document == "cover-letter" {
		kind = documents.KindCover
	}
	text := req.Format == "text"
	if text && kind != documents.KindCover {
		return nil, nil
	}
	if p := d.r.d.Page; p.Documents != nil && d.e.Run.Report != nil && d.e.Run.Report.TrackerID != 0 {
		docs, err := p.Documents.List(ctx, d.e.Run.Report.TrackerID)
		if err != nil {
			d.note("documents unreadable: %v", err)
		} else if pl := d.pick(ctx, docs, kind, req); pl != nil {
			return pl, nil
		}
	}
	if kind == documents.KindResume && !text && d.r.d.Page.ProfileResume != nil {
		pl, why, err := d.r.d.Page.ProfileResume(ctx, d.e.Run.Report)
		if err != nil {
			d.note("profile résumé unreadable: %v", err)
			return nil, nil
		}
		d.choose("résumé: " + why)
		return pl, nil
	}
	return nil, nil
}

// transcript serves the Answer Bank's transcript PDF to a transcript slot. A
// slot with none stored is left as the page has it, never given the résumé.
func (d *docResumes) transcript(ctx context.Context) (*engine.ResumePayload, error) {
	var pl *engine.ResumePayload
	if d.r.d.Page.Transcript != nil {
		var err error
		if pl, err = d.r.d.Page.Transcript(ctx); err != nil {
			d.note("transcript unreadable: %v", err)
			return nil, nil
		}
	}
	if pl == nil {
		d.mu.Lock()
		d.lacked = true
		d.mu.Unlock()
	}
	return pl, nil
}

// noTranscript reports a transcript slot that asked when none is stored.
func (d *docResumes) noTranscript() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lacked
}

// pick returns the newest ready version of kind whose verification holds: its
// PDF, or for a letter box its text, never cut to the box's limit.
func (d *docResumes) pick(ctx context.Context, docs []documents.Document, kind documents.Kind, req engine.ResumeRequest) *engine.ResumePayload {
	for _, doc := range docs {
		if doc.Kind != kind || !doc.Ready || doc.PDFPath == "" {
			continue
		}
		v, err := d.r.d.Page.Documents.Verification(ctx, doc)
		if err != nil || v == nil || v.State != documents.PDFReady || v.Stale != "" {
			d.note("%s version %d is not attachable: unverified or stale", kind, doc.ID)
			continue
		}
		pl := &engine.ResumePayload{FileName: fmt.Sprintf("%s.pdf", kind), MediaType: "application/pdf"}
		var data []byte
		if req.Format == "text" {
			src, e := d.r.d.Page.Documents.Source(ctx, doc.ID)
			data, err = []byte(strings.TrimSpace(src.Text)), e
			if n := len(utf16.Encode([]rune(string(data)))); err == nil && req.MaxLength > 0 && n > req.MaxLength {
				d.mu.Lock()
				d.skipped = append(d.skipped, fmt.Sprintf("cover letter: the letter is %d characters and the page's box takes at most %d; it was not cut, the box is left for you", n, req.MaxLength))
				d.mu.Unlock()
				return nil
			}
			pl.FileName, pl.MediaType = "cover-letter.txt", "text/plain"
		} else {
			data, err = d.r.d.Page.Documents.ReadPDF(ctx, doc)
		}
		if err != nil || len(data) == 0 {
			d.note("%s version %d could not be read", kind, doc.ID)
			continue
		}
		d.mu.Lock()
		d.attached = append(d.attached, doc)
		d.mu.Unlock()
		if kind == documents.KindResume {
			d.choose(fmt.Sprintf("résumé: the tailored one (version %d), its checks passed", doc.Version))
		}
		pl.ContentBase64 = base64.StdEncoding.EncodeToString(data)
		return pl
	}
	return nil
}

// skips returns the uploads a demo workspace refused and the letters a box
// could not take, once each.
func (d *docResumes) skips() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, s := range d.skipped {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// attachedIDs lists the versions served, once each.
func (d *docResumes) attachedIDs() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []int64
	for _, doc := range d.attached {
		if !slices.Contains(ids, doc.ID) {
			ids = append(ids, doc.ID)
		}
	}
	return ids
}

// observed reports whether a document of kind was attached by this run.
func (d *docResumes) observed(kind documents.Kind) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, doc := range d.attached {
		if doc.Kind == kind {
			return true
		}
	}
	return false
}

// A cover-letter upload is the one slot a run cannot fill by copying: the
// letter has to be written, and the writer grounds every paragraph in a full
// evaluation report of the posting. So before a page with a cover-letter slot
// is filled, a candidacy with no ready letter gets one: from its full report
// when it has one, otherwise after a full evaluation of the posting (a quick
// screen is never enough, and never what is asked for). The evaluation reads
// the posting at the run's posting address, never the form's own text. When a
// step cannot be done the slot stays empty and the page report says why; the
// run itself goes on.

// Letters makes the cover letter of a candidacy.
type Letters interface {
	// Evaluate runs a full evaluation of the posting and waits for it. It
	// returns the posting's report as it is bound now, nil when none was
	// written.
	Evaluate(ctx context.Context, postingURL string) (*ReportBinding, error)
	// Write makes the candidacy's cover letter with its PDF and waits for it.
	Write(ctx context.Context, candidacy int64) error
}

// LetterEvaluationWait bounds the wait for the evaluation. The evaluation
// itself keeps running past it.
const LetterEvaluationWait = 300 * time.Second

// ReportFinal is the report kind of a full evaluation.
const ReportFinal = "final"

// coverFieldRE is an upload whose own id or name is the cover letter
// (Greenhouse's "cover_letter", labelled only "Attach").
var coverFieldRE = lazyregexp.New(`(?i)(?:^|[^a-z])cover[_-]?letter(?:$|[^a-z])`)

var coverSlotRE = lazyregexp.New(`(?i)\bcover(?:ing)?\s+letter\b|\bmotivation(?:al)?\s+letter\b|\bletter\s+of\s+(?:interest|motivation)\b`)

// letterBoxRE reads a text box's folded label as the letter itself, never a
// question about one; stage-resume.ts holds the same rule.
var (
	letterBoxRE = lazyregexp.New(`^(?:(?:paste|write|add|enter) (?:your |a )?)?(?:your )?(?:cover(?:ing)? letter|motivation(?:al)? letter|letter of (?:interest|motivation))(?: here)?(?: (?:required|optional))*$`)
	nonWordRE   = lazyregexp.New(`[^\p{L}\p{N}]+`)
)

func letterBox(kind, label string) bool {
	return kind == "textarea" && letterBoxRE.MatchString(strings.TrimSpace(nonWordRE.ReplaceAllString(strings.ToLower(label), " ")))
}

// coverSlot reports an empty upload slot of the page labelled for a cover
// letter alone (a slot that also names the résumé is the résumé's) or whose
// own id or name is the letter, or an empty text box labelled as the letter.
func coverSlot(st *PageState) bool {
	for _, in := range st.Inputs {
		if in.Filled || in.Disabled {
			continue
		}
		if (in.Kind == "file" || in.Type == "file") && !in.ParserOnly && (coverSlotRE.MatchString(in.Label) && !statementResumeRE.MatchString(in.Label) || coverFieldRE.MatchString(in.ID) || coverFieldRE.MatchString(in.Name)) {
			return true
		}
		if letterBox(in.Kind, in.Label) {
			return true
		}
	}
	return false
}

// LetterSlot reads a run record's leftover the way coverSlot reads a page: an
// upload slot labelled for the cover letter alone, or a text box labelled as
// the letter (the matcher names a textarea's kind "text").
func LetterSlot(kind, label string) bool {
	switch kind {
	case "file":
		return coverSlotRE.MatchString(label) && !statementResumeRE.MatchString(label)
	case "text", "textarea":
		return letterBox("textarea", label)
	}
	return false
}

// letterReady reports whether the candidacy already has a cover letter whose
// PDF passed its checks.
func (s *session) letterReady(ctx context.Context, candidacy int64) bool {
	if s.r.d.Page.Documents == nil || candidacy == 0 {
		return false
	}
	docs, err := s.r.d.Page.Documents.List(ctx, candidacy)
	if err != nil {
		return false
	}
	for _, d := range docs {
		if d.Kind == documents.KindCover && d.Ready && d.PDFPath != "" {
			return true
		}
	}
	return false
}

// prepareLetter runs once per run, on the first page with a cover-letter slot
// or box.
// It returns the line the page report keeps and whether a letter was made.
func (s *session) prepareLetter(ctx context.Context, st *PageState) (string, bool) {
	l := s.r.d.Page.Letters
	if l == nil || s.lettered || !coverSlot(st) {
		return "", false
	}
	s.lettered = true
	run := s.e.Run
	rep := run.Report
	if rep != nil && rep.TrackerID != 0 && s.letterReady(ctx, rep.TrackerID) {
		return "", false
	}
	evaluated := ""
	if rep == nil || rep.Kind != ReportFinal || rep.TrackerID == 0 {
		ectx, cancel := context.WithTimeout(ctx, LetterEvaluationWait)
		b, err := l.Evaluate(ectx, run.Input.URL)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			return fmt.Sprintf("cover letter: the evaluation of this posting is still running after %d s; it goes on, and the letter can be written once it ends", int(LetterEvaluationWait/time.Second)), false
		case err != nil:
			return "cover letter: the evaluation of this posting failed: " + err.Error(), false
		case b == nil:
			return "cover letter: the evaluation wrote no report, so there is nothing to ground a letter in", false
		case b.Kind != ReportFinal:
			return fmt.Sprintf("cover letter: the evaluation produced only a quick screen (report #%03d), which has no role snapshot to ground a letter in", b.Number), false
		case b.TrackerID == 0:
			return fmt.Sprintf("cover letter: report #%03d has no tracker row to write the letter for", b.Number), false
		}
		run.Report, rep = b, b
		evaluated = fmt.Sprintf("no full report, so the posting was evaluated first (report #%03d); ", b.Number)
	}
	if err := l.Write(ctx, rep.TrackerID); err != nil {
		return "cover letter: " + evaluated + "the letter could not be written: " + err.Error(), false
	}
	return fmt.Sprintf("cover letter: %swritten from report #%03d", evaluated, rep.Number), true
}

// ByNumber binds the report with number n the way Bind does: company and role
// from its live tracker row, else from the report itself.
func (s SQLReports) ByNumber(ctx context.Context, n int64) (*ReportBinding, error) {
	b := &ReportBinding{Number: n}
	err := s.DB.QueryRowContext(ctx, `SELECT company, role, markdown_path, kind FROM report_index WHERE report_number = ?`, n).Scan(&b.Company, &b.Role, &b.Path, &b.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var company, role string
	err = s.DB.QueryRowContext(ctx, `SELECT id, company, role FROM tracker_entries WHERE archived_at IS NULL AND report_number = ? ORDER BY id LIMIT 1`, n).Scan(&b.TrackerID, &company, &role)
	switch {
	case err == nil:
		b.Company, b.Role = company, role
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	return b, nil
}
