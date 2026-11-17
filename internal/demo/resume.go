package demo

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Page geometry in points: US Letter with 0.75 inch side margins.
const (
	pageW, pageH    = 612.0, 792.0
	marginX         = 54.0
	marginTop       = 48.0
	marginBottom    = 44.0
	bodySize        = 10.5
	bodyLead        = 13.6
	bulletIndent    = 16.0
	maxContentWidth = pageW - 2*marginX
	headingSize     = 12.0
	nameSize        = 24.0
	contactSize     = 9.5
	ruleGray        = "0.55"
)

type elemKind int

const (
	elemName elemKind = iota
	elemContact
	elemHeading
	elemRole
	elemBullet
	elemPara
)

// elem is one block of the CV. A role carries its organisation and dates.
type elem struct {
	kind                elemKind
	text, org, dateText string
}

var (
	roleRE  = lazyregexp.New(`^(.*?)\s*\((\d{4}-\d{2})(?: to (\d{4}-\d{2}|present))?\)$`)
	monthsE = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

// monthYear writes 2022-04 as "Apr 2022" and "present" as "Present".
func monthYear(ym string) string {
	if ym == "present" {
		return "Present"
	}
	var y, m int
	if _, err := fmt.Sscanf(ym, "%d-%d", &y, &m); err != nil || m < 1 || m > 12 {
		return ym
	}
	return fmt.Sprintf("%s %d", monthsE[m-1], y)
}

// parseCV reads the markdown subset the persona's CV uses: a "# " name, plain
// lines before the first heading as contact lines, "## " sections, "### "
// roles ("Title, Organisation (YYYY-MM to YYYY-MM|present)"), "- " bullets and
// paragraphs, where an unmarked line continues the block above it.
func parseCV(md string) []elem {
	var out []elem
	section := ""
	open := false // the last block takes continuation lines
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			open = false
		case strings.HasPrefix(t, "# "):
			out, open = append(out, elem{kind: elemName, text: t[2:]}), false
		case strings.HasPrefix(t, "## "):
			section = t[3:]
			out, open = append(out, elem{kind: elemHeading, text: section}), false
		case strings.HasPrefix(t, "### "):
			out, open = append(out, roleElem(t[4:], section)), false
		case strings.HasPrefix(t, "- "):
			out, open = append(out, elem{kind: elemBullet, text: t[2:]}), true
		case open:
			out[len(out)-1].text += " " + t
		case section == "":
			out = append(out, elem{kind: elemContact, text: t})
		default:
			out, open = append(out, elem{kind: elemPara, text: t}), true
		}
	}
	return out
}

func roleElem(head, section string) elem {
	e := elem{kind: elemRole, text: head}
	m := roleRE.FindStringSubmatch(head)
	if m == nil {
		return e
	}
	e.text = m[1]
	switch {
	case strings.EqualFold(section, "Education"):
		e.dateText = monthYear(m[3])
	case m[3] == "":
		e.dateText = monthYear(m[2])
	default:
		e.dateText = monthYear(m[2]) + " – " + monthYear(m[3])
	}
	if title, org, ok := strings.Cut(e.text, ", "); ok {
		e.text, e.org = title, org
	}
	return e
}

// page accumulates one page's content stream.
type page struct {
	buf bytes.Buffer
	y   float64
	err error
}

func (p *page) text(font string, size, x, y float64, s string) {
	b, err := winAnsi(s)
	if err != nil {
		p.err = errors.Join(p.err, err)
		return
	}
	fmt.Fprintf(&p.buf, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", font, size, x, y, pdfEscape(b))
}

func (p *page) rule(y float64) {
	fmt.Fprintf(&p.buf, "%s G 0.6 w %.2f %.2f m %.2f %.2f l S 0 G\n", ruleGray, marginX, y, pageW-marginX, y)
}

// wrapWidth breaks s at spaces into lines no wider than width points. A word
// wider than a line stays whole on its own line.
func wrapWidth(s string, size, width float64) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if cur != "" && textWidth(next, size, false) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur = next
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// pdfEscape writes bytes for a PDF literal string: the delimiters are escaped
// and a byte above ASCII is written as an octal escape.
func pdfEscape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '(' || c == ')' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "\\%03o", c)
		}
	}
	return sb.String()
}

// layout draws the elements top down and fails when they run past the bottom
// margin.
func layout(elems []elem) (*page, error) {
	p := &page{y: pageH - marginTop}
	centered := func(font string, size float64, s string, bold bool) {
		p.text(font, size, (pageW-textWidth(s, size, bold))/2, p.y, s)
	}
	for i, e := range elems {
		switch e.kind {
		case elemName:
			p.y -= nameSize * 0.8
			centered("F2", nameSize, e.text, true)
			p.y -= 4
		case elemContact:
			p.y -= contactSize * 1.35
			centered("F1", contactSize, e.text, false)
		case elemHeading:
			p.y -= 9
			p.y -= headingSize
			p.text("F2", headingSize, marginX, p.y, e.text)
			p.y -= 3.5
			p.rule(p.y)
			p.y -= 2
		case elemRole:
			if i > 0 && elems[i-1].kind != elemHeading {
				p.y -= 4
			}
			p.y -= bodyLead
			p.text("F2", bodySize, marginX, p.y, e.text)
			if e.dateText != "" {
				p.text("F1", bodySize, pageW-marginX-textWidth(e.dateText, bodySize, false), p.y, e.dateText)
			}
			if e.org != "" {
				p.y -= bodyLead
				p.text("F1", bodySize, marginX, p.y, e.org)
			}
		case elemBullet:
			for j, l := range wrapWidth(e.text, bodySize, maxContentWidth-bulletIndent) {
				p.y -= bodyLead
				if j == 0 {
					p.text("F1", bodySize, marginX+5, p.y, "•")
				}
				p.text("F1", bodySize, marginX+bulletIndent, p.y, l)
			}
		case elemPara:
			for _, l := range wrapWidth(e.text, bodySize, maxContentWidth) {
				p.y -= bodyLead
				p.text("F1", bodySize, marginX, p.y, l)
			}
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	if p.y < marginBottom {
		return nil, fmt.Errorf("demo: the résumé does not fit on one page: %.0f points too long", marginBottom-p.y)
	}
	return p, nil
}

// renderResume lays the CV out on one US Letter page in Helvetica and
// Helvetica-Bold and returns the PDF. The output has no dates or random ids,
// so the same CV always gives the same bytes.
func renderResume(cv string) ([]byte, error) {
	elems := parseCV(cv)
	name := "Resume"
	if len(elems) > 0 && elems[0].kind == elemName {
		name = elems[0].text
	}
	p, err := layout(elems)
	if err != nil {
		return nil, err
	}
	title, err := winAnsi(name + " - resume")
	if err != nil {
		return nil, err
	}
	author, err := winAnsi(name)
	if err != nil {
		return nil, err
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>", pageW, pageH),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", p.buf.Len(), p.buf.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Title (%s) /Author (%s) /Producer (Applysmith demo) >>", pdfEscape(title), pdfEscape(author)),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, len(objects), xref)
	return out.Bytes(), nil
}
