package gmail

import (
	"context"
	"errors"
	"time"

	"applysmith/internal/vault"
)

// Health is the offline read model behind GET /api/gmail/health. Building it
// makes no network call.
type Health struct {
	// Connected is true while an app password is stored.
	Connected bool `json:"connected"`
	// Login says how Gmail last answered the stored password: ok, refused, or
	// unknown when the keychain cannot be read. Empty while not connected.
	Login string `json:"login"`
	// LoginError is Gmail's answer to a refused sign-in.
	LoginError string `json:"loginError,omitempty"`
	// Sync is never-run, healthy, partial or degraded.
	Sync string `json:"sync"`
	// Running is true while a run is in progress; the previous run's failure
	// is then not the story.
	Running          bool       `json:"running"`
	CursorAgeSeconds *int64     `json:"cursorAgeSeconds"`
	FailedStage      string     `json:"failedStage,omitempty"`
	LatestRun        *RunRecord `json:"latestRun,omitempty"`
	Mailbox          string     `json:"mailbox,omitempty"`
}

// Login states.
const (
	LoginOK      = "ok"
	LoginRefused = loginRefused
	LoginUnknown = "unknown"
)

// Server health-document component states.
const (
	ComponentUnconfigured = "unconfigured"
	ComponentDegraded     = "degraded"
	ComponentOK           = "ok"
)

// Health derives the read model from the keychain, the stored facts and the
// latest runs.
func (s *Service) Health(ctx context.Context) (Health, error) {
	h := Health{Mailbox: s.Repo.Mailbox(ctx)}
	runs, err := s.Repo.Runs(ctx, MaxRuns)
	if err != nil {
		return h, err
	}
	var finished *RunRecord
	for _, r := range runs {
		if r.Status == "running" {
			h.Running = true
			continue
		}
		if finished == nil {
			finished = r
		}
	}
	if len(runs) > 0 {
		h.LatestRun = runs[0]
	}

	switch err := s.Account.connected(); {
	case errors.Is(err, vault.ErrNotFound):
	case err != nil:
		h.Connected, h.Login = true, LoginUnknown
	case s.Repo.stateOr(ctx, keyLoginStatus) == loginRefused:
		h.Connected, h.Login, h.LoginError = true, LoginRefused, s.Repo.stateOr(ctx, keyLoginError)
	default:
		h.Connected, h.Login = true, LoginOK
	}

	switch {
	case finished == nil:
		h.Sync = "never-run"
	default:
		failed := ""
		for _, st := range finished.Stages {
			if st.Status == StageFailed {
				failed = st.Stage
				break
			}
		}
		h.FailedStage = failed
		switch {
		case failed != "":
			h.Sync = "degraded"
		case finished.CoverageComplete && s.continuityIntact(ctx):
			h.Sync = "healthy"
		default:
			h.Sync = "partial"
		}
	}
	if _, updated, ok := s.Repo.Cursor(ctx); ok && !updated.IsZero() {
		age := max(int64(s.now().Sub(updated)/time.Second), 0)
		h.CursorAgeSeconds = &age
	}
	return h, nil
}

// continuityIntact is true when a complete run has finished since the last
// cursor reset.
func (s *Service) continuityIntact(ctx context.Context) bool {
	reset, ok := parseStamp(s.Repo.stateOr(ctx, keyCursorResetAt))
	if !ok {
		return true
	}
	_, updated, has := s.Repo.Cursor(ctx)
	return has && updated.After(reset)
}

// Component is the state of the `gmail` component of the server health
// document.
func (s *Service) Component(ctx context.Context) string {
	h, err := s.Health(ctx)
	switch {
	case err != nil:
		return ComponentDegraded
	case !h.Connected:
		return ComponentUnconfigured
	case h.Login == LoginRefused || h.Sync == "degraded":
		return ComponentDegraded
	}
	return ComponentOK
}
