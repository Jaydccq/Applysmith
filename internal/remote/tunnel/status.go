// Package tunnel supervises the cloudflared child that publishes the loopback
// remote listener over HTTPS. The product never downloads, installs or updates
// the executable: it uses the one the user installed.
package tunnel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is the supervisor's state, as written to the status file.
type State string

const (
	StateOff      State = "off"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateError    State = "error"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)

// Reasons an error or failed state carries in Status.Reason.
const (
	ReasonNotFound   = "cloudflared_not_found"
	ReasonUnusable   = "cloudflared_unusable"
	ReasonNeedsURL   = "public_url_required"
	ReasonNeedsToken = "named_tunnel_required"
	ReasonExited     = "exited"
	ReasonStartFail  = "start_failed"
	ReasonRetries    = "retries_exhausted"
)

// Status is the tunnel document. It holds no secret.
type Status struct {
	Mode                State      `json:"mode"`
	PublicURL           string     `json:"publicUrl"`
	PID                 int        `json:"pid"`
	Version             string     `json:"version"`
	StartedAt           *time.Time `json:"startedAt"`
	LastRestartAt       *time.Time `json:"lastRestartAt"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	Reason              string     `json:"reason,omitempty"`
	Error               string     `json:"error"`
	// Stale is set by ReadFile, never written: the file claims running but has
	// not been touched for StaleAfter.
	Stale bool `json:"stale,omitempty"`
}

const (
	heartbeatEvery = 10 * time.Second
	// StaleAfter is how old the file may be while it claims the tunnel runs.
	StaleAfter = 30 * time.Second
)

// WriteFile replaces path with the status document: a temporary file in the
// same directory, then a rename, so a reader never sees half a document.
func WriteFile(path string, st Status) error {
	st.Stale = false
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tunnel-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// ReadFile reads the status document. A missing or unreadable file is the
// off state. A file that says running but is older than StaleAfter is marked
// stale.
func ReadFile(path string, now time.Time) Status {
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{Mode: StateOff}
	}
	var st Status
	if json.Unmarshal(data, &st) != nil || st.Mode == "" {
		return Status{Mode: StateOff}
	}
	if info, err := os.Stat(path); err == nil && st.Mode == StateRunning && now.Sub(info.ModTime()) > StaleAfter {
		st.Stale = true
	}
	return st
}
