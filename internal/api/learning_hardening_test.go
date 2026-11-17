package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

// fillWithholds sends a manual fill and fails when the server stores its sentinel.
func (h *harness) fillWithholds(label, sentinel string, extra map[string]any) {
	h.t.Helper()
	h.ensureLearnRun()
	if r := h.postEvents(learnEvent(label, "manual-fill", extra)); r.StatusCode != 200 || r.data()["accepted"] != float64(1) {
		h.t.Fatalf("%s: %d %s", label, r.StatusCode, r.body)
	}
	if h.dbHolds(sentinel) {
		h.t.Errorf("%s: the value %q reached the database", label, sentinel)
	}
}

func TestLearningRoute_OptionSetsAreWithheldWhateverTheControlKind(t *testing.T) {
	h := newDomainHarness(t)
	for _, c := range []struct {
		label string
		extra map[string]any
	}{
		{"kind-text", map[string]any{"question": "Choose one", "controlKind": "text", "options": []string{"Male", "Female", "Prefer not to say"}, "optionLabel": "Female ZQK-KIND", "after": "Female ZQK-KIND"}},
		{"single-radio", map[string]any{"question": "Please confirm", "controlKind": "radio-group", "options": []string{"Female ZQK-SINGLE"}, "optionLabel": "Female ZQK-SINGLE", "after": "Female ZQK-SINGLE"}},
		{"single-veteran", map[string]any{"question": "Please confirm", "controlKind": "radio-group", "options": []string{"I am a veteran ZQK-VET"}, "optionLabel": "I am a veteran ZQK-VET", "after": "I am a veteran ZQK-VET"}},
		{"polish", map[string]any{"question": "Choose one", "controlKind": "select", "options": []string{"Mężczyzna ZQK-PL", "Kobieta"}, "optionLabel": "Mężczyzna ZQK-PL", "after": "Mężczyzna ZQK-PL"}},
		{"swedish", map[string]any{"question": "Choose one", "controlKind": "text", "options": []string{"Man ZQK-SV", "Kvinna"}, "optionLabel": "Man ZQK-SV", "after": "Man ZQK-SV"}},
	} {
		sentinel := c.extra["after"].(string)
		sentinel = sentinel[strings.LastIndex(sentinel, "ZQK"):]
		h.fillWithholds(c.label, sentinel, c.extra)
	}
}

func TestLearningRoute_SpelledOutSecretsAndNewLanguageQuestionsAreWithheld(t *testing.T) {
	h := newDomainHarness(t)
	for i, q := range []string{"Pass word", "P a s s w o r d", "passwd", "pwd", "Enter your pass word", "Your pwd",
		"Płeć", "Kön", "\u041f\u043e\u043b", "\u0627\u0644\u062c\u0646\u0633", "\u0932\u093f\u0902\u0917"} {
		sentinel := "ZQK-Q" + string(rune('A'+i))
		h.fillWithholds("q"+sentinel, sentinel, map[string]any{"question": q, "controlKind": "text", "after": sentinel})
	}
}

func TestLearningRoute_InvisibleCharactersDoNotMakeASystemWriteGroundTruth(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueRun()
	if r := h.post(systemWritesPath, wire.SystemWriteRequest{RunID: run, Selector: "#city", Value: "Springfield"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for i, v := range []string{"Springfield\u200b", "Spring\u200dfield", "\ufeffSpringfield", "Spring\u00adfield", "Springfield\u00a0"} {
		if h.fillGroundTruth("inv"+string(rune('a'+i)), run, "#x", "Which city "+string(rune('a'+i)), v) {
			t.Errorf("%q was taken as ground truth", v)
		}
	}
}

// The harness clock reads 03:04, so this is an hour before it.
const finishedAnHourEarlier = "2026-01-02T02:04:00.000Z"

func TestSystemWritesRoute_RefusesARunThatFinishedLongAgo(t *testing.T) {
	h := newDomainHarness(t)
	h.ensureLearnRun()
	run := "run_" + strings.Repeat("ef", 12)
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (id, kind, status, created_at, updated_at, finished_at) VALUES (?, 'autofill', 'succeeded', ?, ?, ?)`,
			run, learnEventTime, finishedAnHourEarlier, finishedAnHourEarlier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := h.post(systemWritesPath, wire.SystemWriteRequest{RunID: run, Selector: "#c", Value: "Chicago"})
	if r.StatusCode != 422 || r.errCode() != "validation_failed" || h.rowCount("system_writes") != 0 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if !h.fillGroundTruth("finished-fill", run, "#c", "Which city after the run", "Chicago") {
		t.Fatal("a manual fill after the run ended is still accepted, and nothing was recorded against it")
	}
}
