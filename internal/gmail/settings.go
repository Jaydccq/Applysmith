package gmail

import "context"

// Switch keys in the state table. A switch is on unless its value is "off".
const (
	keyAlertsOff  = "alerts_off"
	keyDigestsOff = "scheduled_digests_off"
	keyAlertsSeed = "alerts_seeded"
)

// Switches are the two user switches of the unattended Gmail work.
type Switches struct {
	// Alerts raises instant alerts; states are recorded either way.
	Alerts bool
	// ScheduledDigests runs the 08:00 and 18:00 digests; a manual run is
	// unaffected.
	ScheduledDigests bool
}

// Switches reads the switches; both are on by default.
func (r *Repo) Switches(ctx context.Context) Switches {
	return Switches{Alerts: r.stateOr(ctx, keyAlertsOff) == "", ScheduledDigests: r.stateOr(ctx, keyDigestsOff) == ""}
}

// SetSwitches changes the switches that are not nil.
func (r *Repo) SetSwitches(ctx context.Context, alerts, scheduledDigests *bool) error {
	kv := map[string]string{}
	set := func(key string, on *bool) {
		if on == nil {
			return
		}
		kv[key] = ""
		if !*on {
			kv[key] = "off"
		}
	}
	set(keyAlertsOff, alerts)
	set(keyDigestsOff, scheduledDigests)
	return r.SetState(ctx, kv)
}
