package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ashbyJob struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Department         string `json:"department"`
	EmploymentType     string `json:"employmentType"`
	Location           string `json:"location"`
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
	PublishedAt  string `json:"publishedAt"`
	IsListed     *bool  `json:"isListed"`
	IsRemote     bool   `json:"isRemote"`
	JobURL       string `json:"jobUrl"`
	Plain        string `json:"descriptionPlain"`
	HTML         string `json:"descriptionHtml"`
	Compensation struct {
		Summary string `json:"compensationTierSummary"`
	} `json:"compensation"`
}

func parseAshby(body []byte, b Board) ([]Posting, error) {
	var doc struct {
		Jobs []ashbyJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("ashby listing: %w", err)
	}
	var out []Posting
	for _, j := range doc.Jobs {
		if j.ID == "" || strings.TrimSpace(j.Title) == "" || (j.IsListed != nil && !*j.IsListed) {
			continue
		}
		desc := j.Plain
		if desc == "" {
			desc = plainText(j.HTML)
		}
		base := Posting{
			ATS: Ashby, Board: b.Slug, JobID: j.ID, Title: strings.TrimSpace(j.Title), Company: b.company(),
			URL: j.JobURL, Department: j.Department, EmploymentType: j.EmploymentType, Remote: j.IsRemote,
			PostedAt: parseTime(j.PublishedAt), Compensation: j.Compensation.Summary,
			Description: truncateRunes(strings.TrimSpace(desc), maxDescription),
		}
		if base.URL == "" {
			base.URL = "https://jobs.ashbyhq.com/" + b.Slug + "/" + j.ID
		}
		locs := []string{j.Location}
		for _, s := range j.SecondaryLocations {
			locs = append(locs, s.Location)
		}
		for _, loc := range locs {
			if strings.TrimSpace(loc) == "" && len(locs) > 1 {
				continue
			}
			p := base
			p.Location = strings.TrimSpace(loc)
			out = append(out, p)
		}
	}
	return out, nil
}
