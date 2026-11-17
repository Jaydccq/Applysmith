package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/cvparse"
	"applysmith/internal/workspace"
)

// editableFiles are the workspace files the profile and Discovery pages edit.
// The answer bank has its own routes, and the policy file has its own
// session-only route.
var editableFiles = map[workspace.File]bool{
	workspace.FileProfile: true, workspace.FileCV: true, workspace.FileNarrative: true, workspace.FileVoice: true, workspace.FilePortals: true,
}

// reasonFileExists tells an upload that a file of that name is already stored,
// so the page can choose another name instead of reading a validation failure.
const reasonFileExists = "file_exists"

func configFile(c *apikit.Call) (workspace.File, *apikit.Error) {
	f, err := workspace.ParseFile(c.R.PathValue("file"))
	if err != nil || !editableFiles[f] {
		return "", apikit.NewErr(wire.CodeNotFound, "no editable file is called %q", c.R.PathValue("file"))
	}
	return f, nil
}

// configError maps a workspace failure onto the API errors. A refused edit
// names every finding, up to the workspace's own cap, so the page can show them.
func configError(err error) error {
	var invalid *workspace.InvalidError
	var conflict *workspace.ConflictError
	var wsErr *workspace.Error
	switch {
	case errors.As(err, &invalid):
		e := apikit.NewErr(wire.CodeValidationFailed, "%s", invalid.Report.String())
		if f := issueFields(invalid.Report); len(f) > 0 {
			e.Details = &wire.ErrorDetails{Fields: f}
		}
		return e
	case errors.As(err, &conflict):
		return apikit.NewErr(wire.CodeRevisionConflict, "%s changed on disk; reload it and apply the change again", conflict.File)
	case errors.As(err, &wsErr):
		switch wsErr.Code {
		case workspace.CodePayloadTooLarge:
			return apikit.NewErr(wire.CodePayloadTooLarge, "%s", wsErr.Message)
		case workspace.CodeFileMissing:
			return apikit.NewErr(wire.CodeNotFound, "%s", wsErr.Message)
		case workspace.CodeFileExists:
			return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", wsErr.Message), reasonFileExists)
		}
		return apikit.NewErr(wire.CodeValidationFailed, "%s", wsErr.Message)
	}
	return err
}

// issueFields words the first finding of each path for the field the page
// shows it beside: what to enter, without the file position or the code.
func issueFields(r workspace.Report) map[string]string {
	out := map[string]string{}
	for _, i := range r.Issues {
		if _, seen := out[i.Path]; seen || i.Path == "" {
			continue
		}
		m := i.Message
		switch {
		case i.Code == workspace.CodeRequired:
			m = "fill this in"
		case i.Code == workspace.CodeNotAllowed && len(i.Allowed) > 0:
			m = "choose one of " + strings.Join(i.Allowed, ", ")
		case strings.HasPrefix(m, "expected "):
			m, _, _ = strings.Cut("enter "+strings.TrimPrefix(m, "expected "), ", found ")
		}
		m = plainWords.Replace(m)
		out[i.Path] = strings.ToUpper(m[:1]) + m[1:] + "."
	}
	return out
}

var plainWords = strings.NewReplacer("an integer", "a whole number", "integer", "whole number", "in the range ", "from ")

func toConfigIssues(r workspace.Report) []wire.ConfigIssue {
	out := make([]wire.ConfigIssue, 0, len(r.Issues))
	for _, i := range r.Issues {
		out = append(out, wire.ConfigIssue{Path: i.Path, Line: i.Line, Code: i.Code, Message: i.Message})
	}
	return out
}

// plainYAML turns a decoded document into values JSON can carry: a date the
// parser read as a timestamp goes back to its calendar text.
func plainYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = plainYAML(x)
		}
	case []any:
		for i, x := range t {
			t[i] = plainYAML(x)
		}
	case time.Time:
		return t.Format("2006-01-02")
	}
	return v
}

