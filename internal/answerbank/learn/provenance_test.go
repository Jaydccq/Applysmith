package learn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *env) fillWith(run, selector, question, value string, trusted bool) ManualFill {
	e.t.Helper()
	u := fillUpload("jobs.example.test", question, value)
	u.RunID, u.Selector, u.TrustedInput = run, selector, trusted
	if res := e.ingest(u); res.Accepted != 1 {
		e.t.Fatalf("%+v", res)
	}
	var truth bool
	if err := e.st.Read().QueryRow(`SELECT ground_truth FROM manual_fills ORDER BY id DESC LIMIT 1`).Scan(&truth); err != nil {
		e.t.Fatal(err)
	}
	return ManualFill{GroundTruth: truth}
}

// A manual fill equal to a value the system wrote during that run is the
// system's own value read back: stored, never ground truth, whatever selector
// the client names or leaves out.
func TestAnswerBank_ManualFills_SystemWriteIsNotGroundTruth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run, other := e.issueRun(), e.issueRun()
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#city", "Springfield"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, selector, value string }{
		{"same control", "#city", " springfield "},
		{"another selector", "#other", "Springfield"},
		{"no selector", "", "Springfield"},
		{"selector in another case", "#CITY", "Springfield"},
	} {
		if f := e.fillWith(run, c.selector, "Which city "+c.name, c.value, true); f.GroundTruth {
			t.Errorf("%s: the system's own write is not the user's", c.name)
		}
	}
	if f := e.fillWith(run, "#city", "Which city?", "Shelbyville", true); !f.GroundTruth {
		t.Fatalf("another value in the same run is the user's: %+v", f)
	}
	if f := e.fillWith(other, "#city", "Which city is it", "Springfield", true); !f.GroundTruth {
		t.Fatalf("the same value in another run is the user's: %+v", f)
	}
}

// A fill outside any run cannot be compared with the system's writes, so it is
// stored and is never ground truth.
func TestAnswerBank_ManualFills_WithoutARunAreNeverGroundTruth(t *testing.T) {
	e := newEnv(t)
	if f := e.fillWith("", "#city", "Where do you live", "Springfield", true); f.GroundTruth {
		t.Fatalf("%+v", f)
	}
	for i := 0; i < 3; i++ {
		u := fillUpload("jobs.example.test", "Favourite shell", "fish")
		u.RunID, u.At = "", baseTime.Add(-time.Hour*time.Duration(i+1))
		e.ingest(u)
	}
	e.generate()
	if cs := e.candidates(""); len(cs) != 0 {
		t.Fatalf("fills outside a run are not evidence: %+v", cs)
	}
}

// Without the client's statement that the value came from a trusted user
// event, a manual fill is stored but is not ground truth, and it feeds no
// candidate.
func TestAnswerBank_ManualFills_NeedTheTrustedInputStatement(t *testing.T) {
	e := newEnv(t)
	if f := e.fillWith(testRun, "", "Preferred editor", "Vim", false); f.GroundTruth {
		t.Fatalf("%+v", f)
	}
	if f := e.fillWith(testRun, "", "Preferred editor ", "Vim", true); !f.GroundTruth {
		t.Fatalf("%+v", f)
	}
	for i := 0; i < 3; i++ {
		u := fillUpload("jobs.example.test", "Favourite shell", "fish")
		u.TrustedInput = false
		u.At = baseTime.Add(-time.Hour * time.Duration(i+1))
		e.ingest(u)
	}
	e.generate()
	if cs := e.candidates(""); len(cs) != 0 {
		t.Fatalf("fills nobody attested are not evidence: %+v", cs)
	}
	// A change over a system value that nobody attested does not count against
	// the entry that wrote it.
	u := fillUpload("jobs.example.test", "City", "Shelbyville")
	u.Before, u.EntryID, u.TrustedInput = "Springfield", "address-city", false
	e.ingest(u)
	if e.count("answer_events WHERE entry_id = 'address-city' AND counted = 1") != 0 {
		t.Fatal("an unattested change counted as a correction")
	}
}

