package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// indexVersion changes when the stored index shape or the tokenizer changes,
// which makes the next sync rebuild every row.
const (
	indexVersion = "6"
	indexMetaKey = "kb_index_version"
	maxDepth     = 4
)

// SourceFailure names an evidence source that could not be read.
type SourceFailure struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

// SyncReport describes one reconciliation of the files with the index.
type SyncReport struct {
	Failures []SourceFailure `json:"failures,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Changed  int             `json:"changed"`
	// Read names the sources that were read without failing.
	Read []string `json:"-"`
}

type fileEntry struct {
	rel      string
	kind     string
	data     []byte
	mod      time.Time
	tooLarge bool
}

// sourceOf names the source a document kind belongs to.
func sourceOf(kind string) string {
	switch kind {
	case KindSnippet:
		return "snippets"
	case KindBrief:
		return "briefs"
	}
	return kind
}

// collect reads every knowledge file and fact source. A path that cannot be
// read becomes a failure of its source, never a silent omission.
func (s *Service) collect() ([]fileEntry, []SourceFailure, []string) {
	var entries []fileEntry
	var failures []SourceFailure
	read := map[string]bool{}
	fail := func(source string, err error) {
		failures = append(failures, SourceFailure{Source: source, Error: err.Error()})
	}
	load := func(rel, kind string) {
		abs := s.absPath(rel)
		fi, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err == nil && !fi.Mode().IsRegular() {
			return
		}
		var data []byte
		if err == nil && fi.Size() > maxFileBytes {
			marker := []byte(fmt.Sprintf("oversize:%d", fi.Size()))
			entries = append(entries, fileEntry{rel: rel, kind: kind, mod: fi.ModTime(), data: marker, tooLarge: true})
			return
		}
		if err == nil {
			data, err = os.ReadFile(abs)
		}
		if err != nil {
			fail(sourceOf(kind), err)
			return
		}
		read[sourceOf(kind)] = true
		entries = append(entries, fileEntry{rel: rel, kind: kind, data: data, mod: fi.ModTime()})
	}
	for _, f := range factSourceFiles {
		load(f.file, f.kind)
	}
	// The root may itself be a symlink (a synced folder); the walk starts at
	// its target and the paths it reports stay below knowledge/.
	kroot := s.knowledgeRoot()
	if _, err := os.Lstat(kroot); errors.Is(err, fs.ErrNotExist) {
		return entries, failures, readNames(read)
	} else if err != nil {
		fail("knowledge", err)
		return entries, failures, readNames(read)
	}
	root, err := filepath.EvalSymlinks(kroot)
	if err != nil {
		fail("knowledge", err)
		return entries, failures, readNames(read)
	}
	walkFailed := false
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel := KnowledgeDir
		if r, rerr := filepath.Rel(root, p); rerr == nil && r != "." {
			rel += "/" + filepath.ToSlash(r)
		}
		if err != nil {
			if p == root {
				walkFailed = true
				fail("knowledge", err)
				return fs.SkipAll
			}
			// The trailing slash lets a folder name its own source, as
			// knowledge/companies does the briefs.
			fail(sourceOfPath(rel+"/"), err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		inside := strings.TrimPrefix(strings.TrimPrefix(rel, KnowledgeDir), "/")
		if d.IsDir() {
			if inside != "" && strings.Count(inside, "/")+1 > maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || path.Ext(rel) != ".md" || path.Base(rel) == "index.md" {
			return nil
		}
		if strings.HasPrefix(inside, CompaniesDir+"/") {
			if strings.Count(inside, "/") == 1 {
				load(rel, KindBrief)
			}
			return nil
		}
		load(rel, KindSnippet)
		return nil
	})
	if err != nil {
		fail("knowledge", err)
	} else if !walkFailed {
		read["snippets"], read["briefs"] = true, true
	}
	return entries, failures, readNames(read)
}

func readNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type docRow struct {
	path, kind, id, category, state, warning string
	fileHash, contentHash, title, desc, meta string
	mod                                      string
}

type sectionRow struct {
	ord                                  int
	heading, text, hash, tokens, sensive string
}

// build parses one file into its index rows. A file that cannot be parsed or
// that holds a secret keeps a row without sections, so the warning survives.
func (s *Service) build(e fileEntry) (docRow, []sectionRow) {
	row := docRow{path: e.rel, kind: e.kind, state: "ok", fileHash: fileHash(e.data), mod: e.mod.UTC().Format(time.RFC3339)}
	bad := func(state, msg string) (docRow, []sectionRow) {
		row.state, row.warning = state, fmt.Sprintf("%s: %s", e.rel, msg)
		return row, nil
	}
	if e.tooLarge {
		return bad("broken", "file is larger than 1 MiB and was skipped")
	}
	if err := CheckSecrets(e.rel, string(e.data)); err != nil {
		return bad("secret", err.Error())
	}
	if e.kind == KindProfile {
		secs, err := profileSections(e.data)
		if err != nil {
			return bad("broken", err.Error())
		}
		sn := &Snippet{ID: "profile", Path: e.rel}
		var rows []sectionRow
		for i, p := range secs {
			sn.Sections = append(sn.Sections, Section{Heading: p.label, Text: p.text})
			rows = append(rows, newSectionRow(i, p.label, p.text, p.sensitive))
		}
		sn.SectionCount = len(rows)
		return finishRow(row, sn), rows
	}
	sn, err := ParseSnippet(e.rel, e.data)
	if err != nil {
		return bad("broken", err.Error())
	}
	switch e.kind {
	case KindCV, KindNarrative:
		sn.ID, sn.Category = e.kind, ""
	case KindBrief:
		sn.Category = ""
		sn.ID = strings.TrimSuffix(path.Base(e.rel), ".md")
	}
	var rows []sectionRow
	for i, sec := range sn.Sections {
		rows = append(rows, newSectionRow(i, sec.Heading, sec.Text, ""))
	}
	sn.SectionCount = len(rows)
	return finishRow(row, sn), rows
}

func newSectionRow(ord int, heading, text, sensitive string) sectionRow {
	body := heading + "\n" + text
	return sectionRow{ord: ord, heading: heading, text: text, hash: textHash(body), tokens: strings.Join(Tokens(body), " "), sensive: sensitive}
}

func finishRow(row docRow, sn *Snippet) docRow {
	row.id, row.category, row.title, row.desc, row.contentHash = sn.ID, sn.Category, sn.Title, sn.Description, sn.ContentHash
	meta := *sn
	meta.Sections = nil
	b, _ := json.Marshal(meta)
	row.meta = string(b)
	return row
}

// Sync reconciles the index with the files: new and changed files are parsed
// again (detected by content hash, never by timestamp), vanished files lose
// their rows, and an index that is missing, partial or from another version is
// rebuilt. It is called before every retrieval.
func (s *Service) Sync(ctx context.Context) (SyncReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked(ctx, false)
}

// Reindex discards the whole index and rebuilds it from the files. Drafts,
// gaps and cached embeddings are not part of the index and are kept.
func (s *Service) Reindex(ctx context.Context) (SyncReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked(ctx, true)
}

func (s *Service) syncLocked(ctx context.Context, force bool) (SyncReport, error) {
	entries, failures, read := s.collect()
	rep := SyncReport{Failures: failures, Read: read}
	if !force {
		// Most passes find nothing to do, and a read can tell: they leave the
		// single writer alone.
		ok, err := s.indexCurrent(ctx, entries, failures)
		if err != nil {
			return rep, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if ok {
			rep.Warnings, err = s.warnings(ctx)
			return rep, err
		}
	}
	now := s.stamp()
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := s.resetIfStale(ctx, tx, force); err != nil {
			return err
		}
		if err := dropPartialDocs(ctx, tx); err != nil {
			return err
		}
		known, err := loadHashes(ctx, tx)
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, e := range entries {
			present[e.rel] = true
			if known[e.rel] == fileHash(e.data) {
				continue
			}
			row, secs := s.build(e)
			if err := replaceDoc(ctx, tx, row, secs, now); err != nil {
				return err
			}
			rep.Changed++
		}
		failed := failedSources(failures)
		for p := range known {
			if present[p] || sourceFailed(failed, p) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_docs WHERE path = ?`, p); err != nil {
				return err
			}
			rep.Changed++
		}
		if rep.Changed > 0 {
			return afterChange(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return rep, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	rep.Warnings, err = s.warnings(ctx)
	return rep, err
}

// indexCurrent reports, from a read alone, that the index already matches the
// files: it has this version, no document is partial, every file read has its
// hash, and no indexed path is gone.
func (s *Service) indexCurrent(ctx context.Context, entries []fileEntry, failures []SourceFailure) (bool, error) {
	db := s.st.Read()
	var v string
	var partial int
	err := db.QueryRowContext(ctx, `SELECT coalesce((SELECT value FROM workspace_meta WHERE key = ?), ''),
		(SELECT count(*) FROM kb_docs WHERE `+partialDocs+`)`, indexMetaKey).Scan(&v, &partial)
	if err != nil || v != indexVersion || partial > 0 {
		return false, err
	}
	known, err := loadHashes(ctx, db)
	if err != nil {
		return false, err
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.rel] = true
		if known[e.rel] != fileHash(e.data) {
			return false, nil
		}
	}
	failed := failedSources(failures)
	for p := range known {
		if !present[p] && !sourceFailed(failed, p) {
			return false, nil
		}
	}
	return true, nil
}

