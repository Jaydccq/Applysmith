package agent

import (
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	allDays   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	dayGroups = map[string][]string{
		"daily": allDays, "everyday": allDays, "every day": allDays,
		"weekdays": {"mon", "tue", "wed", "thu", "fri"}, "weekday": {"mon", "tue", "wed", "thu", "fri"},
		"weekends": {"sat", "sun"}, "weekend": {"sat", "sun"},
	}
	dayLong = map[string]string{"sunday": "sun", "monday": "mon", "tuesday": "tue", "wednesday": "wed", "thursday": "thu", "friday": "fri", "saturday": "sat"}
	clockRE = lazyregexp.New(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
)

// parseDays turns what a person or a model writes for a cadence's days
// ("daily", "weekdays", "mon,wed,fri", "Monday") into three-letter weekdays.
func parseDays(in []string) ([]string, error) {
	var out []string
	for _, item := range in {
		for _, d := range strings.Split(item, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			switch {
			case d == "":
			case dayGroups[d] != nil:
				out = append(out, dayGroups[d]...)
			case dayLong[d] != "":
				out = append(out, dayLong[d])
			case len(d) == 3 && strings.Contains(strings.Join(allDays, " "), d):
				out = append(out, d)
			default:
				return nil, fmt.Errorf("%q is not a day; use daily, weekdays, weekends or days such as mon,wed,fri", d)
			}
		}
	}
	return out, nil
}

// parseClock turns "09:00", "9am" or "6:30 pm" into HH:MM.
func parseClock(s string) (string, error) {
	m := clockRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return "", fmt.Errorf("%q is not a time; use 09:00, 9am or 6:30 pm", s)
	}
	h, _ := strconv.Atoi(m[1])
	min := 0
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	switch m[3] {
	case "am", "pm":
		if h < 1 || h > 12 {
			return "", fmt.Errorf("%q is not a time", s)
		}
		h %= 12
		if m[3] == "pm" {
			h += 12
		}
	}
	if h > 23 || min > 59 {
		return "", fmt.Errorf("%q is not a time", s)
	}
	return fmt.Sprintf("%02d:%02d", h, min), nil
}
