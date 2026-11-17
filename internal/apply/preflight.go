package apply

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProfileCheck is what the profile load found.
type ProfileCheck struct {
	// Missing names identity fields the profile lacks.
	Missing []string
	// ResumeErr is why the résumé file the profile names cannot be read, or "".
	ResumeErr string
}

// Environment is the machine the chain runs on. Each method is one preflight
// check; the attach hands the browser to the run so no second launch occurs.
type Environment interface {
	// Health asks GET /api/health once and returns the HTTP status; an error
	// means no HTTP answer arrived.
	Health(ctx context.Context) (status int, err error)
	// AnswerBank loads the bank and names the file it read.
	AnswerBank(ctx context.Context) (file string, entries int, err error)
	Profile(ctx context.Context) (ProfileCheck, error)
	Attach(ctx context.Context) (Window, error)
}

// Check is one preflight check's verdict.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// Verdict is the environment verdict of one invocation. A list takes one and
// shares it across its rows.
type Verdict struct {
	Checks []Check
	Window Window
}

// Blocked reports whether any check failed.
func (v *Verdict) Blocked() bool {
	for _, c := range v.Checks {
		if !c.OK {
			return true
		}
	}
	return false
}

// Summary names every failed check with its remediation, in one line.
func (v *Verdict) Summary() string {
	var parts []string
	for _, c := range v.Checks {
		if !c.OK {
			p := c.Name + ": " + c.Detail
			if c.Remedy != "" {
				p += " (" + c.Remedy + ")"
			}
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

// PreflightTiming holds the health probe's clocks; a zero field takes the
// specified value.
type PreflightTiming struct {
	Every, Each, Total time.Duration
	Sleep              func(context.Context, time.Duration) error
}

func (t PreflightTiming) withDefaults() PreflightTiming {
	if t.Every == 0 {
		t.Every = 2 * time.Second
	}
	if t.Each == 0 {
		t.Each = 10 * time.Second
	}
	if t.Total == 0 {
		t.Total = 60 * time.Second
	}
	if t.Sleep == nil {
		t.Sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return t
}

// Preflight takes the environment verdict. Every check runs even after one
// fails, so the summary names all of them. The health probe repeats every
// 2 seconds while no HTTP answer arrives, each probe bounded at 10 seconds,
// for at most 60 seconds; any HTTP status is final.
func Preflight(ctx context.Context, env Environment, timing PreflightTiming) *Verdict {
	t := timing.withDefaults()
	v := &Verdict{}
	v.Checks = append(v.Checks, healthCheck(ctx, env, t))

	file, n, err := env.AnswerBank(ctx)
	switch {
	case err != nil:
		v.Checks = append(v.Checks, Check{Name: "answer bank", Detail: fmt.Sprintf("cannot load %s: %v", orWord(file, "the bank"), err), Remedy: "repair or restore the answer bank file"})
	case n < 1:
		v.Checks = append(v.Checks, Check{Name: "answer bank", Detail: fmt.Sprintf("%s holds no entries", orWord(file, "the bank")), Remedy: "seed the bank from the Answer Bank page"})
	default:
		v.Checks = append(v.Checks, Check{Name: "answer bank", OK: true, Detail: fmt.Sprintf("%d entries from %s", n, file)})
	}

	pc, err := env.Profile(ctx)
	switch {
	case err != nil:
		v.Checks = append(v.Checks, Check{Name: "profile", Detail: "the profile cannot be loaded: " + err.Error(), Remedy: "fix profile.yml"})
	case len(pc.Missing) > 0:
		v.Checks = append(v.Checks, Check{Name: "profile", Detail: "missing " + strings.Join(pc.Missing, ", "), Remedy: "complete the profile page"})
	case pc.ResumeErr != "":
		v.Checks = append(v.Checks, Check{Name: "profile", Detail: "the résumé file is not readable: " + pc.ResumeErr, Remedy: "restore the résumé file the profile names"})
	default:
		v.Checks = append(v.Checks, Check{Name: "profile", OK: true, Detail: "identity fields and résumé file read"})
	}

	w, err := env.Attach(ctx)
	if err != nil {
		v.Checks = append(v.Checks, Check{Name: "apply window", Detail: err.Error(), Remedy: "start the app so the managed browser can open its apply window"})
	} else {
		v.Window = w
		v.Checks = append(v.Checks, Check{Name: "apply window", OK: true, Detail: "attached"})
	}
	return v
}

func orWord(file, word string) string {
	if file != "" {
		return file
	}
	return word
}

func healthCheck(ctx context.Context, env Environment, t PreflightTiming) Check {
	remedy := "start the server (applysmith serve) and retry"
	var last error
	attempts := int(t.Total/t.Every) + 1
	// One deadline bounds the whole probe, so probes that each run to their
	// own limit cannot stretch it past Total.
	ctx, stop := context.WithTimeout(ctx, t.Total)
	defer stop()
	for i := 0; ; i++ {
		pc, cancel := context.WithTimeout(ctx, t.Each)
		status, err := env.Health(pc)
		cancel()
		if err == nil {
			if status >= 200 && status < 300 {
				return Check{Name: "server", OK: true, Detail: fmt.Sprintf("GET /api/health answered %d", status)}
			}
			return Check{Name: "server", Detail: fmt.Sprintf("GET /api/health answered %d", status), Remedy: remedy}
		}
		last = err
		if ctx.Err() != nil || i+1 >= attempts {
			break
		}
		if t.Sleep(ctx, t.Every) != nil {
			break
		}
	}
	return Check{Name: "server", Detail: fmt.Sprintf("no HTTP answer within %s: %v", t.Total, last), Remedy: remedy}
}