func failedSources(fs []SourceFailure) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Source] = true
	}
	return m
}

// sourceFailed reports whether the source holding path could not be read.
func sourceFailed(failed map[string]bool, p string) bool {
	return failed[sourceOfPath(p)] || failed["knowledge"] && strings.HasPrefix(p, KnowledgeDir+"/")
}

// sourceOfPath maps an indexed path back to its source name.
func sourceOfPath(p string) string {
	switch {
	case p == "cv.md":
		return KindCV
	case p == "profile.yml":
		return KindProfile
	case p == "narrative.md":
		return KindNarrative
	case strings.HasPrefix(p, KnowledgeDir+"/"+CompaniesDir+"/"):
		return "briefs"
	}
	return "snippets"
}

func (s *Service) resetIfStale(ctx context.Context, tx *sql.Tx, force bool) error {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, indexMetaKey).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !force && v == indexVersion {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_docs`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, indexMetaKey, indexVersion)
	return err
}

// dropPartialDocs removes documents whose section rows do not match what their
// metadata promises, so the next pass parses them again.
func dropPartialDocs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM kb_docs WHERE `+partialDocs)
	return err
}

// partialDocs is the condition on kb_docs that marks a partial document.
const partialDocs = `state IN ('ok', 'duplicate')
	AND (SELECT count(*) FROM kb_sections s WHERE s.path = kb_docs.path) <> coalesce(json_extract(meta_json, '$.section_count'), 0)`

