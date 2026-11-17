package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	logFileName  = "server.log"
	logMaxBytes  = 10 << 20
	logKeepFiles = 5
)

// rotatingFile is an io.Writer over a log file that rolls over at maxBytes
// and keeps keep files in total (the live one plus keep-1 older ones).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func openRotating(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		// A rotation that could not move the live file leaves it open: the
		// line goes there and the next write tries the rotation again.
		if err := r.rotate(); err != nil && r.f == nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate moves the live file aside and opens a new one. It reopens the live
// path even when the move failed; when no file could be opened r.f is nil and
// the next write opens it.
func (r *rotatingFile) rotate() error {
	err := r.f.Close()
	r.f = nil
	if err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep-1))
	for i := r.keep - 2; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if r.keep > 1 {
		err = os.Rename(r.path, r.path+".1")
	} else {
		err = os.Remove(r.path)
	}
	return errors.Join(err, r.open())
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

// logLevel reads APPLYSMITH_LOG_LEVEL; anything unrecognized means info.
func logLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// OpenLog opens a log file that rotates like the server log: 10 MiB per file,
// 5 files kept. The desktop shell writes its log the same way.
func OpenLog(path string) (io.WriteCloser, error) {
	return openRotating(path, logMaxBytes, logKeepFiles)
}
