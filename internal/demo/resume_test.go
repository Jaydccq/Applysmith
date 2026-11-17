package demo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pdfObjects checks the file's structure the way a reader opens it: the
// startxref offset leads to the table, every table entry leads to its own
// "N 0 obj", and the trailer names the table size and the catalog. It returns
// the body of each object by number.
func pdfObjects(t *testing.T, pdf []byte) map[int]string {
	t.Helper()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("the file has no PDF header or end marker")
	}
	i := bytes.LastIndex(pdf, []byte("startxref\n"))
	if i < 0 {
		t.Fatal("no startxref")
	}
	rest := string(pdf[i+len("startxref\n"):])
	xrefAt, err := strconv.Atoi(strings.SplitN(rest, "\n", 2)[0])
	if err != nil || xrefAt <= 0 || xrefAt >= len(pdf) {
		t.Fatalf("startxref %q is not an offset into the file", rest)
	}
	table := string(pdf[xrefAt:])
	if !strings.HasPrefix(table, "xref\n") {
		t.Fatalf("startxref points at %q, not the xref table", table[:min(20, len(table))])
	}
	lines := strings.Split(table, "\n")
	var first, count int
	if _, err := fmt.Sscanf(lines[1], "%d %d", &first, &count); err != nil || first != 0 {
		t.Fatalf("xref subsection header %q", lines[1])
	}
	if lines[2] != "0000000000 65535 f " {
		t.Fatalf("the free entry is %q", lines[2])
	}
	objs := map[int]string{}
	for n := 1; n < count; n++ {
		entry := lines[2+n]
		if len(entry) != 19 || !strings.HasSuffix(entry, " 00000 n ") {
			t.Fatalf("xref entry %d is %q", n, entry)
		}
		off, err := strconv.Atoi(entry[:10])
		if err != nil {
			t.Fatal(err)
		}
		head := strconv.Itoa(n) + " 0 obj\n"
		if !bytes.HasPrefix(pdf[off:], []byte(head)) {
			t.Fatalf("xref entry %d points at %q, want %q", n, pdf[off:off+12], head)
		}
		end := bytes.Index(pdf[off:], []byte("\nendobj\n"))
		if end < 0 {
			t.Fatalf("object %d has no endobj", n)
		}
		objs[n] = string(pdf[off+len(head) : off+end])
	}
	trailer := strings.Join(lines[2+count:], "\n")
	if !strings.Contains(trailer, "/Size "+strconv.Itoa(count)) || !strings.Contains(trailer, "/Root 1 0 R") {
		t.Fatalf("trailer %q does not name the table size and the catalog", trailer)
	}
	return objs
}

var tjRE = regexp.MustCompile(`\((?:\\.|[^\\)])*\) Tj`)

// pdfText returns the strings the page shows, in content-stream order.
func pdfText(t *testing.T, objs map[int]string) []string {
	t.Helper()
	content := objs[4]
	m := regexp.MustCompile(`(?s)^<< /Length (\d+) >>\nstream\n(.*)\nendstream$`).FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("object 4 is not a stream: %.60q", content)
	}
	if n, _ := strconv.Atoi(m[1]); n != len(m[2]) {
		t.Fatalf("/Length is %d, the stream holds %d bytes", n, len(m[2]))
	}
	var out []string
	for _, tj := range tjRE.FindAllString(m[2], -1) {
		s := tj[1 : len(tj)-len(") Tj")]
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			switch {
			case s[i] != '\\':
				b.WriteByte(s[i])
			case s[i+1] >= '0' && s[i+1] <= '7':
				v, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
				b.WriteString(winAnsiRune(byte(v)))
				i += 3
			default:
				b.WriteByte(s[i+1])
				i++
			}
		}
		out = append(out, b.String())
	}
	return out
}

func winAnsiRune(c byte) string {
	for r, x := range winAnsiExtra {
		if x.code == c {
			return string(r)
		}
	}
	return "?"
}

