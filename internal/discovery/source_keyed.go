package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TheirStack bounds.
const (
	theirStackBase    = "https://api.theirstack.com"
	theirStackTimeout = 10 * time.Second
	theirStackLimit   = 25
)

var errKeyRejected = errors.New("the key was rejected; check it in Settings")

// keyedStatus turns a refused request into an error that carries no response
// body, because a vendor may echo a credential back.
func keyedStatus(res Response) error {
	switch {
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		return errKeyRejected
	case res.Status == http.StatusPaymentRequired:
		return errors.New("the account has no credit left")
	case res.Status >= 400:
		return fmt.Errorf("the request answered %d", res.Status)
	}
	return nil
}

func bearer(key string) http.Header {
	return http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}}
}

// readTheirStack searches the keyed jobs API. A search must carry a narrowing
// filter; without one it is refused before any request.
func readTheirStack(ctx context.Context, r *run) error {
	qs, err := r.queries()
	if err != nil {
		return err
	}
	if len(qs) == 0 {
		qs = []Query{{P: map[string]string{}}}
	}
	limit := theirStackLimit
	if r.req.Limit > 0 {
		limit = r.req.Limit
	}
	for _, q := range qs {
		body, ok := theirStackBody(q, r.window(0), limit)
		if !ok {
			return &endRun{status: RunFailed, reason: "no-filter", err: errors.New("a TheirStack search needs at least one of: posting age (posted_within or a window), posted_at_gte, posted_at_lte, company_domain, company_linkedin_url, company_name")}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.queryRan()
		label := q.label()
		b, _ := json.Marshal(body)
		res, err := r.client.Do(ctx, Request{Method: http.MethodPost, URL: r.base("theirstack", theirStackBase) + "/v1/jobs/search", Body: b,
			Header: bearer(r.key), Timeout: theirStackTimeout})
		if err == nil {
			err = keyedStatus(res)
		}
		if err != nil {
			r.queryFailed(label, err)
			continue
		}
		posts, err := parseTheirStack(res)
		if err != nil {
			r.queryFailed(label, err)
			continue
		}
		if err := r.emit(ctx, label, posts); err != nil {
			return err
		}
	}
	return nil
}

// theirStackBody builds the request, or reports ok false when the search has
// no narrowing filter.
func theirStackBody(q Query, window time.Duration, limit int) (map[string]any, bool) {
	body := map[string]any{"page": 0, "limit": limit}
	narrow := false
	if window > 0 {
		body["posted_at_max_age_days"] = int((window + day - 1) / day)
		narrow = true
	}
	if n, _ := strconv.Atoi(q.Get("posted_within_days")); n > 0 {
		body["posted_at_max_age_days"] = n
		narrow = true
	}
	for _, k := range []string{"posted_at_gte", "posted_at_lte"} {
		if v := q.Get(k); v != "" {
			body[k] = v
			narrow = true
		}
	}
	for param, field := range map[string]string{"company_domain": "company_domain_or", "company_linkedin_url": "company_linkedin_url_or", "company_name": "company_name_or"} {
		if v := q.Get(param); v != "" {
			body[field] = splitList(v)
			narrow = true
		}
	}
	if t := firstNonEmpty(q.Get("title"), q.Search); t != "" {
		body["job_title_or"] = splitList(t)
	}
	if c := q.Get("country"); c != "" {
		body["job_country_code_or"] = splitList(c)
	}
	return body, narrow
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return slices.Clip(out)
}

func parseTheirStack(res Response) ([]Posting, error) {
	var doc struct {
		Data *[]struct {
			ID       any    `json:"id"`
			Title    string `json:"job_title"`
			URL      string `json:"url"`
			FinalURL string `json:"final_url"`
			Date     string `json:"date_posted"`
			Company  string `json:"company"`
			Location string `json:"location"`
			Short    string `json:"short_location"`
			Remote   bool   `json:"remote"`
			Desc     string `json:"description"`
			Salary   string `json:"salary_string"`
			Object   struct {
				Name string `json:"name"`
			} `json:"company_object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil || doc.Data == nil {
		return nil, errLayout{"the response is not a job list"}
	}
	var out []Posting
	for _, j := range *doc.Data {
		u := firstNonEmpty(j.FinalURL, j.URL)
		if j.Title == "" || u == "" {
			continue
		}
		p := Posting{JobID: fmt.Sprint(j.ID), Title: j.Title, URL: u, Company: firstNonEmpty(j.Object.Name, j.Company),
			Location: firstNonEmpty(j.Location, j.Short), Remote: j.Remote, Compensation: j.Salary, PostedAt: parseTime(j.Date),
			Description: truncateRunes(plainText(j.Desc), maxDescription)}
		out = append(out, p)
	}
	return out, nil
}
