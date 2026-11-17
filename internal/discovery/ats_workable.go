package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
)

type workableJob struct {
	Title       string `json:"title"`
	Shortcode   string `json:"shortcode"`
	Employment  string `json:"employment_type"`
	Telecommute bool   `json:"telecommuting"`
	Department  string `json:"department"`
	PublishedOn string `json:"published_on"`
	CreatedAt   string `json:"created_at"`
	Description string `json:"description"`
	City        string `json:"city"`
	State       string `json:"state"`
	Country     string `json:"country"`
	Locations   []struct {
		City    string `json:"city"`
		Region  string `json:"region"`
		Country string `json:"country"`
		Hidden  bool   `json:"hidden"`
	} `json:"locations"`
}

// parseWorkable keeps one candidate per location entry. A job listed twice
// under one shortcode stays twice, so the location scope is applied to each
// entry before de-duplication.
func parseWorkable(body []byte, b Board) ([]Posting, error) {
	var doc struct {
		Name string        `json:"name"`
		Jobs []workableJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("workable listing: %w", err)
	}
	// The account's own name wins over the registry's.
	company := firstNonEmpty(doc.Name, b.company())
	var out []Posting
	for _, j := range doc.Jobs {
		if j.Shortcode == "" || strings.TrimSpace(j.Title) == "" {
			continue
		}
		base := Posting{
			ATS: Workable, Board: b.Slug, JobID: j.Shortcode, Title: strings.TrimSpace(j.Title), Company: company,
			URL:        "https://apply.workable.com/" + b.Slug + "/j/" + j.Shortcode,
			Department: j.Department, EmploymentType: j.Employment, Remote: j.Telecommute,
			PostedAt:    parseTime(firstNonEmpty(j.PublishedOn, j.CreatedAt)),
			Description: truncateRunes(plainText(j.Description), maxDescription),
		}
		var locs []string
		for _, l := range j.Locations {
			if !l.Hidden {
				locs = append(locs, joinNonEmpty(l.City, l.Region, l.Country))
			}
		}
		if len(locs) == 0 {
			locs = []string{joinNonEmpty(j.City, j.State, j.Country)}
		}
		for _, loc := range locs {
			p := base
			p.Location = loc
			out = append(out, p)
		}
	}
	return out, nil
}
