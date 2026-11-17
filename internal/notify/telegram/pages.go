package telegram

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"applysmith/internal/vault"
)

// SendPages sends the pages of the announcement id to the paired chat, in
// order, with markup on the last message only, and returns the chat and the id
// of the last message. The text goes through the same redaction as every
// outbound message. A page that cannot be sent stops the rest and is returned;
// each page Telegram accepted is recorded, so the next call for the same
// announcement goes on after it.
func (g *Gateway) SendPages(ctx context.Context, id int64, pages []string, markup *Markup) (chat, last int64, err error) {
	if _, err := g.cfg.Token(); err != nil {
		return 0, 0, ErrNotPaired
	}
	o, ok := g.state.Owner(ctx)
	if !ok {
		return 0, 0, ErrNotPaired
	}
	sent, last := g.state.PagesSent(ctx, id)
	for i := sent; i < len(pages); i++ {
		var m *Markup
		if i == len(pages)-1 {
			m = markup
		}
		if last, err = g.sender.Send(ctx, o.ChatID, vault.Redact(pages[i]), m, false); err != nil {
			return o.ChatID, last, err
		}
		if err := g.state.SetPagesSent(ctx, id, i+1, last); err != nil {
			// The page is out; without its record a failure of a later page sends it again.
			g.cfg.Log.Warn("a sent page of a Telegram digest could not be recorded", "error", err.Error())
		}
	}
	if err := g.state.ForgetPages(ctx, id); err != nil {
		g.cfg.Log.Warn("the sent pages of a Telegram digest could not be cleared", "error", err.Error())
	}
	return o.ChatID, last, nil
}

// markdown parses the Ask Agent's answers, which are written for the
// dashboard's Markdown view.
var markdown = goldmark.New(goldmark.WithExtensions(extension.Table))

// formatted turns one page of Markdown into Telegram's HTML and into the plain
// text that says the same, which is sent when Telegram refuses the HTML.
// Telegram shows no headings, lists or tables: a heading is bold, a list item
// starts with a bullet or its number, and a table row is a line of its cells.
// Code inside bold, italics or a link loses its code style, because Telegram
// refuses that nesting.
func formatted(page string) (htmlText, plain string) {
	src := []byte(page)
	f := &tgFormat{src: src}
	_ = ast.Walk(markdown.Parser().Parse(text.NewReader(src)), f.visit)
	return strings.TrimSpace(f.h.String()), strings.TrimSpace(f.p.String())
}

type tgFormat struct {
	src    []byte
	h, p   strings.Builder
	styled int // open bold, italic and link entities
	linkAt int // where the open link's label starts in the plain text
}

// both writes s to the plain text and, escaped, to the HTML.
func (f *tgFormat) both(s string) {
	f.p.WriteString(s)
	f.h.WriteString(html.EscapeString(s))
}

// style opens (or closes) an entity that code may not sit inside.
func (f *tgFormat) style(tag string, entering bool) {
	if entering {
		f.styled++
		f.h.WriteString("<" + tag + ">")
		return
	}
	f.styled--
	f.h.WriteString("</" + tag + ">")
}

// block separates a block from the one before it: a blank line at the top
// level, a line break inside a list, quote or table.
func (f *tgFormat) block(n ast.Node) {
	switch {
	case n.PreviousSibling() == nil:
	case n.Parent().Kind() == ast.KindDocument:
		f.both("\n\n")
	default:
		f.both("\n")
	}
}

func (f *tgFormat) lines(n ast.Node) string {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(f.src))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (f *tgFormat) visit(n ast.Node, entering bool) (ast.WalkStatus, error) {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock, *ast.List, *ast.Blockquote, *east.Table, *east.TableRow:
		if entering {
			f.block(n)
		}
	case *ast.Heading:
		if entering {
			f.block(n)
		}
		f.style("b", entering)
	case *east.TableHeader:
		f.style("b", entering)
	case *east.TableCell:
		if entering && n.PreviousSibling() != nil {
			f.both(" · ")
		}
	case *ast.ListItem:
		if !entering {
			break
		}
		f.block(n)
		for p := n.Parent().Parent(); p != nil; p = p.Parent() {
			if p.Kind() == ast.KindListItem {
				f.both("  ")
			}
		}
		l := n.Parent().(*ast.List)
		if !l.IsOrdered() {
			f.both("• ")
			break
		}
		i := l.Start
		for s := n.PreviousSibling(); s != nil; s = s.PreviousSibling() {
			i++
		}
		f.both(strconv.Itoa(i) + ". ")
	case *ast.ThematicBreak:
		if entering {
			f.block(n)
			f.both("—")
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		if entering {
			f.block(n)
			f.h.WriteString("<pre>")
			f.both(f.lines(n))
			f.h.WriteString("</pre>")
		}
		return ast.WalkSkipChildren, nil
	case *ast.HTMLBlock:
		if entering {
			f.block(n)
			f.both(f.lines(n))
		}
		return ast.WalkSkipChildren, nil
	case *ast.Emphasis:
		if n.Level >= 2 {
			f.style("b", entering)
		} else {
			f.style("i", entering)
		}
	case *ast.CodeSpan:
		if entering {
			var b strings.Builder
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					b.Write(t.Segment.Value(f.src))
				}
			}
			if f.styled == 0 {
				f.h.WriteString("<code>")
				f.both(b.String())
				f.h.WriteString("</code>")
			} else {
				f.both(b.String())
			}
		}
		return ast.WalkSkipChildren, nil
	case *ast.Link:
		dest := string(n.Destination)
		if !strings.HasPrefix(dest, "https://") && !strings.HasPrefix(dest, "http://") {
			break // a link inside the dashboard: its label alone
		}
		if entering {
			f.styled++
			f.h.WriteString(`<a href="` + html.EscapeString(dest) + `">`)
			f.linkAt = f.p.Len()
			break
		}
		f.styled--
		f.h.WriteString("</a>")
		if f.p.String()[f.linkAt:] != dest {
			f.p.WriteString(" (" + dest + ")")
		}
	case *ast.AutoLink:
		if entering {
			f.both(string(n.URL(f.src)))
		}
	case *ast.RawHTML:
		if entering {
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				f.both(string(seg.Value(f.src)))
			}
		}
	case *ast.Text:
		if entering {
			f.both(string(util.UnescapePunctuations(n.Segment.Value(f.src))))
			if n.SoftLineBreak() || n.HardLineBreak() {
				f.both("\n")
			}
		}
	case *ast.String:
		if entering {
			f.both(string(n.Value))
		}
	}
	return ast.WalkContinue, nil
}
