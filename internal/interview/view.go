package interview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// eventScanLimit bounds how many events one view reads, newest first.
const eventScanLimit = 5000

// Workspace returns the latest stages (the cancelled apart), feedback, mock turns and packs of a
// candidacy. A record that cannot be read is skipped and reported, so the
// valid ones still show. A candidacy with no records reads as empty.
func (s *Service) Workspace(ctx context.Context, candidacy int64) (Workspace, error) {
	if err := s.CandidacyExists(ctx, candidacy); err != nil {
		return Workspace{}, err
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT id, kind, stage_id, body FROM interview_events WHERE candidacy_id = ? ORDER BY id DESC LIMIT ?`,
		candidacy, eventScanLimit)
	if err != nil {
		return Workspace{}, err
	}
	defer rows.Close()
	w := Workspace{CandidacyID: candidacy, Stages: []Stage{}, Cancelled: []Stage{}, Feedback: []Feedback{}, MockTurns: []MockTurn{}, Packs: []PackInfo{}, NextActions: []Step{}}
	seen, packed := map[string]bool{}, map[string]bool{}
	unreadable := 0
	for rows.Next() {
		var id int64
		var kind, stage, body string
		if err := rows.Scan(&id, &kind, &stage, &body); err != nil {
			return Workspace{}, err
		}
		switch kind {
		case "stage":
			var st Stage
			if json.Unmarshal([]byte(body), &st) != nil || st.ID == "" {
				unreadable++
			} else if list := &w.Stages; !seen[st.ID] {
				seen[st.ID], st.Revision = true, id
				if st.Cancelled {
					list = &w.Cancelled
				}
				if len(*list) < ViewLimit {
					*list = append(*list, st)
				}
			}
		case "feedback":
			var f Feedback
			if json.Unmarshal([]byte(body), &f) != nil {
				unreadable++
			} else if len(w.Feedback) < ViewLimit {
				f.ID = id
				w.Feedback = append(w.Feedback, f)
			}
		case "mock-turn":
			var t MockTurn
			if json.Unmarshal([]byte(body), &t) != nil {
				unreadable++
			} else if len(w.MockTurns) < ViewLimit {
				t.ID = id
				w.MockTurns = append(w.MockTurns, t)
			}
		case "pack":
			var p PackInfo
			if json.Unmarshal([]byte(body), &p) != nil {
				unreadable++
				continue
			}
			// A stage counts as packed even when its pack is past the capped list.
			packed[p.Stage] = true
			if len(w.Packs) < ViewLimit {
				w.Packs = append(w.Packs, p)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Workspace{}, err
	}
	if unreadable > 0 {
		w.Degraded = true
		w.Repair = fmt.Sprintf("%d interview record(s) could not be read and are skipped; the others are shown", unreadable)
	}
	w.NextActions = nextWorkspaceActions(w, packed)
	return w, nil
}

func nextWorkspaceActions(w Workspace, packed map[string]bool) []Step {
	out := []Step{}
	if len(w.Stages) == 0 {
		return append(out, Step{Text: "Add an interview stage to start preparing.", Kind: StepStage})
	}
	for _, st := range w.Stages {
		if !packed[st.ID] {
			out = append(out, Step{Text: fmt.Sprintf("Generate a prep pack for stage %s.", st.ID), Kind: StepPack, Target: st.ID})
		}
		if !st.UserConfirmed {
			out = append(out, Step{Text: fmt.Sprintf("Confirm the details of stage %s.", st.ID), Kind: StepConfirm, Target: st.ID})
		}
	}
	return out
}

// Save stores a pack as the next revision of its stage and logs it. A pack
// that breaks the bounds is refused and nothing is stored.
func (s *Service) Save(ctx context.Context, p Pack) (Pack, error) {
	if err := Validate(p); err != nil {
		return Pack{}, err
	}
	p.CreatedAt = s.stamp()
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT max(revision) FROM interview_packs WHERE candidacy_id = ? AND stage_id = ?`, p.Candidacy, p.Stage).Scan(&last); err != nil {
			return err
		}
		p.Revision = int(last.Int64) + 1
		body, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO interview_packs (candidacy_id, stage_id, revision, body, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.Candidacy, p.Stage, p.Revision, string(body), p.CreatedAt); err != nil {
			return err
		}
		_, err = s.appendEvent(ctx, tx, p.Candidacy, "pack", p.Stage, PackInfo{Stage: p.Stage, Revision: p.Revision, Generation: p.Generation, CreatedAt: p.CreatedAt})
		return err
	})
	return p, err
}

// Pack returns one revision of a stage's pack; revision 0 is the latest.
func (s *Service) Pack(ctx context.Context, candidacy int64, stage string, revision int) (Pack, error) {
	q := `SELECT body FROM interview_packs WHERE candidacy_id = ? AND stage_id = ? `
	args := []any{candidacy, stage}
	if revision > 0 {
		q += `AND revision = ? `
		args = append(args, revision)
	}
	q += `ORDER BY id DESC LIMIT 1`
	var body string
	err := s.d.Store.Read().QueryRowContext(ctx, q, args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Pack{}, fmt.Errorf("%w: no prep pack for stage %q", ErrNotFound, stage)
	}
	if err != nil {
		return Pack{}, err
	}
	var p Pack
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return Pack{}, fmt.Errorf("interview: the stored pack cannot be read: %w", err)
	}
	return p, nil
}

// StoryUse is an application whose prep pack drew on a story.
type StoryUse struct {
	Candidacy     int64
	Company, Role string
}

// StoryUses maps each story id to the applications whose newest pack of a
// stage drew on it, newest first, each application once. A pack that cannot
// be read is left out here; the prep view of its application reports it.
func (s *Service) StoryUses(ctx context.Context) (map[string][]StoryUse, error) {
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT p.candidacy_id, t.company, t.role, p.body FROM interview_packs p JOIN tracker_entries t ON t.id = p.candidacy_id
		WHERE p.revision = (SELECT max(revision) FROM interview_packs l WHERE l.candidacy_id = p.candidacy_id AND l.stage_id = p.stage_id) ORDER BY p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]StoryUse{}
	for rows.Next() {
		var u StoryUse
		var body string
		if err := rows.Scan(&u.Candidacy, &u.Company, &u.Role, &body); err != nil {
			return nil, err
		}
		var p Pack
		if json.Unmarshal([]byte(body), &p) != nil {
			continue
		}
		for _, q := range p.Questions {
			for _, id := range q.StoryIDs {
				if !slices.ContainsFunc(out[id], func(x StoryUse) bool { return x.Candidacy == u.Candidacy }) {
					out[id] = append(out[id], u)
				}
			}
		}
	}
	return out, rows.Err()
}
