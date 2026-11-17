package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/workspace"
)

// feedOf renders an Atom feed of n entries about distinct companies.
func feedOf(n int, body string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<entry><title>Company%d raises $5M</title><link href="https://news.example.test/%d"/><published>2026-10-10T08:00:00Z</published><content>%s</content></entry>`, i, i, body)
	}
	b.WriteString(`</feed>`)
	return b.String()
}

func TestSignals_Tick_RunsStagesInOrderAndWritesSummary(t *testing.T) {
	e := newEnv(t)
	e.profile()
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Example Corp raises $14M</title>
<link href="https://news.example.test/a"/><published>2026-10-11T08:00:00Z</published><content>Example Corp raised $14M. The company plans to hire 20 engineers this year.</content></entry></feed>`)
	e.sets.Portals.Companies = []workspace.Company{{Name: "Example Corp", ATS: "greenhouse", Slug: "examplecorp"}}
	e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build things."}, ghJob{2, "Account Executive", "Austin", ""})
	e.brain.fn = func(task, prompt string) (string, bool) {
		switch task {
		case "feed-classify":
			return `{"company":"Example Corp"}`, true
		case "signal-assess":
			return assessReply(prompt, "plans to hire 20 engineers this year", IntentStrong, 0.9, "true", TypeFunding), true
		}
		return "", false
	}
	var lines []string
	sum := e.tick(TickRequest{Progress: func(l string) { lines = append(lines, l) }})
	if sum.Status != TickCompleted || sum.Ingested != 1 || sum.Identified != 1 || sum.Events != 1 || sum.Assess.OK != 1 || len(sum.TierChanges) != 1 || sum.TierChanges[0].To != TierHot ||
		sum.Check.Scanned != 1 || sum.Check.Seen != 2 || sum.Check.Passed != 1 || sum.Handoff.Inserted != 1 || sum.Notified.Hot != 1 || !sum.Notified.Digest {
		t.Fatalf("summary = %+v", sum)
	}
	order := []string{"signals-alerts:", "identified", "assessed", "checked"}
	at := 0
	for _, l := range lines {
		if at < len(order) && strings.Contains(l, strings.TrimSuffix(order[at], ":")) {
			at++
		}
	}
	if at != len(order) {
		t.Fatalf("progress out of order: %v", lines)
	}
	if n := e.count(`SELECT count(*) FROM signal_ticks WHERE status = 'completed'`); n != 1 {
		t.Fatalf("stored ticks = %d", n)
	}
	st, err := e.svc.Status(e.ctx)
	if err != nil || st.LastTick == nil || st.LastTick.Ingested != 1 || st.Tiers[TierHot] != 1 || st.Active != 0 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if n := e.count(`SELECT count(*) FROM inbox WHERE source = 'signals'`); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
}

func TestSignals_Tick_CrashStillWritesReachedCounters(t *testing.T) {
	e := newEnv(t)
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", feedOf(317, "x"))
	e.svc.hook = func(stage string) error {
		if stage == "assess" {
			return errors.New("assessment exploded")
		}
		return nil
	}
	sum, err := e.svc.Tick(e.ctx, TickRequest{})
	if err == nil {
		t.Fatal("the tick did not fail")
	}
	if sum.Status != TickFailed || sum.Ingested != 317 || !strings.Contains(sum.Error, "exploded") {
		t.Fatalf("summary = %+v", sum)
	}
	var status, raw string
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT status, summary_json FROM signal_ticks`).Scan(&status, &raw); err != nil {
		t.Fatal(err)
	}
	if status != TickFailed || !strings.Contains(raw, `"ingested":317`) || !strings.Contains(raw, `"status":"failed"`) {
		t.Fatalf("stored = %s %s", status, raw)
	}
	// A failed tick frees the claim.
	e.svc.hook = nil
	if _, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{SourceHN}}); err != nil {
		t.Fatalf("the next tick: %v", err)
	}
}

func TestSignals_Tick_SingleActiveNamesTheRunning(t *testing.T) {
	e := newEnv(t)
	id, err := e.svc.claimTick(e.ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Tick(e.ctx, TickRequest{})
	var active *ActiveError
	if !errors.As(err, &active) || active.TickID != id || !strings.Contains(err.Error(), fmt.Sprint(id)) {
		t.Fatalf("second start: %v", err)
	}
	// A claim nobody has refreshed for longer than the budget is taken over.
	e.clk.advance(staleTick + time.Minute)
	if _, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{SourceHN}}); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM signal_ticks WHERE status = 'running'`); n != 0 {
		t.Fatalf("running ticks = %d", n)
	}
}

