package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/discovery"
	"applysmith/internal/gmail"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

func TestScheduler_Registry_NetworkNeedPerKind(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	local := []string{"cost.report", "archive.sweep"}
	for _, name := range reg.Names() {
		k, _ := reg.Get(name)
		if want := !slices.Contains(local, name); k.Network != want {
			t.Errorf("%s: network need %v, want %v", name, k.Network, want)
		}
	}
	// memory.consolidate calls the model, so it needs the network; the
	// archive sweep reads and writes the local tracker only.
	if k, _ := reg.Get("memory.consolidate"); !k.Network {
		t.Error("memory.consolidate runs offline")
	}
}

// Every kind but a scan's has a human name for the runs board, so a new kind
// cannot reach the board or a notice under its identifier. A scan is titled
// by its source.
func TestScheduler_Registry_EveryKindHasAHumanTitleOnTheBoard(t *testing.T) {
	reg, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	tasks := 0
	for _, name := range reg.Names() {
		if strings.HasPrefix(name, "discover.") {
			continue
		}
		tasks++
		if title := notify.TaskTitle(name); title == "" || title == name {
			t.Errorf("kind %s has no human title", name)
		}
	}
	if tasks == 0 {
		t.Fatal("the catalog holds no task kind")
	}
}

func TestScheduler_Leases_NoKindUsesApplyWindow(t *testing.T) {
	reg, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Names()) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, k := range reg.All() {
		if strings.HasPrefix(k.Name, "apply") || k.SubmitCapable {
			t.Errorf("kind %s drives the apply window", k.Name)
		}
		for _, l := range k.Leases {
			if !slices.Contains(scheduler.Leases, l) || strings.Contains(string(l), "apply") {
				t.Errorf("kind %s holds lease %q", k.Name, l)
			}
		}
	}
}

func TestScheduler_Kinds_PriceCheckFailureRecorded(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusInternalServerError) }))
	defer bad.Close()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys, err := vault.New("0123456789ab", vault.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	reg := scheduler.NewRegistry()
	if err := Register(reg, Deps{Router: llm.New(st, keys, llm.WithCatalogURL(bad.URL))}); err != nil {
		t.Fatal(err)
	}
	k, ok := reg.Get("price.check")
	if !ok {
		t.Fatal("price.check is not registered")
	}
	_, err = k.Run(context.Background(), scheduler.Invocation{})
	if err == nil || !strings.Contains(err.Error(), "the price catalog could not be read") {
		t.Fatalf("a failed fetch is not a failure with its reason: %v", err)
	}
}

// The spec gives leads.evaluate browser and llm:evaluation: its deep
// evaluations read a page in the scan window when no ATS API serves the URL.
func TestScheduler_Registry_LeadsEvaluateLeasesMatchCapabilities(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	k, ok := reg.Get("leads.evaluate")
	if !ok {
		t.Fatal("leads.evaluate is not registered")
	}
	if want := []scheduler.Lease{scheduler.LeaseBrowser, scheduler.LeaseEvaluation}; !slices.Equal(k.Leases, want) {
		t.Fatalf("leases %v, want %v", k.Leases, want)
	}
	if !k.Drives.Evaluation || !k.Drives.EvaluationReadsPages || k.Drives.ScanWindow || k.Drives.TrackerWrite {
		t.Fatalf("drives %+v", k.Drives)
	}
}

