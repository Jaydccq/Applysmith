package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistry_Upsert_IdempotentFirstSourceWins(t *testing.T) {
	e := newEnv(t)
	if n, err := e.rp.RegistryAdd(e.ctx, "list-a", Board{ATS: Lever, Slug: "Example-Co"}); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := e.rp.RegistryAdd(e.ctx, "list-b", Board{ATS: Lever, Slug: "example-co"}); n != 0 {
		t.Fatal("same key added twice")
	}
	if row := e.registry(Board{ATS: Lever, Slug: "example-co"}); row.Source != "list-a" {
		t.Fatalf("source = %s", row.Source)
	}
	if _, err := e.rp.RegistryAdd(e.ctx, "x", Board{ATS: "taleo", Slug: "x"}); err == nil {
		t.Fatal("an unknown ATS must be rejected")
	}
	if _, err := e.rp.RegistryAdd(e.ctx, "x", Board{ATS: Lever, Slug: "-bad"}); err == nil {
		t.Fatal("an invalid slug must be rejected")
	}
}

func TestRegistry_StatsPruneReviveBlock(t *testing.T) {
	e := newEnv(t)
	e.add(ghBoard("a-co"), ghBoard("b-co"), ghBoard("c-co"))
	e.srv.set("/v1/boards/a-co/jobs", route{status: 404})
	e.stableBoard("b-co")
	e.stableBoard("c-co")
	for i := 0; i < 3; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	if err := e.rp.RegistrySetStatus(e.ctx, ghBoard("c-co"), StatusBlocked); err != nil {
		t.Fatal(err)
	}
	stats, _ := e.rp.RegistryStats(e.ctx)
	if stats[StatusDead] != 1 || stats[StatusBlocked] != 1 {
		t.Fatalf("stats = %v", stats)
	}
	if n, _ := e.rp.RegistryPrune(e.ctx); n != 0 {
		t.Fatal("a recent dead row must not be pruned")
	}
	e.clk.advance(181 * 24 * time.Hour)
	if n, _ := e.rp.RegistryPrune(e.ctx); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := e.rp.RegistryGet(e.ctx, ghBoard("a-co")); err == nil {
		t.Fatal("pruned row still present")
	}
	if err := e.rp.RegistrySetStatus(e.ctx, ghBoard("nope-co"), StatusActive); err == nil {
		t.Fatal("reviving an unknown board must fail")
	}
}

func TestRegistry_PruneSparesRevivedRows(t *testing.T) {
	e := newEnv(t)
	e.srv.set("/v1/boards/a-co/jobs", route{status: 404})
	e.add(ghBoard("a-co"))
	for i := 0; i < 3; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	if err := e.rp.RegistrySetStatus(e.ctx, ghBoard("a-co"), StatusActive); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(400 * 24 * time.Hour)
	if n, _ := e.rp.RegistryPrune(e.ctx); n != 0 {
		t.Fatal("a revived row was pruned")
	}
}

const seedList = "# example public list\nats,slug,company\ngreenhouse,example-co,Example Co\nlever,Another-Co\nashby,third-co\ntaleo,fourth-co\nlever,another-co\nworkable,bad slug!\n"

func seedServer(t *testing.T, body string, cutAt int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []byte(body)
		etag := fmt.Sprintf(`"seed-%x"`, sha256.Sum256(data))
		w.Header().Set("ETag", etag)
		if rng := r.Header.Get("Range"); rng != "" && (r.Header.Get("If-Range") == "" || r.Header.Get("If-Range") == etag) {
			start, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start:])
			return
		}
		if cutAt > 0 && cutAt < len(data) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:cutAt])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Close the connection mid-body.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegistry_Seed_ValidatesAndRecordsSource(t *testing.T) {
	e := newEnv(t)
	srv := seedServer(t, seedList, 0)
	s := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	res := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv", Licence: "MIT"}})
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if res[0].Inserted != 3 || res[0].Rejected != 2 || res[0].Rows != 3 {
		t.Fatalf("result = %+v", res[0])
	}
	row := e.registry(Board{ATS: Lever, Slug: "another-co"})
	if want := srv.URL + "/list.csv (2026-10-11)"; row.Source != want {
		t.Fatalf("source = %q, want %q", row.Source, want)
	}
	if e.registry(ghBoard("example-co")).Company != "Example Co" {
		t.Fatal("company not stored")
	}
	var url, sum, licence string
	if err := e.st.Read().QueryRow(`SELECT url, sha256, licence FROM data_sources`).Scan(&url, &sum, &licence); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(seedList))
	if url != srv.URL+"/list.csv" || sum != hex.EncodeToString(h[:]) || licence != "MIT" {
		t.Fatalf("data_sources = %s %s %s", url, sum, licence)
	}
	// Seeding again adds nothing.
	res = s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}})
	if res[0].Inserted != 0 {
		t.Fatalf("second seed inserted %d", res[0].Inserted)
	}
}

func TestRegistry_Seed_FailingSourceDoesNotStopOthers(t *testing.T) {
	e := newEnv(t)
	good := seedServer(t, seedList, 0)
	bad := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bad.Close)
	s := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	res := s.Seed(e.ctx, []SeedSource{{URL: bad.URL + "/x"}, {URL: good.URL + "/y"}})
	if res[0].Err == nil || res[1].Err != nil || res[1].Inserted != 3 {
		t.Fatalf("results = %+v", res)
	}
	if SeedFailed(res) != nil {
		t.Fatal("one success is not a total failure")
	}
	if SeedFailed(res[:1]) == nil {
		t.Fatal("all failing must be an error")
	}
}

