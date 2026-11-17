package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/documents/toolchain"
	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

var errTimeout = errors.New("document run timed out")

// job is the state of one running request.
type job struct {
	s        *Service
	runID    string
	req      Request
	warnings []string
	partial  map[string][]byte
	gen      Generation
	retr     Retrieval
	// noModel is set when there is no usable model route, whether the service
	// has no model at all or the router reports that no provider is configured.
	noModel bool
	// lastCall is the ledger call id of the latest model call that answered.
	lastCall string
	// only names the one document a warning is about, by its index in
	// warnings; a warning not in it is about the whole run.
	only map[int]Kind
}

func (j *job) warn(format string, args ...any) {
	j.warnings = append(j.warnings, relativePaths(fmt.Sprintf(format, args...), j.s.d.Workspace.Root))
}

// warnOn is a warning about document k alone, which only k's record keeps.
func (j *job) warnOn(k Kind, format string, args ...any) {
	j.warn(format, args...)
	if j.only == nil {
		j.only = map[int]Kind{}
	}
	j.only[len(j.warnings)-1] = k
}

// warningsFor are the run's warnings that concern document k.
func (j *job) warningsFor(k Kind) []string {
	var out []string
	for i, w := range j.warnings {
		if o, ok := j.only[i]; !ok || o == k {
			out = append(out, w)
		}
	}
	return out
}

// absPathRE finds an absolute path inside a message.
var absPathRE = lazyregexp.New(`(^|[\s("'=:])((?:/[^/\s"'():]+)+/?|[A-Za-z]:\\[^\s"'():]+)`)

// relativePaths rewrites the absolute paths of a message that is stored and
// shown to the user: a path inside the workspace becomes workspace-relative,
// any other keeps only its last element.
func relativePaths(msg, root string) string {
	return absPathRE.ReplaceAllStringFunc(msg, func(m string) string {
		sub := absPathRE.FindStringSubmatch(m)
		lead, p := sub[1], sub[2]
		if rest, ok := strings.CutPrefix(filepath.ToSlash(p), filepath.ToSlash(root)+"/"); ok {
			return lead + rest
		}
		return lead + path.Base(filepath.ToSlash(p))
	})
}

func (j *job) keep(name string, v any) {
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		b, _ = json.MarshalIndent(v, "", "  ")
	}
	j.partial[name] = b
}

