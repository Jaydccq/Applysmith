package assets

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"
)

func TestTemplatesCallOnlyEncoders(t *testing.T) {
	allowed := map[string]bool{"tex": true, "rich": true, "url": true, "paper": true}
	for _, tpl := range []struct{ name, src string }{{"resume.tex", mustRead("resume.tex")}, {"cover.tex", mustRead("cover.tex")}} {
		tree, err := parse.Parse(tpl.name, tpl.src, "<%", "%>", funcs)
		if err != nil {
			t.Fatalf("%s: %v", tpl.name, err)
		}
		for _, node := range tree[tpl.name].Root.Nodes {
			walkActions(t, tpl.name, node, allowed)
		}
	}
}

// walkActions requires every printing action to be a call of an encoder
// function, so no field can reach the output unencoded.
func walkActions(t *testing.T, name string, n parse.Node, allowed map[string]bool) {
	t.Helper()
	switch n := n.(type) {
	case *parse.ActionNode:
		if n.Pipe == nil || len(n.Pipe.Decl) > 0 {
			return // a variable declaration prints nothing
		}
		if len(n.Pipe.Cmds) != 1 || !isEncoderCall(n.Pipe.Cmds[0], allowed) {
			t.Errorf("%s: action %q prints without an encoder", name, n.String())
		}
	case *parse.IfNode:
		walkList(t, name, n.List, allowed)
		walkList(t, name, n.ElseList, allowed)
	case *parse.RangeNode:
		walkList(t, name, n.List, allowed)
		walkList(t, name, n.ElseList, allowed)
	case *parse.WithNode:
		walkList(t, name, n.List, allowed)
		walkList(t, name, n.ElseList, allowed)
	case *parse.TemplateNode:
		t.Errorf("%s: nested template %q is not allowed", name, n.Name)
	}
}

func walkList(t *testing.T, name string, l *parse.ListNode, allowed map[string]bool) {
	if l == nil {
		return
	}
	for _, n := range l.Nodes {
		walkActions(t, name, n, allowed)
	}
}

func isEncoderCall(c *parse.CommandNode, allowed map[string]bool) bool {
	id, ok := c.Args[0].(*parse.IdentifierNode)
	return ok && allowed[id.Ident]
}

func TestRender_MaliciousContentStaysLiteral(t *testing.T) {
	payloads := []string{
		`\input{/etc/hosts}`, `\write18{touch /tmp/x}`, `\directlua{os.execute("x")}`,
		`$(touch x)`, "100% done\n\\end{document}", `\openout1=x`, `}\input{y}{`,
	}
	for _, p := range payloads {
		r := Resume{Paper: PaperA4, FirstName: p, LastName: p, Email: p, Profile: p, Languages: p,
			Competencies: []Competency{{Label: p, Text: p}},
			Experience:   []Entry{{Dates: p, Title: p, Organization: p, Location: p, Bullets: []string{p}}}}
		c := Cover{Paper: PaperLetter, Name: p, Date: p, Salutation: p, Closing: p,
			Contact: []Link{{Label: p}}, Blocks: []Block{{Text: p, Bullets: []string{p}}}}
		for name, render := range map[string]func() ([]byte, error){
			"resume": func() ([]byte, error) { return RenderResume(r) },
			"cover":  func() ([]byte, error) { return RenderCover(c) },
		} {
			out, err := render()
			if err != nil {
				t.Fatalf("%s %q: %v", name, p, err)
			}
			assertLiteral(t, name, p, out)
		}
	}
}

// assertLiteral checks that no command of the payload reached the source: a
// backslash is only ever written as \textbackslash{}, so none of these control
// words can appear, and the payload's end-of-document cannot add a second one.
func assertLiteral(t *testing.T, name, payload string, out []byte) {
	t.Helper()
	body := string(out)
	for _, cmd := range []string{`\input`, `\write`, `\directlua`, `\openout`, `\immediate`} {
		if strings.Contains(body, cmd) {
			t.Errorf("%s: payload %q put %s into the TeX source", name, payload, cmd)
		}
	}
	if strings.Count(body, `\end{document}`) != 1 {
		t.Errorf("%s: payload %q changed the number of end-of-document commands", name, payload)
	}
}

func TestRender_RefusesUnknownPaperAndLinks(t *testing.T) {
	good := Resume{Paper: PaperA4, FirstName: "Alex", LastName: "Example", Email: "alex@example.com"}
	if _, err := RenderResume(good); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	bad := good
	bad.Paper = Paper("a4paper,draft")
	if _, err := RenderResume(bad); !errors.Is(err, ErrContent) {
		t.Errorf("unknown paper: err = %v, want ErrContent", err)
	}
	bad = good
	bad.Links = []Link{{Label: "x", URL: "javascript:alert(1)"}}
	if _, err := RenderResume(bad); !errors.Is(err, ErrContent) {
		t.Errorf("bad link: err = %v, want ErrContent", err)
	}
	if _, err := RenderCover(Cover{Paper: PaperA4, Name: "A", Contact: []Link{{Label: "x", URL: "file:///etc/hosts"}}}); !errors.Is(err, ErrContent) {
		t.Errorf("bad cover link: err = %v, want ErrContent", err)
	}
}

