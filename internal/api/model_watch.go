package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
)

const (
	modelWatchMeta  = "llm.model-watch"
	modelWatchRetry = time.Hour
)

// watchEpisode is one model that left the catalog and has not been fixed
// since. Since names the episode, so the same model breaking again after a fix
// is announced again and a repeated check is not.
type watchEpisode struct {
	Task      string `json:"task"`
	Model     string `json:"model"`
	Since     string `json:"since"`
	Announced bool   `json:"announced"`
}

type watchState struct {
	Checked  string         `json:"checked"` // local day of the last completed check
	Retry    string         `json:"retry"`   // no attempt before this instant
	Skipped  string         `json:"skipped"` // why the last attempt did nothing
	Episodes []watchEpisode `json:"episodes"`
}

// modelWatchTimer is the built-in daily watch of the routed OpenRouter models.
// It reads the public catalog, announces each model that left it once until
// the row is fixed, and never changes a routing row.
type modelWatchTimer struct {
	st     *store.Store
	router *llm.Router
	policy *notify.Policy
}

func (modelWatchTimer) Name() string { return "model-watch" }
func (modelWatchTimer) Describe() string {
	return "Once a day: reads OpenRouter's public model catalog and announces a routed model that has left it, once until the row is fixed; never changes a routing row. Needs an OpenRouter provider with a stored key"
}

func (t modelWatchTimer) Status(ctx context.Context, now time.Time) scheduler.TimerStatus {
	if err := t.router.WatchReady(ctx, false); err != nil {
		return scheduler.TimerStatus{Reason: err.Error()}
	}
	if st, err := t.load(ctx); err == nil && st.Skipped != "" && now.Before(parseWatchTime(st.Retry)) {
		return scheduler.TimerStatus{Reason: st.Skipped}
	}
	return scheduler.TimerStatus{Active: true}
}

func (t modelWatchTimer) Tick(ctx context.Context, now time.Time) error {
	st, err := t.load(ctx)
	if err != nil {
		return err
	}
	day := now.In(time.Local).Format("2006-01-02")
	if st.Checked == day || now.Before(parseWatchTime(st.Retry)) {
		return nil
	}
	retry := now.Add(modelWatchRetry).UTC().Format(time.RFC3339)
	if err := t.router.WatchReady(ctx, true); err != nil {
		st.Skipped, st.Retry = err.Error(), retry
		return t.save(ctx, st)
	}
	st.Skipped = ""
	gone, err := t.router.RoutedModelsMissing(ctx)
	if err != nil {
		st.Retry = retry
		return errors.Join(fmt.Errorf("the model catalog could not be read; retrying in an hour: %w", err), t.save(ctx, st))
	}
	st.Episodes = reconcileEpisodes(st.Episodes, gone, now)
	// The episodes are stored before they are announced, so a crash between the
	// two repeats the same announcement key instead of opening a new episode.
	if err := t.save(ctx, st); err != nil {
		return err
	}
	var errs []error
	for i := range st.Episodes {
		e := &st.Episodes[i]
		if e.Announced {
			continue
		}
		err := t.policy.Announce(ctx, notify.Event{Key: fmt.Sprintf("model-gone|%s|%s|%s", e.Task, e.Model, e.Since), Kind: notify.EventModelGone,
			Title: "A routed model left the catalog", Link: "/settings#settings-models",
			Body: fmt.Sprintf("The %s task routes to %s, which OpenRouter no longer lists. The row is unchanged; choose another model.", e.Task, e.Model)})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		e.Announced = true
	}
	st.Checked, st.Retry = day, ""
	if len(errs) > 0 {
		st.Checked, st.Retry = "", retry
	}
	return errors.Join(append(errs, t.save(ctx, st))...)
}

// reconcileEpisodes keeps the open episodes that are still gone, drops those
// that were fixed and opens one for each newly gone model.
func reconcileEpisodes(open []watchEpisode, gone []llm.GoneModel, now time.Time) []watchEpisode {
	var out []watchEpisode
	for _, g := range gone {
		i := slices.IndexFunc(open, func(e watchEpisode) bool { return e.Task == g.Task && e.Model == g.Model })
		if i >= 0 {
			out = append(out, open[i])
			continue
		}
		out = append(out, watchEpisode{Task: g.Task, Model: g.Model, Since: now.UTC().Format(time.RFC3339)})
	}
	return out
}

func parseWatchTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func (t modelWatchTimer) load(ctx context.Context) (watchState, error) {
	var raw string
	err := t.st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, modelWatchMeta).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return watchState{}, nil
	}
	if err != nil {
		return watchState{}, err
	}
	var st watchState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return watchState{}, nil // an unreadable record is a first check
	}
	return st, nil
}

func (t modelWatchTimer) save(ctx context.Context, st watchState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return t.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, modelWatchMeta, string(raw))
		return err
	})
}
