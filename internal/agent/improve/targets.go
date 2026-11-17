package improve

import (
	"context"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Bounds of a round's selection.
const (
	maxTargets = 3
	maxSuite   = 4
	lookback   = 7 * 24 * time.Hour
	// scanTurns bounds how many recent model turns a selection reads.
	scanTurns = 200
)

// candidate is a recent model turn with what the record says about it.
type candidate struct {
	turnID, sessionID string
	status, reason    string
	calls             int
	firstTool         string
	next              string // the user's next message in the conversation
	declined          bool
	signals           []string
}

// correctionRE matches a user's next message that says the answer was wrong
// or not what was asked. Chinese forms are escaped (the repository is
// English-only): bu dui, cuo le, bu shi, wo shuo de shi, chong xin, li jie cuo.
var correctionRE = lazyregexp.New(`(?i)^\s*(no\b|nope\b|wrong\b|incorrect\b|that'?s not\b|that is not\b|not what i\b|i (said|asked|meant)\b|you didn'?t\b|try again\b)|` +
	`(?i)\b(that'?s wrong|you misunderstood|i didn'?t ask)\b|` +
	"\u4e0d\u5bf9|\u9519\u4e86|^\\s*\u4e0d\u662f|\u6211\u8bf4\u7684\u662f|\u91cd\u65b0|\u7406\u89e3\u9519")

// minScore is the least score that makes a turn a target: an error or a
// correction alone, or the cap and a declined card together. The cap alone is
// a person asking for many things; a declined card alone is the guard working.
const minScore = 2

// score ranks a turn as a target; below minScore it is not one.
func (c *candidate) score() int {
	n := 0
	if c.status == "error" && c.reason != "cancelled" {
		c.signals = append(c.signals, "ended in an error ("+c.reason+")")
		n += 3
	}
	// The cap is reached by design when a person asks for more than twelve
	// things at once, so it weighs less than an error or a correction.
	if c.calls >= 12 {
		c.signals = append(c.signals, "reached the tool-call cap")
		n++
	}
	if c.next != "" && correctionRE.MatchString(c.next) {
		c.signals = append(c.signals, "the user's next message corrected it")
		n += 3
	}
	if c.declined {
		c.signals = append(c.signals, "a card it held was declined")
		n++
	}
	return n
}

// recent reads the recent model turns, newest first, with their signals
// computed.
func (s *Service) recent(ctx context.Context) ([]*candidate, error) {
	since := s.now().Add(-lookback).UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.Read().QueryContext(ctx, `
		SELECT t.id, t.session_id, t.status, t.reason,
			(SELECT count(*) FROM chat_messages m WHERE m.turn_id = t.id AND m.role = 'tool_call'),
			coalesce((SELECT m.tool_name FROM chat_messages m WHERE m.turn_id = t.id AND m.role = 'tool_call' ORDER BY m.id LIMIT 1), ''),
			coalesce((SELECT n.body FROM chat_messages n WHERE n.session_id = t.session_id AND n.role = 'user'
				AND n.id > (SELECT max(m.id) FROM chat_messages m WHERE m.turn_id = t.id) ORDER BY n.id LIMIT 1), ''),
			EXISTS (SELECT 1 FROM agent_confirmations c WHERE c.turn_id = t.id AND c.state = 'declined')
		FROM chat_turns t
		WHERE t.origin = 'model' AND t.status IN ('final', 'error') AND t.started_at >= ?
		ORDER BY t.started_at DESC LIMIT ?`, since, scanTurns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*candidate
	for rows.Next() {
		c := &candidate{}
		if err := rows.Scan(&c.turnID, &c.sessionID, &c.status, &c.reason, &c.calls, &c.firstTool, &c.next, &c.declined); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// selectTurns picks a round's targets and its regression suite. A target is a
// turn with a signal not yet examined under the version; the suite is clean
// turns that made a tool call, one per first tool, from other conversations
// than the targets.
func (s *Service) selectTurns(ctx context.Context, version int64) (targets, suite []*candidate, err error) {
	all, err := s.recent(ctx)
	if err != nil {
		return nil, nil, err
	}
	examined := map[string]bool{}
	rows, err := s.DB.Read().QueryContext(ctx, `SELECT turn_id FROM agent_improve_examined WHERE version = ?`, version)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		examined[id] = true
	}
	rows.Close()
	type scored struct {
		c *candidate
		n int
	}
	var ranked []scored
	for _, c := range all {
		if n := c.score(); n >= minScore && !examined[c.turnID] {
			ranked = append(ranked, scored{c, n})
		}
	}
	// Highest score first; recent first among equals (all is newest first).
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].n > ranked[j-1].n; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	sessionsUsed := map[string]bool{}
	for _, r := range ranked {
		if len(targets) == maxTargets {
			break
		}
		if sessionsUsed[r.c.sessionID] {
			continue
		}
		sessionsUsed[r.c.sessionID] = true
		targets = append(targets, r.c)
	}
	firsts := map[string]bool{}
	for _, c := range all {
		if len(suite) == maxSuite {
			break
		}
		if c.status != "final" || len(c.signals) > 0 || c.calls == 0 || firsts[c.firstTool] || sessionsUsed[c.sessionID] {
			continue
		}
		firsts[c.firstTool] = true
		suite = append(suite, c)
	}
	return targets, suite, nil
}

// signalText joins a target's signals for the prompts.
func (c *candidate) signalText() string { return strings.Join(c.signals, "; ") }
