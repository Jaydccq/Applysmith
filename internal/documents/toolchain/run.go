package toolchain

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/cvparse"
)

// Limits of one subprocess, fixed by the document-toolchain spec.
const (
	EngineTimeout   = 60 * time.Second
	HelperTimeout   = 30 * time.Second
	killGrace       = 2 * time.Second
	MaxLogBytes     = 1 << 20
	MaxPDFBytes     = 10 << 20
	MaxScratchBytes = 100 << 20
)

// Typed failures of a subprocess.
var (
	ErrTimeout   = errors.New("toolchain: the process exceeded its time limit")
	ErrLogLimit  = errors.New("toolchain: the process exceeded its output limit")
	ErrCancelled = errors.New("toolchain: cancelled")
)

// command is one fixed invocation: an absolute executable and an argument
// vector, never a shell line.
type command struct {
	Path    string
	Args    []string
	Dir     string
	Env     []string
	Stdin   []byte
	Timeout time.Duration
}

type output struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// limitedBuffer keeps at most max bytes and records that more arrived.
type limitedBuffer struct {
	buf        bytes.Buffer
	max        int
	onOverflow func()
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room < len(p) {
		if room > 0 {
			l.buf.Write(p[:room])
		}
		l.onOverflow()
		return len(p), nil
	}
	return l.buf.Write(p)
}

// run executes c and stops its whole process group on timeout, cancellation
// or excess output: terminate first, kill after killGrace.
func run(ctx context.Context, c command) (output, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = HelperTimeout
	}
	ctx, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	overflow := func() { cancel(ErrLogLimit) }
	stdout, stderr := &limitedBuffer{max: MaxLogBytes, onOverflow: overflow}, &limitedBuffer{max: MaxLogBytes, onOverflow: overflow}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return output{}, fmt.Errorf("toolchain: start %s: %w", c.Path, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
		// A process that ended by itself after passing the output limit is
		// still over the limit: its kept output is cut short.
		if errors.Is(context.Cause(ctx), ErrLogLimit) {
			err = ErrLogLimit
		}
	case <-ctx.Done():
		signalGroup(cmd.Process.Pid, false)
		select {
		case <-done:
		case <-time.After(killGrace):
			signalGroup(cmd.Process.Pid, true)
			<-done
		}
		// Descendants that ignored the termination request are gone now.
		signalGroup(cmd.Process.Pid, true)
		switch cause := context.Cause(ctx); {
		case errors.Is(cause, ErrLogLimit):
			err = ErrLogLimit
		case errors.Is(cause, context.DeadlineExceeded):
			err = ErrTimeout
		default:
			err = ErrCancelled
		}
	}
	out := output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), ExitCode: cmd.ProcessState.ExitCode()}
	switch {
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrCancelled), errors.Is(err, ErrLogLimit):
		return out, err
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("toolchain: %s exited with status %d", baseName(c.Path), out.ExitCode)
		}
		return out, err
	}
	return out, nil
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// ExtractPDFText reads a PDF through Poppler alone; importing knowledge does not
// require TeX, Python or a complete document-generation installation. Input is
// stdin, output is UTF-8 stdout, with the shared time/output/process bounds.
func ExtractPDFText(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > MaxPDFBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return "", errors.New("expected a PDF smaller than 10 MiB")
	}
	dirs := SearchDirs(os.Getenv)
	executable, ok := lookup(dirs, "pdftotext")
	if !ok {
		return "", fmt.Errorf("%w: install Poppler (`brew install poppler` on macOS)", exec.ErrNotFound)
	}
	out, err := run(ctx, command{Path: executable, Args: []string{"-enc", "UTF-8", "-", "-"},
		Stdin: data, Env: []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator)), "LC_ALL=C"}})
	if err != nil {
		return "", err
	}
	if !utf8.Valid(out.Stdout) {
		return "", errors.New("PDF extraction returned invalid UTF-8")
	}
	if strings.TrimSpace(string(out.Stdout)) == "" {
		return "", errors.New("the PDF has no selectable text; paste its text instead, or apply OCR before import")
	}
	return string(out.Stdout), nil
}

// MaxCVTextBytes is the workspace CV's UTF-8 text limit, separate from the
// 10 MiB selected-file limit used for PDFs and DOCX archives.
const MaxCVTextBytes = 256 << 10

