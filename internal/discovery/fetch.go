package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// BoardResult is one visit to a board.
type BoardResult struct {
	Postings    []Posting
	NotModified bool
	ETag        string
	// Truncated is set for a paged board with more postings than the page
	// cap; Lost counts the postings not read.
	Truncated bool
	Lost      int
}

// BoardFailure is a visit that counts as a strike: a 404, 410, 401 or 403, an
// unparsable payload, or an empty posting list.
type BoardFailure struct {
	Status int
	Reason string
}

func (e *BoardFailure) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s (%d)", e.Reason, e.Status)
	}
	return e.Reason
}

// FetchBoard reads a board's listing through the ATS API. Results from a
// conditional request that answers 304 carry no postings. A Lever board the
// global API answers 404 for is read from the EU API.
func (c *Client) FetchBoard(ctx context.Context, ep Endpoints, b Board, etag string) (BoardResult, error) {
	if b.ATS == SmartRecruiters {
		return c.fetchSmartRecruiters(ctx, ep, b)
	}
	list := ep.ListURL(b, 0)
	res, err := c.Get(ctx, list, etag)
	if eu := ep.leverEUListURL(b); err == nil && res.Status == http.StatusNotFound && eu != "" {
		list = eu
		res, err = c.Get(ctx, list, etag)
	}
	if err != nil {
		return BoardResult{}, err
	}
	if err := judge(res, list); err != nil {
		return BoardResult{}, err
	}
	if res.NotModified {
		return BoardResult{NotModified: true, ETag: res.ETag}, nil
	}
	var posts []Posting
	switch b.ATS {
	case Greenhouse:
		posts, err = parseGreenhouse(res.Body, b)
	case Lever:
		posts, err = parseLever(res.Body, b)
	case Ashby:
		posts, err = parseAshby(res.Body, b)
	case Workable:
		posts, err = parseWorkable(res.Body, b)
	default:
		return BoardResult{}, fmt.Errorf("unsupported ats %q", b.ATS)
	}
	if err != nil {
		return BoardResult{}, &BoardFailure{Reason: "unparsable payload: " + err.Error()}
	}
	if len(posts) == 0 {
		return BoardResult{}, &BoardFailure{Reason: "empty posting list"}
	}
	return BoardResult{Postings: posts, ETag: res.ETag}, nil
}

// judge maps a response to a strike, a transient failure or success. A body
// that is not JSON at all (a sign-in page, an error page from a proxy) says
// nothing about the board, and neither does a redirect off the host: only a
// JSON document of the wrong shape is an unparsable payload.
func judge(res Response, rawURL string) error {
	switch res.Status {
	case http.StatusNotFound, http.StatusGone, http.StatusUnauthorized, http.StatusForbidden:
		return &BoardFailure{Status: res.Status, Reason: "board not available"}
	case http.StatusOK:
		if !json.Valid(res.Body) {
			return &TransientError{URL: rawURL, Status: res.Status, Err: errors.New("answered with something other than JSON")}
		}
		return nil
	case http.StatusNotModified:
		return nil
	}
	return &TransientError{URL: rawURL, Status: res.Status, Err: fmt.Errorf("unexpected status %d", res.Status)}
}

func (c *Client) fetchSmartRecruiters(ctx context.Context, ep Endpoints, b Board) (BoardResult, error) {
	var out BoardResult
	total, read := 0, 0
	for page := 0; page < srMaxPages; page++ {
		res, err := c.Get(ctx, ep.ListURL(b, page), "")
		if err != nil {
			return BoardResult{}, err
		}
		if err := judge(res, ep.ListURL(b, page)); err != nil {
			return BoardResult{}, err
		}
		posts, t, err := parseSmartRecruiters(res.Body, b)
		if err != nil {
			return BoardResult{}, &BoardFailure{Reason: "unparsable payload: " + err.Error()}
		}
		total = t
		out.Postings = append(out.Postings, posts...)
		read += srPageSize
		if read >= total || t == 0 {
			break
		}
	}
	if len(out.Postings) == 0 {
		return BoardResult{}, &BoardFailure{Reason: "empty posting list"}
	}
	if total > srMaxPages*srPageSize {
		out.Truncated = true
		out.Lost = total - srMaxPages*srPageSize
	}
	return out, nil
}

// FetchDescription reads the description text of a posting from the ATS when
// the listing omitted it: Greenhouse's detail, whose text includes the
// application form's questions, and a SmartRecruiters posting's own record,
// since its listing carries no text. The other boards list the text.
func (c *Client) FetchDescription(ctx context.Context, ep Endpoints, p Posting) (string, error) {
	if p.ATS == SmartRecruiters {
		res, err := c.Get(ctx, ep.base(SmartRecruiters)+"/v1/companies/"+url.PathEscape(p.Board)+"/postings/"+url.PathEscape(p.JobID), "")
		if err != nil {
			return "", err
		}
		if res.Status != http.StatusOK {
			return "", fmt.Errorf("smartrecruiters posting answered %d", res.Status)
		}
		return parseSmartRecruitersDetail(res.Body)
	}
	if p.ATS != Greenhouse {
		return p.Description, nil
	}
	res, err := c.Get(ctx, ep.greenhouseDetailURL(Board{ATS: Greenhouse, Slug: p.Board}, p.JobID), "")
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		return "", fmt.Errorf("greenhouse detail answered %d", res.Status)
	}
	return parseGreenhouseDetail(res.Body)
}
