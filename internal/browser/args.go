package browser

import (
	"fmt"
	"strings"
)

// disabledFeatures keeps pages rendering while their window is covered or the
// screen is locked. Translate is disabled so a translation bar never changes
// page geometry. MacAppCodeSignClone is disabled because Chrome copies its
// whole app bundle into the per-user temporary folder at every launch and
// removes the copy only on a normal quit: every Chrome stopped by a signal
// leaves one behind (docs/architecture/browser.md, "Launch argument list").
const disabledFeatures = "MacWebContentsOcclusion,WebContentsOcclusion,CalculateNativeWinOcclusion,Translate,MacAppCodeSignClone"

// FlagReason pairs a launch argument with the reason it is present. The list
// is the audited launch contract; docs/architecture/browser.md prints it.
type FlagReason struct {
	Flag   string
	Reason string
}

// staticFlags are the arguments that do not depend on the profile or the port.
var staticFlags = []FlagReason{
	{"--no-startup-window", "no window opens at launch, so a scan run never shows an apply window and each lane creates its own window on demand"},
	{"--no-first-run", "skips the first-run experience so an unattended launch shows no welcome page"},
	{"--no-default-browser-check", "skips the default-browser prompt"},
	{"--disable-background-timer-throttling", "background tabs keep their timers running at full rate"},
	{"--disable-renderer-backgrounding", "renderers of background tabs keep normal priority"},
	{"--disable-backgrounding-occluded-windows", "a covered window is not treated as hidden"},
	{"--disable-features=" + disabledFeatures, "disables window-occlusion detection so a covered or locked-screen window keeps rendering; disables the translate bar; disables the app-bundle copy Chrome makes at launch, which a signal stop leaves on disk"},
	{"--password-store=basic", "keeps Chrome off the macOS Keychain for an unattended first launch"},
	{"--use-mock-keychain", "pairs with --password-store=basic so no Keychain prompt can appear"},
	{"--hide-crash-restore-bubble", "no restore prompt after an unclean exit"},
}

// forbiddenFlagPrefixes must never reach Chrome. The launch contract excludes
// anything that sets automation indicators, bypasses the endpoint checks or
// alters the identity Chrome presents.
var forbiddenFlagPrefixes = []string{
	"--enable-automation",
	"--headless",
	"--remote-debugging-pipe",
	"--remote-debugging-address",
	"--remote-allow-origins",
	"--load-extension",
	"--enable-unsafe-extension-debugging",
	"--proxy-server",
	"--proxy-pac-url",
	"--proxy-auto-detect",
	"--user-agent",
	"--lang",
	"--disable-blink-features",
	"--no-sandbox",
}

// launchArgs returns the full, explicit argument list for one launch.
// hostRules maps hostnames to loopback for tests; empty in production.
func launchArgs(profile string, port int, hostRules string) []string {
	args := []string{
		"--user-data-dir=" + profile,
		fmt.Sprintf("--remote-debugging-port=%d", port),
	}
	for _, f := range staticFlags {
		args = append(args, f.Flag)
	}
	if hostRules != "" {
		// Tests only: a name mapped to loopback may be one whose real domain is pinned to HTTPS, served here with a throwaway certificate.
		// Keep fixture runs independent of Chrome component downloads, which
		// otherwise share the browser download staging directory.
		args = append(args, "--host-resolver-rules="+hostRules, "--ignore-certificate-errors", "--disable-component-update")
	}
	return args
}

// forbiddenFlag returns the first forbidden flag found in args, or "".
func forbiddenFlag(args []string) string {
	for _, a := range args {
		for _, p := range forbiddenFlagPrefixes {
			if a == p || strings.HasPrefix(a, p+"=") {
				return p
			}
		}
		if a == "--remote-debugging-port=0" {
			return a
		}
	}
	return ""
}
