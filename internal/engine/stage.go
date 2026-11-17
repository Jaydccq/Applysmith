package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// staleStage is how old an abandoned run directory must be before a new page
// removes it: one left by a server that died before its tab closed.
const staleStage = 24 * time.Hour

// liveStage holds the run directories some page of this process still owns,
// so a sweep never removes the file of a tab that is open (a kept tab can
// outlive staleStage).
var liveStage = struct {
	mu   sync.Mutex
	dirs map[string]int
}{dirs: map[string]int{}}

func holdStage(dir string) {
	liveStage.mu.Lock()
	liveStage.dirs[dir]++
	liveStage.mu.Unlock()
}

func releaseStage(dir string) {
	liveStage.mu.Lock()
	if liveStage.dirs[dir]--; liveStage.dirs[dir] <= 0 {
		delete(liveStage.dirs, dir)
	}
	liveStage.mu.Unlock()
}

var errPageClosed = errors.New("the page is closed, nothing is staged for it")

// stageName makes a document's name safe as one path element: no separator,
// no control character, and never empty, "." or "..".
func stageName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	if strings.Trim(name, ".") == "" {
		return "document"
	}
	return name
}

// stage writes a document the browser must read from disk into a directory
// private to this run and returns its path. Every document gets a directory of
// its own, so equal names never collide. The file is kept until the tab closes
// (or Close is called): the browser reads a file input's file when the page
// uses it, long after the fill that set it has returned.
func (p *Page) stage(runID, fileName string, data []byte) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errPageClosed
	}
	if p.host.StageDir == "" {
		return "", errors.New("no staging directory is configured")
	}
	if err := os.MkdirAll(p.host.StageDir, 0o700); err != nil {
		return "", err
	}
	run := filepath.Join(p.host.StageDir, runID)
	if err := os.Mkdir(run, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if len(p.staged) == 0 || p.staged[len(p.staged)-1] != run {
		p.staged = append(p.staged, run)
		holdStage(run)
	}
	p.nstage++
	dir := filepath.Join(run, fmt.Sprint(p.nstage))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, stageName(filepath.Base(fileName)))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Close removes everything this page staged and stops it staging more. It runs
// by itself when the tab closes. An owner may call it earlier only when the tab
// is not being kept for the user: a kept tab's file input still points at the
// staged file, which then goes with the tab, not with the run. Safe to call
// more than once.
func (p *Page) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, d := range p.staged {
		_ = os.RemoveAll(d)
		releaseStage(d)
	}
	p.staged = nil
}

// sweepStaged removes run directories under root that no page has touched for
// staleStage and that no live page of this process owns.
func sweepStaged(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		liveStage.mu.Lock()
		_, live := liveStage.dirs[dir]
		liveStage.mu.Unlock()
		if live || !e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleStage {
			_ = os.RemoveAll(dir)
		}
	}
}
