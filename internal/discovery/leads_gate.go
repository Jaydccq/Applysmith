package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
)

// Ingress names the lead path a gate rejection came from.
const (
	IngressFeed      = "feed"
	IngressExpansion = "expansion"
	IngressEvaluate  = "evaluate"
)

// LeadChecker applies the title filter, the blocked list, the location lists,
// the scope and the eligibility gate to a candidate on a lead path, and
// reports every rule that failed, not only the first.
type LeadChecker struct {
	Filters Filters
	Scope   string
	Gate    Gate
}

// Failing returns the rules p fails; empty means p passes. An empty title or
// location is not checked: a lead may name neither.
func (c LeadChecker) Failing(p Posting) []string {
	var rules []string
	if c.Filters.CompanyBlocked(p.Company) {
		rules = append(rules, FilterBlockedCompany)
	}
	if strings.TrimSpace(p.Title) != "" && !c.Filters.Title.Check(p.Title).Pass {
		rules = append(rules, FilterTitle)
	}
	if p.Location != "" {
		if !c.Filters.LocationOK(p.Location) {
			rules = append(rules, FilterLocation)
		}
		if c.Scope != "" && !ApplyScope(c.Scope, p.Location).Pass {
			rules = append(rules, FilterScope)
		}
	}
	if c.Gate != nil {
		row := InboxRow{URL: p.URL, Company: p.Company, Role: p.Title, Location: p.Location, Description: p.Description, Source: p.Source}
		for _, rule := range strings.Split(c.Gate(row), ",") {
			if rule = strings.TrimSpace(rule); rule != "" && !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
	}
	return rules
}

// RoleVerdict is the outcome of one named role.
type RoleVerdict struct {
	Role  string
	Rules []string // empty when the role passed
}

// Narrow gates a lead role by role. A role passes when at least one of the
// locations passes (a lead naming several locations is rejected on geography
// only when every location fails). It returns the roles that passed and the
// verdicts of those that did not. A lead naming no role is gated once, on its
// company and locations.
func (c LeadChecker) Narrow(base Posting, roles, locations []string) (kept []string, rejected []RoleVerdict) {
	locs := locations
	if len(locs) == 0 {
		locs = []string{""}
	}
	check := func(role string) []string {
		var union []string
		for _, loc := range locs {
			p := base
			p.Title, p.Location = role, loc
			rules := c.Failing(p)
			if len(rules) == 0 {
				return nil
			}
			for _, r := range rules {
				if !slices.Contains(union, r) {
					union = append(union, r)
				}
			}
		}
		return union
	}
	if len(roles) == 0 {
		if rules := check(""); len(rules) > 0 {
			rejected = append(rejected, RoleVerdict{Rules: rules})
		}
		return nil, rejected
	}
	for _, role := range roles {
		if rules := check(role); len(rules) > 0 {
			rejected = append(rejected, RoleVerdict{Role: role, Rules: rules})
		} else {
			kept = append(kept, role)
		}
	}
	return kept, rejected
}

// LeadGateAudit records a rejected candidate with every rule it failed.
func (r *Repo) LeadGateAudit(ctx context.Context, ingress string, p Posting, rules []string) error {
	rj, _ := json.Marshal(nonNilStrings(rules))
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO lead_gate_audit (at, ingress, identity_key, title, location, company, url, rules_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.stamp(), ingress, p.key(), p.Title, p.Location, p.Company, p.URL, string(rj))
		return err
	})
}
