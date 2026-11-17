// Clean-room: docs/provenance.md#CR-AGENT-LOOP

package pirpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// NudgeText is the steering message sent when a turn repeats itself.
const NudgeText = "You are repeating the same step. Change your approach or ask the user what to do."

// Call is one tool call of a step.
type Call struct {
	Name string
	Args json.RawMessage
}

// Step is one model response as the system recorded it: the reply text and
// the ordered tool calls.
type Step struct {
	Text  string
	Calls []Call
}

// Verdict is the repetition guard's answer for a step.
type Verdict int

const (
	Continue Verdict = iota
	// Nudge asks the caller to steer the model once.
	Nudge
	// Stuck asks the caller to end the turn.
	Stuck
)

// RepetitionGuard counts consecutive identical steps: the third identical
// step earns one nudge and the next identical step ends the turn. A step that
// differs resets the count, so a model that changes course after the nudge is
// not penalised. Identity ignores the reserved status phrase argument and
// object key order; it does not look at call ids.
type RepetitionGuard struct {
	last string
	run  int
}

// Observe records a step.
func (g *RepetitionGuard) Observe(s Step) Verdict {
	fp := s.fingerprint()
	if fp == g.last {
		g.run++
	} else {
		g.last, g.run = fp, 1
	}
	switch {
	case g.run == 3:
		return Nudge
	case g.run >= 4:
		return Stuck
	}
	return Continue
}

// CallKey identifies one call by its tool and its arguments, with the same
// identity as a step: the status phrase and key order do not count.
func CallKey(name string, args json.RawMessage) string {
	return Step{Calls: []Call{{Name: name, Args: args}}}.fingerprint()
}

func (s Step) fingerprint() string {
	h := sha256.New()
	h.Write([]byte(s.Text))
	for _, c := range s.Calls {
		h.Write([]byte{0})
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write(canonicalArgs(c.Args))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalArgs re-encodes an argument object with sorted keys, numbers kept
// as written and the reserved "step" key removed.
func canonicalArgs(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	if m, ok := v.(map[string]any); ok {
		delete(m, "step")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
