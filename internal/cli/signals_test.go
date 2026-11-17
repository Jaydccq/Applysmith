package cli

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/signals"
	"applysmith/internal/store"
)

const signalsPortals = `version: 1
sources:
  signals-hn:
    enabled: true
    queries: ["series a hiring"]
`

func TestSignalsCLI_RelatedCommandsExist(t *testing.T) {
	te := newTestEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search_by_date", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[{"objectID":"1","title":"Example Corp raises $5M","url":"https://news.example.test/a","created_at_i":` + itoa(time.Now().Unix()) + `}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	run := func(args ...string) output {
		t.Helper()
		var out, errw syncBuffer
		e := te.env(&out, &errw)
		e.signalEndpoints = signals.Endpoints{HN: srv.URL}
		code := e.run(context.Background(), append(append([]string{}, args...), "--workspace", te.ws))
		return output{code, out.String(), errw.String()}
	}
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if err := os.WriteFile(filepath.Join(te.ws, "portals.yml"), []byte(signalsPortals), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := te.run(context.Background(), "signals"); o.code != 2 || !strings.Contains(o.stderr, "applysmith signals tick") {
		t.Fatalf("no arguments: %+v", o)
	}
	for _, sub := range []string{"tick", "backfill", "rescore", "replay", "decompose", "status"} {
		if !strings.Contains(signalsUsage, "applysmith signals "+sub) {
			t.Errorf("usage lacks %s", sub)
		}
	}
	if o := run("signals", "status"); o.code != 0 || !strings.Contains(o.stdout, "no tick has run") {
		t.Fatalf("status: %+v", o)
	}
	if o := run("signals", "tick", "--sources", "signals-hn"); o.code != 0 || !strings.Contains(o.stdout, "signals-hn") || !strings.Contains(o.stdout, "ok") {
		t.Fatalf("tick: %+v", o)
	}
	if o := run("signals", "tick", "--sources", "signals-rss"); o.code != 2 {
		t.Fatalf("unknown source: %+v", o)
	}
	if o := run("signals", "backfill", "--days", "0"); o.code != 2 {
		t.Fatalf("backfill without days: %+v", o)
	}
	if o := run("signals", "rescore"); o.code != 0 || !strings.Contains(o.stdout, "rescored 0 companies") {
		t.Fatalf("rescore: %+v", o)
	}
	if o := run("signals", "decompose", "--company", "Nobody Inc"); o.code != 1 {
		t.Fatalf("decompose: %+v", o)
	}
	bad := filepath.Join(t.TempDir(), "v2.json")
	_ = os.WriteFile(bad, []byte(`{"version":"v9"}`), 0o600)
	if o := run("signals", "replay", "--policy", bad); o.code != 2 || !strings.Contains(o.stderr, "declares version") {
		t.Fatalf("replay with a mismatched file: %+v", o)
	}
	good := filepath.Join(t.TempDir(), "v1.json")
	data, _ := os.ReadFile("../../internal/signals/policy/v1.json")
	_ = os.WriteFile(good, data, 0o600)
	if o := run("signals", "replay", "--policy", good); o.code != 0 || !strings.Contains(o.stdout, "COMPANY") {
		t.Fatalf("replay: %+v", o)
	}
	// Nothing is resolved after one tick: there is no hit rate to give.
	if o := run("signals", "status"); o.code != 0 || !strings.Contains(o.stdout, "last tick") || !strings.Contains(o.stdout, "0 pending; none resolved yet") ||
		strings.Contains(o.stdout, "hit rate") {
		t.Fatalf("status after a tick: %+v", o)
	}

	// A tick that is already running refuses a second one and names it.
	te.withStore(func(st *store.Store) {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO signal_ticks (run_id, status, started_at, heartbeat_at) VALUES ('', 'running', ?, ?)`, now, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	if o := run("signals", "tick", "--sources", "signals-hn"); o.code != 1 || !strings.Contains(o.stderr, "already running") {
		t.Fatalf("second tick: %+v", o)
	}

	// Status, replay and decompose only read, so they answer with the app
	// open; the commands that write still need the workspace to themselves.
	te.startServe()
	if o := run("signals", "status"); o.code != 0 || !strings.Contains(o.stdout, "last tick") {
		t.Fatalf("status beside the server: %+v", o)
	}
	if o := run("signals", "replay", "--policy", good); o.code != 0 || !strings.Contains(o.stdout, "COMPANY") {
		t.Fatalf("replay beside the server: %+v", o)
	}
	if o := run("signals", "decompose", "--company", "Nobody Inc"); o.code != 1 || !strings.Contains(o.stderr, "no company named") {
		t.Fatalf("decompose beside the server: %+v", o)
	}
	if o := run("signals", "rescore"); o.code == 0 || !strings.Contains(o.stderr, "server_already_running") {
		t.Fatalf("rescore beside the server: %+v", o)
	}
}
