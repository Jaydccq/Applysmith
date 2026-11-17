package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"applysmith/internal/vault"
)

// ErrConfirmRequired refuses resync, reclassify and purge without the
// confirmation flag.
var ErrConfirmRequired = errors.New("this changes stored Gmail data; confirm it with --yes")

// Resync clears the cursor, records the reset time and bootstraps again. The
// confirmation and an audit row are mandatory.
func (s *Syncer) Resync(ctx context.Context, actor string, trig Trigger, confirm bool) (*RunRecord, error) {
	if !confirm {
		_ = s.Repo.Audit(ctx, actor, "gmail resync", "refused: confirmation missing")
		return nil, ErrConfirmRequired
	}
	if err := s.Repo.guardDemo(ctx); err != nil {
		return nil, err
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	// The cursor is cleared in the transaction that takes the run claim, so a
	// refused resync leaves the cursor of the run that holds it.
	rec, err := s.Repo.beginRun(ctx, trig, ModeBootstrap, s.Version, func(tx *sql.Tx) error {
		return setState(ctx, tx, map[string]string{
			keyCursor: "", keyCursorUpdated: "", keyResumeToken: "", keyBootstrapStart: "",
			keyCursorResetAt: stamp(s.Repo.now()),
		})
	})
	if err == nil {
		rec, err = s.execute(ctx, rec, "")
	}
	outcome := "failed"
	switch {
	case err != nil:
		outcome = "failed: " + err.Error()
	case rec.Status == "succeeded":
		outcome = "ok"
	default:
		outcome = "failed: " + rec.Error
	}
	_ = s.Repo.Audit(ctx, actor, "gmail resync", outcome)
	return rec, err
}

// Reclassify re-runs the classify and store stages over stored message bodies
// under the current classifier version. It makes no Gmail request. A message
// whose body has aged out keeps its decision.
func (s *Syncer) Reclassify(ctx context.Context, actor string, trig Trigger, confirm bool) (*RunRecord, error) {
	if !confirm {
		_ = s.Repo.Audit(ctx, actor, "gmail reclassify", "refused: confirmation missing")
		return nil, ErrConfirmRequired
	}
	if err := s.Repo.guardDemo(ctx); err != nil {
		return nil, err
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	rec, err := s.Repo.beginRun(ctx, trig, ModeReclassify, s.Version, nil)
	if err != nil {
		return nil, err
	}
	rec.stage(StageList).Status = StageSkipped
	rec.stage(StageFetch).Status = StageSkipped
	msgs, err := s.Repo.storedMessages(ctx)
	if err != nil {
		rec.fail(StageStore, err)
	} else {
		mailbox := s.Repo.Mailbox(ctx)
		for start := 0; start < len(msgs); start += s.batch() {
			chunk := msgs[start:min(start+s.batch(), len(msgs))]
			scored, cerr := s.classifyBatch(chunk, mailbox)
			if cerr != nil {
				rec.Counts.Failed++
				rec.fail(StageClassify, cerr)
				break
			}
			rec.Counts.Classified += len(scored)
			_, review, serr := s.Repo.storeBatch(ctx, rec.ID, s.Version, scored, nil, true)
			if serr != nil {
				rec.fail(StageStore, serr)
				break
			}
			rec.Counts.Stored += len(scored)
			rec.Counts.Review += review
		}
		rec.markOK(StageClassify)
		rec.markOK(StageStore)
		s.link(ctx, rec)
	}
	now := s.Repo.now().UTC()
	rec.EndedAt = &now
	rec.Status = "succeeded"
	if rec.FailedStage != "" {
		rec.Status = "failed"
	}
	rec.CoverageComplete = rec.FailedStage == ""
	// Written even when the caller gave up, as Syncer.finish writes it.
	if serr := s.Repo.saveRun(context.WithoutCancel(ctx), rec, nil); serr != nil {
		return rec, serr
	}
	outcome := "ok"
	if rec.Status != "succeeded" {
		outcome = "failed: " + rec.Error
	}
	_ = s.Repo.Audit(ctx, actor, "gmail reclassify", outcome)
	return rec, nil
}

// storedMessages rebuilds the messages whose body is still retained.
func (r *Repo) storedMessages(ctx context.Context) ([]*Message, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT e.message_id, e.thread_id, e.received_ms, e.from_header, e.to_header,
		e.cc_header, e.subject, e.snippet, e.labels, b.body_text, b.raw_text
		FROM gmail_evidence e JOIN gmail_bodies b ON b.message_id = e.message_id
		LEFT JOIN gmail_tombstones t ON t.message_id = e.message_id
		WHERE t.message_id IS NULL ORDER BY e.received_ms, e.message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		var m Message
		var labels string
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.InternalMS, &m.From, &m.To, &m.Cc, &m.Subject, &m.Snippet, &labels, &m.Text, &m.HTML); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(labels), &m.Labels)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// gmailTables lists every table of Gmail-derived data, children first.
var gmailTables = []string{"gmail_digest_feedback", "gmail_digests", "gmail_thread_texts", "gmail_alert_states", "gmail_signal_events", "gmail_confirmations", "gmail_outcomes", "gmail_link_rejections", "gmail_thread_done", "gmail_reminder_snoozes", "gmail_thread_links", "gmail_links", "gmail_bodies", "gmail_decisions", "gmail_tombstones", "gmail_evidence", "gmail_runs", "gmail_audit", "gmail_state"}

// Purge deletes all Gmail rows and the app password after an explicit
// confirmation. The audit row is written first and then removed with
// the rest, so a purge leaves no trace of the mailbox.
func Purge(ctx context.Context, repo *Repo, v *vault.Vault, confirm bool) error {
	if !confirm {
		return ErrConfirmRequired
	}
	err := repo.st.Write(ctx, func(tx *sql.Tx) error {
		for _, t := range gmailTables {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
				return fmt.Errorf("purge %s: %w", t, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := v.Delete(secretAppPassword); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return err
	}
	return nil
}
