package browser

import "strings"

// allowedEnvNames and allowedEnvPrefixes define what a Chrome process may see:
// path, home, user, locale, temporary-directory and display variables.
var allowedEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"LANG": true, "LANGUAGE": true,
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true, "XDG_RUNTIME_DIR": true,
	"SYSTEMROOT": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
	"__CF_USER_TEXT_ENCODING": true,
}

// secretSuffixes and secretPrefix mark names that never reach the browser,
// even when an allowlist rule would otherwise admit them.
var secretSuffixes = []string{"KEY", "TOKEN", "SECRET", "PASSWORD"}

const secretPrefix = "APPLYSMITH_SECRET_"

// browserEnv filters environ (KEY=VALUE entries) down to the allowlist.
//
// The result is never nil: a nil Env makes os/exec inherit the whole parent
// environment, secrets included.
func browserEnv(environ []string) []string {
	out := []string{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !envAllowed(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func envAllowed(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, secretPrefix) {
		return false
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(upper, s) {
			return false
		}
	}
	return allowedEnvNames[upper] || strings.HasPrefix(upper, "LC_")
}