func loadHashes(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT path, file_hash FROM kb_docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		m[p] = h
	}
	return m, rows.Err()
}

func replaceDoc(ctx context.Context, tx *sql.Tx, r docRow, secs []sectionRow, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_docs WHERE path = ?`, r.path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_docs (path, kind, id, category, state, warning, file_hash, content_hash, title, description, meta_json, modified_at, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.path, r.kind, r.id, r.category, r.state, r.warning, r.fileHash, r.contentHash, r.title, r.desc, nonEmptyJSON(r.meta), r.mod, now); err != nil {
		return err
	}
	for _, sec := range secs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_sections (path, ord, heading, text, section_hash, tokens, sensitive) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.path, sec.ord, sec.heading, sec.text, sec.hash, sec.tokens, sec.sensive); err != nil {
			return err
		}
	}
	return nil
}

func nonEmptyJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// afterChange settles what depends on the set of documents: which duplicate id
// wins, the cached vectors of sections that no longer exist, and previous
// versions of snippets that are gone.
func afterChange(ctx context.Context, tx *sql.Tx) error {
	if err := markDuplicates(ctx, tx); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM kb_embeddings WHERE section_hash NOT IN (SELECT section_hash FROM kb_sections)`,
		`DELETE FROM kb_previous WHERE path NOT IN (SELECT path FROM kb_docs)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// markDuplicates keeps, for each snippet id, the file that sorts first by path
// and marks the others as ignored with a warning that names both.
func markDuplicates(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT path, id, state, warning FROM kb_docs
		WHERE kind = 'snippet' AND state IN ('ok', 'duplicate') ORDER BY path`)
	if err != nil {
		return err
	}
	type doc struct{ path, id, state, warning string }
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.path, &d.id, &d.state, &d.warning); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	winner := map[string]string{}
	for _, d := range docs {
		state, warning := "ok", ""
		if w, dup := winner[d.id]; dup {
			state = "duplicate"
			warning = fmt.Sprintf("%s: id %q is also declared by %s; %s wins by path and this file is ignored", d.path, d.id, w, w)
		} else {
			winner[d.id] = d.path
		}
		if state != d.state || warning != d.warning {
			if _, err := tx.ExecContext(ctx, `UPDATE kb_docs SET state = ?, warning = ? WHERE path = ?`, state, warning, d.path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) warnings(ctx context.Context) ([]string, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT warning FROM kb_docs WHERE warning <> '' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// indexedDoc is a document loaded from the index.
type indexedDoc struct {
	Snippet
	Kind       string
	State      string
	ModifiedAt string
	FileHash   string
	secs       []indexedSection
}

type indexedSection struct {
	Section
	Hash      string
	Tokens    []string
	Sensitive string
}

// loadDocs returns the usable documents of the index (state ok), ordered by
// path. Documents of a failed source are left out.
func (s *Service) loadDocs(ctx context.Context, failed map[string]bool) ([]*indexedDoc, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT path, kind, state, modified_at, file_hash, meta_json FROM kb_docs WHERE state = 'ok' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	var docs []*indexedDoc
	byPath := map[string]*indexedDoc{}
	for rows.Next() {
		var d indexedDoc
		var meta string
		if err := rows.Scan(&d.Path, &d.Kind, &d.State, &d.ModifiedAt, &d.FileHash, &meta); err != nil {
			rows.Close()
			return nil, err
		}
		if sourceFailed(failed, d.Path) {
			continue
		}
		if err := json.Unmarshal([]byte(meta), &d.Snippet); err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, &d)
		byPath[d.Path] = &d
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	srows, err := s.st.Read().QueryContext(ctx, `SELECT path, ord, heading, text, section_hash, tokens, sensitive FROM kb_sections ORDER BY path, ord`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var p, tokens string
		var sec indexedSection
		var ord int
		if err := srows.Scan(&p, &ord, &sec.Heading, &sec.Text, &sec.Hash, &tokens, &sec.Sensitive); err != nil {
			return nil, err
		}
		d := byPath[p]
		if d == nil {
			continue
		}
		sec.Tokens = strings.Fields(tokens)
		sec.Facts = d.factsForHeading(sec.Heading)
		d.secs = append(d.secs, sec)
		d.Sections = append(d.Sections, sec.Section)
	}
	return docs, srows.Err()
}

// snapshot reconciles the index and loads its documents under one lock, so a
// concurrent sync cannot interleave between the two reads. Documents of a
// source that could not be read are left out.
func (s *Service) snapshot(ctx context.Context) (SyncReport, []*indexedDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep, err := s.syncLocked(ctx, false)
	if err != nil {
		return rep, nil, err
	}
	docs, err := s.loadDocs(ctx, failedSources(rep.Failures))
	if err != nil {
		return rep, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return rep, docs, nil
}

// Warnings returns the load warnings of the current index (broken files,
// duplicate ids, rejected secrets).
func (s *Service) Warnings(ctx context.Context) ([]string, error) {
	rep, err := s.Sync(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(rep.Warnings)
	return rep.Warnings, nil
}
