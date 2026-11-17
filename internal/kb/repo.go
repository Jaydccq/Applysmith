package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Repo is a GitHub repository added as a knowledge source.
type Repo struct {
	ID         int64        `json:"id"`
	Owner      string       `json:"owner"`
	Name       string       `json:"name"`
	Branch     string       `json:"branch"`
	CommitSHA  string       `json:"commitSha"`
	Status     string       `json:"status"`
	Report     IngestReport `json:"report"`
	CreatedAt  string       `json:"createdAt"`
	IngestedAt string       `json:"ingestedAt,omitempty"`
}

var (
	repoPartRE = lazyregexp.New(`^[A-Za-z0-9_.-]{1,100}$`)
	shaRE      = lazyregexp.New(`^[0-9a-f]{40}$`)
)

// ParseRepoRef reads "owner/repo" or an https GitHub URL (with an optional
// .git suffix or trailing path) into owner and name.
func ParseRepoRef(ref string) (owner, name string, err error) {
	ref = strings.TrimSpace(ref)
	bad := func() (string, string, error) {
		return "", "", fmt.Errorf("%w: %q is not a repository; use owner/repo or an https://github.com URL", ErrInvalid, ref)
	}
	path := ref
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "github.com/") {
		if !strings.Contains(ref, "://") {
			ref = "https://" + ref
		}
		u, perr := url.Parse(ref)
		if perr != nil || u.Scheme != "https" || (u.Hostname() != "github.com" && u.Hostname() != "www.github.com") || u.User != nil {
			return bad()
		}
		path = strings.Trim(u.Path, "/")
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || (len(parts) > 2 && !strings.Contains(ref, "://")) {
		return bad()
	}
	// GitHub treats names case-insensitively; one lower-case form is stored.
	owner, name = strings.ToLower(parts[0]), strings.ToLower(strings.TrimSuffix(parts[1], ".git"))
	for _, p := range []string{owner, name} {
		if !repoPartRE.MatchString(p) || p == "." || p == ".." || strings.HasPrefix(p, ".") && len(p) < 2 {
			return bad()
		}
	}
	return owner, name, nil
}

func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var report string
	if err := row.Scan(&r.ID, &r.Owner, &r.Name, &r.Branch, &r.CommitSHA, &r.Status, &report, &r.CreatedAt, &r.IngestedAt); err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(report), &r.Report)
	return r, nil
}

const repoColumns = `id, owner, name, branch, commit_sha, status, report_json, created_at, ingested_at`

// AddRepo registers a repository as a source. Adding it twice returns the
// existing row. Nothing is fetched until it is ingested.
func (s *Service) AddRepo(ctx context.Context, ref string) (Repo, error) {
	owner, name, err := ParseRepoRef(ref)
	if err != nil {
		return Repo{}, err
	}
	var out Repo
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kb_repos (owner, name, created_at) VALUES (?, ?, ?)`, owner, name, s.stamp()); err != nil {
			return err
		}
		out, err = scanRepo(tx.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM kb_repos WHERE owner = ? AND name = ?`, owner, name))
		return err
	})
	return out, err
}

// GetRepo returns one source.
func (s *Service) GetRepo(ctx context.Context, id int64) (Repo, error) {
	r, err := scanRepo(s.st.Read().QueryRowContext(ctx, `SELECT `+repoColumns+` FROM kb_repos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: repository %d", ErrNotFound, id)
	}
	return r, err
}

// ListRepos returns the sources, oldest first.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT `+repoColumns+` FROM kb_repos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RemoveRepo deletes a source, its cached files and its unpublished drafts,
// and returns the token that undoes it within UndoWindow. Snippets the user
// already published from it stay.
func (s *Service) RemoveRepo(ctx context.Context, id int64) (string, error) {
	t := tombstone{kind: "repository"}
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT owner || '/' || name FROM kb_repos WHERE id = ?`, id).Scan(&t.name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: repository %d", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		for _, c := range [][2]string{{"kb_repos", "id = ?"}, {"kb_repo_blobs", "repo_id = ?"}, {"kb_drafts", "repo_id = ? AND status <> 'published'"}} {
			rows, err := copyRows(ctx, tx, c[0], c[1], id)
			if err != nil {
				return err
			}
			t.rows = append(t.rows, rows)
		}
		for _, q := range []string{
			`DELETE FROM kb_drafts WHERE repo_id = ? AND status <> 'published'`,
			`DELETE FROM kb_repos WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return dropOrphanAnswers(ctx, tx, &t)
	})
	if err != nil {
		return "", err
	}
	return s.keep(t), nil
}

// dropOrphanAnswers deletes the answers no draft holds any more, keeping a copy
// in t.
func dropOrphanAnswers(ctx context.Context, tx *sql.Tx, t *tombstone) error {
	const orphan = `draft_key NOT IN (SELECT key FROM kb_drafts)`
	answers, err := copyRows(ctx, tx, "kb_story_answers", orphan)
	if err != nil {
		return err
	}
	t.rows = append(t.rows, answers)
	_, err = tx.ExecContext(ctx, `DELETE FROM kb_story_answers WHERE `+orphan)
	return err
}