func TestSignals_Tick_SourceSubsetRunsOnlyThose(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring")
	e.keys[KeyX] = "x-" + strings.Repeat("k", 12)
	e.enable(SourceHN, "q")
	e.web.on(xHost, "/twitter", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tweets":[]}`)
	})
	e.brain.fn = func(task, prompt string) (string, bool) { return `{"company":"X"}`, true }
	e.company("Example Corp", raiseBody, epoch.Add(-time.Hour), adapterAlerts) // pending work that a subset must not touch
	e.exec(`UPDATE signal_documents SET identified_at = NULL`)
	sum := e.tick(TickRequest{Sources: []string{SourceX}})
	if len(sum.Sources) != 1 || sum.Sources[0].ID != SourceX || !sum.Limited {
		t.Fatalf("sources = %+v", sum.Sources)
	}
	if len(e.web.requests(hnHost)) != 0 || len(e.web.requests(secHost)) != 0 {
		t.Fatal("another collector ran")
	}
	if e.brain.total() != 0 || len(e.note.notices) != 0 || e.count(`SELECT count(*) FROM signal_assessments`) != 0 || e.count(`SELECT count(*) FROM signal_checks`) != 0 {
		t.Fatal("a limited tick assessed, checked or notified")
	}
	if _, err := ParseSources("signals-x, signals-hn"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSources("signals-rss"); err == nil {
		t.Fatal("an unknown source was accepted")
	}
	if _, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{"nope"}}); err == nil {
		t.Fatal("an unknown source ran")
	}
}

func TestSignals_Collectors_UniformDocsIdempotentIsolated(t *testing.T) {
	e := newEnv(t)
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	e.web.status(feedHost, "/alerts/feeds", http.StatusInternalServerError)
	now := e.clk.now().Unix()
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(hnFixture("1", "Ask HN: Who is hiring? (October 2026)", "", now-10)))
	e.web.body(hnHost, "/api/v1/items/1", "application/json", `{"id":1,"created_at_i":1,"children":[{"id":11,"created_at_i":`+fmt.Sprint(now)+`,"text":"Example Corp | Remote<p>`+strings.Repeat("long ", 30000)+`"}]}`)
	sum := e.tick(TickRequest{Sources: []string{SourceAlerts, SourceHNHiring}})
	byID := map[string]SourceResult{}
	for _, s := range sum.Sources {
		byID[s.ID] = s
	}
	if byID[SourceAlerts].Status != SrcFailed || !strings.Contains(byID[SourceAlerts].Reason, "500") {
		t.Fatalf("alerts = %+v", byID[SourceAlerts])
	}
	if byID[SourceHNHiring].Status != SrcOK || byID[SourceHNHiring].New != 1 {
		t.Fatalf("hn-hiring = %+v", byID[SourceHNHiring])
	}
	var adapter, srcID, url, fetched, title string
	var bodyLen int
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT adapter, source_id, url, fetched_at, title, length(CAST(body AS BLOB)) FROM signal_documents`).Scan(&adapter, &srcID, &url, &fetched, &title, &bodyLen); err != nil {
		t.Fatal(err)
	}
	if adapter != adapterHNHiring || srcID != SourceHNHiring || url == "" || fetched == "" || title == "" || bodyLen > maxBodyBytes {
		t.Fatalf("doc = %s %s %s %s %q %d", adapter, srcID, url, fetched, title, bodyLen)
	}
}

func TestSignals_NoSecondProcessOrDatabase(t *testing.T) {
	e := newEnv(t)
	e.tick()
	dir := filepath.Dir(e.st.Path())
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if !strings.HasPrefix(en.Name(), filepath.Base(e.st.Path())) {
			t.Errorf("a file beside the main database: %s", en.Name())
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, banned := range []string{"sql.Open(", "ListenAndServe", "net.Listen(", "exec.Command"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s contains %s", f, banned)
			}
		}
	}
	rows, _ := e.st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'signal\_%' ESCAPE '\'`)
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if n < 15 {
		t.Fatalf("signal tables = %d", n)
	}
}

