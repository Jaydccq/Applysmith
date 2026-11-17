package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/demo"
	"applysmith/internal/discovery"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// ReasonDemo marks a step the demo persona completes. It never makes a step
// incomplete.
const ReasonDemo ReasonKind = "demo"

// demoPersonaReason is the reason a step the persona fills is complete.
const demoPersonaReason = "demo persona"

// ErrNotDemoWorkspace refuses a demo over or inside a workspace that is not a
// demo: the user's own data is never mixed with the example persona.
var ErrNotDemoWorkspace = errors.New("demo_not_over_workspace")

// DemoOptions locate the demo workspace. Dir empty is ~/Applysmith-Demo.
type DemoOptions struct {
	Dir     string
	HomeDir string
}

// DemoResult reports what a demo init wrote.
type DemoResult struct {
	Root     string
	Created  []string
	Postings int
}

// ResolveDemo returns the demo workspace's directory, checked against the
// workspace rules. The environment and the recorded workspace never choose it.
func ResolveDemo(o DemoOptions) (string, error) {
	home := o.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = h
	}
	dir := o.Dir
	if dir == "" {
		dir = filepath.Join(home, demo.DefaultDir)
	}
	return workspace.Resolve(workspace.ResolveOptions{Flag: dir, Getenv: func(string) string { return "" }, HomeDir: home})
}

