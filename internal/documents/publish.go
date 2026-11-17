package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

const artifactKind = "document"

// manifestFile is a version's first published file, so its artifact row's path.
const manifestFile = "document-manifest.json"

// version is everything one published version holds before it is written.
type version struct {
	m          Manifest
	req        Request
	lang       string
	paper      assets.Paper
	pages      int
	posting    string
	retr       Retrieval
	resume     *ResumeDoc
	letter     *LetterDoc
	resumeGate Gate
	letterGate Gate
	review     *ReviewRecord
	// carried holds the files of an existing version that a PDF run copies
	// forward unchanged; from is that version's documents.
	carried map[string][]byte
	from    []Document
	bundle  string
	// tight selects the résumé template's spacing adjustment.
	tight bool
	// repair is the record of the layout repair rounds, when any ran. A kind
	// in repaired has content that differs from what was drafted.
	repair   *RepairRecord
	repaired map[Kind]bool
}

// built is the compiled state of one document of a version.
type built struct {
	state    string
	out      toolchain.BuildOutput
	layout   toolchain.LayoutExpect
	finals   []toolchain.Check
	warnings []string
	// srcHash is the hash of the structured content this PDF was rendered
	// from; a repair round refuses content that no longer hashes to it.
	srcHash string
	// kept is the verdict of a PDF taken over from the version this one was
	// loaded from, and tex the TeX that PDF was built from.
	kept *Verification
	tex  []byte
}

// pdfBuilder is the part of the toolchain a pair build uses.
type pdfBuilder interface {
	Build(ctx context.Context, in toolchain.BuildInput) (toolchain.BuildOutput, error)
}

// texSet holds, per document, the TeX as last rendered and what its final PDF
// must show.
type texSet struct {
	tex    map[Kind][]byte
	expect map[Kind]Expectation
}

func (j *job) finishVersion(ctx context.Context, v *version) (Result, error) {
	s := j.s
	now := s.d.Now()
	ts := &texSet{tex: map[Kind][]byte{}, expect: map[Kind]Expectation{}}
	for _, k := range j.kinds(v) {
		if err := render(v, ts, k, now); err != nil {
			return Result{}, err
		}
	}
	builds := map[Kind]*built{}
	for _, d := range v.from {
		if b := j.s.keptPDF(ctx, d, v); b != nil {
			builds[d.Kind], ts.tex[d.Kind] = b, b.tex
		}
	}
	var err error
	if j.req.PDF {
		if err := j.stage(ctx, StageCompile); err != nil {
			return Result{}, err
		}
		if builds, err = j.compile(ctx, v, ts, builds, now); err != nil {
			return Result{}, err
		}
		if err := j.stage(ctx, StageVerify); err != nil {
			return Result{}, err
		}
	}
	if err := j.stage(ctx, StagePublish); err != nil {
		return Result{}, err
	}
	files, err := v.files(j, ts.tex[KindResume], ts.tex[KindCover], builds, now)
	if err != nil {
		return Result{}, err
	}
	ids, err := j.publish(ctx, v, files, builds, now)
	if err != nil {
		return Result{}, err
	}
	return Result{Published: true, Documents: ids, Generation: j.gen, Retrieval: v.retr}, nil
}

// render expands the template of one document from its current structured
// content. The final checks hold the PDF to what the template was given to
// print.
func render(v *version, ts *texSet, k Kind, now time.Time) error {
	if k == KindResume {
		r, err := buildResume(v.m, *v.resume, v.paper)
		if err != nil {
			return err
		}
		r.Tight = v.tight
		tex, err := assets.RenderResume(r)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		ts.tex[k], ts.expect[k] = tex, expectResume(r)
		return nil
	}
	c, err := buildCover(v.m, *v.letter, v.paper, now)
	if err != nil {
		return err
	}
	tex, err := assets.RenderCover(c)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ts.tex[k], ts.expect[k] = tex, expectCover(c)
	return nil
}

