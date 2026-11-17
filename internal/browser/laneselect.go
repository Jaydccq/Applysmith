package browser

import "strings"

// guardedDomains are the sites whose challenge or restricted-account cost is
// higher than the bytes saved: they always use the scan lane, and the scan-lane
// resource policy blocks nothing on them.
var guardedDomains = []string{"indeed.com", "ziprecruiter.com", "glassdoor.com", "google.com", "linkedin.com"}

// Guarded reports whether host is, or lies below, a guarded site.
func Guarded(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return withinAny(host, guardedDomains)
}

// LaneRequest is what a caller says about the lane it needs.
type LaneRequest struct {
	// Host is the main host of the source being scanned.
	Host string
	// Watch is the user's setting to see scans in the window.
	Watch bool
}

// LaneChoice is the lane a scan uses and how its tabs are opened.
type LaneChoice struct {
	Lane Lane
	Tab  TabOptions
}

// SelectScanLane picks the lane of a scan. A scan defaults to the scan lane.
// When the user asked to watch scans it runs in the apply window and loads
// everything. A guarded source always uses the scan lane.
func SelectScanLane(r LaneRequest) LaneChoice {
	if r.Watch && !Guarded(r.Host) {
		return LaneChoice{Lane: LaneApply, Tab: TabOptions{LoadEverything: true}}
	}
	return LaneChoice{Lane: LaneScan}
}
