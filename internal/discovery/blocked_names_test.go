package discovery

import (
	"testing"

	"applysmith/internal/workspace"
)

func TestBlocked_AbbreviatedCompanyNamesStayDistinct(t *testing.T) {
	f := NewFilters(workspace.Portals{}, []string{NormalizeCompany("Dr. Pepper")})
	for company, want := range map[string]bool{"Dr. Pepper": true, "Dr. Martens": false, "Dr Pepper Inc.": true} {
		if got := f.CompanyBlocked(company); got != want {
			t.Errorf("CompanyBlocked(%q) = %v, want %v", company, got, want)
		}
	}
}