// The spec gives inbox.evaluate what leads.evaluate holds: its evaluations
// may read a page in the scan window. A run takes the schedule's limit, and
// without one the default: the drain itself has none, so an unset limit would
// send nothing. The evaluations it starts are the run's own, told through its
// roll-up (F-2), so the drain is handed the run.
func TestScheduler_Registry_InboxEvaluateLeasesAndLimit(t *testing.T) {
	var limits []int
	var runs []string
	k := inboxEvaluate(Deps{InboxEvaluate: func(_ context.Context, n int, run string) (string, error) {
		limits, runs = append(limits, n), append(runs, run)
		return "", nil
	}})
	if want := []scheduler.Lease{scheduler.LeaseBrowser, scheduler.LeaseEvaluation}; !slices.Equal(k.Leases, want) {
		t.Fatalf("leases %v, want %v", k.Leases, want)
	}
	for i, in := range []map[string]any{nil, {"limit": float64(5)}} {
		if _, err := k.Run(context.Background(), scheduler.Invocation{RunID: fmt.Sprintf("run_%d", i), Inputs: in}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(limits, []int{discovery.InboxEvaluateLimit, 5}) || !slices.Equal(runs, []string{"run_0", "run_1"}) {
		t.Fatalf("limits %v, runs %v", limits, runs)
	}
}

// The spec gives archive.sweep tracker:write alone, and the run archives only
// when the schedule says so.
func TestScheduler_Registry_ArchiveSweepLease(t *testing.T) {
	var applied []bool
	reg := scheduler.NewRegistry()
	err := Register(reg, Deps{Archive: func(_ context.Context, apply bool) (string, error) {
		applied = append(applied, apply)
		return "ok", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	k, ok := reg.Get("archive.sweep")
	if !ok {
		t.Fatal("archive.sweep is not registered")
	}
	if !slices.Equal(k.Leases, []scheduler.Lease{scheduler.LeaseTracker}) || !k.Drives.TrackerWrite || k.Network {
		t.Fatalf("leases %v drives %+v network %v", k.Leases, k.Drives, k.Network)
	}
	if err := k.ValidateInputs(map[string]any{"apply": "yes"}); err == nil {
		t.Error("a non-boolean apply was accepted")
	}
	for _, in := range []map[string]any{nil, {"apply": false}, {"apply": true}} {
		if _, err := k.Run(context.Background(), scheduler.Invocation{Inputs: in}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(applied, []bool{false, false, true}) {
		t.Fatalf("apply flags %v: a sweep with no input must only preview", applied)
	}
	if len(Pending()) != 0 {
		t.Errorf("pending kinds %v", Pending())
	}
}

// Gmail returns a recorded failed sync with nil error. A missing app password
// must still stop as needs-you, rather than a retryable generic schedule
// failure.
func TestScheduler_GmailRecordedSignInFailureNeedsYou(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	keys, err := vault.New("0123456789ab", vault.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	stack := gmail.Open(st, keys, gmail.Options{})
	summary, err := gmailSync(Deps{Gmail: stack}).Run(ctx, scheduler.Invocation{})
	var jobErr *scheduler.JobError
	if !errors.As(err, &jobErr) || jobErr.Class != scheduler.FailNeedsYou || jobErr.Reason != ReasonNeedsGmailSignIn {
		t.Fatalf("no app password: summary=%q err=%v", summary, err)
	}
	if !strings.Contains(summary, "0 listed") {
		t.Fatalf("sync counts lost: %q", summary)
	}
	var status, reason string
	if err := st.Read().QueryRow(`SELECT status, error FROM gmail_runs`).Scan(&status, &reason); err != nil || status != "failed" || !gmail.AuthFailureText(reason) {
		t.Fatalf("recorded sync: status=%q reason=%q err=%v", status, reason, err)
	}
}

func TestScheduler_LeadsExpand_PreservesTemporaryAndPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		retry   bool
	}{
		{"http503", &discovery.TransientError{URL: "https://careers.example.com", Status: 503}, true},
		{"rate limit", &discovery.RateLimitedError{Host: "careers.example.com"}, true},
		{"mixed failures", errors.Join(errors.New("owned admission failure"), &discovery.TransientError{URL: "https://careers.example.com", Status: 503}), true},
		{"admission failure", errors.New("owned admission failure"), false},
		{"completed", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := scheduler.NewRegistry()
			if err := Register(reg, Deps{LeadsExpand: func(context.Context) (string, error) { return "partial expansion", tc.failure }}); err != nil {
				t.Fatal(err)
			}
			kind, ok := reg.Get("leads.expand")
			if !ok {
				t.Fatal("missing expansion kind")
			}
			summary, err := kind.Run(t.Context(), scheduler.Invocation{})
			if summary != "partial expansion" || !errors.Is(err, tc.failure) {
				t.Fatalf("summary=%q err=%v cause=%v", summary, err, tc.failure)
			}
			var jobError *scheduler.JobError
			retry := errors.As(err, &jobError) && jobError.Class == scheduler.FailTransient
			if retry != tc.retry {
				t.Fatalf("retry=%t want=%t err=%v", retry, tc.retry, err)
			}
		})
	}
}
