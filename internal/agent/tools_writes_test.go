package agent

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/tracker"
)

// The user's reason is kept when the row already has the status asked for:
// "skip Lio, it is in Munich" on a row skipped before still records why, once.
func TestTrackerSetStatus_ARowAlreadyThereKeepsTheNoteOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, err := f.tracker.Add(ctx, tracker.AddRow{Company: "Lio", Role: "Engineer", URL: "https://jobs.example.test/lio/1", Status: tracker.Skipped})
	if err != nil {
		t.Fatal(err)
	}
	tool := trackerSetStatus(f.tracker)
	for range 2 {
		res, err := tool.Run(ctx, ToolCall{Args: []byte(`{"id":` + itoa64(e.ID) + `,"status":"skipped","note":"it is in Munich"}`)})
		if err != nil || !strings.Contains(res.Text, "already skipped") {
			t.Fatalf("result = %q, %v", res.Text, err)
		}
	}
	got, err := f.tracker.Get(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got.Entry.Notes, "it is in Munich") != 1 {
		t.Fatalf("notes = %q, want the reason once", got.Entry.Notes)
	}
}