func TestSignals_NoSecretsStoredAndSummariesRedacted(t *testing.T) {
	e := newEnv(t)
	email := "alex.secret@example.org"
	key := "x-" + strings.Repeat("k", 14)
	e.sets.Profile.Candidate.Email = email
	e.keys[KeyX] = key
	e.enable(SourceEdgar)
	e.enable(SourceX, "hiring")
	e.web.on(secHost, "/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied for "+email, http.StatusForbidden)
	})
	e.web.on(xHost, "/twitter", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+key, http.StatusInternalServerError)
	})
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1")
	e.web.body(feedHost, "/alerts", "application/atom+xml", feedOf(2, "Contact "+"press@example.org"))
	sum := e.tick()
	raw := fmt.Sprintf("%+v", sum)
	if strings.Contains(raw, email) || strings.Contains(raw, key) {
		t.Fatalf("the summary holds a secret: %s", raw)
	}
	tables, _ := e.st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'signal\_%' ESCAPE '\'`)
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	for _, n := range names {
		rows, err := e.st.Read().Query(`SELECT * FROM ` + n)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			if s := fmt.Sprint(vals...); strings.Contains(s, email) || strings.Contains(s, key) {
				t.Errorf("%s holds a secret: %s", n, s)
			}
		}
		rows.Close()
	}
	// The email reached the SEC host only, in the user agent.
	for _, r := range e.web.requests("") {
		if r.Host != secHost && strings.Contains(r.UA+r.Query+r.Path, email) {
			t.Errorf("the contact email reached %s", r.Host)
		}
	}
}

func TestSignals_IngestFailureKeepsCursorAndValidatorsRetryable(t *testing.T) {
	for _, source := range []string{SourceHN, SourceAlerts} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", source, partial), func(t *testing.T) {
				e := newEnv(t)
				scope := "hiring"
				now := e.clk.now().Unix()
				if source == SourceHN {
					e.enable(source, scope)
					e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(
						hnFixture("1", "Example A hiring", "https://news.example.test/1", now-10),
						hnFixture("2", "Example B hiring", "https://news.example.test/2", now)))
				} else {
					scope = "https://" + feedHost + "/alerts/feeds/1/2"
					e.alertFeed(scope)
					e.web.on(feedHost, "/alerts/feeds", func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("If-None-Match") == `"batch"` {
							w.WriteHeader(http.StatusNotModified)
							return
						}
						w.Header().Set("ETag", `"batch"`)
						w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 08:00:00 GMT")
						fmt.Fprint(w, strings.ReplaceAll(feedOf(2, "hiring"), "news.example.test/0", "news.example.test/2"))
					})
				}
				condition := "1"
				wantStored := 0
				if partial {
					condition = "NEW.url = 'https://news.example.test/2'"
					wantStored = 1
				}
				e.exec("CREATE TRIGGER refuse_document BEFORE INSERT ON signal_documents WHEN " + condition + " BEGIN SELECT RAISE(FAIL, 'owned ingestion refusal'); END")
				sum := e.tick(TickRequest{Sources: []string{source}})
				got := sum.Sources[0]
				if got.Status != SrcFailed || got.Docs != 2 || got.New != wantStored || !strings.Contains(got.Reason, "owned ingestion refusal") || !strings.Contains(sum.Line(), "1 failed sources") {
					t.Errorf("failed source=%+v", got)
				}
				state, err := e.svc.repo.CollectorState(e.ctx, source, scope)
				if err != nil || state.Cursor != 0 || state.ETag != "" || state.LastModified != "" {
					t.Errorf("checkpoint advanced after failed ingest: %+v %v", state, err)
				}
				if got := e.count(`SELECT count(*) FROM signal_documents`); got != wantStored {
					t.Fatalf("stored=%d want=%d", got, wantStored)
				}
				stored, err := e.svc.Status(e.ctx)
				if err != nil || stored.LastTick == nil || stored.LastTick.Sources[0].Status != SrcFailed {
					t.Errorf("persisted summary=%+v error=%v", stored.LastTick, err)
				}
				e.exec("DROP TRIGGER refuse_document")
				retry := e.tick(TickRequest{Sources: []string{source}}).Sources[0]
				if retry.Status != SrcOK || retry.New != 2-wantStored || e.count(`SELECT count(*) FROM signal_documents`) != 2 {
					t.Errorf("retry=%+v documents=%d", retry, e.count(`SELECT count(*) FROM signal_documents`))
				}
				state, err = e.svc.repo.CollectorState(e.ctx, source, scope)
				if err != nil || source == SourceHN && state.Cursor != now || source == SourceAlerts && (state.ETag != `"batch"` || state.LastModified == "") {
					t.Errorf("recovered checkpoint=%+v error=%v", state, err)
				}
				if again := e.tick(TickRequest{Sources: []string{source}}).Sources[0]; again.New != 0 || again.Docs != 0 || again.Status != SrcOK {
					t.Errorf("repeat=%+v", again)
				}
			})
		}
	}
}

