package guard

import "errors"

const ruleGitleaks = "gitleaks"

// Rule is one named check over a Tree.
type Rule struct {
	Name  string
	Check func(*Tree) ([]Finding, error)
}

// SkipError reports a rule that could not run because an optional input is
// missing. The test harness prints it as a notice; release mode never returns
// it.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "SKIPPED: " + e.Reason }

// Skipped builds a SkipError.
func Skipped(reason string) error { return &SkipError{Reason: reason} }

// IsSkipped reports whether err is a SkipError.
func IsSkipped(err error) bool {
	var s *SkipError
	return errors.As(err, &s)
}

// Rules returns every rule in a stable order.
func Rules() []Rule {
	return []Rule{
		tokenRule(ruleTokens, envTokens),
		tokenRule(ruleRetired, envRetired),
		{ruleEmail, checkEmail},
		{rulePhone, checkPhone},
		{ruleHomePath, checkHomePath},
		{ruleSecret, checkSecrets},
		{ruleGitleaks, checkGitleaks},
		{ruleLanguage, checkLanguage},
		{ruleComment, checkComments},
		{ruleBudget, checkBudgets},
		{ruleFileLength, checkFileLength},
		{ruleGofmt, checkGofmt},
		{ruleBuildOutput, checkBuildOutput},
		{ruleInternalPath, checkInternalPaths},
		{ruleDeps, checkDependencyBudgets},
		{ruleRegexp, checkRegexpCompiles},
		{ruleLayers, checkLayers},
		{rulePure, checkPurity},
		{ruleProvenance, checkProvenance},
		{ruleSimilarity, checkSimilarity},
		{ruleDocSources, checkDocumentSources},
		{ruleNotice, checkNotice},
		{ruleNoticeDrift, checkNoticeDrift},
		{ruleCaptchaScope, checkCaptchaScope},
		{ruleLoopbackListener, checkLoopbackListeners},
	}
}
