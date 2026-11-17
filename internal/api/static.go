package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>Applysmith</title><p>The dashboard assets are not built into this binary.</p>`

// static serves the embedded dashboard. Paths without a file extension fall
// back to the index document so client-side routes survive a reload; a missing
// file with an extension and any unknown API path are typed errors.
func (s *Server) static(c *apikit.Call) (apikit.Reply, error) {
	p := path.Clean("/" + c.R.PathValue("path"))
	if p == "/api" || strings.HasPrefix(p, "/api/") {
		if requestIsRemote(c.R) {
			return apikit.Reply{}, remoteForbidden()
		}
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such route")
	}
	switch {
	case requestIsRemote(c.R):
		// A tunnel visitor without a device credential gets the pairing page
		// for any page address and nothing else.
		if c.P.Kind != apikit.PrincipalRemoteDevice {
			if path.Ext(p) != "" {
				return apikit.Reply{}, errUnauthenticated()
			}
			return s.pairPage(c)
		}
	case p == "/":
		serve, err := s.openDocument(c)
		if err != nil || !serve {
			return apikit.Reply{}, err
		}
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = "index.html"
	}
	if s.cfg.Assets == nil {
		if name != "index.html" && path.Ext(name) != "" {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such file")
		}
		c.W.Header().Set("Cache-Control", "no-store")
		c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(c.W, placeholderPage)
		c.Handled = true
		return apikit.Reply{}, nil
	}
	data, err := fs.ReadFile(s.cfg.Assets, name)
	if err != nil {
		if path.Ext(name) != "" {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such file")
		}
		name = "index.html"
		if data, err = fs.ReadFile(s.cfg.Assets, name); err != nil {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no dashboard is installed")
		}
	}
	h := c.W.Header()
	switch {
	case strings.HasPrefix(name, "assets/"):
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "index.html":
		h.Set("Cache-Control", "no-cache")
	default:
		h.Set("Cache-Control", "no-cache")
	}
	if compressible(name, data) {
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(c.R.Header.Get("Accept-Encoding")) {
			data = s.gzipped.of(name, data)
			h.Set("Content-Encoding", "gzip")
		}
	}
	http.ServeContent(c.W, c.R, name, time.Time{}, bytes.NewReader(data))
	c.Handled = true
	return apikit.Reply{}, nil
}

// gzipCache holds the gzip form of each embedded dashboard file, made on its
// first compressed request: the files cannot change while the binary runs, so
// each is compressed once at the best level and the set is bounded by the
// bundle.
type gzipCache struct{ m sync.Map }

func (g *gzipCache) of(name string, data []byte) []byte {
	if v, ok := g.m.Load(name); ok {
		return v.([]byte)
	}
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = w.Write(data)
	_ = w.Close()
	v, _ := g.m.LoadOrStore(name, b.Bytes())
	return v.([]byte)
}

// compressible reports a text file large enough for gzip to pay off; images
// and fonts are compressed formats already.
func compressible(name string, data []byte) bool {
	switch path.Ext(name) {
	case ".js", ".mjs", ".css", ".html", ".svg", ".json", ".map", ".txt", ".webmanifest":
		return len(data) >= 1024
	}
	return false
}

// acceptsGzip reads an Accept-Encoding header: gzip (or *) listed without a
// zero quality.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if coding = strings.ToLower(strings.TrimSpace(coding)); coding != "gzip" && coding != "*" {
			continue
		}
		q := 1.0
		if k, v, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		return q > 0
	}
	return false
}
