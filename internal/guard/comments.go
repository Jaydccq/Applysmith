package guard

import (
	"bytes"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"strings"
)

const ruleComment = "comment"

// comment is one physical line of comment text. Hash-style comments keep their
// leading marker so a tracker reference written with a hash stays visible.
type comment struct {
	line int
	col  int // byte offset of text within its line
	text string
}

type commentStyle int

const (
	styleNone commentStyle = iota
	styleGo
	styleC    // line and block comments, quoted strings
	styleCSS  // block comments only
	styleSQL  // "--" line and block comments
	styleHash // "#" line comments
	styleHTML // <!-- -->
)

func styleOf(p string) commentStyle {
	base := path.Base(p)
	switch base {
	case "Makefile", "makefile", "GNUmakefile", "Dockerfile", ".gitignore", ".gitattributes", ".env.example":
		return styleHash
	case "go.mod", "go.work":
		return styleC
	}
	switch ext(p) {
	case ".go":
		return styleGo
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return styleC
	case ".css", ".scss":
		return styleCSS
	case ".sql":
		return styleSQL
	case ".sh", ".bash", ".zsh", ".yml", ".yaml", ".toml", ".mk":
		return styleHash
	case ".html", ".htm":
		return styleHTML
	}
	return styleNone
}

// extractComments returns the comment lines of a file in the given style.
func extractComments(style commentStyle, src []byte) []comment {
	switch style {
	case styleGo:
		return goComments(src)
	case styleC:
		return cComments(src, true, true)
	case styleCSS:
		return cComments(src, false, false)
	case styleSQL:
		return sqlComments(src)
	case styleHash:
		return hashComments(src)
	case styleHTML:
		return htmlComments(src)
	}
	return nil
}

// splitComment turns a possibly multi-line comment starting at (line, col)
// into one comment per physical line.
func splitComment(line, col int, text string) []comment {
	var out []comment
	for i, part := range strings.Split(text, "\n") {
		c := comment{line: line + i, text: part}
		if i == 0 {
			c.col = col
		}
		out = append(out, c)
	}
	return out
}

func goComments(src []byte) []comment {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) {}, scanner.ScanComments)
	var out []comment
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok == token.COMMENT {
			p := fset.Position(pos)
			out = append(out, splitComment(p.Line, p.Column-1, lit)...)
		}
	}
}

// cComments lexes "//" and "/* */" comments while skipping quoted strings.
func cComments(src []byte, lineComments, backtick bool) []comment {
	var out []comment
	line, lineStart := 1, 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\n':
			line++
			lineStart = i + 1
		case c == '"' || c == '\'' || (backtick && c == '`'):
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == '\n' {
					line++
					lineStart = i + 1
					if c != '`' {
						break
					}
				}
				if src[i] == c {
					break
				}
			}
		case lineComments && c == '/' && i+1 < len(src) && src[i+1] == '/':
			end := i
			for end < len(src) && src[end] != '\n' {
				end++
			}
			out = append(out, comment{line: line, col: i - lineStart, text: string(src[i:end])})
			i = end - 1
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			stop := len(src)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			text := string(src[i:stop])
			out = append(out, splitComment(line, i-lineStart, text)...)
			line += strings.Count(text, "\n")
			if nl := strings.LastIndexByte(text, '\n'); nl >= 0 {
				lineStart = i + nl + 1
			}
			i = stop - 1
		}
	}
	return out
}

func sqlComments(src []byte) []comment {
	out := cComments(src, false, false)
	for i, l := range strings.Split(string(src), "\n") {
		if off := strings.Index(l, "--"); off >= 0 && strings.Count(l[:off], "'")%2 == 0 {
			out = append(out, comment{line: i + 1, col: off, text: l[off:]})
		}
	}
	return out
}

