package wire

// PortalCompany is one row of the company table of portals.yml, joined with
// what the registry knows about its board. Board is `ats:slug` when the entry
// is, or its careers address resolves to, a supported board. LastScannedAt and
// JobCount are null until a scan has read that board.
type PortalCompany struct {
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	ATS            string   `json:"ats"`
	Slug           string   `json:"slug"`
	CareersURL     string   `json:"careersUrl"`
	Board          string   `json:"board"`
	Status         string   `json:"status"`
	LastScannedAt  *Time    `json:"lastScannedAt" tstype:"Time | null"`
	JobCount       *SafeInt `json:"jobCount" tstype:"SafeInt | null"`
	OwnTitleFilter bool     `json:"ownTitleFilter"`
}

// PortalCompanyList is the company table.
type PortalCompanyList struct {
	Companies []PortalCompany `json:"companies"`
}

// PortalRegistry is the ATS registry's count by status and the time of the
// newest seed download. Enabled is the registry.enabled switch of portals.yml.
type PortalRegistry struct {
	Active       SafeInt `json:"active"`
	Dead         SafeInt `json:"dead"`
	Blocked      SafeInt `json:"blocked"`
	Total        SafeInt `json:"total"`
	LastSeededAt *Time   `json:"lastSeededAt" tstype:"Time | null"`
	Enabled      bool    `json:"enabled"`
}

// PortalDetectRequest is a company name or a careers address.
type PortalDetectRequest struct {
	Input string `json:"input"`
}

// PortalCandidate is a board that may be the company's. Live is true when the
// board answered with at least one posting; Reason says why it did not.
type PortalCandidate struct {
	ATS        string  `json:"ats"`
	Slug       string  `json:"slug"`
	Company    string  `json:"company"`
	Jobs       SafeInt `json:"jobs"`
	Live       bool    `json:"live"`
	Reason     string  `json:"reason"`
	InRegistry bool    `json:"inRegistry"`
	InPortals  bool    `json:"inPortals"`
}

// PortalDetection is what the add form previews before anything is saved. Kind
// is board (an address naming a supported board), careers_url (an address no
// supported board matches, read through careers-page expansion) or name (boards
// guessed from the name; the user picks the right one).
type PortalDetection struct {
	Input      string            `json:"input"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	CareersURL string            `json:"careersUrl"`
	Candidates []PortalCandidate `json:"candidates"`
	Note       string            `json:"note"`
}

// PortalTargetRoles is what "Use my target roles" would write into
// portals.yml: one line per change, and the edits that make them, quoted
// against the file's revision for PATCH /api/config/portals. No lines means
// there is nothing to write.
type PortalTargetRoles struct {
	Revision string     `json:"revision"`
	Lines    []string   `json:"lines"`
	Ops      []ConfigOp `json:"ops"`
}