// ExtractCVText converts a selected local file without storing it or inferring
// facts. A text with no Markdown heading, as a PDF's or DOCX's is, gets the
// headings and list items cv.md is read by (cvparse.Markdown); its words are
// unchanged. The caller presents the returned text for review before saving it.
func ExtractCVText(ctx context.Context, name string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > MaxPDFBytes {
		return "", errors.New("CV files are limited to 10 MiB")
	}
	var text string
	var err error
	switch strings.ToLower(path.Ext(strings.ReplaceAll(name, `\`, "/"))) {
	case "", ".md", ".markdown", ".txt":
		text = string(data)
	case ".pdf":
		text, err = ExtractPDFText(ctx, data)
	case ".docx":
		text, err = extractDOCX(ctx, data)
	default:
		return "", errors.New("select a PDF, DOCX, Markdown or plain-text CV")
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text = cvparse.Markdown(text)
	switch {
	case len(text) > MaxCVTextBytes:
		return "", errors.New("extracted CV text exceeds 256 KiB; shorten the document before importing")
	case !utf8.ValidString(text) || strings.ContainsRune(text, 0):
		return "", errors.New("CV text must be UTF-8 without NUL bytes")
	case utf8.RuneCountInString(strings.TrimSpace(text)) < 200:
		return "", errors.New("extracted CV has fewer than 200 characters and may be image-only; paste the CV text instead")
	}
	return text, nil
}

// extractDOCX reads only main text, headers and footers. Embedded files,
// relationships and external links are never opened. The aggregate expanded
// XML and text are bounded even when the selected ZIP compresses extremely well.
func extractDOCX(ctx context.Context, data []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("the DOCX is not a readable ZIP document")
	}
	files := map[string]*zip.File{}
	for _, f := range z.File {
		if files[f.Name] != nil {
			return "", errors.New("the DOCX has duplicate package parts")
		}
		files[f.Name] = f
	}
	remaining := int64(MaxPDFBytes)
	read := func(name string) ([]byte, error) {
		f := files[name]
		if f == nil {
			return nil, errors.New("a required DOCX package part is missing")
		}
		if f.UncompressedSize64 > uint64(remaining) {
			return nil, errors.New("expanded DOCX text parts exceed 10 MiB")
		}
		r, err := f.Open()
		if err != nil {
			return nil, errors.New("a DOCX package part cannot be opened")
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, remaining+1))
		if err != nil {
			return nil, errors.New("a DOCX package part cannot be read")
		}
		if int64(len(b)) > remaining {
			return nil, errors.New("expanded DOCX text parts exceed 10 MiB")
		}
		remaining -= int64(len(b))
		return b, nil
	}
	root, err := read("_rels/.rels")
	if err != nil {
		return "", err
	}
	rels, err := docxRelations(root)
	if err != nil {
		return "", err
	}
	main := ""
	for _, r := range rels {
		if docxRelationIs(r.Type, "officeDocument") && r.Mode != "External" {
			if main != "" {
				return "", errors.New("the DOCX has multiple main documents")
			}
			main = path.Clean(strings.TrimPrefix(r.Target, "/"))
		}
	}
	if main == "" || strings.HasPrefix(main, "../") {
		return "", errors.New("the DOCX has no internal main document")
	}
	parts := map[string]string{main: "document"}
	relName := path.Join(path.Dir(main), "_rels", path.Base(main)+".rels")
	if files[relName] != nil {
		b, err := read(relName)
		if err != nil {
			return "", err
		}
		rr, err := docxRelations(b)
		if err != nil {
			return "", err
		}
		for _, r := range rr {
			if r.Mode == "External" {
				continue
			}
			kind := ""
			if docxRelationIs(r.Type, "header") {
				kind = "hdr"
			}
			if docxRelationIs(r.Type, "footer") {
				kind = "ftr"
			}
			if kind == "" {
				continue
			}
			target := strings.TrimPrefix(r.Target, "/")
			if !strings.HasPrefix(r.Target, "/") {
				target = path.Join(path.Dir(main), target)
			}
			target = path.Clean(target)
			if strings.HasPrefix(target, "../") {
				return "", errors.New("a DOCX text part leaves the package")
			}
			parts[target] = kind
		}
	}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		b, err := read(name)
		if err != nil {
			return "", err
		}
		if err := docxPart(ctx, b, parts[name], &out); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func docxPart(ctx context.Context, data []byte, root string, out *strings.Builder) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	// listed is true once the open paragraph has written its list bullet.
	listed := false
	wordNS := func(s string) bool {
		return s == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" || s == "http://purl.oclc.org/ooxml/wordprocessingml/main"
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("a DOCX text part contains malformed XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || t.Name.Local != root || !wordNS(t.Name.Space) {
					return errors.New("a DOCX text part has an invalid document root")
				}
			}
			depth++
			if !wordNS(t.Name.Space) {
				continue
			}
			switch t.Name.Local {
			case "t":
				var value string
				if err := d.DecodeElement(&value, &t); err != nil {
					return errors.New("DOCX text is malformed")
				}
				depth--
				if out.Len()+len(value) > MaxCVTextBytes {
					return errors.New("extracted CV text exceeds 256 KiB")
				}
				out.WriteString(value)
			case "tab":
				out.WriteByte('\t')
			case "br", "cr":
				out.WriteByte('\n')
			case "numPr":
				// A list paragraph's bullet is drawn from its numbering, not
				// held in its text; numId 0 takes a paragraph out of a list.
				var n struct {
					ID struct {
						Val string `xml:"val,attr"`
					} `xml:"numId"`
				}
				if err := d.DecodeElement(&n, &t); err != nil {
					return errors.New("DOCX text is malformed")
				}
				depth--
				if n.ID.Val != "0" && !listed {
					out.WriteString("• ")
					listed = true
				}
			}
		case xml.EndElement:
			depth--
			if wordNS(t.Name.Space) && t.Name.Local == "p" {
				out.WriteByte('\n')
				listed = false
			}
		}
		if out.Len() > MaxCVTextBytes {
			return errors.New("extracted CV text exceeds 256 KiB")
		}
	}
	if roots != 1 {
		return errors.New("the DOCX text part is empty")
	}
	return nil
}

type docxRelation struct {
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

func docxRelations(data []byte) ([]docxRelation, error) {
	var v struct {
		XMLName   xml.Name       `xml:"http://schemas.openxmlformats.org/package/2006/relationships Relationships"`
		Relations []docxRelation `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &v); err != nil {
		return nil, errors.New("DOCX package relationships are malformed")
	}
	for _, r := range v.Relations {
		if r.Target == "" {
			return nil, errors.New("a DOCX relationship has no target")
		}
	}
	return v.Relations, nil
}

func docxRelationIs(value, kind string) bool {
	return value == "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"+kind ||
		value == "http://purl.oclc.org/ooxml/officeDocument/relationships/"+kind
}
