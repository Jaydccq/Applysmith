package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Posting is what the identity functions read from a posting, an inbox row, a
// report or a tracker row. Every field is optional.
type Posting struct {
	// Key is an identity key computed earlier (stored beside the URL). When
	// set it is used as is.
	Key         string
	URL         string
	Source      string
	SourceJobID string
	Company     string
	Role        string
	Location    string
	// Text is the posting text; it feeds the content hash of last resort.
	Text string
}

// IdentityKey returns the key in order of preference: the ATS-native key, the
// canonical URL, `{source}:{source job id}`, the company, role and place, and
// a hash of the text. It is empty when the posting carries nothing to key on.
func (p Posting) IdentityKey() string {
	if p.Key != "" {
		return p.Key
	}
	if k, err := URLKey(p.URL); err == nil {
		return k
	}
	if src, id := strings.ToLower(strings.TrimSpace(p.Source)), strings.TrimSpace(p.SourceJobID); src != "" && id != "" {
		return src + ":" + id
	}
	if k := p.nameKey(); k != "" {
		return k
	}
	if t := strings.Join(words(fold(p.Text)), " "); t != "" {
		sum := sha256.Sum256([]byte(t))
		return "content:" + hex.EncodeToString(sum[:16])
	}
	return ""
}

// nameKey is `{company}|{role}` with the place appended when the posting names
// one, so two places of one role are two postings. It is empty for a
// placeholder company or an empty role.
func (p Posting) nameKey() string {
	names := openingCompanyNames(p.Company)
	role := strictRole(p.Role)
	if len(names) == 0 || role == "" {
		return ""
	}
	k := names[0] + "|" + role
	if tokens := placeTokens(p.Location); len(tokens) > 0 {
		k += "|" + strings.Join(tokens, "-")
	}
	return k
}
