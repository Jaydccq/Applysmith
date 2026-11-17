package onboarding

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

type scriptedPrompter struct{ answers []string }

func (p *scriptedPrompter) Ask(label, def string) (string, error) {
	a := p.answers[0]
	p.answers = p.answers[1:]
	if a == "" {
		return def, nil
	}
	return a, nil
}
func (p *scriptedPrompter) Secret(string) (string, error) { return "", nil }

func yearOnlyCV() string {
	long := strings.Repeat("Platform engineering across data pipelines and tooling for several teams. ", 6)
	return "# Riley Tester\n\n## Experience\n\n### Engineer, Northwind Systems (2019 to 2021)\n\n" + long
}

// Onboarding output changed for year-only CVs: the draft keeps the bare year
// and the profile refuses it until a month is given.
func TestCVStepKeepsAYearOnlyDateAndRefusesItUntilAMonthIsGiven(t *testing.T) {
	env := setupEnv(t)
	body, _ := json.Marshal(map[string]string{"markdown": yearOnlyCV()})
	res := mustApply(t, env, "cv", string(body))
	if res.Draft == nil || len(res.Draft.Employment) != 1 || res.Draft.Employment[0].Start != "2019" || res.Draft.Employment[0].End != "2021" {
		t.Fatalf("draft = %+v", res.Draft)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "without a month") {
		t.Fatalf("notes = %v", res.Notes)
	}
	if _, err := apply(t, env, "cv", `{"employment":[{"employer":"Northwind Systems","title":"Engineer","start":"2019","end":"2021"}]}`, false); err == nil {
		t.Fatal("a bare year was stored")
	}
	if p, _, _ := profileData(env); len(p.History.Employment) != 0 {
		t.Fatalf("history = %+v", p.History)
	}
}

func TestCVQuestionsAskForTheMissingMonthAndNeverDefaultIt(t *testing.T) {
	env := setupEnv(t)
	body, _ := json.Marshal(map[string]string{"markdown": yearOnlyCV()})
	mustApply(t, env, "cv", string(body))
	// stored cv.md, confirm, start month 6, end month left empty
	in := &scriptedPrompter{answers: []string{"", "yes", "13", "6", ""}}
	var out bytes.Buffer
	f := &Flow{Env: env, In: in, Out: &out}
	got, err := f.askCV(bg())
	if err != nil {
		t.Fatal(err)
	}
	jobs := got["employment"].([]cvJob)
	if len(jobs) != 1 || jobs[0].Start != "2019-06" || jobs[0].End != "" {
		t.Fatalf("jobs = %+v", jobs)
	}
	if !strings.Contains(out.String(), "2019 (month not stated)") || !strings.Contains(out.String(), "answer a month number") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCVFile_PDFUsesLocalExtraction(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pdftotext"), []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf 'Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. Riley Sample Platform Engineer. '\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	path := filepath.Join(t.TempDir(), "cv.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := ImportCVFile(bg(), path)
	if err != nil || !strings.Contains(text, "Riley Sample") {
		t.Fatalf("PDF import=%q %v", text, err)
	}
}

func TestCVImport_ReviewPrecedesAnyWrite(t *testing.T) {
	for _, confirm := range []string{"no", "yes"} {
		t.Run(confirm, func(t *testing.T) {
			env := setupEnv(t)
			before, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV))
			profile, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile))
			path := filepath.Join(t.TempDir(), "cv.txt")
			os.WriteFile(path, []byte(yearOnlyCV()), 0600)
			var out bytes.Buffer
			f := Flow{Env: env, In: &scriptedPrompter{answers: []string{path, confirm, "no"}}, Out: &out}
			in, err := f.askCV(bg())
			if (err == nil) != (confirm == "yes") {
				t.Fatalf("confirmation=%s error=%v", confirm, err)
			}
			if confirm == "yes" && in["markdown"] != yearOnlyCV() {
				t.Fatal("approved text missing")
			}
			if !strings.Contains(out.String(), yearOnlyCV()) {
				t.Fatal("text was not offered for review")
			}
			after, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV))
			pafter, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile))
			if !bytes.Equal(before, after) || !bytes.Equal(profile, pafter) {
				t.Fatal("preview wrote a file")
			}
		})
	}
}

func TestCVImport_OrdinaryDOCXReachesCompleteAfterFactConfirmation(t *testing.T) {
	env := setupEnv(t)
	before, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV))
	profile, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, text := range map[string]string{
		"_rels/.rels":       `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Riley Tester</w:t></w:r></w:p><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Experience</w:t></w:r></w:p><w:p><w:r><w:t>Engineer, Northwind Systems (2020-01 to present)</w:t></w:r></w:p><w:p><w:r><w:t>` + strings.Repeat("Built internal tooling and maintained platform services. ", 6) + `</w:t></w:r></w:p></w:body></w:document>`,
	} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selected.docx")
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	flow := Flow{Env: env, In: &scriptedPrompter{answers: []string{path, "yes", "yes"}}, Out: &out}
	input, err := flow.askCV(bg())
	if err != nil {
		t.Fatal(err)
	}
	cvNow, _ := os.ReadFile(env.Workspace.Path(workspace.FileCV))
	pNow, _ := os.ReadFile(env.Workspace.Path(workspace.FileProfile))
	if !bytes.Equal(before, cvNow) || !bytes.Equal(profile, pNow) {
		t.Fatal("preview wrote workspace")
	}
	raw, _ := json.Marshal(input)
	res, err := Apply(bg(), env, "cv", raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Detection.Status != StepComplete || res.Draft == nil || len(res.Draft.Employment) != 1 || strings.Contains(strings.Join(res.Notes, " "), "nothing is stored") {
		t.Fatalf("applied=%+v", res)
	}
	p, _, bad := profileData(env)
	if bad != nil || len(p.History.Employment) != 1 || p.History.Employment[0].Employer != "Northwind Systems" || p.History.Employment[0].Start != "2020-01" {
		t.Fatalf("confirmed profile=%+v %v", p, bad)
	}
}
