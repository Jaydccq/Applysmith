package main

import (
	"io"
	"os"
	"strings"
	"sync"
)

// tailWindow is how much of the end of a log file is read to find its last
// lines.
const tailWindow = 256 << 10

// tailFile returns the last n lines of a log file, or nil when it is missing or
// empty.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	off := max(st.Size()-tailWindow, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 {
		lines = lines[1:] // the first line was cut by the window
	}
	return lastLines(lines, n)
}

// lineRing keeps the last lines this process logged, for a failure that
// happens before any log file exists.
type lineRing struct {
	mu    sync.Mutex
	lines []string
	part  string
}

func (r *lineRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part += string(p)
	for {
		line, rest, ok := strings.Cut(r.part, "\n")
		if !ok {
			break
		}
		r.lines = append(r.lines, line)
		r.part = rest
	}
	if len(r.lines) > 4*maxLogLines {
		r.lines = append([]string(nil), r.lines[len(r.lines)-maxLogLines:]...)
	}
	return len(p), nil
}

func (r *lineRing) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), lastLines(r.lines, maxLogLines)...)
}
