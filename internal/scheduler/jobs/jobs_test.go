package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/memory"
	"applysmith/internal/discovery"
	"applysmith/internal/gmail"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

func registry(t *testing.T, scan ScanFunc) *scheduler.Registry {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	reg := scheduler.NewRegistry()
	err := Register(reg, Deps{Workspace: ws, Scan: scan, Gmail: &gmail.Stack{}, Router: &llm.Router{}, KB: &kb.Service{}, Memory: &memory.Consolidator{},
		Registry: func(context.Context) (string, error) { return "ok", nil }, Liveness: func(context.Context) (string, error) { return "ok", nil },
		LeadsExpand: func(context.Context) (string, error) { return "ok", nil }, LeadsEvaluate: func(context.Context) (string, error) { return "ok", nil },
		InboxEvaluate: func(context.Context, int, string) (string, error) { return "ok", nil },
		Archive:       func(context.Context, bool) (string, error) { return "ok", nil },
		Signals:       func(context.Context, []string, func(string)) (string, error) { return "ok", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// CLI validation and pack previews must see the same kinds as the live server.
func TestScheduler_Registry_CatalogMatchesLiveKinds(t *testing.T) {
	live := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	catalog, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog.Names(), live.Names()) {
		t.Fatalf("catalog %v differs from live kinds %v", catalog.Names(), live.Names())
	}
	for _, kind := range live.All() {
		got, _ := catalog.Get(kind.Name)
		if !reflect.DeepEqual(got.Leases, kind.Leases) || got.Drives != kind.Drives || got.Network != kind.Network {
			t.Errorf("catalog capabilities differ for %s", kind.Name)
		}
	}
}

func TestScheduler_Registry_SourceKindsFollowCatalog(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	for _, src := range discovery.Sources {
		if _, ok := reg.Get("discover." + src.Name); !ok {
			t.Errorf("source %s has no kind", src.Name)
		}
	}
	if _, ok := reg.Get("discover.nonexistent"); ok {
		t.Fatal("a kind exists for a source the catalog does not list")
	}
}

func TestScheduler_Registry_DiscoverKindLeases(t *testing.T) {
	d := Deps{Scan: func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	}}
	api := discoverKind(d, discovery.SourceInfo{Name: "examplejobs", Kind: discovery.KindAPI})
	browser := discoverKind(d, discovery.SourceInfo{Name: "examplebrowser", Kind: discovery.KindBrowser})
	if got := api.Leases; len(got) != 1 || got[0] != scheduler.LeaseEvaluation {
		t.Errorf("api leases %v", got)
	}
	if got := browser.Leases; len(got) != 2 || got[1] != scheduler.LeaseBrowser {
		t.Errorf("browser leases %v", got)
	}
	reg := scheduler.NewRegistry()
	for _, k := range []scheduler.Kind{api, browser} {
		if err := reg.Register(k); err != nil {
			t.Fatalf("%s fails the registry self-check: %v", k.Name, err)
		}
	}
}

func TestScheduler_Registry_DiscoverInputsRejectUnknownAndSubmit(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	k, _ := reg.Get("discover.registry")
	for name, in := range map[string]map[string]any{
		"submit flag":   {"submit": true},
		"posting url":   {"url": "https://jobs.example.com/p/1"},
		"unknown scope": {"scope": "mars"},
		"bad sort":      {"sort": "random"},
		"topN too big":  {"topN": 501},
		"wrong type":    {"retryKnown": "yes"},
	} {
		if err := k.ValidateInputs(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := k.ValidateInputs(map[string]any{"scope": discovery.ScopeUS, "sort": "recency", "topN": 20, "retryKnown": true}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduler_Kinds_DiscoverMapsTheScanRecord(t *testing.T) {
	var got discovery.ScanRequest
	rec := discovery.Record{Status: discovery.RunCompleted, Counts: discovery.Counts{Discovered: 40, Inserted: 7, Queued: 5}}
	scan := func(_ context.Context, req discovery.ScanRequest, _ func(string)) (discovery.Record, error) {
		got = req
		return rec, nil
	}
	reg := registry(t, func(ctx context.Context, req discovery.ScanRequest, p func(string)) (discovery.Record, error) {
		return scan(ctx, req, p)
	})
	k, _ := reg.Get("discover.registry")
	in := scheduler.Invocation{Inputs: map[string]any{"topN": float64(15), "budgetSeconds": float64(90), "sort": "recency"}, Progress: func(string) {}}
	summary, err := k.Run(context.Background(), in)
	if err != nil || summary != "40 discovered, 7 new, 5 queued for evaluation" {
		t.Fatalf("%q %v", summary, err)
	}
	if got.Source != "registry" || got.TopN == nil || *got.TopN != 15 || got.Budget.Seconds() != 90 || got.Sort != "recency" {
		t.Fatalf("request %+v", got)
	}
	rec.Reason = discovery.ReasonBudget
	if summary, err := k.Run(context.Background(), in); err != nil || summary != "40 discovered, 7 new, 5 queued for evaluation; scan budget reached" {
		t.Fatalf("budget summary: %q %v", summary, err)
	}
	rec.Source = "exa"
	if summary, err := k.Run(context.Background(), in); err != nil || summary != "40 discovered, 7 new, 5 queued for evaluation; spend cap reached, the searches left run first next time" {
		t.Fatalf("spend-cap summary: %q %v", summary, err)
	}
	rec.Status, rec.Reason = discovery.RunNeedsSignIn, "login wall"
	_, err = k.Run(context.Background(), in)
	var je *scheduler.JobError
	if !errors.As(err, &je) || je.Class != scheduler.FailNeedsYou || je.Reason != scheduler.ReasonNeedsSignIn {
		t.Fatalf("sign-in: %v", err)
	}
	rec.Status, rec.Reason = discovery.RunFailed, "board errors"
	if _, err = k.Run(context.Background(), in); err == nil || !strings.Contains(err.Error(), "board errors") {
		t.Fatalf("failed scan: %v", err)
	}
}

// Exercise dispatch for every source, including each source's prerequisite
// failures. A registered kind must preserve its own source, not capture another
// loop iteration or turn a blocked/no-key scan into a successful job.
func TestScheduler_Kinds_EverySourceDispatchAndPrerequisites(t *testing.T) {
	for _, src := range discovery.Sources {
		t.Run(src.Name, func(t *testing.T) {
			var got discovery.ScanRequest
			rec := discovery.Record{Status: discovery.RunCompleted, Counts: discovery.Counts{Discovered: 3, Inserted: 2, Queued: 1}}
			reg := registry(t, func(_ context.Context, req discovery.ScanRequest, _ func(string)) (discovery.Record, error) {
				got = req
				return rec, nil
			})
			kind, ok := reg.Get("discover." + src.Name)
			if !ok {
				t.Fatal("source kind is missing")
			}
			inv := scheduler.Invocation{Inputs: map[string]any{"topN": float64(1)}, Progress: func(string) {}}
			summary, err := kind.Run(t.Context(), inv)
			if err != nil || got.Source != src.Name || got.TopN == nil || *got.TopN != 1 || summary != "3 discovered, 2 new, 1 queued for evaluation" {
				t.Fatalf("dispatch: request=%+v summary=%q err=%v", got, summary, err)
			}
			for _, status := range []string{discovery.RunBlocked, discovery.RunFailed, discovery.RunNeedsSignIn} {
				rec.Status, rec.Reason = status, "test prerequisite"
				if _, err := kind.Run(t.Context(), inv); err == nil {
					t.Errorf("%s was reported as successful", status)
				}
			}
			rec.Status, rec.Reason = discovery.RunSkipped, discovery.ReasonNoKey
			_, err = kind.Run(t.Context(), inv)
			var jobErr *scheduler.JobError
			if !errors.As(err, &jobErr) || jobErr.Class != scheduler.FailNeedsYou || jobErr.Reason != ReasonNeedsKey {
				t.Fatalf("missing credential was not needs-you: %v", err)
			}
		})
	}
}

func TestScheduler_Kinds_RegistryRefreshAndLivenessAudit(t *testing.T) {
	reg := registry(t, nil)
	refresh, ok := reg.Get("registry.refresh")
	if !ok || !refresh.Network || len(refresh.Leases) != 0 {
		t.Fatalf("registry.refresh = %+v, %v", refresh, ok)
	}
	audit, ok := reg.Get("liveness.audit")
	if !ok || len(audit.Leases) != 2 || audit.Leases[0] != scheduler.LeaseBrowser || audit.Leases[1] != scheduler.LeaseTracker || !audit.Drives.ScanWindow {
		t.Fatalf("liveness.audit = %+v, %v", audit, ok)
	}
	bare := scheduler.NewRegistry()
	if err := Register(bare, Deps{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"registry.refresh", "liveness.audit"} {
		if _, ok := bare.Get(name); ok {
			t.Errorf("%s is registered without its service", name)
		}
	}
}

func TestScheduler_Kinds_ScheduledScanSkippedForNoKeyNeedsYou(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{Status: discovery.RunSkipped, Reason: discovery.ReasonNoKey}, nil
	})
	k, _ := reg.Get("discover.theirstack")
	_, err := k.Run(context.Background(), scheduler.Invocation{Inputs: map[string]any{}, Progress: func(string) {}})
	var je *scheduler.JobError
	if !errors.As(err, &je) || je.Class != scheduler.FailNeedsYou || je.Reason != ReasonNeedsKey {
		t.Fatalf("a no-key skip = %v", err)
	}
	// Any other skip stays quiet.
	reg = registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{Status: discovery.RunSkipped, Reason: "no saved searches"}, nil
	})
	k, _ = reg.Get("discover.theirstack")
	if _, err := k.Run(context.Background(), scheduler.Invocation{Inputs: map[string]any{}, Progress: func(string) {}}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduler_Kinds_DiscoverPassesAdHocFlags(t *testing.T) {
	var got discovery.ScanRequest
	reg := registry(t, func(_ context.Context, req discovery.ScanRequest, _ func(string)) (discovery.Record, error) {
		got = req
		return discovery.Record{Status: discovery.RunCompleted}, nil
	})
	k, _ := reg.Get("discover.theirstack")
	in := map[string]any{"title": "Platform Engineer", "limit": float64(10), "postedWithin": "3d"}
	if err := k.ValidateInputs(in); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Run(context.Background(), scheduler.Invocation{Inputs: in, Progress: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Platform Engineer" || got.Limit != 10 || got.Window.Hours() != 72 {
		t.Fatalf("request = %+v", got)
	}
	if err := k.ValidateInputs(map[string]any{"postedWithin": "soon"}); err == nil {
		t.Fatal("a bad window was accepted")
	}
}

func TestSignals_Collect_IsSchedulerKindWithNoSubmitLease(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{}, nil
	})
	k, ok := reg.Get("signals.collect")
	if !ok {
		t.Fatal("signals.collect is not registered")
	}
	if k.SubmitCapable || len(k.Leases) != 1 || k.Leases[0] != scheduler.LeaseEvaluation || k.Drives.ScanWindow || k.Drives.TrackerWrite || !k.Network {
		t.Fatalf("kind = %+v", k)
	}
	if err := k.Check(map[string]any{"sources": "signals-x,signals-hn"}); err != nil {
		t.Fatal(err)
	}
	if err := k.Check(map[string]any{"sources": "signals-rss"}); err == nil {
		t.Fatal("an unknown source passed")
	}
	if _, pending := Pending()["signals.collect"]; pending {
		t.Fatal("signals.collect is still pending")
	}
	var got []string
	r := registryWithSignals(t, func(ctx context.Context, sources []string, progress func(string)) (string, error) {
		got = sources
		return "done", nil
	})
	k2, _ := r.Get("signals.collect")
	sum, err := k2.Run(context.Background(), scheduler.Invocation{Inputs: map[string]any{"sources": "signals-x"}})
	if err != nil || sum != "done" || len(got) != 1 || got[0] != "signals-x" {
		t.Fatalf("run = %q %v %v", sum, err, got)
	}
}

func registryWithSignals(t *testing.T, fn func(context.Context, []string, func(string)) (string, error)) *scheduler.Registry {
	t.Helper()
	reg := scheduler.NewRegistry()
	if err := Register(reg, Deps{Signals: fn}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestScanRecord_PreservesOfflineTransientFailure(t *testing.T) {
	reg := registry(t, func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
		return discovery.Record{Status: discovery.RunFailed, Reason: "host-offline", Error: context.DeadlineExceeded.Error(), Retryable: true}, nil
	})
	k, _ := reg.Get("discover.registry")
	_, err := k.Run(context.Background(), scheduler.Invocation{Inputs: map[string]any{}, Progress: func(string) {}})
	var je *scheduler.JobError
	if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &je) || je.Class != scheduler.FailTransient) {
		t.Fatalf("transient scan record became permanent: %T %v", err, err)
	}
}

type scanner503Window struct{}

func (scanner503Window) OpenTab(context.Context, string, string) (discovery.Tab, error) {
	return &scanner503Tab{}, nil
}

type scanner503Tab struct{ url string }

func (t *scanner503Tab) Goto(_ context.Context, u string) (discovery.Nav, error) {
	t.url = u
	return discovery.Nav{URL: u, Status: 503}, nil
}
func (t *scanner503Tab) Eval(_ context.Context, _ string, out any) error {
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"url": t.url, "cards": 0, "rows": []any{}, "title": "Unavailable", "text": "Temporary server failure"})
	return json.Unmarshal(b, out)
}
func (*scanner503Tab) PopupURLs() []string { return nil }
func (*scanner503Tab) Close()              {}
func (*scanner503Tab) ClearChallenge(context.Context) (discovery.ChallengeClear, error) {
	return discovery.ChallengeClear{Reason: "no-challenge"}, nil
}
func TestScanRecord_ActualHTTP503RemainsTransient(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "scan.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sc := &discovery.Scanner{Repo: discovery.NewRepo(st, time.Now), Browser: &discovery.BrowserEnv{Window: scanner503Window{}, Host: func(context.Context) discovery.HostSample {
		return discovery.HostSample{Verdict: discovery.PressureNormal, FreeMiB: 8000, CPUs: 8}
	}, Sleep: func(context.Context, time.Duration) error { return nil }}}
	reg := registry(t, func(ctx context.Context, req discovery.ScanRequest, _ func(string)) (discovery.Record, error) {
		req.Search = "engineer"
		req.Pages = 1
		rec, err := sc.Run(ctx, req)
		t.Logf("actual scan status=%s reason=%q error=%q attempts=%+v", rec.Status, rec.Reason, rec.Error, rec.Attempts)
		return rec, err
	})
	k, _ := reg.Get("discover.ziprecruiter")
	_, err = k.Run(context.Background(), scheduler.Invocation{Inputs: map[string]any{}, Progress: func(string) {}})
	var je *scheduler.JobError
	if !errors.As(err, &je) || je.Class != scheduler.FailTransient {
		t.Fatalf("actual HTTP503 scan became permanent: %T %v", err, err)
	}
}
