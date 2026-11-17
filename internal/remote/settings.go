// Package remote is the state behind remote access: the paired devices, the
// pairing offers, the audit events and the settings read from policy.yml. The
// tunnel child and the listener belong to internal/remote/tunnel and the API.
package remote

import (
	"applysmith/internal/workspace"
)

// Mode is remote_access.mode.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeTunnel Mode = "tunnel"
)

// Settings are the remote_access keys the server acts on.
type Settings struct {
	Mode             Mode
	TunnelPort       int
	AllowScreenshots bool
	Binary           string
	PublicURL        string
}

// ReadSettings reads policy.yml. A missing file is the defaults; an unreadable
// or invalid one is the restrictive policy, whose mode is off.
func ReadSettings(ws *workspace.Workspace) Settings {
	pol := workspace.RestrictivePolicy()
	if cur, err := ws.Read(workspace.FilePolicy); err == nil {
		if cur.Revision == workspace.RevisionAbsent {
			pol = workspace.DefaultPolicy()
		} else {
			pol, _ = workspace.ParsePolicy(cur.Data)
		}
	}
	ra := pol.RemoteAccess
	mode := ModeOff
	if ra.Mode == string(ModeTunnel) {
		mode = ModeTunnel
	}
	return Settings{Mode: mode, TunnelPort: ra.TunnelPort, AllowScreenshots: ra.AllowScreenshots, Binary: ra.Tunnel.Binary, PublicURL: ra.Tunnel.PublicURL}
}
