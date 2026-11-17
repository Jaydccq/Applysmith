package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"applysmith/internal/identity"
)

// Recovery verdicts of a posting lead.
const (
	RecoveryOpen    = "open"
	RecoveryClosed  = "closed"
	RecoveryUnknown = "unknown"
)

// Recovery is what the board that owns a posting says about it. A lead
// carries a title, a location and a company name copied from whatever named
// the company upstream, and no description; only the board's own record is
// believed.
type Recovery struct {
	Verdict string
	Reason  string
	// URL, Company, Title, Location and Description are the board record's,
	// set for an open posting. Description is plain text, capped at 20,000
	// characters.
	URL, Company, Title, Location, Description string
	Posting                                    Posting
}

type boardListing struct {
	posts []Posting
	err   error
}

// LeadRecoverer recovers posting leads from their boards. It asks each board
// for its listing once for as long as it lives, however many leads belong to
// it, so a run holds one.
type LeadRecoverer struct {
	Client    *Client
	Endpoints Endpoints
	repo      *Repo

	mu       sync.Mutex
	listings map[string]*boardListing
	requests map[string]int
}

// NewLeadRecoverer returns a recoverer for one run.
func NewLeadRecoverer(c *Client, ep Endpoints, repo *Repo) *LeadRecoverer {
	if c == nil {
		c = NewClient()
	}
	return &LeadRecoverer{Client: c, Endpoints: ep, repo: repo, listings: map[string]*boardListing{}, requests: map[string]int{}}
}

// Requests reports how many listing requests were made per board key.
func (rc *LeadRecoverer) Requests() map[string]int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make(map[string]int, len(rc.requests))
	for k, v := range rc.requests {
		out[k] = v
	}
	return out
}

func (rc *LeadRecoverer) listing(ctx context.Context, b Board) *boardListing {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if l := rc.listings[b.Key()]; l != nil {
		return l
	}
	rc.requests[b.Key()]++
	res, err := rc.Client.FetchBoard(ctx, rc.Endpoints, b, "")
	l := &boardListing{posts: res.Postings, err: err}
	// A cancelled run says nothing about the board; do not remember it.
	if ctx.Err() == nil {
		rc.listings[b.Key()] = l
	}
	return l
}

// leadPostingLink is the link a posting lead is recovered by: its best
// single-posting link.
func leadPostingLink(l Lead) string {
	for _, link := range append(RankLinks(l.ApplyLinks), l.Evidence.URL) {
		if k := LinkKind(link); k == LinkATSJob || k == LinkJobDetail {
			return link
		}
	}
	return ""
}

// Recover gives one lead one verdict.
func (rc *LeadRecoverer) Recover(ctx context.Context, l Lead) Recovery {
	link := leadPostingLink(l)
	if link == "" {
		return Recovery{Verdict: RecoveryUnknown, Reason: "the lead has no link to one posting"}
	}
	key, ok := identity.ATSKey(link)
	board, berr := ResolveBoard(link)
	if !ok || berr != nil {
		key, board, ok = rc.linkedBoard(ctx, l, link)
		if !ok {
			return Recovery{Verdict: RecoveryUnknown, Reason: "no supported board owns this link"}
		}
	}
	list := rc.listing(ctx, board)
	var bf *BoardFailure
	switch {
	case errors.As(list.err, &bf) && strings.Contains(bf.Reason, "empty posting list"):
		return Recovery{Verdict: RecoveryClosed, Reason: "the board lists no postings"}
	case list.err != nil:
		return Recovery{Verdict: RecoveryUnknown, Reason: "the board did not answer: " + list.err.Error()}
	}
	for _, p := range list.posts {
		if !strings.EqualFold(p.key(), key) {
			continue
		}
		text := p.Description
		if text == "" {
			var err error
			if text, err = rc.Client.FetchDescription(ctx, rc.Endpoints, p); err != nil {
				return Recovery{Verdict: RecoveryUnknown, Reason: "the board record could not be read: " + err.Error()}
			}
		}
		if strings.TrimSpace(text) == "" {
			return Recovery{Verdict: RecoveryUnknown, Reason: "the board record carries no description"}
		}
		p.Description = truncateRunes(text, maxLeadText)
		return Recovery{Verdict: RecoveryOpen, URL: p.URL, Company: p.Company, Title: p.Title, Location: p.Location, Description: p.Description, Posting: p}
	}
	return Recovery{Verdict: RecoveryClosed, Reason: fmt.Sprintf("the %s board does not list the posting", board.Slug)}
}

// linkedBoard uses the native identity already retained by a signals handoff.
// The link must belong to that exact inbox row; a company name or cached
// description never supplies a board or establishes that a posting is open.
func (rc *LeadRecoverer) linkedBoard(ctx context.Context, l Lead, link string) (string, Board, bool) {
	if rc.repo == nil || l.InboxID == 0 {
		return "", Board{}, false
	}
	row, err := rc.repo.InboxGet(ctx, l.InboxID)
	if err != nil {
		return "", Board{}, false
	}
	canon, err := postingURL(link)
	if err != nil {
		return "", Board{}, false
	}
	matched := false
	for _, raw := range append([]string{row.URL, row.OriginalURL}, row.Aliases...) {
		if other, err := postingURL(raw); err == nil && other == canon {
			matched = true
			break
		}
	}
	if !matched {
		return "", Board{}, false
	}
	boardKey, id, found := strings.Cut(row.IdentityKey, "/")
	board, err := ParseBoardKey(boardKey)
	if !found || err != nil {
		return "", Board{}, false
	}
	key, ok := identity.NativeKey(board.ATS, board.Slug, id)
	return key, board, ok && key == row.IdentityKey
}