// stage records the stage and tells the stream; a cancelled or expired run
// stops here.
func (j *job) stage(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	s := j.s
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'running', stage = ?, updated_at = ? WHERE id = ? AND status IN ('queued', 'running')`,
			name, s.d.Now().UTC().Format(time.RFC3339), j.runID)
		return err
	})
	if err == nil && s.d.Emit != nil {
		s.d.Emit(Event{RunID: j.runID, Stage: name})
	}
	return err
}

// execute runs one request to its end and records the outcome.
func (s *Service) execute(ctx context.Context, runID string, req Request) Result {
	ctx, cancel := context.WithTimeoutCause(ctx, s.runTimeout, errTimeout)
	defer cancel()
	if dl, ok := ctx.Deadline(); ok {
		s.mu.Lock()
		if p := s.byRun[runID]; p != nil {
			p.deadline = dl
		}
		s.mu.Unlock()
	}
	j := &job{s: s, runID: runID, req: req, partial: map[string][]byte{}, gen: GenDeterministic, noModel: s.d.Model == nil}
	var res Result
	var err error
	switch req.Action {
	case ActionPDF:
		res, err = j.runPDF(ctx)
	case ActionEdit:
		res, err = j.runEdit(ctx)
	case ActionRefine:
		res, err = j.runRefine(ctx)
	case ActionInterview:
		res, err = j.runInterview(ctx)
	default:
		res, err = j.runGenerate(ctx)
	}
	if err != nil {
		res = j.failure(ctx, err)
	}
	res.RunID = runID
	res.Warnings = j.warnings
	if !res.Published {
		j.keepPartial()
	}
	s.finish(runID, req, res)
	return res
}

// failure names why a run ended: a known reason by its code, anything else by
// its own words, redacted and bounded, so the user is not left with "error".
func (j *job) failure(ctx context.Context, err error) Result {
	reason := fmt.Sprintf("%.200s", strings.Join(strings.Fields(llm.Redact(err.Error())), " "))
	var unavailable *toolchain.UnavailableError
	var floor *BelowFloorError
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errTimeout):
		reason = ReasonTimeout
	case errors.Is(cause, errCancelled):
		reason = ReasonCancelled
	case errors.Is(cause, errShutdown):
		reason = ReasonInterrupted
	case errors.Is(err, ErrNoPosting):
		reason = ReasonNoPosting
	case errors.Is(err, ErrNoContact):
		reason = ReasonNoContact
	case errors.Is(err, ErrNoValidDraft):
		reason = ReasonNoDraft
	case errors.As(err, &floor):
		reason = ReasonBelowFloor
	case errors.As(err, &unavailable):
		reason = ReasonToolchain
	case errors.Is(err, ErrStaleSource):
		reason = ReasonStale
	case errors.Is(err, ErrInvalid):
		reason = ReasonInput
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrIO), errors.Is(err, store.ErrDiskFull):
		reason = ReasonWrite
	}
	j.s.d.Log.Warn("document run failed", "run", j.runID, "reason", reason)
	j.keep("error.txt", err.Error())
	return Result{Reason: reason}
}

// keepPartial leaves what an unpublished run produced in a private directory,
// marked as draft by its place: it is never listed and never attached.
func (j *job) keepPartial() {
	if len(j.partial) == 0 {
		return
	}
	dir := filepath.Join(j.s.d.Workspace.StateDir(), "documents-runs", j.runID)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	for name, b := range j.partial {
		if len(b) > 2<<20 {
			b = b[:2<<20]
		}
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	pruneRuns(filepath.Dir(dir))
}

// keptRuns is how many unpublished runs keep their partial output.
const keptRuns = 50

// pruneRuns removes the oldest run directories beyond keptRuns.
func pruneRuns(root string) {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) <= keptRuns {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	var dirs []aged
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() {
			dirs = append(dirs, aged{e.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(dirs, func(a, b aged) int { return b.mod.Compare(a.mod) })
	for _, d := range dirs[min(keptRuns, len(dirs)):] {
		_ = os.RemoveAll(filepath.Join(root, d.name))
	}
}

// finish records the terminal state of the run and tells the stream.
func (s *Service) finish(runID string, req Request, res Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res.RunID = runID
	status, outcome := "succeeded", "published"
	switch {
	case res.Published:
	case res.Reason == ReasonCancelled:
		status, outcome = "cancelled", "failed"
	default:
		status, outcome = "failed", "failed"
	}
	reason := ""
	if !res.Published {
		reason = res.Reason
	}
	detail, _ := json.Marshal(map[string]any{"documents": res.Documents, "generation": res.Generation, "warnings": res.Warnings,
		"retrieval": res.Retrieval, "reason": res.Reason, "pack": res.Pack, "proposal": res.Proposal})
	now := s.d.Now().UTC().Format(time.RFC3339)
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, stage = '', failure_reason = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
			status, reason, now, now, runID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE document_jobs SET outcome = ?, detail_json = ?, finished_at = ? WHERE run_id = ?`, outcome, string(detail), now, runID)
		return err
	})
	if err != nil {
		s.d.Log.Error("documents: recording the outcome failed", "run", runID, "err", err)
	}
	if s.d.Emit != nil {
		s.d.Emit(Event{RunID: runID, Final: true, Result: &res})
	}
}