// compile builds each document builds holds no PDF for within the pair budget,
// then repairs the layout defects a build shows. A cancelled run stops; a tool that is missing or a
// build that cannot finish leaves that document without a PDF and says why.
// Neither blocks the draft.
func (j *job) compile(ctx context.Context, v *version, ts *texSet, out map[Kind]*built, now time.Time) (map[Kind]*built, error) {
	tc, err := j.toolchain(ctx)
	if err != nil {
		var unavailable *toolchain.UnavailableError
		if errors.As(err, &unavailable) {
			j.warn("no PDF was made: %v", err)
			for _, k := range j.kinds(v) {
				if out[k] == nil {
					out[k] = &built{state: PDFUnavailable}
				}
			}
			return out, nil
		}
		return nil, err
	}
	var b pdfBuilder = tc
	if j.s.builderFor != nil {
		b = j.s.builderFor(tc)
	}
	bctx, cancel := context.WithTimeout(ctx, j.s.pairBudget)
	defer cancel()
	for _, k := range j.kinds(v) {
		if out[k] != nil {
			continue
		}
		bt, err := j.buildOne(ctx, bctx, b, v, ts, k)
		if err != nil {
			return nil, err
		}
		out[k] = bt
	}
	if err := j.repair(ctx, bctx, b, v, ts, out, now); err != nil {
		return nil, err
	}
	// A defect a repair removed is in the record, not in the warnings.
	for _, k := range j.kinds(v) {
		for _, w := range out[k].warnings {
			j.warnOn(k, "%s: %s", k, w)
		}
	}
	return out, nil
}

// buildOne compiles and verifies one document from its rendered TeX. Only the
// cancellation of the run is an error.
func (j *job) buildOne(ctx, bctx context.Context, b pdfBuilder, v *version, ts *texSet, k Kind) (*built, error) {
	in := toolchain.BuildInput{Name: string(k), TeXSHA256: sha(ts.tex[k])}
	var srcHash string
	if k == KindResume {
		in.Engine, in.TeX = toolchain.EngineLuaLaTeX, ts.tex[k]
		in.Expect = toolchain.PDFExpect{MaxPages: v.pages, MinChars: 150, ASCIIDates: true}
		in.Layout = toolchain.LayoutExpect{Template: toolchain.TemplateResume, Paper: v.paper}
		srcHash = sha(jsonBytes(v.resume))
	} else {
		in.Engine, in.TeX, in.Cover = toolchain.EngineXeLaTeX, ts.tex[k], true
		in.Expect = toolchain.PDFExpect{Pages: 1, MinChars: 150, ASCIIDates: true}
		in.Layout = toolchain.LayoutExpect{Template: toolchain.TemplateCover, Paper: v.paper}
		srcHash = sha(jsonBytes(v.letter))
	}
	o, err := b.Build(bctx, in)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		j.warnOn(k, "%s: no PDF could be verified: %v", k, firstLine(err))
		return &built{state: PDFUnavailable}, nil
	}
	bt := &built{out: o, layout: in.Layout, finals: FinalChecks(ts.expect[k], o), srcHash: srcHash}
	bt.state, bt.warnings = pdfState(o, bt.finals)
	return bt, nil
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

func (j *job) toolchain(ctx context.Context) (*toolchain.Toolchain, error) {
	if j.req.toolchain != nil {
		return j.req.toolchain, nil
	}
	if j.s.d.Toolchain == nil {
		return nil, &toolchain.UnavailableError{Report: toolchain.Report{State: toolchain.StateUnavailable, Missing: []string{"toolchain"}}}
	}
	return j.s.d.Toolchain(ctx)
}

func (j *job) kinds(v *version) []Kind {
	var out []Kind
	if v.resume != nil {
		out = append(out, KindResume)
	}
	if v.letter != nil {
		out = append(out, KindCover)
	}
	return out
}

