package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"

	"applysmith/internal/interview"
	"applysmith/internal/kb"
	"applysmith/internal/tracker"
)

const maxBundleFile = 400_000

// runInterview builds the next prep pack revision for a stage: gather the
// frozen application dossier, build, store.
func (j *job) runInterview(ctx context.Context) (Result, error) {
	s := j.s
	if err := j.stage(ctx, StageRetrieve); err != nil {
		return Result{}, err
	}
	stage, err := s.iv.Stage(ctx, j.req.CandidacyID, j.req.Stage)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	in, err := s.interviewInput(ctx, j.req.CandidacyID, stage)
	if err != nil {
		return Result{}, err
	}
	in.RunID = j.runID
	if err := j.stage(ctx, StageDraft); err != nil {
		return Result{}, err
	}
	pack, err := s.iv.Build(ctx, in)
	if err != nil {
		return Result{}, err
	}
	j.warnings = append(j.warnings, pack.Warnings...)
	if err := j.stage(ctx, StagePublish); err != nil {
		return Result{}, err
	}
	saved, err := s.iv.Save(ctx, pack)
	if err != nil {
		return Result{}, err
	}
	return Result{Published: true, Generation: Generation(saved.Generation),
		Pack: &PackRef{Candidacy: saved.Candidacy, Stage: saved.Stage, Revision: saved.Revision}}, nil
}

// pickVersion prefers the version attached to a submitted application, and
// otherwise the newest.
func pickVersion(docs []Document, kind Kind) *Document {
	var newest *Document
	for i := range docs {
		d := &docs[i]
		if d.Kind != kind {
			continue
		}
		if d.FrozenAt != "" {
			return d
		}
		if newest == nil {
			newest = d
		}
	}
	return newest
}

func versionState(d *Document) string {
	if d.FrozenAt != "" {
		return interview.StateFrozen
	}
	return interview.StateCurrent
}

// bundleFile reads one file of a version directory; a missing or unreadable
// file reads as empty.
func (s *Service) bundleFile(d *Document, name string) string {
	dir, err := s.versionDir(d.Path)
	if err != nil {
		return ""
	}
	b, err := readRegular(filepath.Join(dir, name), maxBundleFile)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *Service) versionDoc(d *Document, file, label string) interview.Doc {
	return interview.Doc{Text: s.bundleFile(d, file), Ref: path.Join(d.Path, file), Label: fmt.Sprintf("%s, version %d", label, d.Version), State: versionState(d)}
}

// submittedResume is the résumé text a mock answer is checked against.
func (s *Service) submittedResume(ctx context.Context, candidacy int64) (string, error) {
	docs, err := s.List(ctx, candidacy)
	if err != nil {
		return "", err
	}
	if d := pickVersion(docs, KindResume); d != nil {
		return s.bundleFile(d, "resume.md"), nil
	}
	return "", nil
}

// interviewInput collects what a pack rests on. The documents of the
// candidacy carry the posting, the requirement ranking and the company facts
// as they were when the pair was made, so a submitted application gives the
// frozen copies; without any document the evaluation report and the knowledge
// base are read now and marked current.
func (s *Service) interviewInput(ctx context.Context, cand int64, stage interview.Stage) (interview.Input, error) {
	e, err := s.candidacy(ctx, cand)
	if err != nil {
		return interview.Input{}, err
	}
	in := interview.Input{Candidacy: cand, Stage: stage, Company: Normalize(e.Company), Role: Normalize(e.Role), PostingKind: interview.KindDossier}
	docs, err := s.List(ctx, cand)
	if err != nil {
		return in, err
	}
	resume, cover := pickVersion(docs, KindResume), pickVersion(docs, KindCover)
	if resume != nil {
		in.Resume = s.versionDoc(resume, "resume.md", "Submitted résumé")
	}
	if cover != nil {
		in.Cover = s.versionDoc(cover, "cover-letter.md", "Submitted cover letter")
	}
	bundle := resume
	if bundle == nil {
		bundle = cover
	}
	if bundle != nil {
		in.Posting = s.versionDoc(bundle, "posting.md", "Job description")
		in.BriefState = versionState(bundle)
		var m Manifest
		if json.Unmarshal([]byte(s.bundleFile(bundle, "evidence-manifest.json")), &m) == nil {
			for _, r := range m.Requirements {
				in.Requirements = append(in.Requirements, r.Text)
			}
			for _, c := range m.CompanyFacts {
				in.Facts = append(in.Facts, interview.Fact{Text: c.Text, Source: c.Source})
			}
		}
		return in, nil
	}
	text, _, err := s.postingFor(ctx, Request{CandidacyID: cand})
	switch {
	case err == nil:
		in.PostingKind, in.ReportNumber = interview.KindReport, reportNumber(e)
		in.Posting = interview.Doc{Text: text, Ref: fmt.Sprintf("report:%d", in.ReportNumber), Label: "Evaluation report", State: interview.StateCurrent}
		reqs, _, _, _ := successProfile(text)
		for _, r := range reqs {
			in.Requirements = append(in.Requirements, r.Text)
		}
	case !errors.Is(err, ErrNoPosting):
		return in, err
	}
	in.Facts, in.BriefState = s.currentBrief(ctx, in.Company, in.Role), interview.StateUnverified
	return in, nil
}

func reportNumber(e tracker.Entry) int64 {
	if e.ReportNumber != nil {
		return *e.ReportNumber
	}
	return 0
}

// currentBrief reads the company brief now, for a candidacy with no documents.
func (s *Service) currentBrief(ctx context.Context, company, role string) []interview.Fact {
	if s.d.Retriever == nil || company == "" {
		return nil
	}
	res, err := s.d.Retriever.Search(ctx, kb.Query{Query: company + " " + role, Kinds: []string{kb.KindBrief}, K: 4})
	if err != nil {
		return nil
	}
	var out []interview.Fact
	for _, f := range res.Facts {
		if f.SourceKind == kb.KindBrief && briefAbout(f, company) {
			out = append(out, interview.Fact{Text: f.Text, Source: f.SourceRef})
		}
	}
	return out
}
