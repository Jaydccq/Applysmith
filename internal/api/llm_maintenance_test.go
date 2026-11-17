package api

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

// readyWithSeed opens the store, lets seed write rows and hands it to the
// server, whose startup pass must act on them.
func readyWithSeed(t *testing.T, h *harness, seed func(st *store.Store)) *store.Store {
	t.Helper()
	id, err := h.ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(h.ws.StateDir(), store.DBFile), store.Options{Now: h.clock.now, WorkspaceID: id})
	if err != nil {
		t.Fatal(err)
	}
	seed(st)
	if err := h.srv.Ready(st); err != nil {
		t.Fatal(err)
	}
	return st
}

func execAll(t *testing.T, st *store.Store, stmts ...string) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, s := range stmts {
			if _, err := tx.Exec(s); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// waitFor polls ok at the pace the request limit allows for up to five
// seconds; waitLong gives work that needs it thirty.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	pollUntil(t, what, 5*time.Second, ok)
}

func pollUntil(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	pause := newPollPause()
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		pause()
	}
}

func TestLLMMaintenance_StartupPassAppliesRetention(t *testing.T) {
	h := newUnreadyHarness(t)
	if _, err := h.ws.Init(); err != nil {
		t.Fatal(err)
	}
	row := func(id, at string) string {
		return `INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, billing)
			VALUES ('` + id + `', '` + at + `', 'narrative', 'p', 'fake', 'm', 1, 'primary', 'ok', 'free')`
	}
	st := readyWithSeed(t, h, func(st *store.Store) {
		execAll(t, st,
			row("old", "2025-01-01T00:00:00.000Z"), row("new", "2026-01-01T00:00:00.000Z"),
			`INSERT INTO llm_capture (ledger_id, captured_at, prompt, completion) VALUES (1, '2026-01-01T00:00:00.000Z', 'p', 'c')`,
			`INSERT INTO llm_capture (ledger_id, captured_at, prompt, completion) VALUES (2, '2026-01-02T02:00:00.000Z', 'p', 'c')`)
	})
	count := func(q string) int {
		var n int
		if err := st.Read().QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waitFor(t, "the retention sweep", func() bool {
		return count(`SELECT count(*) FROM llm_ledger`) == 1 && count(`SELECT count(*) FROM llm_capture`) == 1
	})
}

func TestLLMMaintenance_StartupPassDeliversTheCapAlertThroughTheNotificationPolicy(t *testing.T) {
	h := newUnreadyHarness(t)
	if _, err := h.ws.Init(); err != nil {
		t.Fatal(err)
	}
	st := readyWithSeed(t, h, func(st *store.Store) {
		execAll(t, st, `INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
			VALUES ('spend', '2026-01-02T01:00:00.000Z', 'narrative', 'p', 'openai-compatible', 'm', 1, 'primary', 'ok', 3.5, 'metered')`)
	})
	waitFor(t, "the cap alert to be delivered", func() bool {
		var n int
		_ = st.Read().QueryRow(`SELECT count(*) FROM llm_cap_alerts WHERE delivered = 1`).Scan(&n)
		return n == 1
	})
	// The alert is a normal announcement of the policy; with no app attached the
	// desktop channel lists it under Notices.
	waitFor(t, "the policy to deliver it", func() bool {
		var n int
		_ = st.Read().QueryRow(`SELECT count(*) FROM notify_outbox o JOIN notify_notices n ON n.handle = 'n-' || o.id
			WHERE o.kind = 'cost-cap' AND o.urgent = 0 AND o.state = 'settled'`).Scan(&n)
		return n == 1
	})
	var title, body string
	if err := st.Read().QueryRow(`SELECT title, body FROM notify_outbox WHERE kind = 'cost-cap'`).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "daily model spend cap") || strings.Contains(strings.ToLower(title+body), "token") {
		t.Fatalf("the alert text: %q %q", title, body)
	}
}
