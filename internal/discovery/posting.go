package discovery

import (
	"time"

	"applysmith/internal/identity"
)

// maxDescription caps stored description text, in characters.
const maxDescription = 20000

// Posting is the common shape every source normalizes into.
type Posting struct {
	Source         string // scan source that found it
	ATS            string
	Board          string // board slug
	Company        string
	Title          string
	Location       string
	Remote         bool
	URL            string // canonical posting URL
	JobID          string // source job id
	PostedAt       time.Time
	Compensation   string
	EmploymentType string
	Department     string
	Description    string

	Signals Signals
	Flags   []string
	// Aliases are other URLs of the same posting (a mirror's), kept for
	// de-duplication.
	Aliases []string
	Score   int
	Reasons []string
	// Novelty is the company's state when the posting was read; ranking uses it.
	novelty int
	// mirrorReason is why a mirror row stayed unresolved.
	mirrorReason string
	// fetchURL is the address a browser scan opens to read the posting: the
	// URL as the board gave it, before canonicalization.
	fetchURL string
}

// Identity is the posting seen by the shared identity functions.
func (p Posting) Identity() identity.Posting {
	// The vendor API names the board and job, so a posting whose URL is on the
	// employer's own site still gets the ATS-native key.
	key, _ := identity.NativeKey(p.ATS, p.Board, p.JobID)
	return identity.Posting{Key: key, URL: p.URL, Source: p.ATS, SourceJobID: p.JobID, Company: p.Company, Role: p.Title, Location: p.Location, Text: p.Description}
}

func (p Posting) key() string { return p.Identity().IdentityKey() }

func (p Posting) hasFlag(f string) bool {
	for _, x := range p.Flags {
		if x == f {
			return true
		}
	}
	return false
}

func (p *Posting) addFlag(f string) {
	if !p.hasFlag(f) {
		p.Flags = append(p.Flags, f)
	}
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (p *Posting) removeFlag(f string) {
	out := p.Flags[:0:0]
	for _, x := range p.Flags {
		if x != f {
			out = append(out, x)
		}
	}
	p.Flags = out
}
