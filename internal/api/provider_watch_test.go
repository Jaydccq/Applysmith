package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// ledgerCall writes one attempt row the way the router does, at the harness
// clock's time.
func ledgerCall(t *testing.T, h *harness, provider, task, outcome string, status int) {
	t.Helper()
	h.clock.advance(time.Minute)
	var code any
	if status != 0 {
		code = status
	}
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt,
			candidate_source, outcome, http_status, billing) VALUES (?, ?, ?, ?, 'openai', 'm', 1, 'primary', ?, ?, 'metered')`,
			"c"+h.clock.now().Format("150405"), h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z"), task, provider, outcome, code)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// OpenRouter's key ran out of credit and every embedding failed for hours with
// nothing said: a provider that refuses three calls in a row is announced once,
// and again only after a call to it succeeded and it was refused anew.
func TestLLM_ProviderWatch_RefusedKeyIsAnnouncedOncePerEpisode(t *testing.T) {
	h := startDomainHarness(t, false)
	alerts := func() int { return countOutbox(t, h, `kind = 'provider-refused'`) }

	ledgerCall(t, h, "openrouter", "embedding", "quota-exhausted", 403)
	ledgerCall(t, h, "openrouter", "embedding", "quota-exhausted", 403)
	tickOnce(t, h)
	if alerts() != 0 {
		t.Fatal("two refusals were announced")
	}
	ledgerCall(t, h, "openrouter", "option-pick", "quota-exhausted", 403)
	tickOnce(t, h)
	waitFor(t, "the alert", func() bool { return alerts() == 1 })
	var text string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT title || ' | ' || body || ' | ' || link FROM notify_outbox WHERE kind = 'provider-refused'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openrouter is out of credit", "3 calls", "embedding, option-pick", "/settings#settings-models"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the alert lacks %q: %q", want, text)
		}
	}

	// Still refused, also once the day-long window has slid past the first
	// refusals: nothing more.
	ledgerCall(t, h, "openrouter", "embedding", "quota-exhausted", 403)
	tickOnce(t, h)
	tickOnce(t, h)
	h.clock.advance(25 * time.Hour)
	for range 3 {
		ledgerCall(t, h, "openrouter", "embedding", "quota-exhausted", 403)
	}
	tickOnce(t, h)
	if alerts() != 1 {
		t.Fatalf("%d alerts while the same episode lasts", alerts())
	}

	// A success ends the episode; refused again, it is a new one.
	ledgerCall(t, h, "openrouter", "embedding", "ok", 0)
	tickOnce(t, h)
	for range 3 {
		ledgerCall(t, h, "openrouter", "embedding", "quota-exhausted", 403)
	}
	tickOnce(t, h)
	waitFor(t, "the second alert", func() bool { return alerts() == 2 })
}
