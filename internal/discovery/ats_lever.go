package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type leverPosting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	CreatedAt  int64  `json:"createdAt"`
	Workplace  string `json:"workplaceType"`
	Plain      string `json:"descriptionPlain"`
	Additional string `json:"additionalPlain"`
	Lists      []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
	Categories struct {
		Commitment   string   `json:"commitment"`
		Department   string   `json:"department"`
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
	} `json:"categories"`
	Salary struct {
		Currency string  `json:"currency"`
		Interval string  `json:"interval"`
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
	} `json:"salaryRange"`
	SalaryDescription string `json:"salaryDescriptionPlain"`
}

func parseLever(body []byte, b Board) ([]Posting, error) {
	var jobs []leverPosting
	if err := json.Unmarshal(body, &jobs); err != nil {
		return nil, fmt.Errorf("lever listing: %w", err)
	}
	var out []Posting
	for _, j := range jobs {
		if j.ID == "" || strings.TrimSpace(j.Text) == "" {
			continue
		}
		desc := []string{j.Plain}
		for _, l := range j.Lists {
			desc = append(desc, l.Text, plainText(l.Content))
		}
		desc = append(desc, j.Additional)
		base := Posting{
			ATS: Lever, Board: b.Slug, JobID: j.ID, Title: strings.TrimSpace(j.Text), Company: b.company(),
			URL: j.HostedURL, Department: j.Categories.Department, EmploymentType: j.Categories.Commitment,
			Description: truncateRunes(strings.TrimSpace(strings.Join(nonEmpty(desc), "\n")), maxDescription),
			Remote:      strings.EqualFold(j.Workplace, "remote"),
		}
		if base.URL == "" {
			base.URL = "https://jobs.lever.co/" + b.Slug + "/" + j.ID
		}
		if j.CreatedAt > 0 {
			base.PostedAt = time.UnixMilli(j.CreatedAt).UTC()
		}
		if j.Salary.Max > 0 {
			base.Compensation = fmt.Sprintf("%s %.0f-%.0f %s", j.Salary.Currency, j.Salary.Min, j.Salary.Max, j.Salary.Interval)
		} else {
			base.Compensation = strings.TrimSpace(j.SalaryDescription)
		}
		locs := j.Categories.AllLocations
		if len(locs) == 0 {
			locs = []string{j.Categories.Location}
		}
		for _, loc := range locs {
			p := base
			p.Location = strings.TrimSpace(loc)
			out = append(out, p)
		}
	}
	return out, nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
