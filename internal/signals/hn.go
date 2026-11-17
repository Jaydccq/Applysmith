package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	hnHits           = 50
	hnThreadSearch   = 50
	hnHiringMaxPosts = 300
	hnItemPage       = "https://news.ycombinator.com/item?id="
)

type hnHit struct {
	ObjectID   string `json:"objectID"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	StoryText  string `json:"story_text"`
	CreatedAt  string `json:"created_at"`
	CreatedAtI int64  `json:"created_at_i"`
}

func (e Endpoints) hnSearch(query, tags string) string {
	v := url.Values{"query": {query}, "tags": {tags}, "hitsPerPage": {strconv.Itoa(hnHits)}}
	return e.hn() + "/api/v1/search_by_date?" + v.Encode()
}

func hnGet(ctx context.Context, env *collectEnv, u string, out any) error {
	res, err := env.Fetch.Do(ctx, Request{URL: u, Gap: gapHN})
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK {
		return statusError("hacker news", res)
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return fmt.Errorf("hacker news: unexpected response: %w", err)
	}
	return nil
}

// collectHN runs the configured queries over stories newer than each query's
// cursor. The cursor advances only after the results were stored.
func collectHN(ctx context.Context, env *collectEnv) (Collected, error) {
	queries := queryStrings(env.Queries)
	if len(queries) == 0 {
		return Collected{}, &SkipError{Reason: "no queries"}
	}
	var out Collected
	var commits []func(context.Context) error
	ok := 0
	for _, q := range queries {
		st, err := env.Repo.CollectorState(ctx, SourceHN, q)
		if err != nil {
			return Collected{}, err
		}
		var resp struct {
			Hits []hnHit `json:"hits"`
		}
		if err := hnGet(ctx, env, env.Endpoints.hnSearch(q, "story"), &resp); err != nil {
			out.Notes = append(out.Notes, err.Error())
			continue
		}
		ok++
		newest := st.Cursor
		for _, h := range resp.Hits {
			if h.CreatedAtI <= st.Cursor {
				continue
			}
			u := h.URL
			if u == "" {
				u = hnItemPage + h.ObjectID
			}
			body := cleanSpace(stripTags(h.StoryText))
			if body == "" {
				body = h.Title
			}
			out.Docs = append(out.Docs, Doc{SourceID: SourceHN, Adapter: adapterHN, URL: u, FetchedAt: env.Now(),
				PublishedAt: time.Unix(h.CreatedAtI, 0).UTC(), Title: cleanSpace(stripTags(h.Title)), Body: body})
			newest = max(newest, h.CreatedAtI)
		}
		if newest > st.Cursor {
			q, newest := q, newest
			commits = append(commits, func(ctx context.Context) error {
				return env.Repo.PutCollectorState(ctx, SourceHN, q, collectorState{Cursor: newest})
			})
		}
	}
	if ok == 0 {
		return Collected{}, &CollectError{Reason: strings.Join(out.Notes, "; "), Retryable: true}
	}
	out.Commit = commitAll(commits)
	return out, nil
}

type hnItem struct {
	ID         int64    `json:"id"`
	CreatedAtI int64    `json:"created_at_i"`
	Text       string   `json:"text"`
	Children   []hnItem `json:"children"`
}

// collectHNHiring reads the newest "Ask HN: Who is hiring?" thread and emits
// one document per top-level comment after the thread's cursor, oldest first,
// about 300 per tick. The cursor moves to the newest comment collected, so a
// later tick takes the ones beyond the cap. Replies are discussion.
func collectHNHiring(ctx context.Context, env *collectEnv) (Collected, error) {
	var search struct {
		Hits []hnHit `json:"hits"`
	}
	if err := hnGet(ctx, env, env.Endpoints.hnSearch("Ask HN: Who is hiring?", "story,author_whoishiring"), &search); err != nil {
		return Collected{}, &CollectError{Reason: err.Error(), Retryable: true}
	}
	sort.SliceStable(search.Hits, func(i, j int) bool { return search.Hits[i].CreatedAtI > search.Hits[j].CreatedAtI })
	var thread *hnHit
	for i, h := range search.Hits {
		if i >= hnThreadSearch {
			break
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Title)), "ask hn: who is hiring?") {
			thread = &search.Hits[i]
			break
		}
	}
	if thread == nil {
		return Collected{}, &CollectError{Reason: "no \"Who is hiring?\" thread among the newest matches", Retryable: true}
	}
	var item hnItem
	if err := hnGet(ctx, env, env.Endpoints.hn()+"/api/v1/items/"+thread.ObjectID, &item); err != nil {
		return Collected{}, &CollectError{Reason: err.Error(), Retryable: true}
	}
	st, err := env.Repo.CollectorState(ctx, SourceHNHiring, thread.ObjectID)
	if err != nil {
		return Collected{}, err
	}
	top := item.Children
	sort.SliceStable(top, func(i, j int) bool { return top[i].CreatedAtI < top[j].CreatedAtI })
	var out Collected
	newest := st.Cursor
	for _, c := range top {
		// Comments of one second share a cursor value, so the cap never splits them.
		if len(out.Docs) >= hnHiringMaxPosts && c.CreatedAtI > newest {
			break
		}
		if c.CreatedAtI <= st.Cursor || strings.TrimSpace(c.Text) == "" {
			continue
		}
		body := htmlToText(c.Text)
		out.Docs = append(out.Docs, Doc{SourceID: SourceHNHiring, Adapter: adapterHNHiring, URL: hnItemPage + strconv.FormatInt(c.ID, 10),
			FetchedAt: env.Now(), PublishedAt: time.Unix(c.CreatedAtI, 0).UTC(), Title: firstLine(body, 100), Body: body})
		newest = c.CreatedAtI
	}
	if newest > st.Cursor {
		id := thread.ObjectID
		out.Commit = func(ctx context.Context) error {
			return env.Repo.PutCollectorState(ctx, SourceHNHiring, id, collectorState{Cursor: newest})
		}
	}
	return out, nil
}
