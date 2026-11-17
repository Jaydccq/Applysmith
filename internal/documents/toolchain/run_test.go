//go:build !windows

package toolchain

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startSlack is the timeout of the timeout tests: long enough for a shell on a
// loaded machine to start its child and write the child's pid before it ends.
const startSlack = 2 * time.Second

func TestRun_TimeoutStopsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	_, err := run(context.Background(), command{Path: "/bin/sh", Timeout: startSlack,
		Args: []string{"-c", `sleep 60 & echo $! > "` + pidFile + `"; wait`}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > startSlack+5*time.Second {
		t.Fatalf("took %v", d)
	}
	assertDead(t, pidFile)
}

func TestRun_KillsDescendantsThatIgnoreTerm(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	start := time.Now()
	_, err := run(context.Background(), command{Path: "/bin/sh", Timeout: startSlack,
		Args: []string{"-c", `trap "" TERM; (trap "" TERM; sleep 60) & echo $! > "` + pidFile + `"; wait`}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d < startSlack+killGrace {
		t.Fatalf("returned after %v, before the %v grace period", d, killGrace)
	}
	assertDead(t, pidFile)
}

func TestRun_ExcessOutputIsTerminated(t *testing.T) {
	start := time.Now()
	out, err := run(context.Background(), command{Path: "/usr/bin/yes", Timeout: 30 * time.Second})
	if !errors.Is(err, ErrLogLimit) {
		t.Fatalf("err = %v, want ErrLogLimit", err)
	}
	if len(out.Stdout) > MaxLogBytes {
		t.Fatalf("kept %d bytes, limit %d", len(out.Stdout), MaxLogBytes)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRun_EnvironmentIsExactlyWhatWasGiven(t *testing.T) {
	t.Setenv("APPLYSMITH_TEST_SECRET", "must-not-leak")
	out, err := run(context.Background(), command{Path: "/usr/bin/env", Env: []string{"PATH=/usr/bin", "FOO=bar"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out.Stdout))
	if len(got) != 2 || strings.Contains(string(out.Stdout), "must-not-leak") {
		t.Fatalf("environment = %q", got)
	}
}

func TestRun_NonZeroExitIsAnError(t *testing.T) {
	out, err := run(context.Background(), command{Path: "/bin/sh", Args: []string{"-c", "echo oops >&2; exit 3"}})
	if err == nil || out.ExitCode != 3 || !strings.Contains(string(out.Stderr), "oops") {
		t.Fatalf("err = %v, exit = %d, stderr = %q", err, out.ExitCode, out.Stderr)
	}
}

func assertDead(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the child never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant %d is still running", pid)
}

func TestExtractPDFTextMissingMalformedAndCancelled(t *testing.T) {
	t.Setenv(EnvToolPath, t.TempDir())
	if _, err := ExtractPDFText(t.Context(), []byte("%PDF-1.4")); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing extractor: %v", err)
	}
	if _, err := ExtractPDFText(t.Context(), []byte("not a PDF")); err == nil {
		t.Fatal("accepted non-PDF")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractPDFText(ctx, []byte("%PDF-1.4")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestExtractPDFTextPoppler(t *testing.T) {
	if _, ok := lookup(SearchDirs(os.Getenv), "pdftotext"); !ok {
		t.Skip("Poppler not installed")
	}
	// A real one-page PDF, with correct byte offsets and a text content stream.
	content := "BT /F1 12 Tf 14 TL 72 720 Td " + strings.Repeat("(Harbor berth allocation solver.) Tj T* ", 8) + "ET"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)}
	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	text, err := ExtractCVText(t.Context(), "selected.pdf", []byte(pdf.String()))
	if err != nil || !strings.Contains(text, "Harbor berth allocation solver") {
		t.Fatalf("extract: %q %v", text, err)
	}
	if _, err := ExtractPDFText(t.Context(), []byte("%PDF-1.4 damaged")); err == nil {
		t.Fatal("accepted damaged PDF")
	}
}

func cvDOCX(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, data := range parts {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCVText_DOCXRelationshipsTextAndBounds(t *testing.T) {
	const ns = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	const relNS = "http://schemas.openxmlformats.org/package/2006/relationships"
	const relType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	makeParts := func() map[string]string {
		return map[string]string{
			"_rels/.rels":                `<Relationships xmlns="` + relNS + `"><Relationship Type="` + relType + `officeDocument" Target="custom/body.xml"/></Relationships>`,
			"custom/body.xml":            `<w:document xmlns:w="` + ns + `"><w:body><w:p><w:r><w:t>Riley &amp; &#x6D4B;&#x8BD5;</w:t><w:tab/><w:t>Platform</w:t><w:br/><w:t>Engineer</w:t></w:r></w:p><w:p><w:r><w:t>` + strings.Repeat("Built internal tooling and maintained platform services. ", 5) + `</w:t></w:r></w:p></w:body></w:document>`,
			"custom/_rels/body.xml.rels": `<Relationships xmlns="` + relNS + `"><Relationship Type="` + relType + `header" Target="../shared/contact.xml"/><Relationship Type="` + relType + `footer" Target="https://example.com/outside.xml" TargetMode="External"/></Relationships>`,
			"shared/contact.xml":         `<w:hdr xmlns:w="` + ns + `"><w:p><w:r><w:t>Contact in header</w:t></w:r></w:p></w:hdr>`,
		}
	}
	for _, strict := range []bool{false, true} {
		parts := makeParts()
		if strict {
			for n, s := range parts {
				parts[n] = strings.ReplaceAll(s, ns, "http://purl.oclc.org/ooxml/wordprocessingml/main")
			}
		}
		got, err := ExtractCVText(t.Context(), "selected.DOCX", cvDOCX(t, parts))
		if err != nil || !strings.Contains(got, "Riley & \u6d4b\u8bd5\tPlatform\nEngineer\n") || !strings.Contains(got, "Contact in header") {
			t.Fatalf("DOCX strict=%t text=%q error=%v", strict, got, err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
	}{
		{"no relationships", func(p map[string]string) { delete(p, "_rels/.rels") }},
		{"missing main", func(p map[string]string) { delete(p, "custom/body.xml") }},
		{"malformed XML", func(p map[string]string) { p["custom/body.xml"] = `<w:document>` }},
		{"unknown XML namespace", func(p map[string]string) {
			p["custom/body.xml"] = `<document><p><t>not WordprocessingML</t></p></document>`
		}},
		{"decompression bound", func(p map[string]string) { p["custom/body.xml"] = strings.Repeat("a", MaxPDFBytes+1) }},
		{"text bound", func(p map[string]string) {
			p["custom/body.xml"] = `<w:document xmlns:w="` + ns + `"><w:p><w:r><w:t>` + strings.Repeat("x", MaxCVTextBytes+1) + `</w:t></w:r></w:p></w:document>`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := makeParts()
			tc.change(p)
			if _, err := ExtractCVText(t.Context(), "cv.docx", cvDOCX(t, p)); err == nil {
				t.Fatal("invalid DOCX accepted")
			}
		})
	}
}

func TestCVText_RefusesUnsupportedEmptyBinaryAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"cv.pdf", []byte("not PDF")}, {"cv.docx", []byte("not ZIP")}, {"cv.exe", []byte("text")},
		{"cv.md", []byte(" ")}, {"cv.txt", []byte{0xff, 0xfe}}, {"cv.txt", []byte{0}},
		{"cv.txt", bytes.Repeat([]byte("x"), MaxCVTextBytes+1)}, {"cv.docx", make([]byte, MaxPDFBytes+1)},
	} {
		if _, err := ExtractCVText(t.Context(), tc.name, tc.data); err == nil {
			t.Fatalf("accepted %s (%d bytes)", tc.name, len(tc.data))
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractCVText(ctx, "cv.txt", []byte("text")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	t.Setenv(EnvToolPath, t.TempDir())
	if _, err := ExtractCVText(t.Context(), "cv.pdf", []byte("%PDF-1.4")); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing Poppler=%v", err)
	}
}

// A selected CV's plain text comes back with the headings cv.md is read by.
func TestCVText_PlainTextGetsTheCVHeadings(t *testing.T) {
	body := strings.Repeat("Built internal tooling and maintained platform services. ", 5)
	got, err := ExtractCVText(t.Context(), "cv.txt", []byte("Riley Sample\nriley@mailbox.test\n\nExperience\n• "+body+"\n"))
	if err != nil || !strings.HasPrefix(got, "# Riley Sample\n") || !strings.Contains(got, "\n## Experience\n- Built internal tooling") {
		t.Fatalf("text=%q error=%v", got, err)
	}
}