func TestSignals_IngestFailureDoesNotStopOtherCollectorsOrLaterStages(t *testing.T) {
	e := newEnv(t)
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", atomFeed)
	now := e.clk.now().Unix()
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(hnFixture("1", "Ask HN: Who is hiring? (October 2026)", "", now-10)))
	e.web.body(hnHost, "/api/v1/items/1", "application/json", fmt.Sprintf(`{"id":1,"children":[{"id":11,"created_at_i":%d,"text":"Example Corp | Remote<p>We are hiring engineers."}]}`, now))
	e.exec(`CREATE TRIGGER refuse_alert BEFORE INSERT ON signal_documents WHEN NEW.source_id = 'signals-alerts' BEGIN SELECT RAISE(FAIL, 'owned alert refusal'); END`)
	var stages []string
	e.svc.hook = func(stage string) error { stages = append(stages, stage); return nil }
	sum := e.tick()
	byID := map[string]SourceResult{}
	for _, src := range sum.Sources {
		byID[src.ID] = src
	}
	if sum.Status != TickCompleted || sum.Ingested != 1 || byID[SourceAlerts].Status != SrcFailed || byID[SourceHNHiring].Status != SrcOK || byID[SourceHNHiring].New != 1 {
		t.Fatalf("summary=%+v", sum)
	}
	if strings.Join(stages, ",") != "identify,assess,score,check,handoff,notify" {
		t.Fatalf("later stages=%v", stages)
	}
	state, err := e.svc.repo.CollectorState(e.ctx, SourceHNHiring, "1")
	if err != nil || state.Cursor != now {
		t.Fatalf("healthy cursor=%+v error=%v", state, err)
	}
}

func TestSignals_TickBudgetIncludesCollector(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Budget = time.Minute
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	observed := make(chan bool, 1)
	e.web.on(feedHost, "/alerts/feeds", func(w http.ResponseWriter, r *http.Request) {
		e.clk.advance(time.Minute)
		select {
		case <-r.Context().Done():
			observed <- true
		case <-time.After(10 * time.Second):
			observed <- false
			fmt.Fprint(w, feedOf(1, "x"))
		}
	})
	sum, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{SourceAlerts}})
	stopped := <-observed
	if err != nil || !stopped || !sum.BudgetReached || sum.Status != TickCompleted {
		t.Fatalf("collector deadline: stopped=%v status=%s budget=%v err=%v", stopped, sum.Status, sum.BudgetReached, err)
	}
}
func TestSignals_InterruptedCheckRemainsDue(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c := e.hot("Example Corp")
	e.identify(c, discovery.Greenhouse, "examplecorp")
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.web.on("", "/v1/boards/examplecorp/jobs", func(w http.ResponseWriter, r *http.Request) { cancel(); <-r.Context().Done() })
	stats, err := e.svc.checkDue(ctx, 0, e.sets, e.clk.now().Add(time.Hour))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("check: %+v %v", stats, err)
	}
	due, _, err := e.svc.repo.dueCompanies(e.ctx, e.clk.now())
	if err != nil || len(due[TierHot]) != 1 {
		t.Fatalf("cancelled company no longer due: due=%v err=%v", due, err)
	}
}

