package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Event names of the browser event log. The set is the audited list of the
// browser-lanes spec; status output and the log file carry these names.
const (
	EventBrowserLaunched     = "browser_launched"
	EventBrowserAdopted      = "browser_adopted"
	EventBrowserExited       = "browser_exited"
	EventBrowserDisconnected = "browser_disconnected"
	EventRecoveryAttempted   = "recovery_attempted"
	EventTabOpened           = "tab_opened"
	EventTabClosed           = "tab_closed"
	EventTabCrashed          = "tab_crashed"
	EventTabUnresponsive     = "tab_unresponsive"
	EventTabResponsive       = "tab_responsive"
	EventPopupClosed         = "popup_closed"
	EventNavigationBlocked   = "navigation_blocked"
	EventDialogHandled       = "dialog_handled"
	EventDownloadSaved       = "download_saved"
	EventDownloadCancelled   = "download_cancelled"
	EventPermissionDenied    = "permission_denied"
	EventCookieSweep         = "cookie_sweep"
	EventSessionImport       = "session_import"
	EventLoadFailed          = "load_failed"
	EventFocusEmulationFail  = "focus_emulation_failed"
	EventRequestRefused      = "request_refused"
	EventIdleStop            = "idle_stop"
)

const (
	// eventLogSize is how many events the instance keeps for the status view.
	eventLogSize = 300
	// eventFieldMax cuts every string field of an event.
	eventFieldMax = 400
	// eventLogRotateAt is the size past which the log file moves aside.
	eventLogRotateAt = 5 << 20
	// eventLogPrevious is the name suffix of the one previous file.
	eventLogPrevious = ".1"
)

// Event is one entry of the browser event log. Detail is the reason or code;
// when it is a URL it holds origin and path only.
type Event struct {
	Time   time.Time
	Kind   string
	Lane   Lane
	Tab    string
	Detail string
}

// eventLine is the JSON shape of one log line.
type eventLine struct {
	At     string `json:"at"`
	Event  string `json:"event"`
	Lane   string `json:"lane,omitempty"`
	Tab    string `json:"tab,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// clip cuts s to the field limit on a rune boundary.
func clip(s string) string {
	if len(s) <= eventFieldMax {
		return s
	}
	s = s[:eventFieldMax]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// scrub reduces a detail that is an address to origin and path and cuts it.
// Anything else (a reason code, a dialog type and message) is only cut.
func scrub(detail string) string {
	if i := strings.Index(detail, "://"); i > 0 && i <= 12 && !strings.ContainsAny(detail[:i], " \t\n") && !strings.ContainsAny(detail, " \t\n") {
		return clip(reduceURL(detail))
	}
	return clip(detail)
}

// eventFile appends events to a JSON-lines file and moves it aside once it
// passes the size limit. Recording never fails into the caller: an unwritable
// file loses the line, the in-memory log keeps it.
type eventFile struct {
	path string

	mu   sync.Mutex
	size int64
	open bool
}

func newEventFile(path string) *eventFile {
	if path == "" {
		return nil
	}
	return &eventFile{path: path}
}

func (f *eventFile) write(e Event) {
	if f == nil {
		return
	}
	line, err := json.Marshal(eventLine{At: e.Time.UTC().Format(time.RFC3339Nano), Event: e.Kind, Lane: string(e.Lane), Tab: clip(e.Tab), Reason: e.Detail})
	if err != nil {
		return
	}
	line = append(line, '\n')
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			return
		}
		if st, err := os.Stat(f.path); err == nil {
			f.size = st.Size()
		}
		f.open = true
	}
	if f.size > eventLogRotateAt {
		if err := os.Rename(f.path, f.path+eventLogPrevious); err == nil {
			f.size = 0
		}
	}
	h, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	n, _ := h.Write(line)
	_ = h.Close()
	f.size += int64(n)
}

// logEvent records an event that belongs to no tab.
func (i *Instance) logEvent(kind string, lane Lane, detail string) {
	i.record(Event{Kind: kind, Lane: lane, Detail: detail})
}

// logTab records an event of one tab.
func (i *Instance) logTab(kind string, t *tab, detail string) {
	i.record(Event{Kind: kind, Lane: t.lane, Tab: string(t.id), Detail: detail})
}

// record keeps the event in memory and appends it to the log file. It never
// returns an error and never waits on anything but the file.
func (i *Instance) record(e Event) {
	e.Time = time.Now()
	e.Detail = scrub(e.Detail)
	i.mu.Lock()
	i.events = append(i.events, e)
	if len(i.events) > eventLogSize {
		i.events = i.events[len(i.events)-eventLogSize:]
	}
	file := i.eventFile
	i.mu.Unlock()
	file.write(e)
}

// Events returns a copy of the recorded events, oldest first.
func (i *Instance) Events() []Event {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Event(nil), i.events...)
}

// blockedURL reduces a refused address for the log: origin and path for a
// hierarchical address, the bare scheme for an opaque one such as javascript:
// or data:, whose remainder is code or content.
func blockedURL(raw string) string {
	if r := reduceURL(raw); r != "" {
		return r
	}
	return urlScheme(raw)
}

// Note records an event of the sign-in sync or another caller that holds the
// instance but not a tab.
func (i *Instance) Note(kind, detail string) { i.logEvent(kind, "", detail) }
