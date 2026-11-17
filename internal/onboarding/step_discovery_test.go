package onboarding

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/signals"
	"applysmith/internal/workspace"
)

func discoveryManager(t *testing.T, env *Env) *scheduler.Manager {
	t.Helper()
	reg, err := jobs.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	return scheduler.NewManager(env.Store, reg, env.now)
}

func scheduleNames(t *testing.T, env *Env) map[string]scheduler.Schedule {
	t.Helper()
	all, err := scheduler.NewRepo(env.Store, env.now).List(bg())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]scheduler.Schedule{}
	for _, s := range all {
		out[s.Name] = s
	}
	return out
}

func writePortals(t *testing.T, env *Env, body string) {
	t.Helper()
	cur, err := env.Workspace.Read(workspace.FilePortals)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Workspace.Write(workspace.FilePortals, []byte(body), cur.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestSetupStep15_NoneChosenCompletesTheStepAndCreatesNothing(t *testing.T) {
	env := setupEnv(t)
	res := mustApply(t, env, "discovery", `{"pack":"none"}`)
	if res.Detection.Status != StepComplete || len(scheduleNames(t, env)) != 0 {
		t.Fatalf("none = %+v, schedules %v", res.Detection, scheduleNames(t, env))
	}
}

func TestSetupStep15_DailyPackStaggersEnabledSourcesAndReplacesOnSwitch(t *testing.T) {
	env := setupEnv(t)
	writePortals(t, env, "version: 1\ncompanies:\n  - name: Example Co\n    ats: greenhouse\n    slug: example\n")
	mgr := discoveryManager(t, env)
	if _, err := mgr.Create(bg(), scheduler.NewSchedule{Name: "Mine", Kind: "cost.report", Preset: scheduler.Preset{Kind: scheduler.PresetDaily, Time: "09:00", Timezone: "UTC"}}); err != nil {
		t.Fatal(err)
	}

	res := mustApply(t, env, "discovery", `{"pack":"daily"}`)
	if res.Detection.Status != StepComplete {
		t.Fatalf("daily = %+v", res.Detection)
	}
	got := scheduleNames(t, env)
	reg, trk := got["Daily scan · Company job boards"], got["Daily scan · Tracked companies"]
	if reg.Preset.Time != "07:00" || trk.Preset.Time != "07:10" || reg.PackID != "daily" || len(got) != 4 {
		t.Fatalf("daily pack: %+v", got)
	}

	mustApply(t, env, "discovery", `{"pack":"twice-daily"}`)
	got = scheduleNames(t, env)
	times := map[string]bool{}
	for _, s := range got {
		if s.PackID == "daily" {
			t.Errorf("switching packs left a daily schedule: %+v", s)
		}
		if s.PackID == "twice-daily" {
			times[s.Preset.Time] = true
		}
	}
	for _, want := range []string{"07:00", "07:10", "17:00", "17:10"} {
		if !times[want] {
			t.Errorf("twice-daily lacks %s: %v", want, times)
		}
	}
	if _, ok := got["Mine"]; !ok || got["Mine"].PackID != "" {
		t.Errorf("the user's own schedule changed: %+v", got["Mine"])
	}

	mustApply(t, env, "discovery", `{"pack":"none"}`)
	if left := scheduleNames(t, env); len(left) != 1 || left["Mine"].Name == "" {
		t.Fatalf("choosing none must remove only the packs' schedules: %v", left)
	}
}

func TestSetupStep15_RejectsAnUnofferedPackUnknownFieldsAndWritesNothingOnDryRun(t *testing.T) {
	env := setupEnv(t)
	for _, body := range []string{`{"pack":"maintenance"}`, `{"pack":""}`, `{}`, `{"pack":"daily","extra":1}`} {
		if _, err := apply(t, env, "discovery", body, false); err == nil {
			t.Errorf("%s was accepted", body)
		}
	}
	res, err := apply(t, env, "discovery", `{"pack":"daily"}`, true)
	if err != nil || !res.DryRun || len(scheduleNames(t, env)) != 0 {
		t.Fatalf("dry run: %+v %v %v", res, err, scheduleNames(t, env))
	}
	if d := status(t, env, "discovery"); d.Status == StepComplete {
		t.Fatalf("a dry run recorded the choice: %+v", d)
	}
}

func TestSetupStep15_InteractiveOfferShowsPacksAndAppliesTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		answer     string
		wantStatus StepStatus
		wantCount  int
	}{{"daily", StepComplete, 2}, {"none", StepComplete, 0}, {"skip", StepSkipped, 0}, {"", StepSkipped, 0}} {
		env := setupEnv(t)
		var out bytes.Buffer
		f := &Flow{Env: env, In: NewLinePrompter(strings.NewReader(tc.answer+"\nno\n"), &out), Out: &out}
		if err := f.offerStep(bg(), mustStep("discovery")); err != nil {
			t.Fatalf("%q: %v", tc.answer, err)
		}
		if !strings.Contains(out.String(), "twice-daily") || !strings.Contains(out.String(), "decide later") {
			t.Errorf("%q: the offer does not show the packs: %s", tc.answer, out.String())
		}
		if got := status(t, env, "discovery").Status; got != tc.wantStatus || len(scheduleNames(t, env)) != tc.wantCount {
			t.Errorf("%q: status %s, schedules %v", tc.answer, got, scheduleNames(t, env))
		}
	}
}