func (s *Server) getConfig(c *apikit.Call) (apikit.Reply, error) {
	f, e := configFile(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	return s.readConfig(f)
}

// configBackups lists the earlier contents the workspace keeps of a file.
func (s *Server) configBackups(c *apikit.Call) (apikit.Reply, error) {
	f, e := configFile(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	kept, err := s.cfg.Workspace.Backups(f)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ConfigBackupList{Backups: []wire.ConfigBackup{}}
	for _, b := range kept {
		out.Backups = append(out.Backups, wire.ConfigBackup{SavedAt: wire.Time{Time: b.SavedAt}, Content: string(b.Data)})
	}
	return apikit.Reply{Data: out}, nil
}

// getPolicyConfig reads policy.yml with its revision for the Settings page.
func (s *Server) getPolicyConfig(c *apikit.Call) (apikit.Reply, error) {
	return s.readConfig(workspace.FilePolicy)
}

func (s *Server) readConfig(f workspace.File) (apikit.Reply, error) {
	ws := s.cfg.Workspace
	cur, err := ws.Read(f)
	if err != nil {
		return apikit.Reply{}, err
	}
	doc := wire.ConfigDocument{File: string(f), Exists: cur.Revision != workspace.RevisionAbsent, Content: string(cur.Data), Revision: cur.Revision, Issues: []wire.ConfigIssue{}}
	if doc.Exists {
		doc.Template = workspace.IsTemplate(f, cur.Data)
		doc.Issues = toConfigIssues(ws.Validate(f, cur.Data))
	}
	if doc.Exists && len(doc.Issues) == 0 {
		switch f {
		case workspace.FileProfile, workspace.FilePortals:
			var values map[string]any
			if yaml.Unmarshal(cur.Data, &values) == nil {
				doc.Values = plainYAML(values).(map[string]any)
			}
		case workspace.FilePolicy:
			// The effective policy: keys the file leaves out show their defaults.
			pol, _ := workspace.ParsePolicy(cur.Data)
			if raw, err := yaml.Marshal(pol); err == nil {
				var values map[string]any
				if yaml.Unmarshal(raw, &values) == nil {
					doc.Values = values
				}
			}
		}
	}
	return apikit.Reply{Data: doc}, nil
}

func (s *Server) putConfig(c *apikit.Call) (apikit.Reply, error) {
	f, e := configFile(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.ConfigSave
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.ExpectedRevision == "" {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "expectedRevision is required; read the file first and quote its revision")
	}
	before := s.openBefore(c, f)
	saved, err := s.cfg.Workspace.Write(f, []byte(req.Content), req.ExpectedRevision)
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	s.moveAfter(c, f, before, saved.Changed)
	return apikit.Reply{Data: wire.ConfigSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}

// openBefore counts the waiting runs' open questions before a save to the
// profile, which the fill answers from, so moveAfter can tell the runs the
// save answered: confirming a relationship list or adding a job answers what
// a run left as surely as a bank entry does.
func (s *Server) openBefore(c *apikit.Call, f workspace.File) map[string]int {
	if f != workspace.FileProfile {
		return nil
	}
	return s.apply.OpenQuestions(c.R.Context())
}

// moveAfter continues the waiting runs a changed save answered.
func (s *Server) moveAfter(c *apikit.Call, f workspace.File, before map[string]int, changed bool) {
	if changed && before != nil {
		s.apply.MoveAnswered(c.R.Context(), before, "", applyapi.DoneFromDashboard)
	}
}

var opKinds = map[string]workspace.OpKind{
	"set": workspace.OpSet, "unset": workspace.OpUnset, "append": workspace.OpAppend, "remove": workspace.OpRemove,
}

func (s *Server) patchConfig(c *apikit.Call) (apikit.Reply, error) {
	f, e := configFile(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	return s.editConfig(c, f)
}

// patchPolicy edits policy.yml by key path. The route admits the dashboard
// session only.
func (s *Server) patchPolicy(c *apikit.Call) (apikit.Reply, error) {
	if c.P.Kind != apikit.PrincipalDashboard || !fromLoopback(c) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "the policy is edited from the dashboard on this computer")
	}
	return s.editConfig(c, workspace.FilePolicy)
}

func (s *Server) editConfig(c *apikit.Call, f workspace.File) (apikit.Reply, error) {
	if !f.IsYAML() {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "structured edits apply to the YAML files only")
	}
	var req wire.ConfigPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.ExpectedRevision == "" || len(req.Ops) == 0 {
		return apikit.Reply{}, apikit.NewErr(wire.CodeValidationFailed, "expectedRevision and at least one operation are required")
	}
	ops := make([]workspace.Op, 0, len(req.Ops))
	for _, o := range req.Ops {
		kind, ok := opKinds[o.Op]
		if !ok {
			return apikit.Reply{}, apikit.InvalidField("op", "op must be set, unset, append or remove")
		}
		ops = append(ops, workspace.Op{Kind: kind, Path: o.Path, Value: o.Value, Index: o.Index, Match: o.Match})
	}
	before := s.openBefore(c, f)
	saved, err := s.cfg.Workspace.Edit(f, ops, req.ExpectedRevision)
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	s.moveAfter(c, f, before, saved.Changed)
	return apikit.Reply{Data: wire.ConfigSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}

func (s *Server) uploadResume(c *apikit.Call) (apikit.Reply, error) {
	data, err := io.ReadAll(c.R.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "a résumé PDF is limited to 10 MiB")
		}
		return apikit.Reply{}, err
	}
	rel, err := s.cfg.Workspace.StoreResume(apikit.QueryOf(c).Str("name"), data)
	if err != nil {
		return apikit.Reply{}, configError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Data: wire.ResumeUploaded{File: rel}}, nil
}