// The system's writes are kept as a keyed digest only, so the table adds no
// copy of a value, a short guessed value cannot be confirmed from it, and they
// age out with the 180-day retention.
func TestAnswerBank_SystemWrites_KeptAsKeyedDigestAndPruned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run := e.issueRun()
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#employer", "Quillfeather Industries"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#employer", "quillfeather   industries"}); err != nil {
		t.Fatal(err)
	}
	if n := e.count("system_writes"); n != 1 {
		t.Fatalf("%d rows, want one for a repeated write", n)
	}
	for _, needle := range []string{"Quillfeather", "quillfeather", "#employer"} {
		if e.dbHolds(needle) {
			t.Fatalf("%q is readable from the database", needle)
		}
	}
	if n := e.count("pragma_table_info('system_writes')"); n != 2 {
		t.Fatalf("%d columns: a digest and a time are all the table holds", n)
	}
	plain := sha256.Sum256([]byte(run + "\x00quillfeather industries"))
	if e.count("system_writes WHERE value_hash = ?", hex.EncodeToString(plain[:])) != 0 {
		t.Fatal("the digest is a plain hash: a guessed value could be confirmed")
	}
	e.clk.advance(EventRetention + 24*time.Hour)
	if _, err := e.s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count("system_writes"); n != 0 {
		t.Fatalf("%d rows survive the retention", n)
	}
}

// The key lives in a 0600 file of the workspace's state directory, not in the
// database, and one workspace's digests mean nothing in another.
func TestAnswerBank_SystemWrites_KeyIsAWorkspaceFile(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	run := a.issueRun()
	if err := a.s.RecordSystemWrites(context.Background(), run, SystemWrite{"#a", "value"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(a.ws.StateDir(), writeKeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, info)
	}
	ha, _ := a.s.writeHash(run, "value")
	hb, _ := b.s.writeHash(run, "value")
	if ha == hb {
		t.Fatal("two workspaces share a key")
	}
}

// A write for a run the server never issued is refused, even when the id has
// the right shape; so is a malformed one.
func TestAnswerBank_SystemWrites_RefuseARunNobodyIssued(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run := e.issueRun()
	unissued := "run_" + strings.Repeat("0", 23) + "f"
	if !runIDRE.MatchString(unissued) {
		t.Fatal("the unissued id must pass the shape check")
	}
	for _, bad := range []struct{ run, selector, value string }{
		{"not-a-run", "#a", "b"}, {run, "", "b"}, {run, "#a", ""}, {"run_" + run[4:] + "0", "#a", "b"}, {unissued, "#a", "b"},
	} {
		if err := e.s.RecordSystemWrites(ctx, bad.run, SystemWrite{bad.selector, bad.value}); !errors.Is(err, ErrSystemWrite) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if n := e.count("system_writes"); n != 0 {
		t.Fatalf("%d rows were written", n)
	}
}

// The application chain issues its own run ids (apply_runs) and its manual
// fills are accepted under them, so its writes are the system's too: one read
// back in that run is not the user's, and a chain run long finished takes none.
func TestAnswerBank_SystemWrites_AnApplicationChainRunRecordsThem(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run := "run_20311104T120000.000_80e54d"
	if !applyRunIDRE.MatchString(run) {
		t.Fatal("the id must have the chain's shape")
	}
	e.exec(`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key) VALUES (?, ?, 'cli', ?, ?)`,
		run, stamp(baseTime.Add(-time.Hour)), "https://jobs.example.com/acme/1", "example:acme/1")
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#city", "Springfield"}); err != nil {
		t.Fatal(err)
	}
	if f := e.fillWith(run, "#city", "Which city", "Springfield", true); f.GroundTruth {
		t.Errorf("the chain's own write read back is not the user's: %+v", f)
	}
	if f := e.fillWith(run, "#city", "Which city?", "Shelbyville", true); !f.GroundTruth {
		t.Errorf("another value in the chain's run is the user's: %+v", f)
	}
	e.exec(`UPDATE apply_runs SET outcome_kind = 'stopped', finished_at = ? WHERE run_id = ?`, stamp(baseTime.Add(-time.Hour)), run)
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#state", "Illinois"}); !errors.Is(err, ErrSystemWrite) {
		t.Errorf("a chain run that finished an hour ago took a write: %v", err)
	}
	if err := e.s.RecordSystemWrites(ctx, "run_20311104T120000.000_80e54e", SystemWrite{"#city", "Springfield"}); !errors.Is(err, ErrSystemWrite) {
		t.Errorf("a chain-shaped id nobody issued took a write: %v", err)
	}
}
