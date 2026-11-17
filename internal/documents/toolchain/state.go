package toolchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Smoke status values of a Report.
const (
	SmokeCurrent = "current"
	SmokeNone    = "none"
	SmokeStale   = "stale"
	SmokeFailed  = "failed"
)

const smokeSchema = 1

// SmokeDocument is the outcome of one compiled and verified synthetic document.
type SmokeDocument struct {
	Name      string   `json:"name"`
	Engine    string   `json:"engine"`
	Passes    int      `json:"passes"`
	TeXSHA256 string   `json:"tex_sha256"`
	PDFSHA256 string   `json:"pdf_sha256"`
	PDFBytes  int      `json:"pdf_bytes"`
	Pages     int      `json:"pages"`
	Verify    string   `json:"verify"`
	Layout    string   `json:"layout"`
	Previews  []string `json:"previews"`
	Problems  []string `json:"problems,omitempty"`
}

// SmokeRecord is the cached result of the explicit smoke. It is valid only
// for the Key it was written under.
type SmokeRecord struct {
	SchemaVersion int             `json:"schema_version"`
	Key           string          `json:"key"`
	At            time.Time       `json:"at"`
	Passed        bool            `json:"passed"`
	ClosureFiles  int             `json:"closure_files"`
	Documents     []SmokeDocument `json:"documents"`
}

// maxSmokeRecords is how many keys keep a record; the oldest are removed when
// a new one is written.
const maxSmokeRecords = 8

func readSmoke(root Root, key string) (SmokeRecord, error) {
	var rec SmokeRecord
	b, err := os.ReadFile(root.smokeFile(key))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	if rec.SchemaVersion != smokeSchema {
		return rec, fmt.Errorf("toolchain: smoke record schema %d", rec.SchemaVersion)
	}
	return rec, nil
}

func smokeStatus(root Root, key string) string {
	rec, err := readSmoke(root, key)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return SmokeNone
	case err != nil, rec.Key != key:
		return SmokeStale
	case !rec.Passed:
		return SmokeFailed
	}
	return SmokeCurrent
}

// writeSmoke publishes the record under its key with a rename, so a reader
// never sees half of it, and drops the records beyond the newest few.
func writeSmoke(root Root, rec SmokeRecord) error {
	rec.SchemaVersion = smokeSchema
	if filepath.Base(rec.Key) != rec.Key || rec.Key == "" || rec.Key == "." || rec.Key == ".." {
		return fmt.Errorf("toolchain: smoke key %q is not a file name", rec.Key)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root.smokeDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root.smokeDir(), "smoke-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), root.smokeFile(rec.Key)); err != nil {
		return err
	}
	pruneSmoke(root)
	return nil
}

// pruneSmoke keeps the newest maxSmokeRecords records. A failure leaves extra
// records, which only cost disk.
func pruneSmoke(root Root) {
	entries, err := os.ReadDir(root.smokeDir())
	if err != nil {
		return
	}
	type rec struct {
		name string
		at   time.Time
	}
	var recs []rec
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			recs = append(recs, rec{e.Name(), fi.ModTime()})
		}
	}
	if len(recs) <= maxSmokeRecords {
		return
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].at.After(recs[j].at) })
	for _, r := range recs[maxSmokeRecords:] {
		_ = os.Remove(filepath.Join(root.smokeDir(), r.name))
	}
}