func TestRender_ListStaysOutsideLetterContent(t *testing.T) {
	out, err := RenderCover(Cover{Paper: PaperA4, Name: "A", Blocks: []Block{{Text: "Intro", Bullets: []string{"one", "two"}}}, Closing: "Bye"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	i, j := strings.Index(s, `\bodyparagraph{Intro}`), strings.Index(s, `\begin{bodylist}`)
	if i < 0 || j < i || strings.Contains(s[i:j], `\end{bodylist}`) {
		t.Fatalf("the list must follow its paragraph, not sit inside it:\n%s", s)
	}
	// A block without text prints its list alone: an empty paragraph stops XeLaTeX.
	bare, err := RenderCover(Cover{Paper: PaperA4, Name: "A", Blocks: []Block{{Bullets: []string{"one"}}}, Closing: "Bye"})
	if err != nil || strings.Contains(string(bare), `\bodyparagraph{}`) || !strings.Contains(string(bare), `\begin{bodylist}`) {
		t.Errorf("a block without text: %v\n%s", err, bare)
	}
	// The header ends with its rule, and the colour returns to black, with or
	// without a date.
	if !strings.Contains(string(bare), `\headerclose`) || strings.Contains(string(bare), `\letterdate`) {
		t.Errorf("a letter without a date: %s", bare)
	}
	// The profile links take their own line, and the company and the subject
	// stand between the date and the salutation.
	head, err := RenderCover(Cover{Paper: PaperA4, Name: "A", Title: "A - Cover Letter", Date: "D", Salutation: "S", Closing: "Bye",
		Contact: []Link{{Label: "a"}}, Links: []Link{{Label: "p", URL: "https://example.com/p"}}, Recipient: []string{"Example Corp", "Re: Engineer"}})
	if hs := string(head); err != nil || !strings.Contains(hs, `\contactitem{a}`+"\n"+`\contactbreak`+"\n"+`\linkitem{https://example.com/p}{p}`+"\n"+`\headerclose`+"\n"+`\letterdate{D}`) ||
		!strings.Contains(hs, `\letterdate{D}`+"\n"+`\recipientline{Example Corp}`+"\n"+`\recipientline{Re: Engineer}`+"\n"+`\salutation{S}`) ||
		!strings.Contains(hs, "pdftitle={A - Cover Letter}") {
		t.Errorf("letter head: %v\n%s", err, head)
	}
	if !strings.Contains(s, `\closing{Bye}`) {
		t.Error("closing must not carry its own line break")
	}
}

// Readability bounds of the résumé template; no adjustment goes past them.
const (
	bodyFontPt    = 11
	geometryScale = "0.80"
	minGapPt      = 1
)

// The tight adjustment changes spacing only: the font size and the geometry
// scale stay at the template's readability bounds in both modes.
func TestResume_TightAdjustmentStaysWithinTheReadabilityBounds(t *testing.T) {
	render := func(tight bool) string {
		out, err := RenderResume(Resume{Paper: PaperA4, FirstName: "Alex", LastName: "Example", Email: "alex@example.com", Tight: tight,
			Experience: []Entry{{Dates: "2020-2021", Title: "Engineer", Organization: "Example", Bullets: []string{"One", "Two"}}},
			Education:  []Entry{{Dates: "2011-2015", Title: "BSc", Organization: "Example University"}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	stock, tight := render(false), render(true)
	for _, src := range []string{stock, tight} {
		if !strings.Contains(src, fmt.Sprintf(`\documentclass[%dpt,`, bodyFontPt)) || !strings.Contains(src, "scale="+geometryScale+"]{geometry}") {
			t.Error("the font size or the geometry scale left the bounds")
		}
		for _, m := range regexp.MustCompile(`\\vspace\{(\d+)pt\}`).FindAllStringSubmatch(src, -1) {
			if n, _ := strconv.Atoi(m[1]); n < minGapPt {
				t.Errorf("a gap of %dpt is below the %dpt minimum", n, minGapPt)
			}
		}
	}
	strip := regexp.MustCompile(`\\entryafterspace\}\{[57]pt\}|\\setlength\{\\(?:topsep|itemsep)\}\{[02]pt\}`)
	if !strings.Contains(tight, `\setlength{\itemsep}{0pt}`) || strings.Contains(stock, `\setlength{\itemsep}{0pt}`) || strip.ReplaceAllString(stock, "") != strip.ReplaceAllString(tight, "") {
		t.Error("the tight adjustment is not a spacing-only change")
	}
}
func TestEmbeddedFiles(t *testing.T) {
	for _, f := range []string{"resume.tex", "cover.tex", "cover.cls", "verify_pdf.py", "verify_layout.py", "requirements.txt"} {
		if b, err := Read(f); err != nil || len(b) == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
	if n := len(FontFiles()); n != 8 {
		t.Errorf("font files = %d, want 8", n)
	}
}
