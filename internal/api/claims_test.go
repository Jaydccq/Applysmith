package api

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/api/wire"
)

func (h *harness) claimPress(body wire.PressClaimRequest) (string, result) {
	h.t.Helper()
	r := h.post(claimsPath, body)
	var env wire.Success[wire.PressClaimGranted]
	if r.StatusCode == 200 {
		strictInto(h.t, r.body, &env)
	}
	return env.Data.ClaimID, r
}

func TestProductionClaimerGrantsOnceAndSettlesToTheLedgerAndTracker(t *testing.T) {
	h := newDomainHarness(t)
	id, r := h.claimPress(claimBody())
	if r.StatusCode != 200 || !strings.HasPrefix(id, "claim_") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// A second walk on the same posting is refused while the claim is live.
	if other, r := h.claimPress(claimBody()); other != "" || r.StatusCode != 409 || r.errReason() != wire.ReasonDuplicateSubmit || !strings.Contains(string(r.body), "right now") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	body := submission("e1", true)
	body["claimId"] = id
	got, rec := h.record(body)
	if rec.StatusCode != 200 || got.Result != wire.SubmissionSettled || got.LedgerID < 1 || got.Tracker != wire.TrackerCreated || h.ledgerRows() != 1 {
		t.Fatalf("%d %s", rec.StatusCode, rec.body)
	}
	again, _ := h.record(body)
	if again.LedgerID != got.LedgerID || h.ledgerRows() != 1 {
		t.Fatalf("a repeated report changed the ledger: %+v rows %d", again, h.ledgerRows())
	}
	if _, r := h.claimPress(claimBody()); r.StatusCode != 409 || r.errReason() != wire.ReasonDuplicateSubmit {
		t.Fatalf("a settled posting was granted again: %d %s", r.StatusCode, r.body)
	}
}

func TestProductionClaimerTwoWalksRace(t *testing.T) {
	h := newDomainHarness(t)
	const walks = 6
	var wg sync.WaitGroup
	codes := make([]int, walks)
	for i := range walks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, r := h.claimPress(claimBody())
			codes[i] = r.StatusCode
		}()
	}
	wg.Wait()
	granted, refused := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			granted++
		case 409:
			refused++
		}
	}
	if granted != 1 || refused != walks-1 || h.ledgerRows() != 1 {
		t.Fatalf("codes %v rows %d", codes, h.ledgerRows())
	}
}

func TestProductionClaimerAnUnreadableLedgerGrantsNothing(t *testing.T) {
	h := newDomainHarness(t)
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`ALTER TABLE submissions RENAME TO gone`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	id, r := h.claimPress(claimBody())
	if id != "" || r.StatusCode != 503 || r.errReason() != wire.ReasonClaimUnavailable || strings.Contains(string(r.body), "claimId") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestProcessDeathAfterTheClaimLeavesAnUncertainOutcomeAtRestart(t *testing.T) {
	h := newDomainHarness(t)
	id, r := h.claimPress(claimBody())
	if id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	h.restart()
	var state string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT state FROM submissions`).Scan(&state); err != nil || state != "may-have-submitted" {
		t.Fatalf("%q %v", state, err)
	}
	if _, r := h.claimPress(claimBody()); r.StatusCode != 409 || r.errReason() != wire.ReasonDuplicateSubmit || !strings.Contains(string(r.body), "may-have-submitted") {
		t.Fatalf("a retry was not blocked: %d %s", r.StatusCode, r.body)
	}
	body := submission("e1", true)
	body["claimId"] = id
	if r := h.post(submissionsPath, body); r.StatusCode != 422 || r.errReason() != wire.ReasonUnknownClaim {
		t.Fatalf("a swept claim settled: %d %s", r.StatusCode, r.body)
	}
	if h.ledgerRows() != 1 {
		t.Errorf("rows %d", h.ledgerRows())
	}
}