func hashComments(src []byte) []comment {
	var out []comment
	for i, l := range strings.Split(string(src), "\n") {
		if i == 0 && strings.HasPrefix(l, "#!") {
			continue
		}
		var quote byte
		for j := 0; j < len(l); j++ {
			c := l[j]
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '#' && (j == 0 || l[j-1] == ' ' || l[j-1] == '\t'):
				out = append(out, comment{line: i + 1, col: j, text: l[j:]})
				j = len(l)
			}
		}
	}
	return out
}

func htmlComments(src []byte) []comment {
	var out []comment
	s := string(src)
	for pos := 0; ; {
		start := strings.Index(s[pos:], "<!--")
		if start < 0 {
			return out
		}
		start += pos
		end := strings.Index(s[start:], "-->")
		stop := len(s)
		if end >= 0 {
			stop = start + end + 3
		}
		line := 1 + strings.Count(s[:start], "\n")
		col := start - (strings.LastIndexByte(s[:start], '\n') + 1)
		out = append(out, splitComment(line, col, s[start:stop])...)
		pos = stop
	}
}

// ---- rules ----

const monthName = `(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|June?|July?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)`

type commentRule struct {
	detail string
	re     *regexp.Regexp
}

var commentRules = []commentRule{
	{"calendar date in a comment", regexp.MustCompile(strings.Join([]string{
		`\b\d{4}-\d{2}-\d{2}\b`,
		`\b\d{4}/\d{1,2}/\d{1,2}\b`,
		`\b\d{1,2}/\d{1,2}/\d{4}\b`,
		`\b` + monthName + `\.?\s+\d{1,2}(?:st|nd|rd|th)?\b(?:,?\s+\d{4})?`,
		`\b\d{1,2}(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthName + `\b(?:,?\s+\d{4})?`,
	}, "|"))},
	{"pull-request, issue or ticket reference in a comment", regexp.MustCompile(strings.Join([]string{
		`\bPR\s*#?\d+\b`,
		`(?i)\bissues?\s*#?\d+\b`,
		`(?i)\b(?:pull|issues)/\d+\b`,
	}, "|"))},
	{"decision attributed to a person or role in a comment", regexp.MustCompile(`(?i)\b(?:(?:approved|requested|decided|authori[sz]ed|mandated|signed off) by|at the request of|per the owner|the owner (?:decided|approved|requested|asked))\b`)},
}

const trackerDetail = "pull-request, issue or ticket reference in a comment"

var trackerRE = regexp.MustCompile(`(?:^|[^\w&#/-])(#(\d{2,}))\b`)

// hexColourLengths are the digit-run lengths that read as a CSS colour.
var hexColourLengths = map[int]bool{3: true, 4: true, 6: true, 8: true}

func commentHits(style commentStyle, text string) []hit {
	var out []hit
	for _, r := range commentRules {
		for _, m := range r.re.FindAllStringIndex(text, -1) {
			out = append(out, hit{off: m[0], text: text[m[0]:m[1]], detail: r.detail})
		}
	}
	for _, m := range trackerRE.FindAllStringSubmatchIndex(text, -1) {
		if style == styleCSS && hexColourLengths[m[5]-m[4]] || overlaps(out, m[2], m[3]) {
			continue
		}
		out = append(out, hit{off: m[2], text: text[m[2]:m[3]], detail: trackerDetail})
	}
	return out
}

func checkComments(t *Tree) ([]Finding, error) {
	var out []Finding
	err := t.eachText(func(p string) bool { return styleOf(p) != styleNone }, func(f textFile) {
		style := styleOf(f.Path)
		for _, c := range extractComments(style, f.Data) {
			line := ""
			if c.line-1 < len(f.Lines) {
				line = f.Lines[c.line-1]
			}
			for _, h := range commentHits(style, c.text) {
				out = append(out, Finding{
					Rule: ruleComment, Path: f.Path, Line: c.line, Col: runeCol(line, c.col+h.off),
					Detail: h.detail, Excerpt: mask(h.text), Value: h.text,
				})
			}
		}
	})
	return out, err
}
