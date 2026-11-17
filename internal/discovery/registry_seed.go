package discovery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Seed download bounds.
const (
	seedTimeout  = 120 * time.Second
	seedMaxBytes = 50 << 20
	seedMaxRows  = 200000
)

// SeedSource is one public slug list. The list is plain text, one board per
// line: `ats,slug[,company]` separated by commas or tabs; blank lines, lines
// starting with `#` and an `ats,slug` header are ignored.
type SeedSource struct {
	URL     string
	Licence string
	// ATS names the board system when the list holds bare slugs (a JSON array
	// of strings, or one slug per line) and not `ats,slug` rows.
	ATS string
}

// SeedResult is the outcome of one list.
type SeedResult struct {
	URL      string
	Rows     int // valid, de-duplicated rows
	Inserted int
	Rejected int
	Err      error
}

// Seeder fetches slug lists into the registry.
type Seeder struct {
	Repo *Repo
	HTTP *http.Client
	// Dir keeps partial downloads so a retry resumes from the bytes received.
	Dir string
}

// Seed fetches every source. One failing source does not stop the others;
// the caller treats all failing as an error.
func (s *Seeder) Seed(ctx context.Context, sources []SeedSource) []SeedResult {
	var out []SeedResult
	for _, src := range sources {
		res := SeedResult{URL: src.URL}
		res.Err = s.seedOne(ctx, src, &res)
		out = append(out, res)
	}
	return out
}

func (s *Seeder) seedOne(ctx context.Context, src SeedSource, res *SeedResult) error {
	payload, err := s.download(ctx, src.URL)
	if err != nil {
		return err
	}
	var boards []Board
	var rejected int
	if src.ATS != "" {
		boards, rejected, err = ParseSlugList(payload, src.ATS)
	} else {
		boards, rejected, err = ParseSeedList(payload)
	}
	if err != nil {
		return err
	}
	res.Rows, res.Rejected = len(boards), rejected
	now := s.Repo.now()
	source := fmt.Sprintf("%s (%s)", src.URL, now.UTC().Format("2006-01-02"))
	sum := sha256.Sum256(payload)
	licence := src.Licence
	if licence == "" {
		licence = "unspecified"
	}
	res.Inserted, err = s.Repo.RegistryAdd(ctx, source, boards...)
	if err != nil {
		return err
	}
	return s.Repo.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO data_sources (kind, url, retrieved_at, sha256, licence) VALUES ('registry', ?, ?, ?, ?)`,
			src.URL, now.UTC().Format(timeLayout), hex.EncodeToString(sum[:]), licence)
		return err
	})
}

// download fetches a URL within the time limit, resuming from a partial file.
func (s *Seeder) download(ctx context.Context, rawURL string) ([]byte, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	name := sha256.Sum256([]byte(rawURL))
	part := filepath.Join(s.Dir, hex.EncodeToString(name[:8])+".part")
	ctx, cancel := context.WithTimeout(ctx, seedTimeout)
	defer cancel()
	validatorFile := part + ".validator"
	have, _ := os.Stat(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	var offset int64
	if have != nil && have.Size() > 0 {
		// Resume only against the version the stored bytes came from; without
		// a validator there is no way to tell, so start over.
		if v, _ := os.ReadFile(validatorFile); len(v) > 0 {
			offset = have.Size()
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			req.Header.Set("If-Range", string(v))
		}
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		offset = 0
		if v := validatorOf(resp.Header); v != "" {
			if err := os.WriteFile(validatorFile, []byte(v), 0o600); err != nil {
				return nil, err
			}
		} else {
			_ = os.Remove(validatorFile)
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		_ = os.Remove(validatorFile)
		return nil, fmt.Errorf("downloading %s: the stored partial file no longer matches; run the command again", rawURL)
	default:
		return nil, fmt.Errorf("downloading %s: status %d", rawURL, resp.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, seedMaxBytes-offset+1))
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if offset+n > seedMaxBytes {
		_ = os.Remove(part)
		_ = os.Remove(validatorFile)
		return nil, fmt.Errorf("downloading %s: larger than %d MB", rawURL, seedMaxBytes>>20)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("downloading %s: %w (the %d bytes received are kept; run the command again to resume)", rawURL, copyErr, offset+n)
	}
	data, err := os.ReadFile(part)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(part)
	_ = os.Remove(validatorFile)
	return data, nil
}

// validatorOf is the value a later If-Range can name: a strong ETag, else the
// modification time.
func validatorOf(h http.Header) string {
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		return etag
	}
	return h.Get("Last-Modified")
}

// ParseSeedList validates the rows of a list. It returns the valid boards
// de-duplicated by (ATS, lowercase slug) and the number of rejected rows.
func ParseSeedList(data []byte) ([]Board, int, error) {
	seen := map[string]bool{}
	var out []Board
	rejected := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == '\t' })
		if len(fields) >= 2 && strings.EqualFold(strings.TrimSpace(fields[0]), "ats") {
			continue
		}
		if len(fields) < 2 {
			rejected++
			continue
		}
		ats, slug := strings.ToLower(strings.TrimSpace(fields[0])), strings.TrimSpace(fields[1])
		if !validATS(ats) || !ValidSlug(slug) {
			rejected++
			continue
		}
		b := Board{ATS: ats, Slug: strings.ToLower(slug)}
		if len(fields) >= 3 {
			b.Company = strings.TrimSpace(fields[2])
		}
		if seen[b.Key()] {
			continue
		}
		seen[b.Key()] = true
		out = append(out, b)
		if len(out) > seedMaxRows {
			return nil, 0, fmt.Errorf("list has more than %d rows", seedMaxRows)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	return out, rejected, nil
}

// SeedFailed reports whether every source failed.
func SeedFailed(results []SeedResult) error {
	if len(results) == 0 {
		return errors.New("no seed sources given")
	}
	for _, r := range results {
		if r.Err == nil {
			return nil
		}
	}
	return errors.New("every seed source failed")
}

// ParseSlugList reads a list of bare slugs for one ATS: a JSON array of
// strings, or one slug per line (a `- ` marker and a trailing `: name` are
// accepted). It returns the valid boards de-duplicated by lowercase slug and
// the number of rejected entries.
func ParseSlugList(data []byte, ats string) ([]Board, int, error) {
	if !validATS(ats) {
		return nil, 0, fmt.Errorf("%q is not a supported ats", ats)
	}
	var raw []string
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &raw); err != nil {
			return nil, 0, fmt.Errorf("the list is not a JSON array of slugs: %w", err)
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "-"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line, _, _ = strings.Cut(line, ":")
			raw = append(raw, strings.Trim(strings.TrimSpace(line), `"'`))
		}
		if err := sc.Err(); err != nil {
			return nil, 0, err
		}
	}
	seen := map[string]bool{}
	var out []Board
	rejected := 0
	for _, slug := range raw {
		slug = strings.TrimSpace(slug)
		if !ValidSlug(slug) {
			rejected++
			continue
		}
		b := Board{ATS: ats, Slug: strings.ToLower(slug)}
		if seen[b.Key()] {
			continue
		}
		seen[b.Key()] = true
		out = append(out, b)
		if len(out) > seedMaxRows {
			return nil, 0, fmt.Errorf("list has more than %d rows", seedMaxRows)
		}
	}
	return out, rejected, nil
}