func storedTickSummary(t *testing.T, e *env, id int64) TickSummary {
	t.Helper()
	var status, raw, finished string
	if err := e.st.Read().QueryRowContext(e.ctx, `SELECT status, summary_json, finished_at FROM signal_ticks WHERE id = ?`, id).Scan(&status, &raw, &finished); err != nil {
		t.Fatal(err)
	}
	var out TickSummary
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if status != out.Status || finished == "" || out.FinishedAt.IsZero() {
		t.Fatalf("persisted tick=%s %s", status, raw)
	}
	return out
}

// tickBlockingModel answers task by spending its share of the budget on the
// test clock.
type tickBlockingModel struct {
	inner   Model
	task    string
	clk     *clock
	spend   time.Duration
	stopped bool
}

func (b *tickBlockingModel) Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	if req.Task != b.task {
		return b.inner.Structured(ctx, req, schema, out)
	}
	b.clk.advance(b.spend)
	if b.stopped = ctx.Err() != nil; b.stopped {
		return nil, ctx.Err()
	}
	return nil, errors.New("model never received the tick deadline")
}

func TestSignals_TickBudgetDuringModelKeepsEvidencePending(t *testing.T) {
	for _, task := range []string{taskClassify, taskAssess} {
		t.Run(task, func(t *testing.T) {
			e := newEnv(t)
			e.profile()
			e.disable(SourceHNHiring)
			e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
			e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", atomFeed)
			e.brain.fn = func(task, prompt string) (string, bool) {
				switch task {
				case taskClassify:
					return `{"company":"Example Corp"}`, true
				case taskAssess:
					return assessReply(prompt, "will hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding), true
				}
				return "", false
			}
			e.svc.d.Budget = time.Minute
			blocker := &tickBlockingModel{inner: e.svc.model, task: task, clk: e.clk, spend: e.svc.d.Budget * 3 / 4}
			e.svc.model = blocker
			sum := e.tick()
			if !blocker.stopped || !sum.BudgetReached || sum.Ingested != 1 || sum.Assess.OK != 0 {
				t.Fatalf("model budget=%+v stopped=%v", sum, blocker.stopped)
			}
			if got := storedTickSummary(t, e, sum.TickID); got.Status != TickCompleted || !got.BudgetReached || got.Ingested != 1 {
				t.Fatalf("stored=%+v", got)
			}
			if task == taskClassify && e.count(`SELECT count(*) FROM signal_documents WHERE identified_at IS NULL`) != 1 {
				t.Fatal("unfinished document was settled")
			}
			if e.count(`SELECT count(*) FROM signal_assessments`) != 0 {
				t.Fatal("unfinished assessment was settled")
			}
			e.svc.model = blocker.inner
			e.svc.d.Budget = 0
			if next := e.tick(); next.Assess.OK != 1 || e.count(`SELECT count(*) FROM signal_events`) != 1 {
				t.Fatalf("pending model work did not recover=%+v", next)
			}
		})
	}
}

type tickDigestNotifier struct {
	inner Notifier
	act   func(context.Context) error
}

func (n tickDigestNotifier) Announce(ctx context.Context, notice Notice) (Delivery, error) {
	if notice.Kind == NoticeDigest {
		return Delivery{}, n.act(ctx)
	}
	return n.inner.Announce(ctx, notice)
}
func (n tickDigestNotifier) NeedsYou(ctx context.Context, a Alert) error {
	return n.inner.NeedsYou(ctx, a)
}

func TestSignals_NotificationDeadlineAndLateCancellationKeepCounters(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(fmt.Sprint("budget=", budget), func(t *testing.T) {
			e := newEnv(t)
			e.disable(SourceHNHiring)
			e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
			e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", atomFeed)
			e.svc.model = noRouteModel{}
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			stopped := false
			e.svc.d.Notifier = tickDigestNotifier{inner: e.note, act: func(ctx context.Context) error {
				if budget {
					e.clk.advance(time.Minute)
				} else {
					cancel()
				}
				if stopped = ctx.Err() != nil; stopped {
					return ctx.Err()
				}
				return errors.New("notification never received the tick deadline")
			}}
			if budget {
				e.svc.d.Budget = time.Minute
			}
			sum, err := e.svc.Tick(ctx, TickRequest{})
			if !stopped || sum.Ingested != 1 || sum.Notified.Digest {
				t.Fatalf("notification=%+v stopped=%v error=%v", sum, stopped, err)
			}
			want := TickFailed
			if budget {
				want = TickCompleted
				if err != nil || !sum.BudgetReached {
					t.Fatalf("budget=%+v %v", sum, err)
				}
			} else if !errors.Is(err, context.Canceled) || sum.BudgetReached {
				t.Fatalf("cancel=%+v %v", sum, err)
			}
			if got := storedTickSummary(t, e, sum.TickID); got.Status != want || got.Ingested != 1 || got.BudgetReached != budget {
				t.Fatalf("stored=%+v", got)
			}
			e.svc.d.Notifier = e.note
			e.svc.d.Budget = 0
			if next := e.tick(); !next.Notified.Digest {
				t.Fatalf("digest retry was consumed=%+v", next)
			}
		})
	}
}

