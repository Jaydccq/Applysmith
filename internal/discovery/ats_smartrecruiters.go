package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	srPageSize = 100
	srMaxPages = 5
)

type srPosting struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Company struct {
		Identifier string `json:"identifier"`
		Name       string `json:"name"`
	} `json:"company"`
	ReleasedDate string `json:"releasedDate"`
	Location     struct {
		City         string `json:"city"`
		Region       string `json:"region"`
		Country      string `json:"country"`
		Remote       bool   `json:"remote"`
		FullLocation string `json:"fullLocation"`
	} `json:"location"`
	Department struct {
		Label string `json:"label"`
	} `json:"department"`
	TypeOfEmployment struct {
		Label string `json:"label"`
	} `json:"typeOfEmployment"`
}

// parseSmartRecruiters reads one page. total is the vendor's totalFound.
func parseSmartRecruiters(body []byte, b Board) (posts []Posting, total int, err error) {
	var doc struct {
		TotalFound int         `json:"totalFound"`
		Content    []srPosting `json:"content"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("smartrecruiters listing: %w", err)
	}
	for _, j := range doc.Content {
		// Without the company identifier no valid URL can be built.
		if j.ID == "" || strings.TrimSpace(j.Name) == "" || j.Company.Identifier == "" {
			continue
		}
		loc := strings.TrimSpace(j.Location.FullLocation)
		if loc == "" {
			loc = joinNonEmpty(j.Location.City, j.Location.Region, strings.ToUpper(j.Location.Country))
		}
		posts = append(posts, Posting{
			ATS: SmartRecruiters, Board: b.Slug, JobID: j.ID, Title: strings.TrimSpace(j.Name),
			Company:  firstNonEmpty(j.Company.Name, b.company()),
			Location: loc, Remote: j.Location.Remote, PostedAt: parseTime(j.ReleasedDate),
			URL:        "https://jobs.smartrecruiters.com/" + j.Company.Identifier + "/" + j.ID,
			Department: j.Department.Label, EmploymentType: j.TypeOfEmployment.Label,
		})
	}
	return posts, doc.TotalFound, nil
}

// parseSmartRecruitersDetail returns the text of one posting's record: its
// sections in the order the posting page shows them, each under its title.
func parseSmartRecruitersDetail(body []byte) (string, error) {
	type section struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	var doc struct {
		JobAd struct {
			Sections struct {
				Company        section `json:"companyDescription"`
				Job            section `json:"jobDescription"`
				Qualifications section `json:"qualifications"`
				Additional     section `json:"additionalInformation"`
			} `json:"sections"`
		} `json:"jobAd"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("smartrecruiters posting: %w", err)
	}
	var parts []string
	sec := doc.JobAd.Sections
	for _, s := range []section{sec.Company, sec.Job, sec.Qualifications, sec.Additional} {
		if t := plainText(s.Text); t != "" {
			parts = append(parts, strings.TrimSpace(s.Title+"\n"+t))
		}
	}
	return truncateRunes(strings.Join(parts, "\n"), maxDescription), nil
}
