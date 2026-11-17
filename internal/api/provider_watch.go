package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
)

const (
	providerWatchMeta = "llm.provider-watch"
	// providerWatchWindow is how far back the ledger is read: a provider no
	// task called for a day starts a new episode when it is refused again.
	providerWatchWindow = 24 * time.Hour
)

// refusedEpisode is one provider refusing every call since its last success.
// Since names the episode and its announcement, so the episode keeps its start
// while the ledger window slides past it, and a provider refused again after a
// success is announced again.
type refusedEpisode struct {
	Provider string `json:"provider"`
	Since    string `json:"since"`
}

// providerWatchTimer announces a model provider that is out of credit or
// holds a dead key, read from the ledger on each tick, once until a call to it
// succeeds again. It never changes a key or a routing row.
type providerWatchTimer struct {
	st     *store.Store
	router *llm.Router
	policy *notify.Policy
}

func (providerWatchTimer) Name() string { return "provider-watch" }
func (providerWatchTimer) Describe() string {
	return "On each tick: reads the model calls of the last day and announces a provider that refused at least three in a row as out of credit or with a dead key, once until a call to it succeeds; never changes a key or a routing row"
}

func (providerWatchTimer) Status(context.Context, time.Time) scheduler.TimerStatus {
	return scheduler.TimerStatus{Active: true}
}

func (t providerWatchTimer) Tick(ctx context.Context, now time.Time) error {
	refused, err := t.router.RefusedProviders(ctx, now.Add(-providerWatchWindow))
	if err != nil {
		return err
	}
	open, err := t.load(ctx)
	if err != nil {
		return err
	}
	var episodes []refusedEpisode
	for _, r := range refused {
		if i := slices.IndexFunc(open, func(e refusedEpisode) bool { return e.Provider == r.Provider }); i >= 0 {
			episodes = append(episodes, open[i])
			continue
		}
		episodes = append(episodes, refusedEpisode{Provider: r.Provider, Since: r.Since.UTC().Format(time.RFC3339)})
	}
	if !slices.Equal(episodes, open) {
		// Stored before they are announced, so a crash between the two repeats
		// the same announcement key instead of opening a new episode.
		if err := t.save(ctx, episodes); err != nil {
			return err
		}
	}
	// The policy ignores a key it announced before, so each open episode is
	// announced once however many ticks see it.
	var errs []error
	for i, e := range episodes {
		errs = append(errs, t.policy.Announce(ctx, refusedEvent(e, refused[i])))
	}
	return errors.Join(errs...)
}

// refusedEvent names the provider, why it refuses, since when and the tasks
// that called it; the reply text, which may name the key, is never shown.
func refusedEvent(e refusedEpisode, r llm.RefusedProvider) notify.Event {
	title, fix := r.Provider+" refused its key", "replace the key"
	if r.Why == llm.RestQuota {
		title, fix = r.Provider+" is out of credit", "add credit or raise the key's limit"
	}
	return notify.Event{Key: fmt.Sprintf("provider-refused|%s|%s", e.Provider, e.Since), Kind: notify.EventProviderRefused,
		Title: title, Link: "/settings#settings-models",
		Body: fmt.Sprintf("Every call to %s since %s was refused (%d calls; tasks: %s). They fall back to other models or wait; %s in Settings, Models.",
			r.Provider, parseWatchTime(e.Since).In(time.Local).Format("Jan 2 15:04"), r.Calls, strings.Join(r.Tasks, ", "), fix)}
}

func (t providerWatchTimer) load(ctx context.Context) ([]refusedEpisode, error) {
	var raw string
	err := t.st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, providerWatchMeta).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []refusedEpisode
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil // an unreadable record is a first check
	}
	return out, nil
}

func (t providerWatchTimer) save(ctx context.Context, episodes []refusedEpisode) error {
	raw, err := json.Marshal(episodes)
	if err != nil {
		return err
	}
	return t.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, providerWatchMeta, string(raw))
		return err
	})
}
