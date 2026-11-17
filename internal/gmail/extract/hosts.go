package extract

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

var sfHostRE = lazyregexp.New(`^career\d*\.(?:sapsf|successfactors)\.(?:com|eu)$`)

// SuccessFactorsHost reports a SuccessFactors career host: the account stage
// looks for an activation mail only on one.
func SuccessFactorsHost(host string) bool {
	return sfHostRE.MatchString(strings.ToLower(strings.TrimSpace(host)))
}
