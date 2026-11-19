// Package assets embeds the reviewed document sources: the two LaTeX
// templates, the cover class, the two PDF verifier scripts, the hash-locked
// Python requirements and the font files. The templates are rendered here from
// typed data; nothing else produces TeX.
package assets

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing/fstest"
)

// The fonts are stored as `zopfli --gzip` files (name plus ".gz") to keep the
// repository small; every accessor below serves them decompressed, under their
// real names.
//
//go:embed resume.tex cover.tex cover.cls verify_pdf.py verify_layout.py requirements.txt fonts/lato/*.gz fonts/raleway/*.gz
var files embed.FS

// TemplateVersion names the revision of the shipped templates. A published
// version records it with the hash of each template file.
const TemplateVersion = "applysmith-templates-2"

// served is the embedded tree with each ".gz" file replaced by its
// decompressed content, built on first use.
var served = sync.OnceValue(func() fs.FS {
	out := fstest.MapFS{}
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		if name, ok := strings.CutSuffix(p, ".gz"); ok {
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				return err
			}
			if b, err = io.ReadAll(zr); err != nil {
				return err
			}
			p = name
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		panic("assets: embedded files are corrupt: " + err.Error())
	}
	return out
})

// FS returns the embedded files, rooted at the assets directory.
func FS() fs.FS { return served() }

// Read returns one embedded file.
func Read(name string) ([]byte, error) { return fs.ReadFile(served(), name) }

// FontFiles lists the font files relative to the assets directory.
func FontFiles() []string {
	var out []string
	for _, dir := range []string{"fonts/lato", "fonts/raleway"} {
		entries, err := fs.ReadDir(served(), dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			out = append(out, dir+"/"+e.Name())
		}
	}
	return out
}
