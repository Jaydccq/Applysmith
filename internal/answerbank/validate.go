package answerbank

import (
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
)

// Validate checks a bank before it is saved. It is stricter than the load: a
// problem the load would repair or drop, with or without a warning, refuses the
// save, so the file never holds what a load would change. It reuses the load's
// own repair and override rules, so the two cannot drift apart.
func Validate(b match.Bank) error {
	var issues []Issue
	add := func(id, format string, args ...any) {
		issues = append(issues, Issue{EntryID: id, Message: fmt.Sprintf(format, args...)})
	}
	if b.Version != 1 {
		add("", "version must be 1, got %d", b.Version)
	}
	seen := map[string]bool{}
	for _, e := range b.Entries {
		switch {
		case strings.TrimSpace(e.ID) == "":
			add("", "an entry has no id")
			continue
		case seen[e.ID]:
			add(e.ID, "duplicate id")
			continue
		}
		seen[e.ID] = true
		validateEntry(e, add)
	}
	validateOverrides(b, add)
	for _, emp := range b.Employers {
		if strings.TrimSpace(emp.Name) == "" {
			add("", "an employer has no name")
		}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func validateEntry(e match.Entry, add func(id, format string, args ...any)) {
	if !e.Kind.Valid() {
		add(e.ID, "unknown kind %s", quoted(string(e.Kind)))
		return
	}
	if e.Category != "" && !match.ValidCategory(e.Category) {
		add(e.ID, "unknown category %s; the allowed values are %s", quoted(e.Category), strings.Join(match.Categories, ", "))
	}
	repairEntry(&e, func(format string, args ...any) { add(e.ID, format, args...) })
	if e.BodyEmpty() && !e.Placeholder {
		add(e.ID, "the %s body is empty; give it an answer or mark the entry placeholder: true", e.Kind)
	}
}

func validateOverrides(b match.Bank, add func(id, format string, args ...any)) {
	hosts := make([]string, 0, len(b.Overrides))
	for host := range b.Overrides {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	for _, host := range hosts {
		name := b.HostName(host)
		if strings.TrimSpace(host) == "" {
			add("", "an override has no host")
			continue
		}
		list := b.Overrides[host]
		_, merged := resolveOverrides(b, list, func(o match.Override, format string, args ...any) {
			add(o.ID, "override on %s: %s", quoted(name), fmt.Sprintf(format, args...))
		})
		for _, o := range list {
			if o.ID == "" {
				add("", "an override on %s has no entry id", quoted(name))
			}
		}
		for id, e := range merged {
			if e.BodyEmpty() && !e.Placeholder {
				add(id, "override on %s leaves the %s body empty; the entry would need placeholder: true", quoted(name), e.Kind)
			}
		}
	}
}
