package kb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub client bounds.
const (
	githubAPI          = "https://api.github.com"
	ghRequestTimeout   = 30 * time.Second
	ghMaxTreeBytes     = 32 << 20
	ghMaxBlobBytes     = 512 << 10
	ghMaxMetaBytes     = 1 << 20
	ghFetchConcurrency = 4
	ghMaxRetries       = 3
	ghMaxBackoff       = 30 * time.Second
)

// ErrRateLimited marks a GitHub reply that asked the client to slow down for
// longer than the client is willing to wait. It is an ErrUnavailable, which
// callers report as a failure worth retrying later.
var ErrRateLimited = fmt.Errorf("%w: GitHub rate limit", ErrUnavailable)

// errReplyTooLarge marks a reply over its size cap; for a file it means the
// file is larger than the ingestion reads.
var errReplyTooLarge = errors.New("reply too large")

// ErrRepoUnavailable marks a repository that cannot be read: missing, private
// without a token, or the token lacks access.
var ErrRepoUnavailable = errors.New("repository unavailable")

// github is the REST client. It reads one repository at one pinned commit.
type github struct {
	base    string
	token   string
	client  *http.Client
	sleep   func(context.Context, time.Duration) error
	backoff time.Duration
}

func newGitHub(base, token string, insecure bool) (*github, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(insecure && u.Scheme == "http")) {
		return nil, fmt.Errorf("%w: the GitHub API address must be https", ErrInvalid)
	}
	host := u.Host
	c := &http.Client{
		Timeout: ghRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Host != host || (req.URL.Scheme != "https" && !insecure) {
				return fmt.Errorf("%w: redirect refused", ErrRepoUnavailable)
			}
			return nil
		},
	}
	return &github{base: strings.TrimRight(base, "/"), token: token, client: c, sleep: sleepCtx, backoff: time.Second}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// get performs one GET with retries on rate limiting and server errors, and
// decodes at most limit bytes of the reply into out.
func (g *github) get(ctx context.Context, p string, limit int64, out any) error {
	var last error
	for attempt := 0; attempt <= ghMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+p, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "applysmith")
		if g.token != "" {
			req.Header.Set("Authorization", "Bearer "+g.token)
		}
		resp, err := g.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrRepoUnavailable) {
				return scrub(err, g.token)
			}
			last = scrub(err, g.token)
		} else {
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			resp.Body.Close()
			switch {
			case rerr != nil:
				last = rerr
			case resp.StatusCode == http.StatusOK:
				if int64(len(body)) > limit {
					return fmt.Errorf("%w: %w: the reply to %s is larger than %d bytes", ErrRepoUnavailable, errReplyTooLarge, p, limit)
				}
				return json.Unmarshal(body, out)
			case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized:
				return fmt.Errorf("%w: GitHub answered %d; a private repository needs a token with read access", ErrRepoUnavailable, resp.StatusCode)
			case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
				wait, signal, ok := retryDelay(resp.Header, g.backoff, attempt)
				if !signal && resp.StatusCode == http.StatusForbidden {
					return fmt.Errorf("%w: GitHub answered 403; the token (if any) lacks read access", ErrRepoUnavailable)
				}
				if !ok || attempt == ghMaxRetries {
					return fmt.Errorf("%w: GitHub answered %d", ErrRateLimited, resp.StatusCode)
				}
				if err := g.sleep(ctx, wait); err != nil {
					return err
				}
				continue
			case resp.StatusCode >= 500:
				last = fmt.Errorf("GitHub answered %d", resp.StatusCode)
			default:
				return fmt.Errorf("%w: GitHub answered %d", ErrRepoUnavailable, resp.StatusCode)
			}
		}
		if attempt < ghMaxRetries {
			if err := g.sleep(ctx, g.backoff<<attempt); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, last)
}

// retryDelay reads how long GitHub asks the client to wait. signal says the
// reply carried a rate-limit header; ok says the wait is short enough to take.
// A 429 without headers backs off exponentially.
func retryDelay(h http.Header, base time.Duration, attempt int) (d time.Duration, signal, ok bool) {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			d := time.Duration(n) * time.Second
			return d, true, d <= ghMaxBackoff
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			d := max(time.Until(time.Unix(n, 0)), 0)
			return d, true, d <= ghMaxBackoff
		}
	}
	return base << attempt, false, true
}

func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return scrubbedError{msg: strings.ReplaceAll(err.Error(), token, "***"), err: err}
}

// scrubbedError reads without the token but keeps the original error for
// errors.Is.
type scrubbedError struct {
	msg string
	err error
}

func (e scrubbedError) Error() string { return e.msg }
func (e scrubbedError) Unwrap() error { return e.err }

type ghTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type ghTree struct {
	Tree      []ghTreeEntry `json:"tree"`
	Truncated bool          `json:"truncated"`
}

// resolve returns the default branch and the commit SHA its tip points at.
func (g *github) resolve(ctx context.Context, owner, repo string) (branch, sha string, err error) {
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	if err := g.get(ctx, base, ghMaxMetaBytes, &meta); err != nil {
		return "", "", err
	}
	if meta.DefaultBranch == "" {
		return "", "", fmt.Errorf("%w: the repository reports no default branch", ErrRepoUnavailable)
	}
	var c struct {
		SHA string `json:"sha"`
	}
	if err := g.get(ctx, base+"/commits/"+url.PathEscape(meta.DefaultBranch), ghMaxMetaBytes, &c); err != nil {
		return "", "", err
	}
	if !shaRE.MatchString(c.SHA) {
		return "", "", fmt.Errorf("%w: the repository returned no commit SHA", ErrRepoUnavailable)
	}
	return meta.DefaultBranch, c.SHA, nil
}

// tree lists the whole tree of the pinned commit.
func (g *github) tree(ctx context.Context, owner, repo, sha string) (ghTree, error) {
	var t ghTree
	err := g.get(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/git/trees/"+sha+"?recursive=1", ghMaxTreeBytes, &t)
	return t, err
}

// blob reads one file by its blob SHA. The blob SHA comes from the pinned
// commit's tree, so every file belongs to that one version.
func (g *github) blob(ctx context.Context, owner, repo, blobSHA string) ([]byte, error) {
	var b struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if !shaRE.MatchString(blobSHA) {
		return nil, fmt.Errorf("%w: bad blob SHA", ErrInvalid)
	}
	if err := g.get(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/git/blobs/"+blobSHA, ghMaxBlobBytes, &b); err != nil {
		return nil, err
	}
	if b.Encoding != "base64" {
		return nil, fmt.Errorf("%w: unexpected blob encoding %q", ErrRepoUnavailable, b.Encoding)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(b.Content, "\n", ""))
}
