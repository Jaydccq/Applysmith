package notify

import (
	"fmt"
	"time"
)

// QuietHours is the profile's notifications.quiet_hours group as read at one
// decision. A group that is disabled, absent or malformed means no quiet hours,
// so a broken file never silences anything.
type QuietHours struct {
	Enabled  bool
	Start    string // 24-hour HH:MM
	End      string
	Timezone string // IANA name; blank means the machine's zone
}

// Active reports whether now falls inside the window. The start is inclusive
// and the end exclusive; a start after the end crosses midnight.
func (q QuietHours) Active(now time.Time) bool {
	if !q.Enabled {
		return false
	}
	start, ok1 := minutesOf(q.Start)
	end, ok2 := minutesOf(q.End)
	if !ok1 || !ok2 || start == end {
		return false
	}
	loc := time.Local
	if q.Timezone != "" {
		l, err := time.LoadLocation(q.Timezone)
		if err != nil {
			return false
		}
		loc = l
	}
	local := now.In(loc)
	at := local.Hour()*60 + local.Minute()
	if start < end {
		return at >= start && at < end
	}
	return at >= start || at < end
}

func minutesOf(hhmm string) (int, bool) {
	var h, m int
	var rest string
	n, _ := fmt.Sscanf(hhmm, "%2d:%2d%s", &h, &m, &rest)
	if n != 2 || len(hhmm) != 5 || hhmm[2] != ':' || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
