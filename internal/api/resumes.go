package api

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

const maxRankField = 500

// resumeRosterError maps a roster read onto the API errors. A missing or
// unusable file is not_found for the file route: the id names nothing to serve.
func resumeRosterError(err error) error {
	var wsErr *workspace.Error
	if errors.As(err, &wsErr) {
		switch wsErr.Code {
		case workspace.CodeFileMissing, workspace.CodeInvalidFormat, workspace.CodeOutOfRange, workspace.CodeConstraint, workspace.CodePathOutside:
			return apikit.NewErr(wire.CodeNotFound, "that résumé has no usable file")
		}
	}
	return configError(err)
}

func (s *Server) listResumes(c *apikit.Call) (apikit.Reply, error) {
	roster, err := s.cfg.Workspace.Roster()
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Data: WireResumes(roster)}, nil
}

func WireResumes(roster []workspace.RosterEntry) wire.ResumeList {
	out := wire.ResumeList{Resumes: make([]wire.ResumeEntry, 0, len(roster))}
	for _, r := range roster {
		out.Resumes = append(out.Resumes, wire.ResumeEntry{ID: r.ID, Label: r.Label, Tags: apikit.NonNil(r.Tags), Default: r.Default,
			Description: r.Description, FileName: r.FileName, Size: apikit.Safe64(r.Size)})
	}
	return out
}

// resumeFile serves the PDF of one roster entry and nothing else: the id picks
// an entry, and the workspace refuses any file outside resumes/.
func (s *Server) resumeFile(c *apikit.Call) (apikit.Reply, error) {
	// In a demo workspace the upload of the fictional persona's document is
	// decided and recorded by the demo gate; the caller names the page in
	// `for`.
	if s.cfg.Workspace.IsDemo() {
		host := ""
		if u, err := url.Parse(c.R.URL.Query().Get("for")); err == nil {
			host = u.Hostname()
		}
		d := gates.DemoWorkspace(gates.DemoInput{Demo: true, Action: gates.DemoUpload, Host: host})
		_ = s.store.Load().GuardDemo(c.R.Context(), gates.DemoUpload, host) // records the decision
		if !d.Allowed() {
			return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "%s", d.Reason), wire.ReasonDemoWorkspace)
		}
	}
	e, data, err := s.cfg.Workspace.ReadResume(c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, resumeRosterError(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": e.FileName}))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(data)
	c.Handled = true
	return apikit.Reply{}, nil
}

func (s *Server) rankResumes(c *apikit.Call) (apikit.Reply, error) {
	var req wire.ResumeRankRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{{"title", req.Title, maxRankField}, {"company", req.Company, maxRankField}, {"description", req.Description, documents.MaxRankDescription}} {
		if n := utf8.RuneCountInString(f.value); n > f.max {
			return apikit.Reply{}, apikit.InvalidField(f.name, "%s must be at most %d characters", f.name, f.max)
		}
	}
	roster, err := s.cfg.Workspace.Roster()
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	plain := make([]workspace.Resume, len(roster))
	for i, r := range roster {
		plain[i] = r.Resume
	}
	ranked := documents.RankResumes(plain, documents.RankPosting{Title: req.Title, Company: req.Company, Description: req.Description})
	out := wire.ResumeRank{Chosen: ranked.Chosen, Ranking: make([]wire.ResumeRankItem, len(ranked.Ranked))}
	for i, r := range ranked.Ranked {
		out.Ranking[i] = wire.ResumeRankItem{ID: r.ID, Score: apikit.Safe(r.Score), Reason: r.Reason}
	}
	return apikit.Reply{Data: out}, nil
}
