package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// X engagement thresholds; verified authors meet half of each.
const (
	xMinLikes     = 10
	xMinReposts   = 5
	xMinViews     = 500
	xMinFollowers = 100
	xTopPages     = 2
	xTopDays      = 7
	xLatestDays   = 3
	xMinFaves     = 10
)

// KeyX is the keychain name of the paid X search API key.
const KeyX = "api-key-x"

type xAuthor struct {
	UserName       string `json:"userName"`
	Followers      int    `json:"followers"`
	IsBlueVerified bool   `json:"isBlueVerified"`
	VerifiedType   string `json:"verifiedType"`
}

type xPost struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	CreatedAt string  `json:"createdAt"`
	Likes     int     `json:"likeCount"`
	Reposts   int     `json:"retweetCount"`
	Views     int     `json:"viewCount"`
	IsReply   bool    `json:"isReply"`
	Author    xAuthor `json:"author"`
	created   time.Time
}

// meetsEngagement applies the four thresholds, halved for verified authors.
func meetsEngagement(p xPost) bool {
	k := 1.0
	if p.Author.IsBlueVerified || p.Author.VerifiedType != "" {
		k = 0.5
	}
	return float64(p.Likes) >= xMinLikes*k && float64(p.Reposts) >= xMinReposts*k &&
		float64(p.Views) >= xMinViews*k && float64(p.Author.Followers) >= xMinFollowers*k
}

func (e Endpoints) xSearch(query, kind, cursor string) string {
	v := url.Values{"query": {query}, "queryType": {kind}}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	return e.x() + "/twitter/tweet/advanced_search?" + v.Encode()
}

type xPage struct {
	Tweets      []xPost `json:"tweets"`
	HasNext     bool    `json:"has_next_page"`
	NextCursor  string  `json:"next_cursor"`
	ErrorStatus string  `json:"status"`
}

func xFetch(ctx context.Context, env *collectEnv, key, u string) (xPage, error) {
	res, err := env.Fetch.Do(ctx, Request{URL: u, Gap: gapX, Header: http.Header{"X-Api-Key": {key}}})
	if err != nil {
		return xPage{}, err
	}
	if res.Status != http.StatusOK {
		return xPage{}, classifyKeyed("X search", res)
	}
	var p xPage
	if err := json.Unmarshal(res.Body, &p); err != nil {
		return xPage{}, fmt.Errorf("X search: unexpected response: %w", err)
	}
	return p, nil
}

func asCollectError(err error, target **CollectError) bool { return errors.As(err, target) }

// collectX makes a Top pass (up to 2 pages, last 7 days, a minimum-likes
// operator) and a Latest pass (1 page, last 3 days) per query, drops replies,
// de-duplicates by id and keeps posts that meet the engagement thresholds. The
// per-query cursor advances only after both passes finished.
func collectX(ctx context.Context, env *collectEnv) (Collected, error) {
	key := env.Secret(KeyX)
	if key == "" {
		return Collected{}, &SkipError{Reason: "no key"}
	}
	queries := queryStrings(env.Queries)
	if len(queries) == 0 {
		return Collected{}, &SkipError{Reason: "no queries"}
	}
	now := env.Now().UTC()
	var out Collected
	var commits []func(context.Context) error
	// A query adds a note per failed pass, so failed queries are counted apart.
	failedQueries := 0
	for _, q := range queries {
		st, err := env.Repo.CollectorState(ctx, SourceX, q)
		if err != nil {
			return Collected{}, err
		}
		seen := map[string]bool{}
		newest := st.Cursor
		var posts []xPost
		passes := []struct {
			kind  string
			pages int
			query string
		}{
			{"Top", xTopPages, fmt.Sprintf("%s min_faves:%d since:%s", q, xMinFaves, now.AddDate(0, 0, -xTopDays).Format("2006-01-02"))},
			{"Latest", 1, fmt.Sprintf("%s since:%s", q, now.AddDate(0, 0, -xLatestDays).Format("2006-01-02"))},
		}
		failed := false
		for _, pass := range passes {
			cursor := ""
			for page := 0; page < pass.pages; page++ {
				p, err := xFetch(ctx, env, key, env.Endpoints.xSearch(pass.query, pass.kind, cursor))
				if err != nil {
					var ce *CollectError
					if asCollectError(err, &ce) && !ce.Retryable {
						return Collected{}, err
					}
					out.Notes = append(out.Notes, err.Error())
					failed = true
					break
				}
				for _, t := range p.Tweets {
					if seen[t.ID] || t.IsReply {
						continue
					}
					seen[t.ID] = true
					t.created, _ = time.Parse(time.RubyDate, t.CreatedAt)
					if t.created.IsZero() {
						t.created, _ = time.Parse(time.RFC3339, t.CreatedAt)
					}
					if meetsEngagement(t) {
						posts = append(posts, t)
					}
				}
				if !p.HasNext || p.NextCursor == "" {
					break
				}
				cursor = p.NextCursor
			}
		}
		for _, t := range posts {
			if !t.created.IsZero() && t.created.Unix() <= st.Cursor {
				continue
			}
			link := "https://x.com/" + t.Author.UserName + "/status/" + t.ID
			out.Docs = append(out.Docs, Doc{SourceID: SourceX, Adapter: adapterX, URL: link, FetchedAt: env.Now(), PublishedAt: t.created.UTC(),
				Title: firstLine(cleanSpace(t.Text), 100), Body: strings.TrimSpace(t.Text)})
			if t.created.Unix() > newest {
				newest = t.created.Unix()
			}
		}
		if failed {
			failedQueries++
		}
		if !failed && newest > st.Cursor {
			q, newest := q, newest
			commits = append(commits, func(ctx context.Context) error {
				return env.Repo.PutCollectorState(ctx, SourceX, q, collectorState{Cursor: newest})
			})
		}
	}
	if len(out.Docs) == 0 && failedQueries == len(queries) {
		return Collected{}, &CollectError{Reason: strings.Join(out.Notes, "; "), Retryable: true}
	}
	out.Commit = commitAll(commits)
	return out, nil
}
