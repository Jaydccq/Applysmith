package workspace

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// Portals is portals.yml.
type Portals struct {
	Version     int               `yaml:"version" v:"required,enum=1"`
	Filters     PortalFilters     `yaml:"filters"`
	Companies   []Company         `yaml:"companies"`
	Sources     map[string]Source `yaml:"sources" v:"keys=registry|tracked|builtin|amazon|a16z|theirstack|github|speedrun|exa|linkedin|linkedin-feed|indeed|ziprecruiter|google-jobs|glassdoor|newgrad|jobright|signals-edgar|signals-alerts|signals-hn|signals-hn-hiring|signals-web|signals-x"`
	Concurrency Concurrency       `yaml:"concurrency"`
	Registry    Registry          `yaml:"registry"`
}

type PortalFilters struct {
	Title         TitleFilter    `yaml:"title"`
	Locations     LocationFilter `yaml:"locations"`
	SkipCompanies []string       `yaml:"skip_companies" ev:"nonblank"`
}

type TitleFilter struct {
	Positive       []string `yaml:"positive" ev:"nonblank"`
	Negative       []string `yaml:"negative" ev:"nonblank"`
	SeniorityBoost []Boost  `yaml:"seniority_boost"`
}

type Boost struct {
	Term   string `yaml:"term" v:"required,nonblank"`
	Weight int    `yaml:"weight" v:"required,min=-10,max=10"`
}

type LocationFilter struct {
	Include []string `yaml:"include" ev:"nonblank"`
	Exclude []string `yaml:"exclude" ev:"nonblank"`
}

type Company struct {
	Name       string         `yaml:"name" v:"required,nonblank"`
	Enabled    *bool          `yaml:"enabled"`
	ATS        string         `yaml:"ats" v:"enum=greenhouse|lever|ashby|smartrecruiters|workable"`
	Slug       string         `yaml:"slug"`
	CareersURL string         `yaml:"careers_url" v:"format=https"`
	Filters    CompanyFilters `yaml:"filters"`
}

// CompanyFilters holds a per-company override. A non-nil Title replaces the
// global title filter for that company, even when it is empty.
type CompanyFilters struct {
	Title *TitleFilter `yaml:"title"`
}

func (e *Company) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }

func (e *Company) cross(c *collector, n *yaml.Node, path string) {
	hasATS := strings.TrimSpace(e.ATS) != "" || strings.TrimSpace(e.Slug) != ""
	hasURL := strings.TrimSpace(e.CareersURL) != ""
	switch {
	case hasATS && hasURL:
		c.add(n, path, CodeConstraint, fmt.Sprintf("company %q: exactly one source form is allowed, either ats with slug or careers_url", e.Name))
	case !hasATS && !hasURL:
		c.add(n, path, CodeConstraint, fmt.Sprintf("company %q: exactly one source form is required, either ats with slug or careers_url", e.Name))
	case hasATS && (e.ATS == "" || strings.TrimSpace(e.Slug) == ""):
		c.add(n, path, CodeConstraint, fmt.Sprintf("company %q: ats and slug must be given together", e.Name))
	}
}

// ValidateFeedURL rejects a feed address the alerts collector will not poll:
// anything that is not https, and the management page of Google Alerts (the
// user pastes the RSS feed address, not the page).
func ValidateFeedURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("a feed address must be an https URL")
	}
	if strings.Contains(u.Hostname(), "google.") && strings.HasPrefix(u.Path, "/alerts") && !strings.HasPrefix(u.Path, "/alerts/feeds/") {
		return errors.New("this is the management page of the alert, not its RSS feed address")
	}
	return nil
}

// cross checks the feed addresses listed under sources.signals-alerts.queries.
func (p *Portals) cross(c *collector, n *yaml.Node, path string) {
	queries := valueNode(valueNode(valueNode(n, "sources"), "signals-alerts"), "queries")
	if queries == nil || queries.Kind != yaml.SequenceNode {
		return
	}
	for i, q := range queries.Content {
		q = deref(q)
		raw := ""
		switch q.Kind {
		case yaml.ScalarNode:
			raw = q.Value
		case yaml.MappingNode:
			if v := valueNode(q, "url"); v != nil {
				raw = v.Value
			}
		}
		if err := ValidateFeedURL(raw); err != nil {
			c.add(q, fmt.Sprintf("sources.signals-alerts.queries[%d]", i), CodeConstraint, err.Error())
		}
	}
}

type Source struct {
	Enabled *bool       `yaml:"enabled"`
	Queries []yaml.Node `yaml:"queries"`
}

type Concurrency struct {
	List   *int `yaml:"list" v:"min=1,max=16"`
	Detail *int `yaml:"detail" v:"min=1,max=16"`
}

// EffectiveList and EffectiveDetail apply the defaults of 4 and 6.
func (c Concurrency) EffectiveList() int   { return orDefault(c.List, 4) }
func (c Concurrency) EffectiveDetail() int { return orDefault(c.Detail, 6) }

func orDefault(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

type Registry struct {
	Enabled *bool `yaml:"enabled"`
}

func (r Registry) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// ParsePortals validates src as portals.yml and decodes it.
func ParsePortals(src []byte) (Portals, Report) {
	rep, root := validateDoc[Portals](FilePortals.Name(), src)
	var p Portals
	if rep.OK() {
		decodeDoc(root, &p)
	}
	return p, rep
}
