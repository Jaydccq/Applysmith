package builder

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

var (
	wantPermissions = []string{"activeTab", "alarms", "cookies", "scripting", "storage", "tabs", "webNavigation"}
	wantHosts       = []string{"http://*/*", "https://*/*"}
)

const wantCSP = "script-src 'self'; object-src 'self'"

type contentScript struct {
	Matches []string `json:"matches"`
	JS      []string `json:"js"`
	World   string   `json:"world"`
	Frames  bool     `json:"all_frames"`
	RunAt   string   `json:"run_at"`
}

type manifestDoc struct {
	ManifestVersion int                        `json:"manifest_version"`
	Version         string                     `json:"version"`
	VersionName     string                     `json:"version_name"`
	MinChrome       string                     `json:"minimum_chrome_version"`
	Permissions     []string                   `json:"permissions"`
	OptionalPerms   []string                   `json:"optional_permissions"`
	Hosts           []string                   `json:"host_permissions"`
	OptionalHosts   []string                   `json:"optional_host_permissions"`
	ContentScripts  []contentScript            `json:"content_scripts"`
	Commands        map[string]json.RawMessage `json:"commands"`
	CSP             map[string]string          `json:"content_security_policy"`
	WebAccessible   json.RawMessage            `json:"web_accessible_resources"`
	ExternallyConn  json.RawMessage            `json:"externally_connectable"`
	BackgroundRaw   struct {
		Worker string `json:"service_worker"`
		Type   string `json:"type"`
	} `json:"background"`
	Icons  map[string]string `json:"icons"`
	Action struct {
		Popup string            `json:"default_popup"`
		Icon  map[string]string `json:"default_icon"`
	} `json:"action"`
}

// CheckManifest holds a built manifest to the install-surface rules of the
// chrome-extension spec: exact permissions and host access, no outside access,
// one command, and the loader as the only content script.
func CheckManifest(raw []byte) error {
	var m manifestDoc
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	var bad []string
	if m.ManifestVersion != 3 {
		bad = append(bad, "manifest_version must be 3")
	}
	if v, err := strconv.Atoi(m.MinChrome); err != nil || v < 120 {
		bad = append(bad, "minimum_chrome_version must be 120 or later")
	}
	if !sameSet(m.Permissions, wantPermissions) {
		bad = append(bad, fmt.Sprintf("permissions are %v, want exactly %v", m.Permissions, wantPermissions))
	}
	if len(m.OptionalPerms) > 0 || len(m.OptionalHosts) > 0 {
		bad = append(bad, "optional permissions are not allowed")
	}
	if !sameSet(m.Hosts, wantHosts) {
		bad = append(bad, fmt.Sprintf("host_permissions are %v, want exactly %v", m.Hosts, wantHosts))
	}
	if len(m.ExternallyConn) > 0 {
		bad = append(bad, "externally_connectable is declared")
	}
	if len(m.WebAccessible) > 0 {
		bad = append(bad, "web_accessible_resources is declared")
	}
	if m.CSP["extension_pages"] != wantCSP {
		bad = append(bad, "extension_pages content security policy must be "+wantCSP)
	}
	if m.BackgroundRaw.Worker != "sw.js" || m.BackgroundRaw.Type != "module" {
		bad = append(bad, "background must be the module service worker sw.js")
	}
	if m.Action.Popup != "popup.html" {
		bad = append(bad, "action must open popup.html")
	}
	for _, size := range iconSizes {
		want := "icons/icon-" + size + ".png"
		if m.Icons[size] != want || m.Action.Icon[size] != want {
			bad = append(bad, "icons and action.default_icon must name "+want+" at size "+size)
		}
	}
	if len(m.ContentScripts) != 1 || !slices.Equal(m.ContentScripts[0].JS, []string{"loader.js"}) {
		bad = append(bad, "the only content script must be loader.js")
	} else if cs := m.ContentScripts[0]; strings.EqualFold(cs.World, "MAIN") || cs.Frames {
		bad = append(bad, "the loader must run in the isolated world of the top frame")
	}
	if _, ok := m.Commands["toggle-panel"]; !ok || len(m.Commands) != 1 {
		bad = append(bad, "commands must be exactly toggle-panel")
	} else if !strings.Contains(string(m.Commands["toggle-panel"]), `"Alt+Shift+C"`) {
		bad = append(bad, "toggle-panel must default to Alt+Shift+C")
	}
	if base, st, ok := strings.Cut(m.VersionName, "+"); !ok || len(st) != StampLen {
		bad = append(bad, "version_name must end in +<stamp>")
	} else if want, err := BuildVersion(base, st); err != nil || m.Version != want {
		bad = append(bad, "version must be the version_name's version followed by the stamp's build number")
	}
	if len(bad) > 0 {
		return fmt.Errorf("manifest: %s", strings.Join(bad, "; "))
	}
	return nil
}

func sameSet(got, want []string) bool {
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(g, w)
}
