package signals

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type feedDoc struct {
	XMLName xml.Name
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Content   string `xml:"content"`
		Summary   string `xml:"summary"`
	} `xml:"entry"`
	Items []struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		PubDate     string `xml:"pubDate"`
	} `xml:"channel>item"`
}

// ParseFeed reads an Atom or RSS feed into documents. The title and summary
// are stripped of markup and entities, so a later evidence quote can match
// them verbatim.
func ParseFeed(data []byte, now time.Time) ([]Doc, error) {
	var f feedDoc
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("not a feed: %w", err)
	}
	switch strings.ToLower(f.XMLName.Local) {
	case "feed", "rss", "rdf":
	default:
		return nil, fmt.Errorf("not a feed: the document is a %q element", f.XMLName.Local)
	}
	var docs []Doc
	add := func(title, link, body, when string) {
		link = strings.TrimSpace(link)
		if link == "" {
			return
		}
		d := Doc{SourceID: SourceAlerts, Adapter: adapterAlerts, URL: link, FetchedAt: now,
			Title: cleanSpace(stripTags(title)), Body: cleanSpace(stripTags(body))}
		for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123} {
			if t, err := time.Parse(layout, strings.TrimSpace(when)); err == nil {
				d.PublishedAt = t.UTC()
				break
			}
		}
		if d.Body == "" {
			d.Body = d.Title
		}
		docs = append(docs, d)
	}
	for _, e := range f.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		body := e.Content
		if body == "" {
			body = e.Summary
		}
		add(e.Title, link, body, firstNonEmpty(e.Published, e.Updated))
	}
	for _, it := range f.Items {
		add(it.Title, it.Link, it.Description, it.PubDate)
	}
	return docs, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func collectAlerts(ctx context.Context, env *collectEnv) (Collected, error) {
	urls := feedURLs(env.Queries)
	if len(urls) == 0 {
		return Collected{}, &SkipError{Reason: "no feed URLs"}
	}
	var out Collected
	var commits []func(context.Context) error
	okFeeds := 0
	for _, u := range urls {
		if err := ValidateFeedURL(u); err != nil {
			out.Notes = append(out.Notes, "rejected feed: "+err.Error())
			continue
		}
		st, err := env.Repo.CollectorState(ctx, SourceAlerts, u)
		if err != nil {
			return Collected{}, err
		}
		hdr := http.Header{}
		if st.ETag != "" {
			hdr.Set("If-None-Match", st.ETag)
		}
		if st.LastModified != "" {
			hdr.Set("If-Modified-Since", st.LastModified)
		}
		res, err := env.Fetch.Do(ctx, Request{URL: u, Header: hdr, Gap: gapRSS})
		if err != nil {
			out.Notes = append(out.Notes, "feed failed: "+err.Error())
			continue
		}
		switch res.Status {
		case http.StatusNotModified:
			okFeeds++
			continue
		case http.StatusOK:
		default:
			out.Notes = append(out.Notes, fmt.Sprintf("feed answered %d", res.Status))
			continue
		}
		docs, err := ParseFeed(res.Body, env.Now())
		if err != nil {
			out.Notes = append(out.Notes, err.Error())
			continue
		}
		okFeeds++
		out.Docs = append(out.Docs, docs...)
		next := collectorState{ETag: res.Header.Get("ETag"), LastModified: res.Header.Get("Last-Modified")}
		commits = append(commits, func(ctx context.Context) error { return env.Repo.PutCollectorState(ctx, SourceAlerts, u, next) })
	}
	if okFeeds == 0 {
		return Collected{}, &CollectError{Reason: strings.Join(out.Notes, "; "), Retryable: true}
	}
	out.Commit = commitAll(commits)
	return out, nil
}
