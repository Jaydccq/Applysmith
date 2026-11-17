package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Resource leases a kind may declare. The set is fixed.
type Lease string

const (
	LeaseBrowser    Lease = "browser"
	LeaseEvaluation Lease = "llm:evaluation"
	LeaseTracker    Lease = "tracker:write"
	LeaseGmail      Lease = "gmail:read"
)

// Leases lists the fixed set.
var Leases = []Lease{LeaseBrowser, LeaseEvaluation, LeaseTracker, LeaseGmail}

// Drives says what a kind does, independently of the leases it declares. The
// registry self-check requires each lease exactly when the capability is used.
type Drives struct {
	ScanWindow bool // drives the scan window of the managed browser
	Evaluation bool // calls a model for evaluation
	// EvaluationReadsPages queues evaluations of postings by URL. Those read a
	// page in the scan window when no public ATS API serves it, so the kind
	// holds the browser lease although it never drives the window itself.
	EvaluationReadsPages bool
	TrackerWrite         bool // changes tracker rows
	GmailRead            bool // reads Gmail
}

// FieldType is the type of one input.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldInt    FieldType = "int"
	FieldBool   FieldType = "bool"
)

// Field is one accepted input key.
type Field struct {
	Name     string
	Type     FieldType
	Enum     []string // for a string
	Min, Max int      // for an int; Max 0 means no upper bound
	Doc      string
}

// Invocation is what a job receives.
type Invocation struct {
	Schedule   string
	ScheduleID int64
	RunID      string
	Manual     bool
	Attempt    int
	Inputs     map[string]any
	// Progress reports a line for the run's stream; it never blocks.
	Progress func(string)
}

// Job runs one occurrence. It returns a one-line summary of counts. It must
// end when ctx is cancelled and bound its own teardown: the leases are
// released when it returns.
type Job func(ctx context.Context, in Invocation) (summary string, err error)

// Kind is one entry of the registry.
type Kind struct {
	Name    string
	Doc     string
	Leases  []Lease
	Drives  Drives
	Network bool // needs the internet; also the only kind of job that is retried
	Fields  []Field
	// Check validates inputs beyond the field types, such as a value that must
	// exist in a catalog.
	Check func(inputs map[string]any) error
	// Artifact is the dashboard path that shows what the kind produces.
	Artifact string
	// SubmitCapable must be false: no scheduled job submits an application.
	SubmitCapable bool
	Run           Job
	// Then is work that runs right after a run of this kind succeeds.
	Then *FollowUp
}

// FollowUp names a kind that runs once, with its own leases and no inputs,
// right after a run of the declaring kind succeeds, when Due says that run
// left it work. An enabled schedule of the named kind replaces it: the user's
// own schedule decides when that work runs. A follow-up has no follow-up.
type FollowUp struct {
	Kind string
	Due  func(ctx context.Context) (bool, error)
}

// Registry is the fixed set of job kinds.
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]Kind
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{kinds: map[string]Kind{}} }

// Register adds a kind after checking it against the rules of the registry.
func (r *Registry) Register(k Kind) error {
	if err := checkKind(k); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.kinds[k.Name]; dup {
		return fmt.Errorf("scheduler: kind %q is registered twice", k.Name)
	}
	r.kinds[k.Name] = k
	return nil
}

