package match

import (
	"applysmith/internal/lazyregexp"
)

// thanksOptionRE is an option that only thanks the applicant.
var thanksOptionRE = lazyregexp.New(`(?i)^\W*(?:thank\s+you|thanks)\W*$`)

// thanksOnlyChoice answers a required choice whose one real option thanks the
// applicant for following an instruction (Lyft: "Please enter your relevant
// employment and military service above using the + Add Another Employment
// link." and "Thank you"): the option states and agrees to nothing, so it is
// chosen. An optional one is left.
func thanksOnlyChoice(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if !s.Q.IsRequired() || len(opts) != 1 || !thanksOptionRE.MatchString(opts[0].Label) {
		return Draft{}, false
	}
	return chosenOption(s, opts[0], "the only option thanks the applicant: it states nothing"), true
}