// "Use my target roles" needs target roles; a dry run lists what it would
// write and writes nothing, and the terminal offer shows the same lines before
// asking, leaves the file alone on no and writes it on yes.
func TestSetupStep15_UseMyTargetRolesPreviewsThenWrites(t *testing.T) {
	_, err := apply(t, setupEnv(t), "discovery", `{"pack":"none","use_target_roles":true}`, false)
	if fe, ok := err.(FieldErrors); !ok || fe["use_target_roles"] == "" {
		t.Fatalf("without target roles: %v", err)
	}
	ready := func() (*Env, func() []byte) {
		env := setupEnv(t)
		mustApply(t, env, "targets", `{"roles":[{"title":"Backend Engineer","seniority":"senior"}],"locations":["Leeds"]}`)
		writePortals(t, env, "version: 1\nsources:\n  indeed:\n    enabled: true\n")
		return env, func() []byte {
			cur, err := env.Workspace.Read(workspace.FilePortals)
			if err != nil {
				t.Fatal(err)
			}
			return cur.Data
		}
	}
	offer := func(env *Env, answer string) string {
		var out bytes.Buffer
		f := &Flow{Env: env, In: NewLinePrompter(strings.NewReader("none\n"+answer+"\nno\n"), &out), Out: &out}
		if err := f.offerStep(bg(), mustStep("discovery")); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	env, portals := ready()
	before := portals()
	dry, err := apply(t, env, "discovery", `{"pack":"none","use_target_roles":true}`, true)
	if err != nil || len(dry.Notes) == 0 || !bytes.Equal(portals(), before) {
		t.Fatalf("dry run: %+v %v\n%s", dry.Notes, err, portals())
	}
	shown := offer(env, "no")
	for _, line := range dry.Notes {
		if !strings.Contains(shown, line) {
			t.Errorf("the offer does not show %q: %s", line, shown)
		}
	}
	if !bytes.Equal(portals(), before) {
		t.Fatalf("declining wrote portals.yml:\n%s", portals())
	}
	env, portals = ready()
	offer(env, "yes\nno")
	if p, rep := workspace.ParsePortals(portals()); !rep.OK() || !slices.Contains(p.Filters.Title.Positive, "Backend Engineer") || len(p.Sources) != 1 {
		t.Fatalf("portals.yml after yes: %s", portals())
	}

	// Turning on the open boards gives each the saved search, and the pack,
	// read after the edit, schedules them.
	env, portals = ready()
	writePortals(t, env, "version: 1\nsources: {}\n")
	if _, err := apply(t, env, "discovery", `{"pack":"none","enable_boards":true}`, false); err == nil {
		t.Fatal("enable_boards without use_target_roles was accepted")
	}
	mustApply(t, env, "discovery", `{"pack":"daily","use_target_roles":true,"enable_boards":true}`)
	p, rep := workspace.ParsePortals(portals())
	if !rep.OK() {
		t.Fatal(rep.String())
	}
	for _, b := range discovery.OpenBoards() {
		if q := p.Sources[b.Name].Queries; len(q) != 1 || q[0].Value != "Backend Engineer" {
			t.Errorf("%s: queries %v\n%s", b.Name, q, portals())
		}
	}
	if _, ok := scheduleNames(t, env)["Daily scan · Indeed"]; !ok {
		t.Errorf("no schedule scans Indeed: %v", slices.Collect(maps.Keys(scheduleNames(t, env))))
	}
}

func TestSetupStep15_AScriptThatRunsOutLeavesTheOptionalStepAlone(t *testing.T) {
	env := setupEnv(t)
	var out bytes.Buffer
	f := &Flow{Env: env, In: NewLinePrompter(strings.NewReader(""), &out), Out: &out}
	if err := f.offerStep(context.Background(), mustStep("discovery")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "left for later") || len(scheduleNames(t, env)) != 0 || status(t, env, "discovery").Status != StepPartial {
		t.Fatalf("out=%q status=%+v", out.String(), status(t, env, "discovery"))
	}
}

func TestDoctor_DiscoverySchedulesWarnsWithoutAScheduleAndPassesWithOne(t *testing.T) {
	env := setupEnv(t)
	run := func() Result {
		rep, err := Run(bg(), Checks(), env, Options{Only: "discovery.schedules"})
		if err != nil || len(rep.Results) != 1 {
			t.Fatalf("%v %+v", err, rep)
		}
		return rep.Results[0]
	}
	if r := run(); r.Status != Warn || !strings.Contains(r.Fix, "applysmith schedule apply-pack") {
		t.Fatalf("no schedule: %+v", r)
	}
	mustApply(t, env, "discovery", `{"pack":"daily"}`)
	if r := run(); r.Status != Pass {
		t.Fatalf("with a schedule: %+v", r)
	}
}

type listTransport struct{ fail bool }

func (l listTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if l.fail {
		return nil, errors.New("offline")
	}
	body := `["alpha-co","beta-co"]`
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestSetupStep15_SeedRegistryFetchesTheListsAndRecordsTheSource(t *testing.T) {
	env := setupEnv(t)
	env.SeedHTTP = &http.Client{Transport: listTransport{}}
	res := mustApply(t, env, "discovery", `{"pack":"none","seed_registry":true}`)
	if notes := strings.Join(res.Notes, "\n"); !strings.Contains(notes, "2 rows, 2 new") {
		t.Fatalf("notes = %q", notes)
	}
	var n int
	if err := env.Store.Read().QueryRow(`SELECT count(*) FROM data_sources WHERE kind = 'registry'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d data_sources rows, %v", n, err)
	}
	var source string
	if err := env.Store.Read().QueryRow(`SELECT source FROM ats_registry WHERE slug = 'alpha-co' AND ats = 'greenhouse'`).Scan(&source); err != nil || !strings.Contains(source, "greenhouse_companies.json (") {
		t.Fatalf("source = %q, %v", source, err)
	}
}

// The note says where to seed again for its reader: the dashboard (the app's
// window included) names its own seed and no command, the terminal the
// command (flow round 6).
func TestSetupStep15_SeedRegistryFailureIsANoteNotAnError(t *testing.T) {
	dashboard := setupEnv(t)
	dashboard.InServer = true
	dashboard.SeedHTTP = &http.Client{Transport: listTransport{fail: true}}
	notes := strings.Join(mustApply(t, dashboard, "discovery", `{"pack":"none","seed_registry":true}`).Notes, "\n")
	if !strings.Contains(notes, "registry list not fetched") || !strings.Contains(notes, "Discovery, Sources and portals") || strings.Contains(notes, "`applysmith") {
		t.Fatalf("dashboard notes = %q", notes)
	}
	env := setupEnv(t)
	env.SeedHTTP = &http.Client{Transport: listTransport{fail: true}}
	res := mustApply(t, env, "discovery", `{"pack":"none","seed_registry":true}`)
	if notes := strings.Join(res.Notes, "\n"); !strings.Contains(notes, "registry list not fetched") || !strings.Contains(notes, "applysmith registry seed") {
		t.Fatalf("notes = %q", notes)
	}
	rep, err := Run(bg(), Checks(), env, Options{Only: "discovery.registry"})
	if err != nil || len(rep.Results) != 1 || rep.Results[0].Status != Warn || !strings.Contains(rep.Results[0].Fix, "applysmith registry seed") {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

func TestDoctor_RegistryPassesOnceSeeded(t *testing.T) {
	env := setupEnv(t)
	env.SeedHTTP = &http.Client{Transport: listTransport{}}
	mustApply(t, env, "discovery", `{"pack":"none","seed_registry":true}`)
	rep, err := Run(bg(), Checks(), env, Options{Only: "discovery.registry"})
	if err != nil || rep.Results[0].Status != Pass {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
}

func TestSetupStep15_EdgarIsOffUntilTheDisclosureIsAcknowledgedAndNeedsAnEmail(t *testing.T) {
	env := setupEnv(t)
	portalsHas := func() bool {
		cur, _ := env.Workspace.Read(workspace.FilePortals)
		p, _ := workspace.ParsePortals(cur.Data)
		s, ok := p.Sources[signals.SourceEdgar]
		return ok && s.Enabled != nil && *s.Enabled
	}
	_, err := apply(t, env, "discovery", `{"pack":"none","enable_edgar":true}`, false)
	var fe FieldErrors
	if !errors.As(err, &fe) || !strings.Contains(fe["acknowledge_edgar_disclosure"], signals.EdgarDisclosure) || portalsHas() {
		t.Fatalf("without the acknowledgement: %v", err)
	}
	if _, err := apply(t, env, "discovery", `{"pack":"none","acknowledge_edgar_disclosure":true}`, false); err == nil {
		t.Error("an acknowledgement without enable_edgar was accepted")
	}
	// setupEnv has no profile email yet: the source stays off with the reason.
	_, err = apply(t, env, "discovery", `{"pack":"none","enable_edgar":true,"acknowledge_edgar_disclosure":true}`, false)
	if !errors.As(err, &fe) || !strings.Contains(fe["enable_edgar"], "needs contact email") || portalsHas() {
		t.Fatalf("without an email: %v", err)
	}
}

func TestSetupStep15_EdgarTurnsOnWithAnEmailAndTheAcknowledgement(t *testing.T) {
	env := setupEnv(t)
	if _, err := editProfile(env, []workspace.Op{setOp("candidate.email", "riley.tester"+"@"+"mailbox-test.dev")}, false); err != nil {
		t.Fatal(err)
	}
	res, err := apply(t, env, "discovery", `{"pack":"none","enable_edgar":true,"acknowledge_edgar_disclosure":true}`, false)
	if err != nil || !strings.Contains(strings.Join(res.Notes, " "), signals.EdgarDisclosure) {
		t.Fatalf("%+v %v", res, err)
	}
	cur, _ := env.Workspace.Read(workspace.FilePortals)
	p, _ := workspace.ParsePortals(cur.Data)
	if s := p.Sources[signals.SourceEdgar]; s.Enabled == nil || !*s.Enabled {
		t.Fatalf("portals sources = %+v", p.Sources)
	}
}

func doctorLeaf(t *testing.T, env *Env, id string) Result {
	t.Helper()
	rep, err := Run(bg(), Checks(), env, Options{Only: id})
	if err != nil || len(rep.Results) != 1 {
		t.Fatalf("%s: %+v, %v", id, rep, err)
	}
	return rep.Results[0]
}

func TestDoctor_ScheduleHygieneNamesTwinsAndChangesNothing(t *testing.T) {
	env := setupEnv(t)
	if r := doctorLeaf(t, env, "discovery.schedule-hygiene"); r.Status != Pass {
		t.Fatalf("no schedules: %+v", r)
	}
	mgr := discoveryManager(t, env)
	for _, name := range []string{"Cost report", "cost report"} {
		if _, err := mgr.Create(bg(), scheduler.NewSchedule{Name: name, Kind: "cost.report", Preset: scheduler.Preset{Kind: scheduler.PresetDaily, Time: "09:00", Timezone: "UTC"}}); err != nil {
			t.Fatal(err)
		}
	}
	r := doctorLeaf(t, env, "discovery.schedule-hygiene")
	if r.Status != Warn || r.Required || !strings.Contains(r.Summary, "duplicate") || !strings.Contains(r.Summary, "Cost report") || !strings.Contains(r.Fix, "applysmith schedule") {
		t.Fatalf("twins: %+v", r)
	}
	if got := scheduleNames(t, env); len(got) != 2 {
		t.Fatalf("the check changed the schedules: %v", got)
	}
}

func TestDoctor_ScheduleAuditComparesTodaysSlotsWithTheRecordedDecisions(t *testing.T) {
	env := setupEnv(t)
	if r := doctorLeaf(t, env, "discovery.schedule-audit"); r.Status != Pass || !strings.Contains(r.Summary, "no occurrence") {
		t.Fatalf("no schedules: %+v", r)
	}
	at := time.Date(2026, 5, 4, 6, 0, 0, 0, time.Local)
	env.Now = func() time.Time { return at }
	mgr := discoveryManager(t, env)
	sc, err := mgr.Create(bg(), scheduler.NewSchedule{Name: "Hourly cost", Kind: "cost.report",
		Preset: scheduler.Preset{Kind: scheduler.PresetRRule, RRule: "FREQ=HOURLY;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(6*time.Hour + 30*time.Minute)
	r := doctorLeaf(t, env, "discovery.schedule-audit")
	if r.Status != Warn || !strings.Contains(r.Summary, "0 ran") || !strings.Contains(r.Summary, "missing") || !strings.Contains(r.Summary, "Hourly cost") || !strings.Contains(r.Fix, "applysmith schedule history") {
		t.Fatalf("nothing decided: %+v", r)
	}
	// Decide every missing slot: the audit passes.
	audit, err := scheduler.NewRepo(env.Store, env.now).DayAudit(bg(), at, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	repo := scheduler.NewRepo(env.Store, env.now)
	for _, slot := range audit.Totals().Missing {
		cur, err := repo.Get(bg(), sc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ClaimRun(bg(), scheduler.Claim{Schedule: cur, ScheduledFor: slot}); err != nil {
			t.Fatal(err)
		}
	}
	if r := doctorLeaf(t, env, "discovery.schedule-audit"); r.Status != Pass || !strings.Contains(r.Summary, "0 missing") {
		t.Fatalf("all decided: %+v", r)
	}
}
