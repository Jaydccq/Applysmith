package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// SkippedFile is a path the ingestion did not read, with the reason.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RedactedFile is a file left out because it holds a secret. Only the path and
// the kind of secret are recorded, never any content.
type RedactedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Ingest statuses.
const (
	IngestDone     = "ingested"
	IngestUpToDate = "up-to-date"
)

// IngestReport describes one ingestion.
type IngestReport struct {
	Status     string         `json:"status"`
	Commit     string         `json:"commit"`
	Branch     string         `json:"branch,omitempty"`
	Listing    string         `json:"listing,omitempty"`
	Considered int            `json:"considered"`
	Read       int            `json:"read"`
	Reused     int            `json:"reused"`
	Fetched    int            `json:"fetched"`
	Skipped    []SkippedFile  `json:"skipped"`
	Redacted   []RedactedFile `json:"redacted"`
	Drafts     []int64        `json:"drafts"`
	Notes      []string       `json:"notes"`
}

func (r *IngestReport) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

func newReport() IngestReport {
	return IngestReport{Skipped: []SkippedFile{}, Redacted: []RedactedFile{}, Drafts: []int64{}, Notes: []string{}}
}

type blobRow struct {
	sha, path, status, reason, text string
	size                            int
}

func (s *Service) github(ctx context.Context) (*github, string, error) {
	token := ""
	if s.opts.GitHubToken != nil {
		token = strings.TrimSpace(s.opts.GitHubToken(ctx))
	}
	base := s.ghBase
	if base == "" {
		base = githubAPI
	}
	g, err := newGitHub(base, token, s.ghInsecure)
	if err != nil {
		return nil, "", err
	}
	if s.ghBackoff > 0 {
		g.backoff = s.ghBackoff
	}
	return g, token, nil
}

