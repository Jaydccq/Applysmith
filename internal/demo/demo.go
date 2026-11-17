// Package demo holds the content of the demo workspace: the shipped example
// persona Alex Example, a few sample postings from fictional companies on
// reserved domains, and a sample application form. Every value is fictional.
// Seeding a workspace with it is the onboarding package's job; serving the
// form is the API's.
package demo

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed persona
var persona embed.FS

const (
	// DefaultDir is the demo workspace's folder name under the home directory.
	DefaultDir = "Applysmith-Demo"
	// SampleApplicationPath is where a server running a demo workspace serves
	// the sample application form, on loopback only.
	SampleApplicationPath = "/demo/sample-application"
	// ResumeFile is the persona's résumé, relative to the workspace.
	ResumeFile = "resumes/alex-example-platform.pdf"
	// PersonaEmail is the address the shipped profile and CV carry.
	PersonaEmail = "alex@example.com"
	// Source names the sample postings in the inbox, where a scan names its
	// source.
	Source = "demo"
	// TutorialPath is the tutorial that walks through the demo, relative to
	// the repository's docs.
	TutorialPath = "docs/user-guide/tutorial.md"
	// Never is what a demo workspace never does, in the words every surface
	// shows.
	Never = "It never presses Submit, creates an account, resets a password, solves a captcha, opens an activation link, sends mail, or sends a Telegram message. It reads mail only to type a site's emailed verification code, and only when the persona's address is a plus alias of your own connected Gmail account. The only files it uploads are the persona's own fictional documents."
)

// Persona files, named as in the workspace.
const (
	FileProfile   = "profile.yml"
	FileCV        = "cv.md"
	FileNarrative = "narrative.md"
	FileVoice     = "voice.md"
)

// File returns one persona file.
func File(name string) []byte {
	b, err := persona.ReadFile("persona/" + name)
	if err != nil {
		panic(fmt.Sprintf("demo: the embedded persona file %s is missing", name))
	}
	return b
}

// SampleApplicationCSP is the policy the form is served with: its own inline
// style and script, nothing loaded, and no form submission anywhere.
const SampleApplicationCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"

// SampleApplication is the HTML of the sample application form.
func SampleApplication() []byte { return File("sample-application.html") }

// Posting is one sample posting, shaped like a scan result.
type Posting struct {
	Company        string `json:"company"`
	Title          string `json:"title"`
	Location       string `json:"location"`
	Remote         bool   `json:"remote"`
	URL            string `json:"url"`
	JobID          string `json:"jobId"`
	Compensation   string `json:"compensation"`
	EmploymentType string `json:"employmentType"`
	Department     string `json:"department"`
	Description    string `json:"description"`
}

// Postings returns the sample postings.
func Postings() []Posting {
	var out []Posting
	if err := json.Unmarshal(File("postings.json"), &out); err != nil {
		panic("demo: the embedded sample postings are not valid JSON: " + err.Error())
	}
	return out
}

// PostingAt returns the sample posting at a URL.
func PostingAt(url string) (Posting, bool) {
	for _, p := range Postings() {
		if p.URL == url {
			return p, true
		}
	}
	return Posting{}, false
}

// Answers maps answer-bank entry ids to the persona's answers for the
// questions its profile does not answer.
func Answers() map[string]string {
	out := map[string]string{}
	if err := json.Unmarshal(File("answers.json"), &out); err != nil {
		panic("demo: the embedded persona answers are not valid JSON: " + err.Error())
	}
	return out
}

// ResumePDF renders the persona's one-page résumé from cv.md. The persona is
// embedded and covered by tests, so a CV that cannot be laid out is a build
// defect.
func ResumePDF() []byte {
	b, err := ResumePDFOf(string(File(FileCV)))
	if err != nil {
		panic(err.Error())
	}
	return b
}

// ResumePDFOf renders the one-page résumé of a CV in the persona's layout. The
// same CV always gives the same bytes, so a caller can tell a résumé it
// rendered from one the user replaced.
func ResumePDFOf(cv string) ([]byte, error) { return renderResume(cv) }
