package guard

import (
	"regexp"
	"strings"
)

const ruleCaptchaScope = "captcha-scope"

// The names below are written in pieces so that this file does not trip the
// rule it implements.
var (
	// solverNames name the one provider the product uses, and the setting that
	// switches it.
	solverNames = []string{"2" + "captcha", "two" + "captcha", "captcha" + "solver", "captcha" + "_solver"}

	// otherSolvers are captcha-solving services the product does not use.
	otherSolvers = []string{
		"anti" + "captcha", "anti-" + "captcha", "cap" + "solver", "cap" + "monster",
		"deathby" + "captcha", "death by " + "captcha", "ru" + "captcha", "az" + "captcha",
		"no" + "pecha", "solve" + "captcha", "bestcaptcha" + "solver", "captchas" + ".io", "cap" + ".guru",
	}

	// solverPaths may name the provider or its setting: the solver, the gate,
	// the secret registry, the policy file, the chain call sites, the setup
	// text and the Settings section that stores the key. A new call site is
	// added here in the same change that adds it.
	solverPaths = []string{
		"internal/captcha/", "internal/gates/", "internal/vault/", "internal/workspace/",
		"internal/apply/", "internal/onboarding/",
		"web/src/pages/settings/SignIn.tsx", "web/src/pages/settings/SignIn.test.tsx",
	}

	// solverImporters may import the solver package; the API's secrets file
	// reads the balance for the key test.
	solverImporters = []string{"internal/captcha/", "internal/apply/", "internal/api/secrets.go"}

	solverImportRE = regexp.MustCompile(`"[^"\n]*/internal/captcha(?:/[^"\n]*)?"`)
)

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// checkCaptchaScope keeps the captcha solver inside its approved call sites:
// no other solving service is named anywhere, and the provider, its setting
// and the solver package appear only on the approved paths. Prose under docs/,
// openspec/ and third_party/ and Markdown files may describe the provider.
func checkCaptchaScope(t *Tree) ([]Finding, error) {
	var out []Finding
	err := t.eachText(nil, func(f textFile) {
		lower := strings.ToLower(string(f.Data))
		for _, name := range otherSolvers {
			if strings.Contains(lower, name) {
				out = append(out, Finding{Rule: ruleCaptchaScope, Path: f.Path, Detail: "names a captcha-solving service the product does not use"})
				break
			}
		}
		if ext(f.Path) == ".go" && !underAny(f.Path, solverImporters) && solverImportRE.Match(f.Data) {
			out = append(out, Finding{Rule: ruleCaptchaScope, Path: f.Path, Detail: "imports the captcha solver but is not an approved call site; add the path to solverImporters on purpose"})
		}
		if ext(f.Path) == ".md" || underAny(f.Path, []string{"docs/", "openspec/", "third_party/"}) || underAny(f.Path, solverPaths) {
			return
		}
		for _, name := range solverNames {
			if strings.Contains(lower, name) {
				out = append(out, Finding{Rule: ruleCaptchaScope, Path: f.Path, Detail: "names the captcha provider or its setting outside the approved paths; add the path to solverPaths on purpose"})
				break
			}
		}
	})
	return out, err
}
