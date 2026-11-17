package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"

	"applysmith/internal/identity"
)

// The duplicate-detection rules, in precedence order.
const (
	RuleReport  = "report number"
	RuleID      = "row id"
	RuleKey     = "identity key"
	RuleOpening = "same opening"
)

// ruleWords say what each rule matched, in the user's words.
var ruleWords = map[string]string{RuleReport: "the same report", RuleID: "the same row", RuleKey: "the same posting", RuleOpening: "the same company and role"}

// candidate is the part of a row the matching rules read.
type candidate struct {
	id       int64
	company  string
	role     string
	location string
	key      string
	aliases  []string
	status   Status
	report   int64 // 0 when the row has no report
	archived bool
}

func (c candidate) hasKey(k string) bool { return c.key == k || slices.Contains(c.aliases, k) }

// posting is the row seen as a posting for the shared predicates. An
// ATS-native alias stands for the row when the primary key is not ATS-native,
// so a row reached through a mirror still refuses a different requisition.
func (c candidate) posting() identity.Posting {
	key := c.key
	if !identity.IsATSNative(key) {
		for _, a := range c.aliases {
			if identity.IsATSNative(a) {
				key = a
				break
			}
		}
	}
	return identity.Posting{Key: key, Company: c.company, Role: c.role, Location: c.location}
}

func loadCandidates(ctx context.Context, q querier) ([]candidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, company, role, location, identity_key, status, COALESCE(report_number, 0), archived_at IS NOT NULL FROM tracker_entries ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	byID := map[int64]int{}
	for rows.Next() {
		var c candidate
		var archived int
		if err := rows.Scan(&c.id, &c.company, &c.role, &c.location, &c.key, &c.status, &c.report, &archived); err != nil {
			return nil, err
		}
		c.archived = archived == 1
		byID[c.id] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	arows, err := q.QueryContext(ctx, `SELECT entry_id, identity_key FROM tracker_aliases ORDER BY identity_key`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var id int64
		var key string
		if err := arows.Scan(&id, &key); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].aliases = append(out[i].aliases, key)
		}
	}
	return out, arows.Err()
}

// matchInput is what a report or a user-added row is matched on.
type matchInput struct {
	report  int64
	entryID int64
	posting identity.Posting // Key is set
	// aliases are other keys of the posting (Report.Aliases); rule 3 matches
	// them as it matches the key.
	aliases []string
}

// findMatch applies the rules in order. Within the first rule that matches
// anything, the row with the highest status order wins, then the lowest id.
// Rule 4 does not apply between two unequal ATS-native keys; the shared
// predicate carries that exception.
func findMatch(cands []candidate, in matchInput) (candidate, string, bool) {
	rules := []struct {
		name string
		hit  func(candidate) bool
	}{
		{RuleReport, func(c candidate) bool { return in.report != 0 && c.report == in.report }},
		{RuleID, func(c candidate) bool { return in.entryID != 0 && c.id == in.entryID }},
		{RuleKey, func(c candidate) bool {
			return in.posting.Key != "" && c.hasKey(in.posting.Key) || slices.ContainsFunc(in.aliases, c.hasKey)
		}},
		{RuleOpening, func(c candidate) bool { return identity.SameOpening(c.posting(), in.posting) }},
	}
	for _, r := range rules {
		var best *candidate
		for i := range cands {
			c := &cands[i]
			if !r.hit(*c) {
				continue
			}
			if best == nil || c.status.Order() > best.status.Order() {
				best = c
			}
		}
		if best != nil {
			return *best, r.name, true
		}
	}
	return candidate{}, "", false
}

// rowKey is the identity key stored with a new row. A posting that carries
// nothing to key on (a placeholder company and no URL) still needs a unique
// key; it can never be matched, which is the point of a placeholder.
func rowKey(p identity.Posting) (string, error) {
	if k := p.IdentityKey(); k != "" {
		return k, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "unkeyed:" + hex.EncodeToString(b[:]), nil
}