// Outcome reads the stored result of a run.
func (s *Service) Outcome(ctx context.Context, runID string) (Result, error) {
	var outcome sql.NullString
	var detail, status, failure string
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT r.status, r.failure_reason, j.outcome, j.detail_json FROM runs r JOIN document_jobs j ON j.run_id = r.id WHERE r.id = ?`, runID).
		Scan(&status, &failure, &outcome, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return Result{}, err
	}
	var d struct {
		Documents  []int64
		Generation Generation
		Warnings   []string
		Retrieval  Retrieval
		Reason     string
		Pack       *PackRef
		Proposal   int64
	}
	_ = json.Unmarshal([]byte(detail), &d)
	return Result{RunID: runID, Published: outcome.String == "published", Documents: d.Documents, Generation: d.Generation,
		Warnings: d.Warnings, Retrieval: d.Retrieval, Reason: firstNonEmpty(failure, d.Reason), Pack: d.Pack, Proposal: d.Proposal}, nil
}

func firstNonEmpty(in ...string) string {
	for _, s := range in {
		if s != "" {
			return s
		}
	}
	return ""
}

// PostingOf is the posting view of a tracker row's evaluation report, the job
// description a tailored run reads when it is given none: the title, the
// requirements with the posting's own words and the keywords. ErrNoPosting
// means the row has no readable report.
func (s *Service) PostingOf(ctx context.Context, candidacy int64) (string, error) {
	text, _, err := s.postingFor(ctx, Request{CandidacyID: candidacy})
	return text, err
}

// postingFor returns the job description of a tailored run: the request's, or
// the candidacy's evaluation report when the request carries none.
func (s *Service) postingFor(ctx context.Context, r Request) (text, source string, err error) {
	if t := strings.TrimSpace(r.Posting); t != "" {
		return t, "request", nil
	}
	e, err := s.candidacy(ctx, r.CandidacyID)
	if err != nil {
		return "", "", err
	}
	if e.ReportNumber != nil {
		var rel string
		qerr := s.d.Store.Read().QueryRowContext(ctx, `SELECT markdown_path FROM report_index WHERE report_number = ?`, *e.ReportNumber).Scan(&rel)
		if qerr == nil && strings.HasPrefix(rel, "reports/") && !strings.Contains(rel, "..") {
			if b, rerr := readRegular(filepath.Join(s.d.Workspace.Root, filepath.FromSlash(rel)), 200_000); rerr == nil && strings.TrimSpace(string(b)) != "" {
				return postingFromReport(string(b)), "evaluation report", nil
			}
		}
	}
	return "", "", ErrNoPosting
}

// postingFromReport turns a final evaluation report into the posting view the
// documents read: the title, the requirements the evaluation took from the
// posting (each with the posting's own words when quoted) and its verbatim
// keywords. The report's verdict, evidence tables and scorecard describe the
// seeker, not the job; read as a posting they made "CV", "label" and "hit"
// the core keywords. A report without a scorecard is returned as it is.
func postingFromReport(md string) string {
	_, rest, ok := strings.Cut(md, "```scorecard\n")
	if !ok {
		return md
	}
	raw, _, _ := strings.Cut(rest, "\n```")
	var card struct {
		Items struct {
			Must []reportItem `json:"must_have"`
			Nice []reportItem `json:"nice_to_have"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(raw), &card) != nil || len(card.Items.Must) == 0 {
		return md
	}
	var b strings.Builder
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "# ") {
			b.WriteString(l + "\n\n")
			break
		}
	}
	// The keywords come before the lists, so no list heading claims them.
	if _, kw, ok := strings.Cut(md, "\n## Keywords\n"); ok {
		kw, _, _ = strings.Cut(strings.TrimSpace(kw), "\n## ")
		b.WriteString(strings.TrimSpace(kw) + "\n\n")
	}
	for _, g := range []struct {
		head  string
		items []reportItem
	}{{"Requirements:", card.Items.Must}, {"Nice to have:", card.Items.Nice}} {
		if len(g.items) == 0 {
			continue
		}
		b.WriteString(g.head + "\n")
		// Two scorecard items may quote one posting line (an experience
		// threshold and a language): it is one requirement. The quote marks
		// around a quotation are not the posting's words.
		seen := map[string]bool{}
		for _, it := range g.items {
			line := strings.TrimSpace(it.Label)
			if q := strings.Trim(strings.TrimSpace(it.Source), "\"'\u201c\u201d"); len(q) > len(line) && !strings.EqualFold(q, "cv") && !strings.EqualFold(q, "narrative") {
				line = q
			}
			if !seen[strings.ToLower(line)] {
				seen[strings.ToLower(line)] = true
				b.WriteString("- " + line + "\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

type reportItem struct {
	Label  string `json:"label"`
	Source string `json:"source"`
}

// readRegular reads a regular file of at most max bytes; a symlink or a
// directory is refused. The file is opened once and the opened file is the one
// checked, so it cannot be swapped between the check and the read.
func readRegular(path string, max int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || !fi.Mode().IsRegular() || !os.SameFile(before, fi) || fi.Size() > max {
		return nil, fmt.Errorf("documents: %s is not a regular file within %d bytes", filepath.Base(path), max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("documents: %s is not a regular file within %d bytes", filepath.Base(path), max)
	}
	return b, nil
}

const snippetChars = 1800

// gather reads the sources of a run: profile, CV, posting, retrieved snippets
// and verified company context.
func (j *job) gather(ctx context.Context) (sourceSet, error) {
	s := j.s
	var src sourceSet
	raw, err := s.d.Workspace.Read(workspace.FileProfile)
	if err != nil {
		return src, fmt.Errorf("%w: profile.yml: %v", ErrInvalid, err)
	}
	p, rep := workspace.ParseProfile(raw.Data)
	if !rep.OK() {
		return src, fmt.Errorf("%w: profile.yml is not valid; run applysmith doctor", ErrInvalid)
	}
	src.Profile, src.ProfileBytes = p, raw.Data
	if cv, err := s.d.Workspace.Read(workspace.FileCV); err == nil && len(cv.Data) > 0 {
		src.CV = string(cv.Data)
	} else {
		src.Warnings = append(src.Warnings, "cv.md could not be read; the documents rest on profile.yml and the knowledge base")
	}
	if j.req.Mode == ModeTailored {
		e, err := s.candidacy(ctx, j.req.CandidacyID)
		if err != nil {
			return src, err
		}
		src.Company, src.Role = Normalize(e.Company), Normalize(e.Role)
		if e.ReportNumber != nil {
			src.ReportNumber = *e.ReportNumber
		}
		if src.Posting, _, err = s.postingFor(ctx, j.req); err != nil {
			return src, err
		}
	}
	query := retrievalQuery(src)
	src.Retrieval.Query = query
	if s.d.Retriever != nil && query != "" {
		res, err := s.d.Retriever.Search(ctx, kb.Query{Query: query, Kinds: []string{kb.CatProjects, kb.CatExperience, kb.CatBehavioral}, K: 8})
		switch {
		case ctx.Err() != nil:
			return src, context.Cause(ctx)
		case err != nil:
			src.Warnings = append(src.Warnings, "knowledge retrieval failed; continued from cv.md alone")
		default:
			for _, f := range res.Facts {
				if f.SourceKind != kb.KindSnippet || strings.HasPrefix(f.ID, "semantic:") {
					continue
				}
				if path, _, _ := strings.Cut(f.SourceRef, "#"); snippetCategory(path) == kb.CatAnswerPatterns {
					continue
				}
				f.Text = trimMarked(f.Text, snippetChars)
				src.Snippets = append(src.Snippets, f)
				src.Retrieval.Items = append(src.Retrieval.Items, RetrievalItem{Source: f.SourceRef, Score: f.Score, Reasons: f.Reasons})
			}
		}
		if src.Company != "" {
			if res, err := s.d.Retriever.Search(ctx, kb.Query{Query: src.Company + " " + src.Role, Kinds: []string{kb.KindBrief}, K: 4}); err == nil {
				for _, f := range res.Facts {
					if f.SourceKind == kb.KindBrief && briefAbout(f, src.Company) {
						src.Brief = append(src.Brief, f)
					}
				}
			}
		}
	}
	src.Retrieval.Retrieved = len(src.Snippets)
	if len(src.Snippets) == 0 {
		src.Warnings = append(src.Warnings, "no knowledge snippets were retrieved; continued from cv.md alone")
	}
	j.retr = src.Retrieval
	return src, nil
}

// briefAbout is true when a retrieved company brief is about the company:
// its text names the company, or its file is named after the company's
// domain ("ramp.com.md" for Ramp). Retrieval is lexical, so the brief of
// another company that shares the role's words ("Software Engineering") is
// returned too, and a letter must never quote it as the employer's.
func briefAbout(f kb.Fact, company string) bool {
	name := strings.TrimSpace(companySuffixRE.ReplaceAllString(company, ""))
	if name == "" {
		return false
	}
	if regexp.MustCompile(`(?i)(?:^|[^\pL\pN])` + regexp.QuoteMeta(name) + `(?:$|[^\pL\pN])`).MatchString(f.Text) {
		return true
	}
	alnum := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	file, _, _ := strings.Cut(f.SourceRef, "#")
	label, _, _ := strings.Cut(filepath.Base(file), ".")
	label, key := alnum(label), alnum(name)
	return len(label) >= 3 && key != "" && (strings.Contains(label, key) || strings.HasPrefix(key, label))
}

// voiceRule hands a letter voice.md, the voice notes the message drafts
// follow: style, never a fact. read is the hash of what it read, which the
// letter the model writes from it records.
func (j *job) voiceRule() (rule, read string) {
	c, err := j.s.d.Workspace.Read(workspace.FileVoice)
	if err != nil || strings.TrimSpace(string(c.Data)) == "" {
		return "", sha(c.Data)
	}
	return dataBlock("voice", trimMarked(string(c.Data), 1500)) + "Write the way the voice notes describe and avoid any word they ban; they state no fact.\n", sha(c.Data)
}

func trimMarked(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " [trimmed]"
}

func retrievalQuery(src sourceSet) string {
	parts := []string{src.Role}
	if src.Posting != "" {
		reqs, kw, _, _ := successProfile(src.Posting)
		parts = append(parts, kw[:min(12, len(kw))]...)
		for i, r := range reqs {
			if i == 3 {
				break
			}
			parts = append(parts, trimMarked(r.Text, 120))
		}
	} else {
		for _, r := range src.Profile.Targets.Roles {
			parts = append(parts, r.Title)
		}
		for _, sk := range src.Profile.Skills {
			parts = append(parts, sk.Name)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// callModel runs one structured call under a task. Cancellation of the run is
// returned as is; every other failure means the model is not usable now.
func (j *job) callModel(ctx context.Context, task, prompt string, schema json.RawMessage, out any, limit time.Duration) error {
	return j.callAs(ctx, systemPrompt, task, prompt, schema, out, limit)
}

func (j *job) callAs(ctx context.Context, system, task, prompt string, schema json.RawMessage, out any, limit time.Duration) error {
	cctx := ctx
	if limit > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	deadline, _ := cctx.Deadline()
	j.lastCall = ""
	resp, err := j.s.d.Model.Structured(cctx, llm.Request{Task: task, System: system, Prompt: prompt, RunID: j.runID, Deadline: deadline}, schema, out)
	if resp != nil {
		j.lastCall = resp.CallID
	}
	switch {
	case err != nil && ctx.Err() != nil:
		return context.Cause(ctx)
	case errors.Is(err, llm.ErrNoRoute), errors.Is(err, llm.ErrUnconfigured):
		j.noModel = true
	}
	return err
}

func (j *job) runGenerate(ctx context.Context) (Result, error) {
	req := j.req
	if err := j.stage(ctx, StageRetrieve); err != nil {
		return Result{}, err
	}
	src, err := j.gather(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := j.stage(ctx, StageProfile); err != nil {
		return Result{}, err
	}
	if err := j.stage(ctx, StageManifest); err != nil {
		return Result{}, err
	}
	m, err := buildManifest(src, req.Mode, req.Pages)
	if err != nil {
		return Result{}, err
	}
	j.keep("evidence-manifest.json", m)
	if err := j.stage(ctx, StageDraft); err != nil {
		return Result{}, err
	}
	v := &version{m: m, req: req, lang: req.Language, paper: req.Paper, pages: req.Pages, posting: src.Posting, retr: src.Retrieval}
	var final ResumeDoc
	if req.Action != ActionCover {
		rd, rg, err := j.draftResume(ctx, m)
		if err != nil {
			return Result{}, err
		}
		v.resume, v.resumeGate, final = &rd, rg, rd
	} else {
		final = j.latestResume(ctx, m)
	}
	if req.Action != ActionResume {
		ld, lg, err := j.draftLetter(ctx, m, final)
		switch {
		case err != nil && ctx.Err() != nil:
			return Result{}, err
		case err != nil && req.Action == ActionCover:
			return Result{}, err
		case err != nil:
			j.warn("the cover letter was not written: %v", err)
		default:
			v.letter, v.letterGate = &ld, lg
		}
	}
	if err := j.stage(ctx, StageReview); err != nil {
		return Result{}, err
	}
	rr, err := j.review(ctx, v)
	if err != nil {
		return Result{}, err
	}
	if err := j.stage(ctx, StageRevise); err != nil {
		return Result{}, err
	}
	if err := j.revise(ctx, v, rr); err != nil {
		return Result{}, err
	}
	v.review = &rr.rec
	return j.finishVersion(ctx, v)
}

// draftResume returns the first draft that passes the gate: the model's, the
// model's with each rejected part replaced by its source text, else the
// deterministic one. Nothing that fails the gate is returned.
func (j *job) draftResume(ctx context.Context, m Manifest) (ResumeDoc, Gate, error) {
	lang := j.req.Language
	if !j.noModel {
		var draft ResumeDraft
		err := j.callModel(ctx, "resume-write", resumePrompt(m, lang, j.req.Pages), resumeSchema, &draft, 0)
		switch {
		case err == nil:
			doc := ResumeDoc{Schema: docSchema, Generation: GenModel, ManifestHash: m.Hash, Language: lang, Limited: m.Limited, ResumeDraft: normalizeResume(draft)}
			g := checkResume(m, doc)
			if g.OK() {
				j.gen = GenModel
				return doc, g, nil
			}
			j.keep("resume-rejected.json", doc)
			if fixed, notes, kept := replaceRejected(m, doc); kept > 0 {
				if fg := checkResume(m, fixed); fg.OK() {
					for _, n := range notes {
						j.warnOn(KindResume, "%s", n)
					}
					j.gen = GenModel
					return fixed, fg, nil
				}
			}
			j.warnOn(KindResume, "the model draft was rejected by the grounding gate (%s); the deterministic builder was used", codes(g))
		case ctx.Err() != nil:
			return ResumeDoc{}, Gate{}, err
		case j.noModel:
		default:
			j.warnOn(KindResume, "no usable model draft (%s); the deterministic builder was used", errKind(err))
		}
	}
	if j.noModel {
		j.warnOn(KindResume, "no model route is configured; the deterministic builder wrote the content")
	}
	doc := deterministicResume(m, lang)
	g := checkResume(m, doc)
	if len(doc.Experience) == 0 && len(doc.Projects) == 0 && doc.Profile == "" && len(doc.Competencies) == 0 {
		g.fail("empty", "the sources hold nothing a résumé can be written from")
	}
	if !g.OK() {
		j.keep("resume-rejected.json", doc)
		return ResumeDoc{}, g, fmt.Errorf("%w: %s", ErrNoValidDraft, codes(g))
	}
	return doc, g, nil
}

func codes(g Gate) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range g.Errors {
		if !seen[f.Code] {
			seen[f.Code] = true
			out = append(out, f.Code)
		}
	}
	return strings.Join(out, ", ")
}

func errKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return string(le.Kind)
	}
	return "error"
}

// inlineIDs are evidence ids a model wrote into its text ("fell to 9 [E12].")
// although the ids belong in the evidence fields; they are never printed.
var inlineIDs = lazyregexp.New(`\s*[\[(]\s*[EC]\d+(?:\s*(?:[,;/&]\s*(?:and\s+)?|\s+and\s+)[EC]\d+)*\s*[\])]`)

// clean normalizes one drafted text and drops the evidence ids written into it.
func clean(s string) string {
	return Normalize(inlineIDs.ReplaceAllString(s, ""))
}

func normalizeResume(d ResumeDraft) ResumeDraft {
	d.Profile = clean(d.Profile)
	for i := range d.Competencies {
		d.Competencies[i].Label, d.Competencies[i].Text = clean(d.Competencies[i].Label), clean(d.Competencies[i].Text)
	}
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for i := range list {
			var keep []Bullet
			for _, b := range list[i].Bullets {
				if b.Text = clean(b.Text); b.Text != "" {
					keep = append(keep, b)
				}
			}
			list[i].Bullets = keep
		}
	}
	return d
}

// draftLetter returns a letter that passes the gate: a model draft, a second
// model draft, then the deterministic letter. r is the final résumé the letter
// pairs with; its cited evidence is what the letter may draw on.
func (j *job) draftLetter(ctx context.Context, m Manifest, r ResumeDoc) (LetterDoc, Gate, error) {
	lang := j.req.Language
	l, _ := lookupLanguage(lang)
	sal := l.salutation
	if m.Recipient != "" && lang == "en" {
		sal = "Dear " + m.Recipient + ","
	}
	voice, voiceRead := j.voiceRule()
	retry := ""
	for attempt := 1; attempt <= 2 && !j.noModel; attempt++ {
		var draft LetterDraft
		err := j.callModel(ctx, "cover-letter", letterPrompt(m, r, lang)+voice+retry, letterSchema, &draft, LetterTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return LetterDoc{}, Gate{}, err
			}
			if !j.noModel {
				j.warnOn(KindCover, "no usable model letter (%s); the deterministic letter was used", errKind(err))
			}
			break
		}
		doc := LetterDoc{Schema: docSchema, Generation: GenModel, ManifestHash: m.Hash, Language: lang, Salutation: sal, VoiceSHA256: voiceRead, LetterDraft: normalizeLetter(draft)}
		if g := checkLetter(m, doc); g.OK() {
			j.gen = GenModel
			return doc, g, nil
		} else {
			j.keep(fmt.Sprintf("cover-letter-rejected-%d.json", attempt), doc)
			j.warnOn(KindCover, "model letter attempt %d was rejected (%s)", attempt, codes(g))
			retry = retryNote(g)
		}
	}
	if j.noModel {
		j.warnOn(KindCover, "no model route is configured; the deterministic letter was written")
	}
	doc, ok := deterministicLetter(m, r, lang)
	if !ok {
		return LetterDoc{}, Gate{}, fmt.Errorf("%w: no letter can be written for language %q without a model", ErrNoValidDraft, lang)
	}
	doc.Salutation = sal
	g := checkLetter(m, doc)
	if !g.OK() {
		j.keep("cover-letter-rejected.json", doc)
		return LetterDoc{}, g, fmt.Errorf("%w: %s", ErrNoValidDraft, codes(g))
	}
	if g.Chars < 1500 {
		j.warnOn(KindCover, "the letter is short because the selected evidence is thin; it was not padded")
	}
	return doc, g, nil
}

// retryNote tells the second letter attempt why the first was refused, so it
// does not write the same sentence again. The findings quote only the draft
// and the evidence, which the prompt already holds.
func retryNote(g Gate) string {
	var b strings.Builder
	b.WriteString("An earlier draft was refused for the reasons below. Write a new letter that gives none of them:\n")
	for _, f := range g.Errors {
		b.WriteString(dataBlock("refused", f.Code+": "+f.Detail))
	}
	return b.String()
}

func normalizeLetter(d LetterDraft) LetterDraft {
	d.Opening, d.Body, d.Closing = clean(d.Opening), clean(d.Body), clean(d.Closing)
	for i := range d.Bullets {
		d.Bullets[i].Text = clean(d.Bullets[i].Text)
	}
	return d
}

// latestResume is the résumé a letter-only run pairs with: the candidacy's
// newest stored one, when it was written for this run's manifest. Anything
// else leaves the letter to the manifest's selected evidence.
func (j *job) latestResume(ctx context.Context, m Manifest) ResumeDoc {
	s := j.s
	docs, err := s.List(ctx, j.req.CandidacyID)
	if err != nil {
		return ResumeDoc{}
	}
	for _, d := range docs {
		if d.Kind != KindResume {
			continue
		}
		dir, err := s.versionDir(d.Path)
		if err != nil {
			return ResumeDoc{}
		}
		raw, err := readRegular(filepath.Join(dir, "resume.json"), 4<<20)
		var rd ResumeDoc
		if err != nil || sha(raw) != d.ContentHash || json.Unmarshal(raw, &rd) != nil || rd.ManifestHash != m.Hash {
			j.warnOn(KindCover, "the latest stored résumé was written for other sources; the letter draws on the selected evidence")
			return ResumeDoc{}
		}
		return rd
	}
	return ResumeDoc{}
}
