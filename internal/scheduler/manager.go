package scheduler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"applysmith/internal/store"
)

// UpcomingCount is how many next occurrences a schedule view carries.
const UpcomingCount = 5

// Manager creates, edits and removes schedules. It writes SQLite only, so it
// works whether or not a server runs; the schedules take effect when a
// scheduler does.
type Manager struct {
	repo *Repo
	reg  *Registry
	now  func() time.Time
}

// NewManager returns a manager over the workspace database.
func NewManager(st *store.Store, reg *Registry, now func() time.Time) *Manager {
	return &Manager{repo: NewRepo(st, now), reg: reg, now: now}
}

// Repo exposes the schedule store.
func (m *Manager) Repo() *Repo { return m.repo }

// Registry returns the job kind registry.
func (m *Manager) Registry() *Registry { return m.reg }

// NewSchedule is a request to create a schedule.
type NewSchedule struct {
	Name    string
	Kind    string
	Inputs  map[string]any
	Preset  Preset
	Enabled *bool // nil means enabled
	Policy  string
	DTStart time.Time // zero means now
}

// Patch changes parts of a schedule; nil fields stay as they are.
type Patch struct {
	Name    *string
	Kind    *string
	Inputs  map[string]any
	Preset  *Preset
	Enabled *bool
	Policy  *string
}

// View is a schedule with what the list and the show command add.
type View struct {
	Schedule
	Next        []time.Time
	UnknownKind bool
	Problem     string // why the schedule cannot be evaluated, if it cannot
}

// validate checks a schedule as a whole.
func (m *Manager) validate(s *Schedule) error {
	s.Name = strings.TrimSpace(s.Name)
	if n := len([]rune(s.Name)); n < 1 || n > MaxNameLen {
		return invalid("name", "the name must be 1 to %d characters", MaxNameLen)
	}
	k, ok := m.reg.Get(s.Kind)
	if !ok {
		return invalid("kind", "unknown job kind %q; valid kinds: %s", s.Kind, strings.Join(m.reg.Names(), ", "))
	}
	if s.Inputs == nil {
		s.Inputs = map[string]any{}
	}
	if err := k.ValidateInputs(s.Inputs); err != nil {
		return invalid("inputs", "%s", err.Error())
	}
	switch s.Policy {
	case "":
		s.Policy = PolicySkip
	case PolicySkip, PolicyFireOnce, PolicyFireAll:
	default:
		return invalid("policy", "the missed-fire policy must be skip, fire-once or fire-all")
	}
	if err := s.Preset.Validate(s.DTStart, m.now()); err != nil {
		return invalid("preset", "%s", err.Error())
	}
	return nil
}

// Create validates and stores a schedule.
func (m *Manager) Create(ctx context.Context, n NewSchedule) (Schedule, error) {
	return m.create(ctx, n, "", "")
}

func (m *Manager) create(ctx context.Context, n NewSchedule, pack, key string) (Schedule, error) {
	s := Schedule{Name: n.Name, Kind: n.Kind, Inputs: n.Inputs, Preset: n.Preset, Enabled: n.Enabled == nil || *n.Enabled,
		Policy: n.Policy, DTStart: n.DTStart, PackID: pack, PackKey: key}
	if s.DTStart.IsZero() {
		s.DTStart = m.now()
	}
	s.DTStart = s.DTStart.UTC().Truncate(time.Millisecond)
	if err := m.validate(&s); err != nil {
		return Schedule{}, err
	}
	return m.repo.Create(ctx, s)
}

// Update applies a patch after validating the result as a whole. Changing what
// a pack-created schedule does removes its pack marker, so a later application
// of the pack leaves it alone; switching it on or off does not.
func (m *Manager) Update(ctx context.Context, id int64, p Patch) (Schedule, error) {
	return m.repo.Update(ctx, id, func(s *Schedule) error {
		before := *s
		before.Inputs = maps.Clone(s.Inputs)
		if p.Name != nil {
			s.Name = *p.Name
		}
		if p.Kind != nil {
			s.Kind = *p.Kind
		}
		if p.Inputs != nil {
			s.Inputs = p.Inputs
		}
		if p.Preset != nil {
			s.Preset = *p.Preset
		}
		if p.Enabled != nil {
			s.Enabled = *p.Enabled
		}
		if p.Policy != nil {
			s.Policy = *p.Policy
		}
		if err := m.validate(s); err != nil {
			return err
		}
		if s.Name != before.Name || s.Kind != before.Kind || s.Policy != before.Policy ||
			s.Preset.Kind != before.Preset.Kind || s.Preset.Time != before.Preset.Time || s.Preset.RRule != before.Preset.RRule ||
			s.Preset.Timezone != before.Preset.Timezone || !slices.Equal(s.Preset.Weekdays, before.Preset.Weekdays) ||
			!maps.EqualFunc(s.Inputs, before.Inputs, func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
			s.PackID, s.PackKey = "", ""
		}
		return nil
	})
}

// View describes one schedule with its next occurrences.
func (m *Manager) View(ctx context.Context, id int64) (View, error) {
	s, err := m.repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	return m.view(s), nil
}

func (m *Manager) view(s Schedule) View {
	v := View{Schedule: s}
	if _, ok := m.reg.Get(s.Kind); !ok {
		v.UnknownKind = true
		v.Problem = fmt.Sprintf("job kind %q is not registered", s.Kind)
	}
	rec, err := NewRecurrence(s.Preset, s.DTStart)
	if err != nil {
		v.Problem = err.Error()
		return v
	}
	if s.Enabled {
		now := m.now()
		v.Next = rec.Between(now, now.Add(10*366*24*time.Hour), UpcomingCount)
	}
	return v
}

// List describes every schedule.
func (m *Manager) List(ctx context.Context) ([]View, error) {
	all, err := m.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(all))
	for i, s := range all {
		out[i] = m.view(s)
	}
	return out, nil
}

// History returns a schedule's history, newest first.
func (m *Manager) History(ctx context.Context, id int64, limit int) ([]HistoryRow, error) {
	if _, err := m.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	return m.repo.History(ctx, id, limit)
}

// Delete removes a schedule. It fails with ErrRunning while the schedule has a
// job in flight; Scheduler.Remove can cancel that job first.
func (m *Manager) Delete(ctx context.Context, id int64) error {
	s, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if s.ActiveRunID != "" {
		return ErrRunning
	}
	return m.repo.Delete(ctx, id)
}

// Remove deletes a schedule. With cancel set, a running job is cancelled and
// awaited first; without it a running job fails the call with ErrRunning.
func (s *Scheduler) Remove(ctx context.Context, id int64, cancel bool) error {
	sc, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if sc.ActiveRunID == "" {
		return s.repo.Delete(ctx, id)
	}
	if !cancel {
		return ErrRunning
	}
	s.runMu.Lock()
	var done chan struct{}
	if j, ok := s.running[sc.ActiveRunID]; ok {
		done = j.done
	}
	for _, j := range s.queue {
		if j.runID == sc.ActiveRunID {
			done = j.done
		}
	}
	s.runMu.Unlock()
	s.Cancel(sc.ActiveRunID)
	if done != nil {
		select {
		case <-done:
		case <-time.After(stopWait):
			return fmt.Errorf("the job did not stop within %s", stopWait)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.repo.Delete(ctx, id)
}