func checkKind(k Kind) error {
	switch {
	case k.Name == "" || strings.ContainsAny(k.Name, " \t\n"):
		return fmt.Errorf("scheduler: kind name %q is not valid", k.Name)
	case k.SubmitCapable:
		return fmt.Errorf("scheduler: kind %q is submit-capable; no scheduled job may submit an application", k.Name)
	case strings.HasPrefix(k.Name, "apply.") || strings.HasPrefix(k.Name, "apply-"):
		return fmt.Errorf("scheduler: kind %q names the apply window, which is not schedulable", k.Name)
	case k.Run == nil:
		return fmt.Errorf("scheduler: kind %q has no job", k.Name)
	}
	for _, f := range k.Fields {
		switch strings.ToLower(f.Name) {
		case "submit", "url", "posting", "postingurl", "applyurl":
			return fmt.Errorf("scheduler: kind %q accepts an input %q that could reach an application", k.Name, f.Name)
		}
	}
	for _, l := range k.Leases {
		if !slices.Contains(Leases, l) {
			return fmt.Errorf("scheduler: kind %q declares unknown lease %q", k.Name, l)
		}
	}
	has := func(l Lease) bool { return slices.Contains(k.Leases, l) }
	for _, pair := range []struct {
		lease Lease
		does  bool
		what  string
	}{
		{LeaseBrowser, k.Drives.ScanWindow || k.Drives.EvaluationReadsPages, "drives the scan window or queues evaluations that read pages in it"},
		{LeaseEvaluation, k.Drives.Evaluation, "calls a model for evaluation"},
		{LeaseTracker, k.Drives.TrackerWrite, "changes tracker rows"},
		{LeaseGmail, k.Drives.GmailRead, "reads Gmail"},
	} {
		if has(pair.lease) != pair.does {
			return fmt.Errorf("scheduler: kind %q must declare lease %s exactly when it %s", k.Name, pair.lease, pair.what)
		}
	}
	return nil
}

// Get returns a kind by name.
func (r *Registry) Get(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	return k, ok
}

// Names lists the registered kinds in order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All returns the registered kinds in name order.
func (r *Registry) All() []Kind {
	names := r.Names()
	out := make([]Kind, len(names))
	for i, n := range names {
		out[i], _ = r.Get(n)
	}
	return out
}

// ValidateInputs checks inputs against a kind's schema. An unknown key is
// rejected by name, so a stored schedule whose option was removed fails with
// the option's name.
func (k Kind) ValidateInputs(in map[string]any) error {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		idx := slices.IndexFunc(k.Fields, func(f Field) bool { return f.Name == key })
		if idx < 0 {
			return fmt.Errorf("input %q is not accepted by %s; accepted inputs: %s", key, k.Name, k.fieldNames())
		}
		if err := k.Fields[idx].check(in[key]); err != nil {
			return err
		}
	}
	if k.Check != nil {
		return k.Check(in)
	}
	return nil
}

func (k Kind) fieldNames() string {
	if len(k.Fields) == 0 {
		return "none"
	}
	names := make([]string, len(k.Fields))
	for i, f := range k.Fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

func (f Field) check(v any) error {
	switch f.Type {
	case FieldString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("input %q must be a string", f.Name)
		}
		if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
			return fmt.Errorf("input %q must be one of %s", f.Name, strings.Join(f.Enum, ", "))
		}
	case FieldInt:
		n, ok := asInt(v)
		if !ok {
			return fmt.Errorf("input %q must be a whole number", f.Name)
		}
		if n < f.Min || (f.Max > 0 && n > f.Max) {
			return fmt.Errorf("input %q must be between %d and %d", f.Name, f.Min, f.Max)
		}
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("input %q must be true or false", f.Name)
		}
	}
	return nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// FailureClass says what a failed job needs.
type FailureClass int

const (
	// FailPermanent is not retried.
	FailPermanent FailureClass = iota
	// FailTransient is a network error, a provider rate limit or a temporary
	// upstream failure; it is retried.
	FailTransient
	// FailNeedsYou is a sign-in, checkpoint, challenge or authorization
	// failure; it is not retried because it needs the user.
	FailNeedsYou
)

// JobError is a failure a job classifies itself.
type JobError struct {
	Class  FailureClass
	Reason string // a short code such as needs-sign-in; empty for none
	Err    error
}

func (e *JobError) Error() string { return e.Err.Error() }
func (e *JobError) Unwrap() error { return e.Err }

// Transient marks err as retryable.
func Transient(err error) error { return &JobError{Class: FailTransient, Err: err} }

// NeedsYou marks err as a failure that waits for the user. reason defaults to
// needs-you; sign-in failures pass ReasonNeedsSignIn.
func NeedsYou(reason string, err error) error {
	if reason == "" {
		reason = ReasonNeedsYou
	}
	return &JobError{Class: FailNeedsYou, Reason: reason, Err: err}
}

// classify reads a job's failure. A job that did not classify its error is
// transient when the cause is a network or deadline error, permanent otherwise.
func classify(err error) (FailureClass, string) {
	var je *JobError
	if errors.As(err, &je) {
		return je.Class, je.Reason
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return FailTransient, ""
	}
	return FailPermanent, ""
}