// cvDraft serves what the CV parser finds in cv.md. It stores nothing: the
// page saves an entry only after the user confirms it, through the profile
// patch. A field the CV does not state stays empty.
func (s *Server) cvDraft(c *apikit.Call) (apikit.Reply, error) {
	cur, err := s.cfg.Workspace.Read(workspace.FileCV)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.CVDraft{Revision: cur.Revision, Employment: []wire.CVJob{}, Education: []wire.CVStudy{}, Skills: []string{}, Identity: map[string]string{}}
	if cur.Revision != workspace.RevisionAbsent {
		out.Identity = answerbank.IdentityFromCV(string(cur.Data))
		d := cvparse.Parse(string(cur.Data))
		for _, j := range d.Employment {
			out.Employment = append(out.Employment, wire.CVJob{Employer: j.Employer, Title: j.Title, Start: j.Start, End: j.End})
		}
		for _, e := range d.Education {
			out.Education = append(out.Education, wire.CVStudy{School: e.School, Degree: e.Degree, Field: e.Field, Start: e.Start, End: e.End})
		}
		out.Skills = append(out.Skills, d.Skills...)
	}
	out.Dismissed = s.cvDraftDismissed(c)
	return apikit.Reply{Data: out}, nil
}

// cvDraftDismissedMeta is the workspace_meta row holding the keys of the draft
// entries the user discarded, newest last, so a discarded entry stays gone.
const cvDraftDismissedMeta = "profile.cv-draft-dismissed"

func (s *Server) cvDraftDismissed(c *apikit.Call) []string {
	keys := []string{}
	if st := s.store.Load(); st != nil {
		var raw string
		if st.Read().QueryRowContext(c.R.Context(), `SELECT value FROM workspace_meta WHERE key = ?`, cvDraftDismissedMeta).Scan(&raw) == nil {
			_ = json.Unmarshal([]byte(raw), &keys)
		}
	}
	return keys
}

// dismissCVDraft discards draft entries for good, or with restore puts them
// back, and answers the draft. The newest 500 keys are kept.
func (s *Server) dismissCVDraft(c *apikit.Call) (apikit.Reply, error) {
	var req wire.CVDraftDismiss
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if len(req.Keys) == 0 || len(req.Keys) > 500 || slices.ContainsFunc(req.Keys, func(k string) bool { return k == "" || len(k) > 1000 }) {
		return apikit.Reply{}, apikit.InvalidField("keys", "keys must be 1 to 500 keys of 1 to 1000 bytes")
	}
	st := s.store.Load()
	if st == nil {
		return apikit.Reply{}, apikit.Unavailable("the workspace database is not open")
	}
	keys := slices.DeleteFunc(s.cvDraftDismissed(c), func(k string) bool { return slices.Contains(req.Keys, k) })
	for _, k := range req.Keys {
		if !req.Restore && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	b, _ := json.Marshal(keys[max(0, len(keys)-500):])
	ctx := c.R.Context()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, cvDraftDismissedMeta, string(b))
		return err
	}); err != nil {
		return apikit.Reply{}, err
	}
	return s.cvDraft(c)
}
