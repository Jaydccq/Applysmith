package guard

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

const (
	ruleHistorySecret = "history-secret"
	// A blob above this size is counted but not read; the size budget already
	// keeps such files out of the tree.
	maxHistoryBlobScan = 16 << 20
)

// HistoryStats counts what a history scan looked at.
type HistoryStats struct {
	Commits int
	Blobs   int
	Skipped int // blobs that were binary or over the scan size limit
}

// ScanHistory applies the secret rules to every blob reachable from any ref of
// the repository at repo. It only reads: it runs rev-list and cat-file.
func ScanHistory(repo string) ([]Finding, HistoryStats, error) {
	var stats HistoryStats
	commits, err := git(repo, "rev-list", "--all", "--count")
	if err != nil {
		return nil, stats, err
	}
	stats.Commits, _ = strconv.Atoi(strings.TrimSpace(string(commits)))
	objects, err := git(repo, "rev-list", "--objects", "--all")
	if err != nil {
		return nil, stats, err
	}
	// "<sha> <path>" for trees and blobs; the first path an object appeared at.
	paths := map[string]string{}
	var order []string
	for _, line := range strings.Split(string(objects), "\n") {
		sha, p, _ := strings.Cut(line, " ")
		if len(sha) < 40 {
			continue
		}
		paths[sha] = p
		order = append(order, sha)
	}
	found, err := scanBlobs(repo, order, paths, &stats)
	return found, stats, err
}

// scanBlobs reads the objects in order through one git cat-file and applies
// the secret rules to every text blob, naming each by its path in paths.
func scanBlobs(repo string, order []string, paths map[string]string, stats *HistoryStats) ([]Finding, error) {
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = repo
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		w := bufio.NewWriter(in)
		for _, sha := range order {
			fmt.Fprintln(w, sha)
		}
		w.Flush()
		in.Close()
	}()
	// A read that stops early leaves git writing into a pipe nobody reads, so
	// it is killed before the wait rather than waited on.
	abort := func(err error) ([]Finding, error) {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}

	var found []Finding
	r := bufio.NewReaderSize(outPipe, 1<<20)
	for range order {
		header, err := r.ReadString('\n')
		if err != nil {
			return abort(fmt.Errorf("reading git cat-file: %w", err))
		}
		f := strings.Fields(header)
		if len(f) != 3 {
			return abort(fmt.Errorf("unexpected git cat-file header %q", strings.TrimSpace(header)))
		}
		size, _ := strconv.Atoi(f[2])
		if f[1] != "blob" || size > maxHistoryBlobScan {
			if _, err := io.CopyN(io.Discard, r, int64(size)+1); err != nil {
				return abort(err)
			}
			if f[1] == "blob" {
				stats.Skipped++
			}
			continue
		}
		data := make([]byte, size+1) // the trailing newline git adds
		if _, err := io.ReadFull(r, data); err != nil {
			return abort(err)
		}
		data = data[:size]
		stats.Blobs++
		if isBinary(data) {
			stats.Skipped++
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, h := range secretHits(line) {
				found = append(found, Finding{
					Rule: ruleHistorySecret, Path: f[0][:12] + ":" + paths[f[0]], Line: i + 1, Col: runeCol(line, h.off),
					Detail: "private key block or provider credential shape in history", Excerpt: mask(h.text), Value: h.text,
				})
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return found, nil
}
