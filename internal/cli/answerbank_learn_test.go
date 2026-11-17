package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// seedLearning posts three corrections for the first default entry and three
// no-entry abstains of one question.
func seedLearning(t *testing.T, te *testEnv) (staleEntry string) {
	t.Helper()
	ws := workspace.Open(te.ws)
	l, err := answerbank.New(ws, nil).File()
	if err != nil || len(l.Bank.Entries) == 0 {
		t.Fatal(err)
	}
	staleEntry = l.Bank.Entries[0].ID
	te.withStore(func(st *store.Store) {
		svc := learn.New(st, answerbank.New(ws, st), learn.Options{})
		defer svc.Close()
		at := time.Now().UTC().Add(-time.Hour)
		var ups []learn.Upload
		for i := 0; i < 3; i++ {
			ups = append(ups, learn.Upload{EventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 2*i), Kind: learn.UploadCorrection, Origin: learn.OriginUserEdit, Host: "jobs.example.com",
				Question: "Some question", ControlKind: "text", Before: "a", After: "b" + string(rune('a'+i)), EntryID: staleEntry, At: at})
			ups = append(ups, learn.Upload{EventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 2*i+1), Kind: learn.UploadUncertainty, Host: "jobs.example.com",
				Question: "How did you hear about Example Corp?", ControlKind: "text", Reason: "no-entry", At: at, ReportNumber: int64(i + 1)})
		}
		if res, err := svc.Ingest(context.Background(), ups); err != nil || len(res.Rejected) != 0 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	return staleEntry
}

func TestAnswerBankEvolveDryRunReportsAndApplyWrites(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("%+v", o)
	}
	stale := seedLearning(t, te)
	path := filepath.Join(te.ws, "answer-bank.yml")
	before, _ := os.ReadFile(path)
	dry := te.run(context.Background(), "answer-bank", "evolve", "--workspace", te.ws, "--json")
	var rep struct {
		Applied bool     `json:"applied"`
		Created int      `json:"created"`
		Stale   []string `json:"stale"`
	}
	if err := json.Unmarshal([]byte(dry.stdout), &rep); err != nil || dry.code != 0 || rep.Applied || rep.Created != 1 || len(rep.Stale) != 1 || rep.Stale[0] != stale {
		t.Fatalf("%+v %v", dry, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a dry run wrote the bank")
	}
	if o := te.run(context.Background(), "answer-bank", "candidates", "list", "--workspace", te.ws); !strings.HasPrefix(o.stdout, "no candidates") {
		t.Fatalf("a dry run stores no candidate: %+v", o)
	}
	real := te.run(context.Background(), "answer-bank", "evolve", "--apply", "--workspace", te.ws)
	if real.code != 0 || !strings.Contains(real.stdout, "did: 1 new candidates") || !strings.Contains(real.stdout, "stale: "+stale) {
		t.Fatalf("%+v", real)
	}
	if after, _ := os.ReadFile(path); !strings.Contains(string(after), "stale_correction_count: 3") {
		t.Fatal("--apply writes the transition")
	}
	list := te.run(context.Background(), "answer-bank", "candidates", "list", "--workspace", te.ws, "--state", "pending")
	fields := strings.Split(strings.TrimSpace(list.stdout), "\t")
	if list.code != 0 || len(fields) < 5 || fields[1] != "pending" {
		t.Fatalf("%+v", list)
	}
	// A review's draft is listed beside a suggestion with no answer of its own.
	te.withStore(func(st *store.Store) {
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE answer_candidates SET review_verdict = 'reusable', review_reason = 'One answer fits every application.', review_draft = 'A colleague', reviewed_at = '2026-10-01T00:00:00Z' WHERE id = ?`, fields[0])
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	if o := te.run(context.Background(), "answer-bank", "candidates", "list", "--workspace", te.ws); !strings.Contains(o.stdout, "reusable: One answer fits every application.\tdraft: \"A colleague\" (check it, then approve with --answer)") {
		t.Fatalf("%+v", o)
	}
	// Nothing approved it on its own.
	if after, _ := os.ReadFile(path); strings.Contains(string(after), "created_by: agent") {
		t.Fatal("the bank holds an agent-created entry nobody approved")
	}
	ap := te.run(context.Background(), "answer-bank", "candidates", "approve", fields[0], "--workspace", te.ws)
	if ap.code != 0 || !strings.Contains(ap.stdout, "approved candidate "+fields[0]) {
		t.Fatalf("%+v", ap)
	}
	if after, _ := os.ReadFile(path); !strings.Contains(string(after), "created_by: agent") {
		t.Fatal("approval writes the entry")
	}
	again := te.run(context.Background(), "answer-bank", "candidates", "approve", fields[0], "--workspace", te.ws)
	if again.code != 1 || !strings.Contains(again.stderr, "not-pending") {
		t.Fatalf("%+v", again)
	}
	// Each decision can be taken back here, and an approval can store the
	// user's own answer (the decisions themselves: learn's decide tests).
	for _, step := range [][]string{{"unapprove", fields[0]}, {"reject", fields[0]}, {"restore", fields[0]}, {"approve", fields[0], "--answer", "A friend"}} {
		if o := te.run(context.Background(), append(append([]string{"answer-bank", "candidates"}, step...), "--workspace", te.ws)...); o.code != 0 {
			t.Fatalf("%v: %+v", step, o)
		}
		if after, _ := os.ReadFile(path); strings.Contains(string(after), "created_by: agent") != (step[0] == "approve") {
			t.Fatalf("%v: the bank holds the approved entry: %v", step, step[0] == "approve")
		}
	}
	if after, _ := os.ReadFile(path); !strings.Contains(string(after), "A friend") {
		t.Fatal("--answer is the stored answer")
	}
	if o := te.run(context.Background(), "answer-bank", "candidates", "approve", "x", "--workspace", te.ws); o.code != 2 {
		t.Fatalf("%+v", o)
	}
}
