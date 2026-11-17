package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
)

// repostWindow is how long a company and role suppress a repost.
const repostWindow = 14 * 24 * time.Hour

var (
	textURLRE = lazyregexp.New(`https?://[^\s<>"')\]]+`)
	spaceRE   = lazyregexp.New(`\s+`)
)

// trackingParam reports a query parameter that identifies a click, not a page.
func trackingParam(k string) bool {
	k = strings.ToLower(k)
	if strings.HasPrefix(k, "utm_") {
		return true
	}
	switch k {
	case "trk", "trkinfo", "trackingid", "refid", "midtoken", "midsig", "lipi", "otptoken", "eid", "fbclid", "gclid", "mc_cid", "mc_eid", "ref", "src":
		return true
	}
	return false
}

// stripTracking removes tracking parameters from every URL in a text.
func stripTracking(text string) string {
	return textURLRE.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.RawQuery == "" {
			return raw
		}
		q := u.Query()
		for k := range q {
			if trackingParam(k) {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	})
}

// normalizeLeadText collapses whitespace, folds case and drops tracking
// parameters, so the same post reads the same twice.
func normalizeLeadText(s string) string {
	return strings.ToLower(spaceRE.ReplaceAllString(strings.TrimSpace(stripTracking(s)), " "))
}

// leadBodyHash is the SHA-256 of the normalized author and body.
func leadBodyHash(author, body string) string {
	sum := sha256.Sum256([]byte(normalizeLeadText(author) + "\n" + normalizeLeadText(body)))
	return hex.EncodeToString(sum[:])
}

// permalinkKey is the stable form of a post permalink: no tracking
// parameters, no fragment, no trailing slash.
func permalinkKey(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	q := u.Query()
	for k := range q {
		if trackingParam(k) {
			q.Del(k)
		}
	}
	u.RawQuery, u.Fragment = q.Encode(), ""
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// leadSeen returns the id of a stored lead with the same permalink or, when
// the post has none, the same author and body.
func (r *Repo) leadSeen(ctx context.Context, dedupKey, permalink, bodyHash string) (int64, bool, error) {
	var id int64
	q := `SELECT id FROM leads WHERE dedup_key = ?`
	args := []any{dedupKey}
	if permalink == "" {
		q += ` OR body_hash = ?`
		args = append(args, bodyHash)
	}
	rows, err := r.s.Read().QueryContext(ctx, q+` ORDER BY id LIMIT 1`, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, false, err
		}
		return id, true, nil
	}
	return 0, false, rows.Err()
}

// leadRepost reports whether every role of a post is already held by a lead
// of the same company stored in the previous 14 days (location ignored,
// company and role compared as the same-opening predicate does).
func (r *Repo) leadRepost(ctx context.Context, company string, roles []string) (int64, bool, error) {
	if len(roles) == 0 || strings.TrimSpace(company) == "" {
		return 0, false, nil
	}
	since := r.now().Add(-repostWindow).UTC().Format(timeLayout)
	recent, err := r.leadsSince(ctx, since)
	if err != nil {
		return 0, false, err
	}
	var first int64
	for _, role := range roles {
		hit := false
		for _, l := range recent {
			if sameLeadRole(l, company, role) {
				hit = true
				if first == 0 {
					first = l.ID
				}
				break
			}
		}
		if !hit {
			return 0, false, nil
		}
	}
	return first, true, nil
}

func sameLeadRole(l Lead, company, role string) bool {
	for _, lr := range l.Roles {
		if identity.SameOpening(identity.Posting{Company: l.Company, Role: lr}, identity.Posting{Company: company, Role: role}) {
			return true
		}
	}
	return false
}

// leadsSince lists the leads stored since a time with what the repost check
// compares: the id, the company and the roles.
func (r *Repo) leadsSince(ctx context.Context, since string) ([]Lead, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, company, roles_json FROM leads WHERE created_at >= ? AND company <> '' AND roles_json <> '[]' ORDER BY id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lead
	for rows.Next() {
		var l Lead
		var roles string
		if err := rows.Scan(&l.ID, &l.Company, &roles); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(roles), &l.Roles)
		out = append(out, l)
	}
	return out, rows.Err()
}
