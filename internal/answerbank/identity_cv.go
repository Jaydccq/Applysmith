package answerbank

import (
	"applysmith/internal/answerbank/match"
	"applysmith/internal/cvparse"
)

// IdentityFromCV is what the identity step offers from cv.md, in the dashboard
// and the interactive setup alike: cvparse.Identity, and the country US when
// the CV writes a US state as the region and no country ("Seattle, WA").
func IdentityFromCV(md string) map[string]string {
	out := cvparse.Identity(md)
	if out["country"] == "" && out["region"] != "" && match.USRegion(out["region"]) {
		out["country"] = "US"
	}
	return out
}