func TestSignals_FinalSummaryFailureIsNotReturnedAsCompleted(t *testing.T) {
	e := newEnv(t)
	e.exec(`CREATE TRIGGER refuse_tick_finish BEFORE UPDATE ON signal_ticks WHEN NEW.status <> 'running' BEGIN SELECT RAISE(FAIL, 'owned tick finish refusal'); END`)
	sum, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{SourceHN}})
	if err == nil || !strings.Contains(err.Error(), "owned tick finish refusal") || sum.Status != TickFailed || sum.Error == "" {
		t.Fatalf("finalization=%+v %v", sum, err)
	}
}

func TestSignals_DeadlineKeepsIndependentFailure(t *testing.T) {
	for _, parentDeadline := range []bool{false, true} {
		for _, joined := range []bool{true} {
			t.Run(fmt.Sprintf("parent=%v/joined=%v", parentDeadline, joined), func(t *testing.T) {
				e := newEnv(t)
				e.exec(`CREATE TRIGGER refuse_audit BEFORE INSERT ON signal_audit BEGIN SELECT RAISE(FAIL, 'owned independent audit refusal'); END`)
				ctx := e.ctx
				e.svc.d.Budget = time.Minute
				if parentDeadline {
					e.svc.d.Budget = time.Hour
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
					defer cancel()
				}
				e.svc.d.Settings = func(ctx context.Context) (Settings, error) {
					err := e.svc.repo.Audit(ctx, "test", 0, "synthetic")
					if err == nil || !strings.Contains(err.Error(), "owned independent audit refusal") {
						t.Fatalf("missing real refusal: %v", err)
					}
					if !parentDeadline {
						e.clk.advance(time.Minute)
					}
					<-ctx.Done()
					if joined {
						err = errors.Join(err, fmt.Errorf("stage stopped: %w", ctx.Err()))
					}
					return Settings{}, err
				}
				sum, err := e.svc.Tick(ctx, TickRequest{Sources: []string{SourceHN}})
				if err == nil || !strings.Contains(err.Error(), "owned independent audit refusal") || sum.Status != TickFailed || sum.BudgetReached == parentDeadline {
					t.Fatalf("independent storage failure masked: summary=%+v error=%v", sum, err)
				}
				if parentDeadline && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("parent cause lost: %v", err)
				}
				if got := storedTickSummary(t, e, sum.TickID); got.Status != TickFailed || !strings.Contains(got.Error, "owned independent audit refusal") {
					t.Fatalf("stored=%+v", got)
				}
			})
		}
	}
}