// IngestRepo reads a repository at one pinned commit and turns what it finds
// into drafts. It never publishes. A commit that was already ingested is
// skipped unless force is set; a forced run fetches only the files whose blob
// SHA is new.
func (s *Service) IngestRepo(ctx context.Context, repoID int64, force bool) (IngestReport, error) {
	repo, err := s.GetRepo(ctx, repoID)
	if err != nil {
		return IngestReport{}, err
	}
	rep := newReport()
	fail := func(err error) (IngestReport, error) {
		rep.Status = "failed"
		rep.note("%v", err)
		s.saveRepo(ctx, repo.ID, repo.Branch, repo.CommitSHA, "failed", rep)
		return rep, err
	}
	gh, token, err := s.github(ctx)
	if err != nil {
		return fail(err)
	}
	branch, sha, err := gh.resolve(ctx, repo.Owner, repo.Name)
	if err != nil {
		return fail(err)
	}
	rep.Branch, rep.Commit = branch, sha
	if repo.CommitSHA == sha && repo.Status == "ingested" && !force {
		rep.Status = IngestUpToDate
		rep.note("the repository is up to date at %s; nothing was fetched", sha[:12])
		return rep, nil
	}

	entries, listing, err := s.listEntries(ctx, gh, token, repo, sha)
	if err != nil {
		return fail(err)
	}
	rep.Listing = listing
	chosen, skipped := selectFiles(entries)
	rep.Considered, rep.Skipped = len(entries), skipped
	files, err := s.readFiles(ctx, gh, repo, chosen, &rep)
	if err != nil {
		return fail(err)
	}
	if err := s.keepBlobs(ctx, repo.ID, chosen); err != nil {
		return fail(err)
	}
	if s.opts.Model == nil {
		rep.note("no model route is configured, so no drafts were written")
	} else if len(files) == 0 {
		rep.note("no readable files, so no drafts were written")
	} else if err := s.draftFromRepo(ctx, repo, sha, files, &rep); err != nil {
		// The commit is not recorded, so the next read drafts again; the files
		// it needs are kept by blob SHA.
		return fail(fmt.Errorf("drafting failed: %w", err))
	}
	rep.Status = IngestDone
	if err := s.saveRepo(ctx, repo.ID, branch, sha, "ingested", rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func (s *Service) saveRepo(ctx context.Context, id int64, branch, sha, status string, rep IngestReport) error {
	b, _ := json.Marshal(rep)
	now := s.stamp()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE kb_repos SET branch = ?, commit_sha = ?, status = ?, report_json = ?, ingested_at = ? WHERE id = ?`,
			branch, sha, status, string(b), now, id)
		return err
	})
}

// listEntries lists the files of the pinned commit. A truncated listing is
// never used: the tree comes from a blobless shallow clone at the same commit.
func (s *Service) listEntries(ctx context.Context, gh *github, token string, repo Repo, sha string) ([]repoEntry, string, error) {
	t, err := gh.tree(ctx, repo.Owner, repo.Name, sha)
	if err != nil {
		return nil, "", err
	}
	if !t.Truncated {
		var out []repoEntry
		for _, e := range t.Tree {
			if e.Type == "blob" && shaRE.MatchString(e.SHA) {
				out = append(out, repoEntry{Path: e.Path, SHA: e.SHA, Size: e.Size})
			}
		}
		return out, "api", nil
	}
	c := s.cloneSeam
	if c == nil {
		c = newCloner(token)
	}
	out, err := c.listTree(ctx, repo.Owner, repo.Name, sha)
	return out, "clone", err
}

type repoFile struct {
	Path string
	Text string
}

// readFiles returns the readable text of each chosen file in rank order. Files
// whose blob SHA is cached are reused; the rest are fetched four at a time.
// When one fetch fails the blobs already fetched are cached all the same.
func (s *Service) readFiles(ctx context.Context, gh *github, repo Repo, chosen []repoEntry, rep *IngestReport) ([]repoFile, error) {
	cached, err := s.cachedBlobs(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	results := make([]blobRow, len(chosen))
	var need []int
	for i, e := range chosen {
		if row, ok := cached[e.SHA]; ok {
			row.path = e.Path
			results[i] = row
			rep.Reused++
			continue
		}
		need = append(need, i)
	}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, ghFetchConcurrency)
	var wg sync.WaitGroup
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, i := range need {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if fetchCtx.Err() != nil {
				return
			}
			data, err := gh.blob(fetchCtx, repo.Owner, repo.Name, chosen[i].SHA)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, errReplyTooLarge) {
				// The clone listing carries no sizes, so a file over the cap
				// is found here and skipped like any other oversized file.
				results[i] = blobRow{sha: chosen[i].SHA, path: chosen[i].Path, status: "binary", reason: "larger than 128 KiB"}
				rep.Fetched++
				return
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			results[i] = classifyBlob(chosen[i], data)
			rep.Fetched++
		}()
	}
	wg.Wait()
	if firstErr != nil {
		// Keep what arrived, so the next run fetches only the rest.
		var done []int
		for _, i := range need {
			if results[i].sha != "" {
				done = append(done, i)
			}
		}
		return nil, errors.Join(firstErr, s.storeBlobs(ctx, repo.ID, results, done))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.storeBlobs(ctx, repo.ID, results, need); err != nil {
		return nil, err
	}
	var files []repoFile
	for _, row := range results {
		switch row.status {
		case "ok":
			files = append(files, repoFile{Path: row.path, Text: row.text})
			rep.Read++
		case "redacted":
			rep.Redacted = append(rep.Redacted, RedactedFile{Path: row.path, Reason: row.reason})
		default:
			rep.Skipped = append(rep.Skipped, SkippedFile{Path: row.path, Reason: row.reason})
		}
	}
	sort.Slice(rep.Skipped, func(i, j int) bool { return rep.Skipped[i].Path < rep.Skipped[j].Path })
	return files, nil
}

// classifyBlob decides what becomes of a fetched file: binary and oversize
// files are skipped, a file with a secret is redacted before any of it is kept.
func classifyBlob(e repoEntry, data []byte) blobRow {
	row := blobRow{sha: e.SHA, path: e.Path, size: len(data)}
	switch {
	case len(data) > maxRepoFileBytes:
		row.status, row.reason = "binary", "larger than 128 KiB"
	case looksBinary(data):
		row.status, row.reason = "binary", "binary content"
	default:
		text := string(data)
		if finds := ScanSecrets(text); len(finds) > 0 {
			row.status, row.reason = "redacted", finds[0].Kind
			return row
		}
		row.status, row.text = "ok", clip(text, storedBlobChars)
	}
	return row
}

func (s *Service) cachedBlobs(ctx context.Context, repoID int64) (map[string]blobRow, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT blob_sha, path, status, reason, size, text FROM kb_repo_blobs WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]blobRow{}
	for rows.Next() {
		var r blobRow
		if err := rows.Scan(&r.sha, &r.path, &r.status, &r.reason, &r.size, &r.text); err != nil {
			return nil, err
		}
		out[r.sha] = r
	}
	return out, rows.Err()
}

func (s *Service) storeBlobs(ctx context.Context, repoID int64, rows []blobRow, fresh []int) error {
	if len(fresh) == 0 {
		return nil
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		for _, i := range fresh {
			r := rows[i]
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO kb_repo_blobs (repo_id, blob_sha, path, status, reason, size, text) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				repoID, r.sha, r.path, r.status, r.reason, r.size, r.text); err != nil {
				return err
			}
		}
		return nil
	})
}

// keepBlobs drops cached files that the commit no longer uses.
func (s *Service) keepBlobs(ctx context.Context, repoID int64, chosen []repoEntry) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		keep := make(map[string]bool, len(chosen))
		for _, e := range chosen {
			keep[e.SHA] = true
		}
		rows, err := tx.QueryContext(ctx, `SELECT blob_sha FROM kb_repo_blobs WHERE repo_id = ?`, repoID)
		if err != nil {
			return err
		}
		var drop []string
		for rows.Next() {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				rows.Close()
				return err
			}
			if !keep[sha] {
				drop = append(drop, sha)
			}
		}
		rows.Close()
		for _, sha := range drop {
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_repo_blobs WHERE repo_id = ? AND blob_sha = ?`, repoID, sha); err != nil {
				return err
			}
		}
		return nil
	})
}
