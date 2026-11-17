// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"bytes"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

const reportCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'"

const reportCSS = `body{font:16px/1.55 system-ui,sans-serif;max-width:52rem;margin:2rem auto;padding:0 1rem;color:#1b1b1b;background:#fff}` +
	`h1,h2{line-height:1.25}h2{margin-top:2rem;border-bottom:1px solid #ddd;padding-bottom:.25rem}` +
	`table{border-collapse:collapse;width:100%}th,td{border:1px solid #ccc;padding:.35rem .5rem;text-align:left;vertical-align:top}` +
	`pre{background:#f5f5f5;padding:.75rem;overflow:auto}code{font-family:ui-monospace,monospace}` +
	`.meta{color:#555;font-size:.9rem}@media(prefers-color-scheme:dark){body{background:#161616;color:#e8e8e8}th,td{border-color:#444}pre{background:#222}h2{border-color:#444}.meta{color:#aaa}}`

var md = goldmark.New(goldmark.WithExtensions(extension.Table))

// escapeAngles turns "<" outside fenced code into an entity, so model text
// that looks like markup is shown as text. Raw HTML is also off in the
// renderer; this makes the text visible rather than omitted.
func escapeAngles(markdown string) string {
	var out []string
	fenced := false
	for _, l := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
		} else if !fenced {
			l = strings.ReplaceAll(l, "<", "&lt;")
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// imagesToLinks turns every markdown image into a plain link, so the page
// holds no <img> with a remote source and makes no request when opened.
func imagesToLinks(doc ast.Node) {
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			images = append(images, img)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		link := ast.NewLink()
		link.Destination, link.Title = img.Destination, img.Title
		for c := img.FirstChild(); c != nil; c = img.FirstChild() {
			link.AppendChild(link, c)
		}
		img.Parent().ReplaceChild(img.Parent(), img, link)
	}
}

// RenderHTML renders a report's markdown as a self-contained page: inline
// styles, no external requests, a restrictive Content-Security-Policy, and no
// raw HTML from model text.
func RenderHTML(title, markdown string) ([]byte, error) {
	header, body, _ := strings.Cut(markdown, "\n\n")
	var buf bytes.Buffer
	src := []byte(escapeAngles(body))
	doc := md.Parser().Parse(text.NewReader(src))
	imagesToLinks(doc)
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return nil, err
	}
	var page bytes.Buffer
	page.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\n")
	page.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"" + reportCSP + "\">\n")
	page.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	page.WriteString("<title>" + html.EscapeString(title) + "</title>\n<style>" + reportCSS + "</style></head><body>\n")
	page.WriteString("<pre class=\"meta\">" + html.EscapeString(header) + "</pre>\n")
	page.Write(buf.Bytes())
	page.WriteString("</body></html>\n")
	return page.Bytes(), nil
}
