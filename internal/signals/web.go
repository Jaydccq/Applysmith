package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Web search bounds.
const (
	webMaxQueries = 10
	webMaxResults = 10
	webTextMax    = 8000
	// webTurnScope keeps the index of the query the next tick starts with.
	webTurnScope = "turn"
)

// SearchResult is one web search hit.
type SearchResult struct {
	URL, Title, Snippet, Text string
	Published                 time.Time
}

// SearchProvider is the configured web-search provider. It never scrapes a
// consumer search engine.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, n int, scope Scope) ([]SearchResult, error)
}

// Scope narrows a search to the Include hosts (none: any) and leaves the
// Exclude hosts out, inside the provider, so excluded pages take no result
// slots.
type Scope struct{ Include, Exclude []string }

// ExaSearch is the Exa search API as a provider, with the key of the exa
// discovery source.
type ExaSearch struct {
	Fetch *Fetcher
	Base  string
	Key   string
}

func (e *ExaSearch) Name() string { return "exa" }

func (e *ExaSearch) Search(ctx context.Context, query string, n int, scope Scope) ([]SearchResult, error) {
	req := map[string]any{
		"query": query, "numResults": n, "type": "neural",
		"contents": map[string]any{"text": map[string]any{"maxCharacters": webTextMax}},
	}
	if len(scope.Include) > 0 {
		req["includeDomains"] = scope.Include
	}
	if len(scope.Exclude) > 0 {
		req["excludeDomains"] = scope.Exclude
	}
	body, _ := json.Marshal(req)
	res, err := e.Fetch.Do(ctx, Request{Method: http.MethodPost, URL: e.Base + "/search", Body: body, Gap: gapWeb,
		Header: http.Header{"X-Api-Key": {e.Key}, "Content-Type": {"application/json"}}})
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, classifyKeyed("web search", res)
	}
	var doc struct {
		Results []struct {
			URL       string `json:"url"`
			Title     string `json:"title"`
			Text      string `json:"text"`
			Published string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		return nil, fmt.Errorf("web search: unexpected response: %w", err)
	}
	var out []SearchResult
	for _, h := range doc.Results {
		r := SearchResult{URL: h.URL, Title: h.Title, Text: capRunes(h.Text, webTextMax)}
		if t, err := time.Parse(time.RFC3339, h.Published); err == nil {
			r.Published = t.UTC()
		}
		out = append(out, r)
	}
	return out, nil
}

// classifyKeyed turns a refused keyed request into a failure with the action
// it needs: an authentication or billing answer is never retried.
func classifyKeyed(what string, res Response) error {
	switch res.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &CollectError{Reason: what + ": authentication failed (key rejected)", RequiredAction: "Check the " + what + " key in the keychain and store a valid one."}
	case http.StatusPaymentRequired:
		return &CollectError{Reason: what + ": payment required (credit exhausted)", RequiredAction: "Top up the " + what + " account."}
	}
	return statusError(what, res)
}

func collectWeb(ctx context.Context, env *collectEnv) (Collected, error) {
	if env.Search == nil {
		return Collected{}, &SkipError{Reason: "no key"}
	}
	queries := queryStrings(env.Queries)
	if len(queries) == 0 {
		return Collected{}, &SkipError{Reason: "no queries"}
	}
	var out Collected
	if n := len(queries); n > webMaxQueries {
		// Ten queries a tick, starting where the last tick stopped, so every
		// query has its turn.
		st, err := env.Repo.CollectorState(ctx, SourceWeb, webTurnScope)
		if err != nil {
			return Collected{}, err
		}
		start := int(st.Cursor) % n
		turn := append(append([]string{}, queries[start:]...), queries[:start]...)
		queries, out.Deferred = turn[:webMaxQueries], turn[webMaxQueries:]
		next := int64((start + webMaxQueries) % n)
		out.Commit = func(ctx context.Context) error {
			return env.Repo.PutCollectorState(ctx, SourceWeb, webTurnScope, collectorState{Cursor: next})
		}
	}
	ok := 0
	for _, q := range queries {
		hits, err := env.Search.Search(ctx, q, webMaxResults, Scope{})
		if err != nil {
			var ce *CollectError
			if asCollectError(err, &ce) && !ce.Retryable {
				return Collected{}, err
			}
			out.Notes = append(out.Notes, err.Error())
			continue
		}
		ok++
		for _, h := range hits {
			body := firstNonEmpty(h.Text, h.Snippet, h.Title)
			out.Docs = append(out.Docs, Doc{SourceID: SourceWeb, Adapter: adapterWeb, URL: h.URL, FetchedAt: env.Now(), PublishedAt: h.Published,
				Title: cleanSpace(stripTags(h.Title)), Body: strings.TrimSpace(capRunes(body, webTextMax))})
		}
	}
	if ok == 0 {
		return Collected{}, &CollectError{Reason: strings.Join(out.Notes, "; "), Retryable: true}
	}
	return out, nil
}
