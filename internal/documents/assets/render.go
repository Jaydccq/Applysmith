package assets

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"

	"applysmith/internal/documents/tex"
)

// Paper is the page size of a document. Only the two listed values reach the
// document class.
type Paper string

const (
	PaperA4     Paper = "a4"
	PaperLetter Paper = "letter"
)

// Link is a labelled address. The URL is validated by tex.URL.
type Link struct {
	Label string
	URL   string
}

// Entry is one dated résumé entry.
type Entry struct {
	Dates        string
	Title        string
	Organization string
	Location     string
	Bullets      []string
	Note         string
}

// Competency is one labelled skill line.
type Competency struct {
	Label string
	Text  string
}

// ResumeLabels are the section headings of one language.
type ResumeLabels struct {
	Summary, Competencies, Experience, Projects, Education, Languages string
}

// EnglishLabels are the section headings of the English résumé.
var EnglishLabels = ResumeLabels{Summary: "Summary", Competencies: "Core Competencies", Experience: "Professional Experience",
	Projects: "Selected Projects", Education: "Education", Languages: "Languages"}

// Resume is everything the résumé template may print.
type Resume struct {
	Paper  Paper
	Labels ResumeLabels
	// Title is the PDF's document title.
	Title        string
	FirstName    string
	LastName     string
	Address      string
	Phone        string
	Email        string
	Links        []Link
	Profile      string
	Competencies []Competency
	Experience   []Entry
	Projects     []Entry
	Education    []Entry
	Languages    string
	// Tight is the template's one reviewed layout adjustment: 1pt entry gaps
	// instead of 3pt and no extra bullet spacing.
	Tight bool
}

// Block is one body paragraph of a letter, optionally followed by a list.
type Block struct {
	Text    string
	Bullets []string
}

// Cover is everything the cover-letter template may print.
type Cover struct {
	Paper Paper
	Name  string
	// Title is the PDF's document title.
	Title string
	// Contact is the first contact line (email, phone); Links, the profile
	// links, take the line under it.
	Contact []Link
	Links   []Link
	Date    string
	// Recipient holds the lines between the date and the salutation: the
	// company, then the role as the letter's subject.
	Recipient  []string
	Salutation string
	Blocks     []Block
	Closing    string
}

// ErrContent is returned for data the templates refuse to print.
var ErrContent = errors.New("assets: content refused")

func paper(p Paper) (string, error) {
	switch p {
	case PaperA4:
		return "a4paper", nil
	case PaperLetter:
		return "letterpaper", nil
	}
	return "", fmt.Errorf("%w: unknown paper %q", ErrContent, string(p))
}

func link(raw string) (string, error) {
	out, err := tex.URL(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrContent, err)
	}
	return out, nil
}

// funcs is the complete function set of the templates. TestTemplatesCallOnlyEncoders
// holds every action to it.
var funcs = template.FuncMap{"tex": tex.Text, "rich": tex.Rich, "url": link, "paper": paper}

func compileTemplate(name, src string) *template.Template {
	return template.Must(template.New(name).Delims("<%", "%>").Funcs(funcs).Option("missingkey=error").Parse(src))
}

var (
	resumeTemplate = compileTemplate("resume.tex", mustRead("resume.tex"))
	coverTemplate  = compileTemplate("cover.tex", mustRead("cover.tex"))
)

func mustRead(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// RenderResume expands the résumé template.
func RenderResume(r Resume) ([]byte, error) { return execute(resumeTemplate, r) }

// RenderCover expands the cover-letter template.
func RenderCover(c Cover) ([]byte, error) { return execute(coverTemplate, c) }

func execute(t *template.Template, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
