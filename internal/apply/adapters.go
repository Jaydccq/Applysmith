package apply

import (
	"applysmith/internal/lazyregexp"
)

var (
	genericSuccess = lazyregexp.New(`(?i)\b(?:thank\s+you\s+for\s+(?:applying|your\s+application|submitting)|thanks\s+for\s+applying|(?:your\s+)?application\s+(?:has\s+been\s+|was\s+|is\s+)?(?:successfully\s+)?(?:submitted|received|sent)|we(?:'ve|\s+have)\s+received\s+your\s+application|application\s+complete)\b`)
	successByATS   = map[string]*lazyregexp.Regexp{
		ATSGreenhouse: lazyregexp.New(`(?i)thank you for applying|your application has been (?:submitted|received)`),
		ATSLever:      lazyregexp.New(`(?i)application submitted|thanks for applying|we(?:'ve| have) received your application`),
		// Ashby's sentence names the employer: "Your application to Juniper
		// Square was successfully submitted."
		ATSAshby:    lazyregexp.New(`(?i)application (?:to [^.\n]{1,80} )?(?:has been |was )?(?:successfully )?(?:submitted|received)|thank you for (?:applying|your application)`),
		ATSWorkday:  lazyregexp.New(`(?i)application submitted|thank you for applying|your application has been submitted`),
		ATSLinkedIn: lazyregexp.New(`(?i)application sent|your application was sent`),
	}
)

// SuccessText reports whether the page text carries the confirmation the
// system's adapter, or the generic list, names. Question lines never count.
func SuccessText(ats string, lines []string) string {
	re := successByATS[ats]
	for _, l := range statementLines(lines) {
		if re != nil && re.MatchString(l) {
			return l
		}
		if genericSuccess.MatchString(l) {
			return l
		}
	}
	return ""
}