func TestRegistry_Seed_InterruptedDownloadResumes(t *testing.T) {
	e := newEnv(t)
	body := strings.Repeat("greenhouse,pad-co\n", 1) + seedList
	srv := seedServer(t, body, 40)
	dir := t.TempDir()
	s := &Seeder{Repo: e.rp, Dir: dir}
	first := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}})
	if first[0].Err == nil {
		t.Fatal("the cut-off download must fail")
	}
	if n, _ := e.rp.RegistryStats(e.ctx); n[StatusActive] != 0 {
		t.Fatal("a partial list must not be imported")
	}
	// The retry continues from the stored bytes.
	var gotRange, gotIfRange string
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange, gotIfRange = r.Header.Get("Range"), r.Header.Get("If-Range")
		inner.ServeHTTP(w, r)
	})
	second := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}})
	if second[0].Err != nil || second[0].Inserted != 4 {
		t.Fatalf("second = %+v", second[0])
	}
	if !strings.HasPrefix(gotRange, "bytes=") || gotRange == "bytes=0-" {
		t.Fatalf("Range = %q, the retry must resume", gotRange)
	}
	if !strings.HasPrefix(gotIfRange, `"seed-`) {
		t.Fatalf("If-Range = %q, a resume must name the version it continues", gotIfRange)
	}
}

func TestRegistry_Seed_ListChangedBetweenAttemptsIsNotSpliced(t *testing.T) {
	e := newEnv(t)
	old := strings.Repeat("greenhouse,old-co\n", 1) + seedList
	srv := seedServer(t, old, 40)
	s := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	if first := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}}); first[0].Err == nil {
		t.Fatal("the cut-off download must fail")
	}
	fresh := seedServer(t, "lever,fresh-one\nlever,fresh-two\n", 0)
	srv.Config.Handler = fresh.Config.Handler
	second := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}})
	if second[0].Err != nil || second[0].Inserted != 2 {
		t.Fatalf("second = %+v", second[0])
	}
}

func TestRegistry_Seed_NoValidatorMeansNoResume(t *testing.T) {
	e := newEnv(t)
	var ranges []string
	cut := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		if cut {
			w.Header().Set("Content-Length", strconv.Itoa(len(seedList)))
			_, _ = w.Write([]byte(seedList[:40]))
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		_, _ = w.Write([]byte(seedList))
	}))
	t.Cleanup(srv.Close)
	s := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	if first := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}}); first[0].Err == nil {
		t.Fatal("the cut-off download must fail")
	}
	cut = false
	second := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/list.csv"}})
	if second[0].Err != nil || second[0].Inserted != 3 || ranges[len(ranges)-1] != "" {
		t.Fatalf("second = %+v ranges = %q", second[0], ranges)
	}
}

func TestRegistrySeed_BareSlugListsByATS(t *testing.T) {
	e := newEnv(t)
	srv := seedServer(t, `["alpha-co","Beta.Co","bad slug!","alpha-co"]`, 0)
	s := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	res := s.Seed(e.ctx, []SeedSource{{URL: srv.URL + "/greenhouse.json", ATS: Greenhouse, Licence: "MIT"}})
	if res[0].Err != nil || res[0].Inserted != 2 || res[0].Rejected != 1 {
		t.Fatalf("result = %+v", res[0])
	}
	if row := e.registry(Board{ATS: Greenhouse, Slug: "beta.co"}); !strings.Contains(row.Source, "/greenhouse.json (2026-10-11)") {
		t.Fatalf("source = %q", row.Source)
	}
	lines, _, err := ParseSlugList([]byte("- gamma-co: Gamma\n# note\n\"delta-co\"\n"), Lever)
	if err != nil || len(lines) != 2 || lines[0].Slug != "gamma-co" || lines[1].Slug != "delta-co" {
		t.Fatalf("lines = %+v, %v", lines, err)
	}
}

func TestRegistryRefresh_AddsNewRowsAndProbesDueDeadBoards(t *testing.T) {
	e := newEnv(t)
	srv := seedServer(t, `["alpha-co","beta-co"]`, 0)
	seeder := &Seeder{Repo: e.rp, Dir: t.TempDir()}
	src := []SeedSource{{URL: srv.URL + "/g.json", ATS: Greenhouse}}
	// A dead board that is due for its 30 day probe and whose board answers again.
	dead := ghBoard("revived-co")
	e.add(dead)
	for i := 0; i < 3; i++ {
		if err := e.rp.RegistryRecord(e.ctx, dead, VisitFailed, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	if e.registry(dead).Status != StatusDead {
		t.Fatal("the board is not dead")
	}
	e.serveGH("revived-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.clk.advance(31 * 24 * time.Hour)
	res, err := RegistryRefresh(e.ctx, seeder, e.sc, eng(), src)
	if err != nil || res.New() != 2 || res.Probe.Probed != 1 || res.Probe.Revived != 1 {
		t.Fatalf("refresh = %+v, %v", res, err)
	}
	// Run again: only new rows are added, existing rows are untouched.
	res, err = RegistryRefresh(e.ctx, seeder, e.sc, eng(), src)
	if err != nil || res.New() != 0 {
		t.Fatalf("second refresh = %+v, %v", res, err)
	}
	// Every source failing is an error and probes nothing.
	bad := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bad.Close)
	if _, err := RegistryRefresh(e.ctx, seeder, e.sc, eng(), []SeedSource{{URL: bad.URL + "/x", ATS: Lever}}); err == nil {
		t.Fatal("a refresh with every source failing must be an error")
	}
}
