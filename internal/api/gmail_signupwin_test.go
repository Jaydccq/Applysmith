package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
)

// The server opens the Gmail stack with the sign-up windows of the apply run
// records, so the account mail a run's own sign-up causes is demoted instead
// of waiting in review as signup_window_unknown.
func TestAPI_Gmail_SignupWindowsComeFromTheApplyRunRecords(t *testing.T) {
	h, _ := gmailHarness(t)
	now := h.clock.now()
	started := now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	st := h.srv.store.Load()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, signins_json)
			VALUES ('run_w', ?, ?, 'cli', 'https://acme.wd5.myworkdayjobs.com/en-US/careers/job/x', 'k', ?)`,
			started, now.Add(-50*time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), `[{"host":"acme.wd5.myworkdayjobs.com","succeeded":"created-account"}]`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "We have received your application for the Backend Engineer position."
	m := &gmail.Message{
		ID: "m1", ThreadID: "t1", InternalMS: now.Add(-55 * time.Minute).UnixMilli(),
		From: "Example Corp <acme" + "@" + "otp.workday.com>", To: "alex@example.com", Subject: "Verify your email address to confirm your application to Example Corp",
		Snippet: text, Text: text, Plain: text,
	}
	v := h.domainOf().gmail.Syncer.Classify(m, "alex@example.com")
	if v.Kind != gmail.VerdictSignal || v.RuleID != classify.SelfTriggeredRule {
		t.Fatalf("verdict %+v: the run's own sign-up mail is demoted", v)
	}
}
