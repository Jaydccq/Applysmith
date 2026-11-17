package discovery

import (
	"fmt"
	"time"
)

type obj = map[string]any

func ghJobs(slug string, jobs ...obj) obj {
	for _, j := range jobs {
		if j["absolute_url"] == nil {
			j["absolute_url"] = fmt.Sprintf("https://job-boards.greenhouse.io/%s/jobs/%v", slug, j["id"])
		}
		if j["updated_at"] == nil {
			j["updated_at"] = iso(epoch.Add(-48 * time.Hour))
		}
	}
	return obj{"jobs": jobs, "meta": obj{"total": len(jobs)}}
}

func ghItem(id int, title, location string) obj {
	return obj{"id": id, "title": title, "location": obj{"name": location}, "company_name": ""}
}

func leverPost(id, title, location string, created time.Time) obj {
	return obj{
		"id": id, "text": title, "hostedUrl": "https://jobs.lever.co/example-lever/" + id,
		"createdAt": created.UnixMilli(), "workplaceType": "onsite",
		"categories":       obj{"commitment": "Full-time", "department": "Engineering", "location": location},
		"descriptionPlain": "Build services for Example Lever.\nYou will need 2+ years of experience.",
	}
}

func ashbyItem(id, title, location string, published time.Time) obj {
	return obj{
		"id": id, "title": title, "department": "Engineering", "employmentType": "FullTime", "location": location,
		"publishedAt": iso(published), "isListed": true, "isRemote": false,
		"jobUrl":           "https://jobs.ashbyhq.com/example-ashby/" + id,
		"descriptionPlain": "Ship product for Example Ashby.",
		"compensation":     obj{"compensationTierSummary": "$120K - $150K"},
	}
}

func srPage(total int, offset int, count int, identifier string) obj {
	var content []obj
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("7439%08d", offset+i)
		content = append(content, obj{
			"id": id, "name": fmt.Sprintf("Software Engineer %d", offset+i), "releasedDate": iso(epoch.Add(-24 * time.Hour)),
			"company":          obj{"identifier": identifier, "name": "Example Sr Co"},
			"location":         obj{"city": "Exampleton", "region": "CA", "country": "us", "fullLocation": "Exampleton, CA, United States"},
			"typeOfEmployment": obj{"label": "Full-time"}, "department": obj{"label": "Engineering"},
		})
	}
	return obj{"offset": offset, "limit": 100, "totalFound": total, "content": content}
}

func workableItem(shortcode, title, city, state, country string, published string) obj {
	return obj{
		"title": title, "shortcode": shortcode, "employment_type": "Full-time", "telecommuting": false, "department": "Engineering",
		"published_on": published, "city": city, "state": state, "country": country,
		"description": "<p>Work on Example Workable products.</p>",
	}
}
