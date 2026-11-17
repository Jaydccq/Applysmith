package learn

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrSystemWrite marks a system write that cannot be recorded.
var ErrSystemWrite = errors.New("invalid system write")

// writeKeyFile is the workspace secret that keys the digests.
const writeKeyFile = "system-writes.key"

// writeKey loads the workspace key once; a failure is not cached, so a later
// call retries.
func (s *Service) writeKey() ([]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.key == nil {
		k, err := s.bank.SecretKey(writeKeyFile)
		if err != nil {
			return nil, err
		}
		s.key = k
	}
	return s.key, nil
}

// writeHash is the digest a system write is kept as: HMAC-SHA256 over the run
// and the value (folded like sameAnswer folds). The control's selector is
// deliberately not part of it, so a fill that names another control, or none,
// still matches the system's write in the same run.
func (s *Service) writeHash(runID, value string) (string, error) {
	key, err := s.writeKey()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(runID + "\x00" + foldAnswer(value)))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// systemWriteGrace is how long after a run finishes the system may still
// record a write for it: a late echo from the page is the system's own, but a
// run closed for good takes no more.
const systemWriteGrace = 10 * time.Minute

// SystemWrite is a value the system writes into the control at Selector.
type SystemWrite struct{ Selector, Value string }

// RecordSystemWrites remembers, in one transaction, that the system itself
// wrote each value into its control during the run. A malformed write is
// skipped and reported with ErrSystemWrite after the others are kept. A manual fill in that run with the same
// value, whatever control it names, is the system's own write read back and is
// stored as not ground truth. Only a keyed digest is kept. The run must be one
// the server issued, a fill run or an application-chain run as RunIssued
// reads them, and not one that finished (succeeded, failed, cancelled or
// review_required; for the chain, any finish) more than systemWriteGrace ago.
// Manual fills are not limited this way: the user finishes a form after
// autofill ends.
func (s *Service) RecordSystemWrites(ctx context.Context, runID string, writes ...SystemWrite) error {
	if !runShaped(runID) {
		return ErrSystemWrite
	}
	var values []string
	for _, w := range writes {
		if w.Selector != "" && len([]rune(w.Selector)) <= maxSelectorRunes && w.Value != "" && len([]rune(w.Value)) <= maxValueRunes {
			values = append(values, w.Value)
		}
	}
	malformed := len(values) < len(writes)
	if len(values) == 0 {
		if malformed {
			return ErrSystemWrite
		}
		return nil
	}
	var finished bool
	var endedAt sql.NullString
	err := s.st.Read().QueryRowContext(ctx, `SELECT status IN ('succeeded', 'failed', 'cancelled', 'review_required'), COALESCE(finished_at, updated_at) FROM runs WHERE id = ?1
		UNION ALL SELECT finished_at IS NOT NULL, finished_at FROM apply_runs WHERE run_id = ?1 LIMIT 1`, runID).Scan(&finished, &endedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: the run was not issued by this server", ErrSystemWrite)
	case err != nil:
		return err
	}
	if finished {
		// Other packages stamp runs with or without milliseconds; RFC 3339
		// reads both. A stamp that cannot be read counts as long ago.
		ended, perr := time.Parse(time.RFC3339, endedAt.String)
		if perr != nil || s.now().Sub(ended) > systemWriteGrace {
			return fmt.Errorf("%w: the run finished more than %s ago", ErrSystemWrite, systemWriteGrace)
		}
	}
	hashes := make([]string, len(values))
	for i, v := range values {
		if hashes[i], err = s.writeHash(runID, v); err != nil {
			return err
		}
	}
	at := stamp(s.now())
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		for _, h := range hashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO system_writes (value_hash, recorded_at) VALUES (?, ?) ON CONFLICT (value_hash) DO NOTHING`, h, at); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && malformed {
		err = ErrSystemWrite
	}
	return err
}

// systemWrote reports whether the system recorded the manual fill's value in
// its run. A lookup that fails reads as a write: the fill is then kept out of
// the ground truth rather than let in.
func (s *Service) systemWrote(ctx context.Context, u Upload) bool {
	for _, v := range []string{u.After, u.OptionLabel} {
		if v == "" {
			continue
		}
		h, err := s.writeHash(u.RunID, v)
		if err != nil {
			return true
		}
		var n int
		if err := s.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM system_writes WHERE value_hash = ?`, h).Scan(&n); err != nil || n > 0 {
			return true
		}
	}
	return false
}

// RunIssued reports whether the server issued the run id.
func (s *Service) RunIssued(ctx context.Context, id string) bool {
	var n int
	err := s.st.Read().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs WHERE id = ?1) + (SELECT count(*) FROM apply_runs WHERE run_id = ?1)`, id).Scan(&n)
	return err == nil && n > 0
}
