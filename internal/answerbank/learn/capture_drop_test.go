package learn

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The vectors are shared with the engine's own test of the same rules.
const captureVectors = "../../../engine/test/capture-vectors.json"

type captureVector struct {
	Name  string `json:"name"`
	Input struct {
		Question    string `json:"question"`
		Value       string `json:"value"`
		ControlKind string `json:"controlKind"`
	} `json:"input"`
	Want string `json:"want"`
	// EngineOnly marks a rule that reads what an upload does not carry.
	EngineOnly bool `json:"engineOnly"`
}

func TestAnswerBank_Learn_CaptureDropSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(captureVectors)
	if err != nil {
		t.Fatal(err)
	}
	var vs []captureVector
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 20 {
		t.Fatalf("only %d shared vectors", len(vs))
	}
	for _, v := range vs {
		if v.EngineOnly {
			continue
		}
		q := v.Input.Question
		if rest, ok := strings.CutPrefix(q, "REPEAT:"); ok {
			parts := strings.SplitN(rest, ":", 2)
			n, _ := strconv.Atoi(parts[1])
			q = strings.Repeat(parts[0], n)
		}
		got := CaptureDrop(CaptureCandidate{Question: q, Value: v.Input.Value, ControlKind: v.Input.ControlKind})
		if got != v.Want {
			t.Errorf("%s: got %q, want %q", v.Name, got, v.Want)
		}
	}
}

func TestAnswerBank_Learn_IngestRejectsTheCaptureTheEngineWouldDrop(t *testing.T) {
	e := newEnv(t)
	// Scenario "Concatenated option list": the Go ingest rejects the same shape of text the engine does not emit.
	u := fillUpload("jobs.example.com", "Where did you hear about us", "Referral--ReferralJob board")
	u.ControlKind = "custom-listbox"
	res := e.ingest(u)
	if len(res.Rejected) != 1 || !strings.HasPrefix(res.Rejected[0].Reason, "capture-dropped") {
		t.Fatalf("%+v", res)
	}
	ok := fillUpload("jobs.example.com", "What is your current employer?", "Example Corp")
	if res := e.ingest(ok); len(res.Rejected) != 0 {
		t.Fatalf("%+v", res)
	}
}
