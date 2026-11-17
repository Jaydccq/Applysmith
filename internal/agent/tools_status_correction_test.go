package agent

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestSubmissionsRecent_ACorrectionIsShownButNotCountedAsAnApplication(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO submissions (identity_key, url, host, company, role, state, confirmed, source, observed_at, created_at, updated_at)
			VALUES ('k', 'https://jobs.example.test/p/1', 'jobs.example.test', 'Example Corp', 'Engineer', 'submitted', 1, 'apply-chain', ?, ?, ?)`, at, at, at)
		if err != nil {
			return err
		}
		first, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO submissions (identity_key, url, host, company, role, state, confirmed, source, observed_at, created_at, updated_at, supersedes, correction)
			VALUES ('k', 'https://jobs.example.test/p/1', 'jobs.example.test', 'Example Corp', 'Engineer', 'submitted', 1, 'apply-chain', ?, ?, ?, ?, 1)`, at, at, at, first)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := submissionsRecent(f.st, func() time.Time { return now })
	res, err := tool.Run(context.Background(), ToolCall{Args: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "1 submissions\n") || strings.Count(res.Text, "\n- ") != 2 || !strings.Contains(res.Text, "a correction of an earlier submission") {
		t.Fatalf("text %q", res.Text)
	}
}

// A row the user marked Applied by hand has no observed submit; it is listed
// apart, and a row an observed submission covers is not listed twice.
func TestSubmissionsRecent_ListsRowsMarkedAppliedApart(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, r := range []struct{ company, key, applied string }{{"Hand Corp", "hand", "2027-03-09"}, {"Seen Corp", "seen", "2027-03-10"}, {"Old Corp", "old", "2027-01-02"}} {
			if _, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, applied_by, applied_at, identity_key, created_at, updated_at)
				VALUES (?, ?, 'Engineer', 'interview', 'user', ?, ?, ?, ?)`, r.applied, r.company, r.applied, r.key, at, at); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO submissions (identity_key, url, host, company, role, state, confirmed, source, observed_at, created_at, updated_at)
			VALUES ('seen', 'https://jobs.example.test/p/2', 'jobs.example.test', 'Seen Corp', 'Engineer', 'submitted', 1, 'apply-chain', ?, ?, ?)`, at, at, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := submissionsRecent(f.st, func() time.Time { return now })
	res, err := tool.Run(context.Background(), ToolCall{Args: []byte(`{"days":7}`)})
	if err != nil {
		t.Fatal(err)
	}
	marked := res.Text[strings.Index(res.Text, "Marked applied"):]
	if !strings.HasPrefix(res.Text, "1 submissions\n") || !strings.Contains(marked, "Hand Corp | Engineer | applied 2027-03-09 | now interview") ||
		strings.Contains(marked, "Seen Corp") || strings.Contains(res.Text, "Old Corp") {
		t.Fatalf("text %q", res.Text)
	}
}