// assessHoldModel answers every call through inner except an assessment of
// a prompt that names hold, which spends the model stages' share of the budget
// on the test clock once the assessments running beside it are stored
// (assessments run several at a time, and the spent budget ends them all).
type assessHoldModel struct {
	inner  Model
	hold   string
	clk    *clock
	spend  time.Duration
	stored func() bool
}

func (m assessHoldModel) Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	if req.Task != taskAssess || !strings.Contains(req.Prompt, m.hold) {
		return m.inner.Structured(ctx, req, schema, out)
	}
	for end := time.Now().Add(30 * time.Second); !m.stored() && time.Now().Before(end); {
		time.Sleep(time.Millisecond)
	}
	m.clk.advance(m.spend)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.New("model never received the tick deadline")
}

// The model stages leave the end of the budget to the later ones: a held
// assessment that would run past the budget still leaves the company it did
// assess its tier, and a hot company's passing posting reaches the inbox.
func TestSignals_TickBudgetDuringAssessmentStillTiersChecksAndHandsOff(t *testing.T) {
	e := newEnv(t)
	gamma := e.readyCompany("Gamma Corp", "gammacorp", ghJob{1, "Software Engineer", "Remote - US", "Build."})
	e.disable(SourceHNHiring)
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	e.web.body(feedHost, "/alerts/feeds", "application/atom+xml", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry>
<title>Example Corp raises $14M</title><link href="https://news.example.test/a"/><published>2026-10-10T08:00:00Z</published>
<content>Example Corp raised and will hire 20 engineers.</content></entry><entry>
<title>Beta Corp raises $9M</title><link href="https://news.example.test/b"/><published>2026-10-10T09:00:00Z</published>
<content>Beta Corp raised and will hire 10 engineers.</content></entry></feed>`)
	e.brain.fn = func(task, prompt string) (string, bool) {
		switch task {
		case taskClassify:
			if strings.Contains(prompt, "Beta Corp") {
				return `{"company":"Beta Corp"}`, true
			}
			return `{"company":"Example Corp"}`, true
		case taskAssess:
			return assessReply(prompt, "will hire 20 engineers", IntentStrong, 0.9, "true", TypeFunding), true
		}
		return "", false
	}
	e.svc.d.Budget = time.Minute
	before := e.count(`SELECT count(*) FROM signal_assessments`)
	stored := func() bool {
		var n int
		err := e.st.Read().QueryRowContext(e.ctx, `SELECT count(*) FROM signal_assessments`).Scan(&n)
		return err == nil && n > before
	}
	e.svc.model = assessHoldModel{inner: e.svc.model, hold: "Beta Corp", clk: e.clk, spend: e.svc.d.Budget * 3 / 4, stored: stored}
	sum := e.tick()
	if sum.Status != TickCompleted || !sum.BudgetReached || sum.Assess.OK != 1 || sum.Assess.Pending != 1 {
		t.Fatalf("summary=%+v", sum)
	}
	if n := e.count(`SELECT count(*) FROM signal_tiers WHERE company_id <> ?`, gamma.ID); n != 1 {
		t.Fatalf("no tier for the assessed company; companies=%d", e.count(`SELECT count(*) FROM signal_companies`))
	}
	if n := e.count(`SELECT count(*) FROM inbox WHERE source = 'signals'`); n != 1 {
		t.Fatalf("%d signal postings in the inbox; check %+v, handoff %+v", n, sum.Check, sum.Handoff)
	}
	// Gamma Corp's assessment and Example Corp's; Beta Corp's is still pending.
	if e.count(`SELECT count(*) FROM signal_assessments`) != 2 {
		t.Fatal("the held assessment was settled")
	}
}