func TestResumePDF_IsAValidOnePagePDFWithCorrectXrefOffsets(t *testing.T) {
	objs := pdfObjects(t, ResumePDF())
	if !strings.Contains(objs[2], "/Count 1") || !strings.Contains(objs[2], "/Kids [3 0 R]") {
		t.Errorf("the page tree is %q, want one page", objs[2])
	}
	pages := 0
	for _, body := range objs {
		if strings.Contains(body, "/Type /Page ") {
			pages++
		}
	}
	if pages != 1 {
		t.Errorf("%d page objects, want 1", pages)
	}
	if !strings.Contains(objs[3], "/MediaBox [0 0 612 792]") {
		t.Errorf("page object %q has no Letter media box", objs[3])
	}
	for _, font := range []string{"/BaseFont /Helvetica ", "/BaseFont /Helvetica-Bold "} {
		if !strings.Contains(objs[5]+objs[6], font) {
			t.Errorf("no font object with %s", font)
		}
	}
	if !strings.Contains(objs[7], "(Alex Example - resume)") {
		t.Errorf("document info is %q", objs[7])
	}
}

func TestResumePDF_IsDeterministicAndCarriesNoTimestamp(t *testing.T) {
	a, b := ResumePDF(), ResumePDF()
	if !bytes.Equal(a, b) {
		t.Fatal("two renders differ")
	}
	for _, s := range []string{"CreationDate", "ModDate", "/ID"} {
		if bytes.Contains(a, []byte(s)) {
			t.Errorf("the PDF carries %s", s)
		}
	}
}

func TestResumePDF_ShowsTheNameEmployersAndSectionsInReadingOrder(t *testing.T) {
	shown := strings.Join(pdfText(t, pdfObjects(t, ResumePDF())), "\n")
	cv := string(File(FileCV))
	want := []string{"Alex Example"}
	for _, l := range strings.Split(cv, "\n") {
		switch {
		case strings.HasPrefix(l, "## "):
			want = append(want, l[3:])
		case strings.HasPrefix(l, "### "):
			if _, org, ok := strings.Cut(l[4:], ", "); ok {
				org, _, _ = strings.Cut(org, " (")
				want = append(want, strings.SplitN(org, ",", 2)[0])
			}
		}
	}
	for _, c := range []string{"Northwind Labs", "Contoso Cloud", "Fabrikam Robotics", "Example University", "Apr 2022 \u2013 Present", "May 2017"} {
		if !strings.Contains(shown, c) {
			t.Errorf("the page does not show %q", c)
		}
	}
	at := 0
	for _, w := range want {
		i := strings.Index(shown[at:], w)
		if i < 0 {
			t.Fatalf("%q is missing or out of order after offset %d", w, at)
		}
		at += i + len(w)
	}
}

func TestResumePDF_TextExtractsWithPdftotext(t *testing.T) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext is not installed")
	}
	path := filepath.Join(t.TempDir(), "resume.pdf")
	if err := os.WriteFile(path, ResumePDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext failed: %v", err)
	}
	text := string(out)
	if strings.Count(text, "\f") != 1 {
		t.Errorf("pdftotext found %d pages, want 1", strings.Count(text, "\f"))
	}
	at := 0
	for _, w := range []string{"Alex Example", "alex@example.com", "Summary", "Experience", "Northwind Labs", "Contoso Cloud", "Fabrikam Robotics", "Projects", "Education", "Example University", "Skills"} {
		i := strings.Index(text[at:], w)
		if i < 0 {
			t.Fatalf("pdftotext output misses %q after offset %d:\n%s", w, at, text)
		}
		at += i + len(w)
	}
}

func TestRenderResume_FailsWhenTheCVDoesNotFitOnOnePage(t *testing.T) {
	cv := "# Alex Example\n\n## Experience\n\n" + strings.Repeat("- a bullet that takes a line of the page\n", 120)
	if _, err := renderResume(cv); err == nil || !strings.Contains(err.Error(), "one page") {
		t.Fatalf("error = %v, want one that says the résumé does not fit on one page", err)
	}
}

func TestRenderResume_RefusesACharacterTheMetricsDoNotCover(t *testing.T) {
	if _, err := renderResume("# Alex \u00c9xample\n"); err == nil {
		t.Fatal("a character outside the metric tables was written with a guessed width")
	}
}