// PrepareDemo creates the demo workspace's files. It refuses, writing
// nothing, a directory that holds or sits inside a workspace without the demo
// marker. The marker is written first, so an interrupted init leaves a demo
// workspace that a second run completes; persona files are created only where
// none exists, so a repeat never overwrites what the user changed.
func PrepareDemo(root string) (*workspace.Workspace, DemoResult, error) {
	ws := workspace.Open(root)
	res := DemoResult{Root: ws.Root}
	for dir := ws.Root; ; dir = filepath.Dir(dir) {
		w := workspace.Open(dir)
		if (dir == ws.Root && w.Holds() || dir != ws.Root && dirExists(w.StateDir())) && !w.IsDemo() {
			return nil, res, fmt.Errorf("%w: %s holds a workspace that is not a demo; the demo needs a folder of its own, such as ~/%s", ErrNotDemoWorkspace, dir, demo.DefaultDir)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if err := ws.MarkDemo(); err != nil {
		return nil, res, err
	}
	// Init seeds the files the persona has no version of (portals, policy);
	// the persona replaces the templates of the others below.
	if _, err := ws.Init(); err != nil {
		return nil, res, err
	}
	files := []struct {
		rel  string
		data []byte
	}{
		{workspace.FileProfile.Name(), demo.File(demo.FileProfile)},
		{workspace.FileCV.Name(), demo.File(demo.FileCV)},
		{workspace.FileNarrative.Name(), demo.File(demo.FileNarrative)},
		{workspace.FileVoice.Name(), demo.File(demo.FileVoice)},
		{demo.ResumeFile, demo.ResumePDF()},
	}
	for _, f := range files {
		created, err := writePersonaFile(ws, f.rel, f.data)
		if err != nil {
			return nil, res, err
		}
		if created {
			res.Created = append(res.Created, f.rel)
		}
	}
	created, err := answerbank.New(ws, nil).Seed()
	if err != nil {
		return nil, res, err
	}
	if created {
		res.Created = append(res.Created, workspace.FileAnswerBank.Name())
	}
	return ws, res, nil
}

// SyncDemoEmail carries a changed persona address into the files that show it,
// so a site that reads the résumé finds the address the profile gives. The CV
// is rewritten only where it still holds the profile's previous address or the
// shipped one, and the résumé PDF only while it is the one rendered from that
// CV: a CV or a PDF the user changed stays as it is.
func SyncDemoEmail(ws *workspace.Workspace, previous, to string) error {
	if to == "" {
		return nil
	}
	cvPath := filepath.Join(ws.Root, workspace.FileCV.Name())
	cv, err := os.ReadFile(cvPath)
	if err != nil {
		return err
	}
	from := ""
	for _, held := range []string{previous, demo.PersonaEmail} {
		if held != "" && held != to && bytes.Contains(cv, []byte(held)) {
			from = held
			break
		}
	}
	if from == "" {
		return nil
	}
	pdfPath := filepath.Join(ws.Root, demo.ResumeFile)
	cur, err := os.ReadFile(pdfPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rendered, rerr := demo.ResumePDFOf(string(cv))
	replacePDF := err == nil && rerr == nil && bytes.Equal(cur, rendered)
	next := bytes.ReplaceAll(cv, []byte(from), []byte(to))
	if err := overwriteFile(cvPath, next); err != nil {
		return err
	}
	if !replacePDF {
		return nil
	}
	pdf, err := demo.ResumePDFOf(string(next))
	if err != nil {
		return err
	}
	return overwriteFile(pdfPath, pdf)
}

// overwriteFile replaces a file through a temporary neighbour, so a crash
// leaves the old file or the new one.
func overwriteFile(path string, data []byte) error {
	tmp := path + ".demo-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writePersonaFile replaces a file that still holds init's template and
// creates a missing one; anything else is the user's and stays.
func writePersonaFile(ws *workspace.Workspace, rel string, data []byte) (bool, error) {
	path := filepath.Join(ws.Root, rel)
	cur, err := os.ReadFile(path)
	switch {
	case err == nil:
		f, ferr := workspace.ParseFile(trimExt(rel))
		if ferr != nil || !workspace.IsTemplate(f, cur) {
			return false, nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp := path + ".demo-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

func trimExt(rel string) string { return rel[:len(rel)-len(filepath.Ext(rel))] }

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// SeedDemo admits the sample postings into the inbox through the scan's own
// admission and fills the answer bank from the persona through the answer
// bank step's writer. The caller holds the workspace for writing. Repeating
// it admits nothing twice.
func SeedDemo(ctx context.Context, ws *workspace.Workspace, st *store.Store, now func() time.Time) (DemoResult, error) {
	res := DemoResult{Root: ws.Root}
	if !ws.IsDemo() {
		return res, ErrNotDemoWorkspace
	}
	doc, err := ws.Read(workspace.FilePortals)
	if err != nil {
		return res, err
	}
	portals, rep := workspace.ParsePortals(doc.Data)
	if !rep.OK() {
		return res, fmt.Errorf("portals.yml: %s", rep.Issues[0].Message)
	}
	prof, err := ws.Read(workspace.FileProfile)
	if err != nil {
		return res, err
	}
	profile, _ := workspace.ParseProfile(prof.Data)
	repo := discovery.NewRepo(st, now)
	for _, p := range demo.Postings() {
		adm, err := repo.AdmitPosting(ctx, portals, profile, discovery.Posting{Source: demo.Source, Company: p.Company, Title: p.Title, Location: p.Location,
			Remote: p.Remote, URL: p.URL, JobID: p.JobID, Compensation: p.Compensation, EmploymentType: p.EmploymentType,
			Department: p.Department, Description: p.Description}, "demo: a sample posting of a fictional company")
		if err != nil {
			return res, err
		}
		switch {
		case adm.Row != nil:
			res.Postings++
		case adm.Invalid != "":
			return res, fmt.Errorf("sample posting %s: %s", p.URL, adm.Invalid)
		}
	}
	env := &Env{Workspace: ws, Store: st, Now: now}
	// The persona answers what it can and its setup does not already state; a
	// question it has no answer to is left to the person trying the demo, as
	// the user's own bank would leave it.
	answers := map[string]string{}
	svc := bankService(env)
	if loaded, err := svc.Bank(); err == nil {
		for id, v := range demo.Answers() {
			if e, ok := loaded.Bank.Find(id); ok && e.BodyEmpty() {
				answers[id] = v
			}
		}
	}
	if report, err := svc.Unanswered(); err == nil {
		for _, g := range report.Blocking() {
			if _, ok := answers[g.ID]; !ok {
				answers[g.ID] = match.AskMe
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"answers": answers, "confirm_review": true})
	if _, err := applyAnswerBank(ctx, env, raw, false); err != nil {
		return res, fmt.Errorf("the demo answer bank: %w", err)
	}
	return res, nil
}
