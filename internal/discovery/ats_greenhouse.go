package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ghJob struct {
	ID             json.Number `json:"id"`
	Title          string      `json:"title"`
	AbsoluteURL    string      `json:"absolute_url"`
	UpdatedAt      string      `json:"updated_at"`
	FirstPublished string      `json:"first_published"`
	CompanyName    string      `json:"company_name"`
	Location       struct {
		Name string `json:"name"`
	} `json:"location"`
	Offices     []GreenhouseOffice `json:"offices"`
	Departments []struct {
		Name string `json:"name"`
	} `json:"departments"`
	Content   string       `json:"content"`
	Questions []ghQuestion `json:"questions"`
}

type ghQuestion struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func parseGreenhouse(body []byte, b Board) ([]Posting, error) {
	var doc struct {
		Jobs []ghJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("greenhouse listing: %w", err)
	}
	var out []Posting
	for _, j := range doc.Jobs {
		id := j.ID.String()
		if id == "" || strings.TrimSpace(j.Title) == "" {
			continue
		}
		p := Posting{
			ATS: Greenhouse, Board: b.Slug, JobID: id, Title: strings.TrimSpace(j.Title),
			Company: firstNonEmpty(j.CompanyName, b.company()), Location: j.place(),
			URL: j.AbsoluteURL, PostedAt: parseTime(firstNonEmpty(j.FirstPublished, j.UpdatedAt)),
		}
		if p.URL == "" {
			p.URL = "https://boards.greenhouse.io/" + b.Slug + "/jobs/" + id
		}
		if len(j.Departments) > 0 {
			p.Department = j.Departments[0].Name
		}
		if j.Content != "" {
			p.Description = truncateRunes(plainText(j.Content), maxDescription)
		}
		p.Remote = isRemoteText(p.Location)
		out = append(out, p)
	}
	return out, nil
}

// place is the posting's place (GreenhouseLocation).
func (j ghJob) place() string { return GreenhouseLocation(j.Location.Name, j.Offices) }

// GreenhouseOffice is one of a Greenhouse posting's offices: its name and the
// address the board gives for it ("Bethesda, Maryland, United States").
type GreenhouseOffice struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

// GreenhouseLocation is a Greenhouse posting's place from its location name
// and its offices. The name stands unless it is a machine slug (Hover's
// "san_francisconew_york") or a site name with no comma and no remote work
// ("NIH-Bethesda"), neither of which a reader can resolve: then the offices'
// addresses stand for it when every office has one. A slug with none gives the
// offices' names, else its own words.
func GreenhouseLocation(name string, offices []GreenhouseOffice) string {
	name = strings.TrimSpace(name)
	var addrs, named []string
	for _, o := range offices {
		if a := strings.TrimSpace(o.Location); a != "" {
			addrs = append(addrs, a)
		}
		if n := strings.TrimSpace(o.Name); n != "" {
			named = append(named, n)
		}
	}
	slug := strings.Contains(name, "_") && !strings.ContainsAny(name, " \t")
	if len(addrs) > 0 && len(addrs) == len(offices) && (slug || name != "" && !strings.Contains(name, ",") && !isRemoteText(name)) {
		return strings.Join(addrs, "; ")
	}
	if !slug {
		return name
	}
	if len(named) > 0 {
		return strings.Join(named, "; ")
	}
	return strings.ReplaceAll(name, "_", " ")
}

// greenhouseDetailURL is the per-job endpoint that returns the application
// form's questions.
func (e Endpoints) greenhouseDetailURL(b Board, jobID string) string {
	return e.base(Greenhouse) + "/v1/boards/" + b.Slug + "/jobs/" + jobID + "?questions=true"
}

// parseGreenhouseDetail returns the body text followed by the form questions'
// labels and descriptions: a requirement such as a clearance can be stated
// only there.
func parseGreenhouseDetail(body []byte) (string, error) {
	var j ghJob
	if err := json.Unmarshal(body, &j); err != nil {
		return "", fmt.Errorf("greenhouse detail: %w", err)
	}
	parts := []string{plainText(j.Content)}
	for _, q := range j.Questions {
		for _, t := range []string{q.Label, q.Description} {
			if t = plainText(t); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return truncateRunes(strings.TrimSpace(strings.Join(parts, "\n")), maxDescription), nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isRemoteText(loc string) bool { return strings.Contains(strings.ToLower(loc), "remote") }