func jsonBytes(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

// bundleName is the ASCII slug of company, role and date, at most 80
// characters, or baseline-<date>.
func bundleName(m Manifest, date string) string {
	if m.Mode == ModeBaseline {
		return "baseline-" + date
	}
	head := slug(m.Company + " " + m.Role)
	if head == "" {
		head = "document"
	}
	if room := 80 - len(date) - 1; len(head) > room {
		head = strings.Trim(head[:room], "-")
	}
	return head + "-" + date
}

func slug(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// files assembles every file of the version. The binding document-manifest.json
// comes first and names the hash of all the others.
func (v *version) files(j *job, resumeTeX, coverTeX []byte, builds map[Kind]*built, now time.Time) ([]store.PublishFile, error) {
	f := map[string][]byte{}
	for name, b := range v.carried {
		f[name] = b
	}
	fresh := v.carried == nil
	if fresh {
		f["posting.md"] = []byte(postingMarkdown(v))
		f["company-research.md"] = []byte(companyMarkdown(v.m))
		f["success-profile.md"] = []byte(successMarkdown(v.m))
		f["resume-selection-manifest.md"] = []byte(selectionMarkdown(v.m))
		f["resume-materials.md"] = []byte(materialsMarkdown(v.m))
		f["evidence-manifest.json"] = jsonBytes(v.m)
		f["document-review.json"] = jsonBytes(v.review)
	}
	if fresh || v.repair != nil {
		f["application-strategy.md"] = []byte(strategyMarkdown(v.m, j.gen, v.repair))
	}
	if v.repair != nil {
		f[repairFile] = jsonBytes(v.repair)
	}
	if v.resume != nil && (fresh || v.repaired[KindResume]) {
		f["resume.json"] = jsonBytes(v.resume)
		f["resume.md"] = []byte(resumeMarkdown(v.m, *v.resume))
		f["resume-quality-gate.md"] = []byte(gateMarkdown("Résumé quality gate", v.m, v.resumeGate))
	}
	if v.letter != nil && (fresh || v.repaired[KindCover]) {
		f["cover-letter.json"] = jsonBytes(v.letter)
		f["cover-letter.md"] = []byte(letterMarkdown(v.m, *v.letter))
		f["cover-letter-quality-gate.md"] = []byte(gateMarkdown("Cover letter quality gate", v.m, v.letterGate))
	}
	if resumeTeX != nil {
		f["resume.tex"] = resumeTeX
	}
	if coverTeX != nil {
		f["cover-letter.tex"] = coverTeX
	}
	verification := verificationFileBody{SchemaVersion: verificationSchema, Verifications: map[Kind]Verification{}}
	for k, b := range builds {
		if len(b.out.PDF) == 0 {
			continue
		}
		f[string(k)+".pdf"] = b.out.PDF
		f[string(k)+".txt"] = []byte(b.out.Text)
		for _, p := range b.out.Previews {
			f[p.Name] = p.PNG
		}
		if b.kept != nil {
			verification.Verifications[k] = *b.kept
		} else {
			verification.Verifications[k] = newVerification(k, b, sha(f[string(k)+".json"]), v.m.Hash, b.layout)
		}
	}
	if len(verification.Verifications) > 0 {
		f[verificationFile] = jsonBytes(verification)
	}
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	hashes := map[string]string{}
	for _, n := range names {
		hashes[n] = sha(f[n])
	}
	manifest := map[string]any{
		"schemaVersion": 1, "bundle": v.bundle, "mode": v.m.Mode, "limitedContext": v.m.Limited, "language": v.lang,
		"paper": v.paper, "pageLimit": v.pages, "generation": j.gen, "inputs": v.m.Inputs, "evidenceManifestHash": v.m.Hash,
		"templates": templateRecord(), "files": hashes, "publication": publicationOf(builds), "run": j.runID,
		"createdAt": now.UTC().Format(time.RFC3339), "engines": enginesOf(builds), "layoutRepair": repairSummary(v.repair, v.tight),
	}
	out := []store.PublishFile{{Path: manifestFile, Content: jsonBytes(manifest)}}
	for _, n := range names {
		out = append(out, store.PublishFile{Path: n, Content: f[n]})
	}
	return out, nil
}

func publicationOf(builds map[Kind]*built) map[string]string {
	out := map[string]string{}
	for k, b := range builds {
		out[string(k)] = b.state
	}
	if len(out) == 0 {
		out["state"] = "draft"
	}
	return out
}

func enginesOf(builds map[Kind]*built) map[string]any {
	out := map[string]any{}
	for k, b := range builds {
		if len(b.out.PDF) > 0 {
			out[string(k)] = map[string]any{"verifyPdfScript": b.out.Verify.ScriptVersion, "verifyLayoutScript": b.out.Layout.ScriptVersion,
				"python": b.out.Verify.Python, "poppler": b.out.Layout.Poppler, "passes": b.out.Passes, "texSha256": b.out.TeXSHA256}
		}
	}
	return out
}

// templateRecord names the template version and the hash of each shipped
// template file the version was rendered from.
func templateRecord() map[string]any {
	hashes := map[string]string{}
	for _, n := range []string{"resume.tex", "cover.tex", "cover.cls"} {
		if b, err := assets.Read(n); err == nil {
			hashes[n] = sha(b)
		}
	}
	return map[string]any{"version": assets.TemplateVersion, "hashes": hashes}
}

// publish writes the version atomically: the files, the document rows and the
// tracker flags commit together or not at all.
func (j *job) publish(ctx context.Context, v *version, pf []store.PublishFile, builds map[Kind]*built, now time.Time) ([]int64, error) {
	s := j.s
	date := now.Format("2006-01-02")
	if v.bundle == "" {
		v.bundle = bundleName(v.m, date)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		n, err := s.nextVersion(ctx, v.bundle)
		if err != nil {
			return nil, err
		}
		dir := path.Join("documents", v.bundle, fmt.Sprint(n))
		files := make([]store.PublishFile, len(pf))
		for i, f := range pf {
			files[i] = store.PublishFile{Path: path.Join(dir, f.Path), Content: f.Content}
		}
		var ids []int64
		_, err = s.d.Store.PublishArtifactTx(ctx, s.d.Workspace.Root, artifactKind,
			func(int64) ([]store.PublishFile, error) { return files, nil },
			func(tx *sql.Tx, _ store.Artifact) error {
				if s.beforeCommit != nil {
					s.beforeCommit()
				}
				var err error
				ids, err = j.insertRows(ctx, tx, v, dir, n, pf, builds, now)
				return err
			})
		if err == nil {
			return ids, nil
		}
		lastErr = err
		if !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	return nil, lastErr
}

func (j *job) insertRows(ctx context.Context, tx *sql.Tx, v *version, dir string, n int, pf []store.PublishFile, builds map[Kind]*built, now time.Time) ([]int64, error) {
	content := map[string][]byte{}
	for _, f := range pf {
		content[f.Path] = f.Content
	}
	var cand any
	if v.req.CandidacyID > 0 {
		cand = v.req.CandidacyID
	}
	var ids []int64
	for _, k := range j.kinds(v) {
		src := string(k) + ".json"
		gen := string(j.gen)
		if k == KindCover && v.letter != nil {
			gen = string(v.letter.Generation)
		}
		if k == KindResume && v.resume != nil {
			gen = string(v.resume.Generation)
		}
		state, pdfPath, pdfSHA, pages := PDFNone, any(nil), any(nil), any(nil)
		warns := j.warningsFor(k)
		if b := builds[k]; b != nil {
			state = b.state
			if len(b.out.PDF) > 0 {
				pdfPath, pdfSHA, pages = path.Join(dir, string(k)+".pdf"), b.out.PDFSHA256, b.out.Pages
			}
		}
		warnJSON, _ := json.Marshal(warns)
		res, err := tx.ExecContext(ctx, `INSERT INTO documents
			(bundle, version, kind, mode, candidacy_id, path, source_path, content_hash, manifest_hash, generation, language, paper, page_limit, limited,
			 pdf_state, pdf_path, pdf_sha256, pages, warnings_json, run_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			v.bundle, n, string(k), string(v.m.Mode), cand, dir, path.Join(dir, src), sha(content[src]), v.m.Hash, gen, v.lang, string(v.paper),
			v.pages, boolInt(v.m.Limited), state, pdfPath, pdfSHA, pages, string(warnJSON), j.runID, now.UTC().Format(time.RFC3339))
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if v.req.ProposalID > 0 {
		if err := acceptIn(ctx, tx, v.req.ProposalID, now.UTC().Format(time.RFC3339)); err != nil {
			return nil, err
		}
	}
	if id, ok := cand.(int64); ok {
		actor := v.req.Actor
		if actor == "" {
			actor = tracker.ActorUser
		}
		if v.letter != nil {
			if _, err := j.s.d.Tracker.SetDocumentFlagIn(ctx, tx, id, actor, tracker.DocCover, true); err != nil {
				return nil, err
			}
		}
		if b := builds[KindResume]; b != nil && len(b.out.PDF) > 0 {
			if _, err := j.s.d.Tracker.SetDocumentFlagIn(ctx, tx, id, actor, tracker.DocResume, true); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
