package mcpserver

import "strconv"

// candidacy is a wire-shaped tracker row with fields the tools must drop.
func candidacy(id int, company, status string) map[string]any {
	return map[string]any{
		"id": id, "date": "2026-09-01", "company": company, "role": "Platform Engineer", "score": 4.2, "scoreKind": "final",
		"status": status, "appliedBy": "user", "appliedAt": "2026-09-03", "resumePdf": true, "coverLetter": true,
		"reportNumber": 12, "notes": "private note about Alex Example", "postingUrl": "https://jobs.example.com/p/" + strconv.Itoa(id),
		"location": "Remote", "source": "greenhouse", "identityKey": "key-" + company, "version": 3,
		"createdAt": "2026-09-01T10:00:00.000Z", "updatedAt": "2026-09-04T10:00:00.000Z", "internalFlag": "secret-extra",
	}
}

func candidacyPage(rows ...map[string]any) map[string]any {
	return map[string]any{"entries": rows, "page": 1, "pageSize": 25, "total": len(rows)}
}

func reportDetail(number int, content string) map[string]any {
	return map[string]any{
		"report": map[string]any{"number": number, "kind": "full", "company": "Example Corp", "role": "Platform Engineer",
			"postingUrl": "https://jobs.example.com/p/1", "score": 4.1, "scoreKind": "final", "recommendation": "apply",
			"legitimacy": "high", "verification": "browser pass 2026-09-01", "createdAt": "2026-09-01T10:00:00.000Z",
			"documentsEligible": true, "profileNote": "extra-field"},
		"header":  map[string]any{"URL": "https://jobs.example.com/p/1", "Legitimacy": "high"},
		"content": content,
	}
}
