//go:build browser

// Command replay loads the stop snapshot of every stopped apply run into a
// throwaway headless Chrome at the run's own address, with no network, no
// site script and every form control blank, fills it once with the current
// engine and the workspace's answer bank and profile, and records which
// required controls the build answers now. It is a developer tool, like
// cmd/measure: it opens the workspace database read-only, writes only under
// <workspace>/.applysmith/replay/, and its output holds labels, kinds, reason
// codes and counts, never a value. See docs/development.md, "Form replay".
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	applaunch "applysmith/internal/launch"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// schemaVersion is the version of results.json; a baseline of another
// version is refused.
const schemaVersion = 1

// Field statuses. A left field's status is statusLeft followed by its reason
// code.
const (
	statusAnswered  = "answered"
	statusLeft      = "left:"
	statusNeedsLive = "needs-live-page"
)

func main() {
	wsFlag := flag.String("workspace", "", "the workspace (default: the one the app uses)")
	baseline := flag.String("baseline", "", "an earlier results.json to compare with; a regression exits 1")
	only := flag.String("only", "", "comma-separated run ids to replay (default: every stop snapshot)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root, err := workspace.Resolve(workspace.ResolveOptions{Flag: *wsFlag})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(2)
	}
	var ids []string
	for _, id := range strings.Split(*only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	res, err := replay(ctx, root, ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(2)
	}
	dir := filepath.Join(workspace.Open(root).StateDir(), "replay", time.Now().UTC().Format("20060102T150405Z"))
	code, err := finish(os.Stdout, dir, res, *baseline)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

// Results is results.json.
type Results struct {
	Schema    int    `json:"schema"`
	Generated string `json:"generated"`
	// Engine is the engine bundle's stamp; Revision the source revision the
	// tool was built from, when the build recorded one.
	Engine   string      `json:"engine"`
	Revision string      `json:"revision,omitempty"`
	Runs     []RunResult `json:"runs"`
}

// RunResult is the replay of one stopped run's last page.
type RunResult struct {
	RunID string `json:"runId"`
	ATS   string `json:"ats"`
	// OriginalClass is the stopped run's outcome class; OriginalLeft counts
	// the required controls its reason names as not answered.
	OriginalClass string `json:"originalClass"`
	OriginalLeft  int    `json:"originalLeft"`
	// Error is the stage code of a replay that did not finish.
	Error  string  `json:"error,omitempty"`
	Fields []Field `json:"fields"`
	// NowAnswered are the labels the original run left that this build answers.
	NowAnswered []string `json:"nowAnswered,omitempty"`
	// Blocked counts the requests of the page that were refused.
	Blocked int `json:"blockedRequests"`

	// refused are the addresses refused, for the test; never written.
	refused []string
}

// Field is one required control of the replayed page.
type Field struct {
	Label  string `json:"label"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status"`
}

// replayCase is one stopped run with its stop snapshot.
type replayCase struct {
	runID, ats, url, class string
	snapshot               string
	left                   []string
}

// replay replays every stop snapshot of the workspace that has its run row.
func replay(ctx context.Context, root string, only []string) (Results, error) {
	ws := workspace.Open(root)
	st, err := store.OpenReadOnly(ctx, filepath.Join(ws.StateDir(), store.DBFile))
	if err != nil {
		return Results{}, err
	}
	defer st.Close()
	cases, err := loadCases(ctx, st, filepath.Join(ws.StateDir(), "apply", "snapshots"), only)
	if err != nil {
		return Results{}, err
	}
	res := Results{Schema: schemaVersion, Generated: time.Now().UTC().Format(time.RFC3339), Engine: engine.Stamp(), Revision: revision(), Runs: []RunResult{}}
	if len(cases) == 0 {
		return res, nil
	}
	tmp, err := os.MkdirTemp("", "applysmith-replay-")
	if err != nil {
		return Results{}, err
	}
	defer os.RemoveAll(tmp)
	inst, err := launch(ctx, tmp)
	if err != nil {
		return Results{}, err
	}
	defer inst.Stop(context.WithoutCancel(ctx))
	runner := newRunner(ws, st, filepath.Join(tmp, "uploads"))
	lane := inst.Lane(browser.LaneApply)
	for _, c := range cases {
		if ctx.Err() != nil {
			return Results{}, ctx.Err()
		}
		res.Runs = append(res.Runs, replayOne(ctx, runner, lane, c))
	}
	return res, nil
}

// leftRE reads one required control the check left from a run's outcome
// reason (gates.SubmitReadiness writes the label quoted).
var leftRE = lazyregexp.New(`page \d+ ("(?:[^"\\]|\\.)*"): required and not answered`)

// loadCases pairs each stop snapshot with its run row. The page the run
// stopped on is the last page its record names, else the posting.
func loadCases(ctx context.Context, st *store.Store, dir string, only []string) ([]replayCase, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*-stop.html"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []replayCase
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), "-stop.html")
		if len(only) > 0 && !slices.Contains(only, id) {
			continue
		}
		c := replayCase{runID: id, snapshot: f}
		var pages, reason string
		err := st.Read().QueryRowContext(ctx, `SELECT ats, posting_url, pages_json, outcome_class, outcome_reason FROM apply_runs WHERE run_id = ?`, id).
			Scan(&c.ats, &c.url, &pages, &c.class, &reason)
		if err != nil {
			continue
		}
		var prs []struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(pages), &prs)
		for i := len(prs) - 1; i >= 0; i-- {
			if prs[i].URL != "" {
				c.url = prs[i].URL
				break
			}
		}
		for _, m := range leftRE.FindAllStringSubmatch(reason, -1) {
			if l, err := strconv.Unquote(m[1]); err == nil {
				c.left = append(c.left, l)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// launch starts the throwaway headless Chrome. No host resolves in it but
// loopback, so even a request that escaped the tab's interception, or one the
// browser makes on its own, cannot leave the machine. Loopback is left to the
// interception alone, which refuses it like any other address, so a test can
// watch the interception work.
func launch(ctx context.Context, root string) (*browser.Instance, error) {
	chrome, err := browser.DiscoverChrome(ctx, os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("no usable Google Chrome: %w", err)
	}
	inst, err := browser.Open(ctx, browser.Options{
		Chrome: chrome, ProfileDir: filepath.Join(root, "profile"), StateDir: filepath.Join(root, "state"),
		DownloadsDir: filepath.Join(root, "downloads"), Environ: os.Environ(),
	}.WithHostRules("MAP * ~NOTFOUND, EXCLUDE 127.0.0.1").WithHeadless())
	if err != nil {
		return nil, err
	}
	watchdog(inst.PID())
	return inst, nil
}

// watchdog kills the browser's process group once this process is gone, so a
// replay that is killed never leaves its Chrome behind.
func watchdog(chromePID int) {
	if chromePID <= 1 {
		return
	}
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 1; done; kill -9 -%d 2>/dev/null; kill -9 %d 2>/dev/null", os.Getpid(), chromePID, chromePID)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

// newRunner is the apply runner over the workspace's answer bank and profile,
// wired as the server wires the fill of a tab, without what reaches beyond
// the workspace files: no model, no board read, no documents, no learning
// upload, no tracker. The store is read-only, so nothing a run would record
// is written.
func newRunner(ws *workspace.Workspace, st *store.Store, stage string) *apply.Runner {
	bank := answerbank.New(ws, st)
	matches := &apply.MatchLog{}
	matcher := bind.New(bind.Deps{Bank: bank, Workspace: ws, Observe: matches.Observe})
	page := apply.PageDeps{
		Applicant: func(context.Context) (json.RawMessage, error) {
			ap, err := bank.Applicant(time.Now())
			if err != nil {
				return nil, err
			}
			return json.Marshal(api.ApplicantSnapshot(ap))
		},
		Matcher: matcher, Matches: matches, Pin: matcher.Pinned, OwnSearch: matcher.OwnSearch,
		Facts: func(context.Context) (match.Facts, error) {
			p, ok := bank.Profile()
			if !ok {
				return match.Facts{}, errors.New("the profile cannot be read")
			}
			return bind.FactsFromProfile(p), nil
		},
		Own: func(context.Context) (apply.Own, error) { return api.OwnFacts(ws, bank, time.Now()), nil },
		Settings: func() apply.Settings {
			return apply.Settings{AutoAcceptLegalDeclarations: bank.Settings(context.Background()).AutoAcceptLegalDeclarations}
		},
		MainWorld: true,
		StageDir:  stage,
	}
	return apply.New(apply.Deps{Store: st, Build: "replay", Page: page})
}

// offline serves one snapshot at one address and refuses every other request.
type offline struct {
	page *url.URL
	body []byte

	mu sync.Mutex
	// documents counts the document requests, served or refused: a second
	// one after the page loaded is a navigation or a submission.
	documents int
	refused   []string
}

// pageCSP keeps every script of the site from running, inline handlers and
// javascript: addresses included; the engine is evaluated through the
// browser's own protocol, which a page's policy does not govern.
const pageCSP = "script-src 'none'; object-src 'none'"

func (o *offline) serve(r browser.OfflineRequest) (browser.OfflineReply, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if r.Type == "Document" {
		o.documents++
	}
	if u, err := url.Parse(r.URL); err == nil && r.Method == "GET" && sameDocument(u, o.page) {
		return browser.OfflineReply{Status: 200, Headers: map[string]string{"Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": pageCSP}, Body: o.body}, true
	}
	o.refused = append(o.refused, r.URL)
	return browser.OfflineReply{}, false
}

func (o *offline) counts() (int, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.documents, slices.Clone(o.refused)
}

func sameDocument(a, b *url.URL) bool {
	pa, pb := cmpPath(a.EscapedPath()), cmpPath(b.EscapedPath())
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && pa == pb && a.RawQuery == b.RawQuery
}

func cmpPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// stripRE are the elements removed from a snapshot before it is served:
// scripts, links (a preconnect or prefetch would open a connection no
// interception sees) and meta directives (a refresh would navigate).
var stripRE = []*lazyregexp.Regexp{
	lazyregexp.New(`(?is)<script\b[^>]*>.*?</script\s*>`),
	lazyregexp.New(`(?is)<script\b[^>]*>`),
	lazyregexp.New(`(?is)<link\b[^>]*>`),
	lazyregexp.New(`(?is)<meta\b[^>]*http-equiv[^>]*>`),
}

func strip(html []byte) []byte {
	for _, re := range stripRE {
		html = re.ReplaceAll(html, nil)
	}
	return html
}

// blankJS removes what is left of the site's handlers and empties every form
// control, attribute and property both, and turns off every scripted choice
// (a pressed or checked ARIA button), so the fill starts from a fresh form and
// nothing the stopped run left in the copy is read. Button labels stay.
const blankJS = `(()=>{
for(const el of document.querySelectorAll('*'))for(const a of [...el.attributes])if(/^on/i.test(a.name))el.removeAttribute(a.name);
for(const a of document.querySelectorAll('[href^="javascript:" i]'))a.removeAttribute('href');
const label=new Set(['submit','button','reset','image']);let n=0;
for(const i of document.querySelectorAll('input')){const t=(i.getAttribute('type')||'').toLowerCase();if(label.has(t))continue;n++;
if(t==='checkbox'||t==='radio'){i.removeAttribute('checked');i.checked=false;continue}
if(t!=='file')i.removeAttribute('value');i.value=''}
for(const t of document.querySelectorAll('textarea')){t.textContent='';t.value='';n++}
for(const s of document.querySelectorAll('select')){for(const o of s.options){o.removeAttribute('selected');o.selected=false}n++}
for(const a of ['aria-pressed','aria-checked'])for(const b of document.querySelectorAll('['+a+'=true]')){b.setAttribute(a,'false');n++}
return n})()`

// replayOne fills the snapshot of one run once and classifies its required
// controls with the check stage's own verdict.
func replayOne(ctx context.Context, runner *apply.Runner, lane *browser.LaneHandle, c replayCase) (out RunResult) {
	out = RunResult{RunID: c.runID, ATS: c.ats, OriginalClass: c.class, OriginalLeft: len(c.left), Fields: []Field{}}
	// A defect one snapshot reaches is that run's result, not the end of the
	// replay; the stack names code, never the page.
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(os.Stderr, "replay: run %s: panic: %v\n%s", c.runID, p, debug.Stack())
			out.Fields, out.NowAnswered, out.Error = []Field{}, nil, "panic"
		}
	}()
	fail := func(stage string) RunResult {
		out.Error = stage
		return out
	}
	html, err := os.ReadFile(c.snapshot)
	if err != nil {
		return fail("read-snapshot")
	}
	page, err := url.Parse(c.url)
	if err != nil || (page.Scheme != "http" && page.Scheme != "https") {
		return fail("no-page-address")
	}
	srv := &offline{page: page, body: strip(html)}
	tab, err := lane.OpenTab(ctx, "replay")
	if err != nil {
		return fail("open-tab")
	}
	defer lane.CloseTab(context.WithoutCancel(ctx), tab)
	if err := lane.ServeOffline(ctx, tab, srv.serve); err != nil {
		return fail("serve")
	}
	defer func() { _, refused := srv.counts(); out.Blocked, out.refused = len(refused), refused }()
	if _, err := lane.Navigate(ctx, tab, c.url); err != nil {
		return fail("navigate")
	}
	if _, err := lane.Evaluate(ctx, tab, blankJS); err != nil {
		return fail("blank")
	}
	loaded, _ := srv.counts()
	f, err := runner.OpenFill(ctx, apply.LaneWindow{Lane: lane}, tab)
	if err != nil {
		return fail("adopt")
	}
	defer f.Close(context.WithoutCancel(ctx))
	fr, err := f.Run(ctx, "replay-"+c.runID)
	if err != nil {
		return fail("fill")
	}
	if fr.NotAnApplication {
		return fail("not-an-application")
	}
	// The engine never presses: any document request after the load, or a
	// page that is no longer the snapshot, is a navigation it caused.
	docs, _ := srv.counts()
	var href string
	raw, err := lane.Evaluate(ctx, tab, `location.href`)
	if err == nil {
		_ = json.Unmarshal(raw, &href)
	}
	if hu, perr := url.Parse(href); err != nil || perr != nil || docs != loaded || !sameDocument(hu, page) {
		return fail("navigated")
	}
	rows, verdict := f.Checked()
	out.Fields = classify(rows, verdict)
	for _, l := range c.left {
		if slices.ContainsFunc(out.Fields, func(f Field) bool { return f.Label == l && f.Status == statusAnswered }) && !slices.Contains(out.NowAnswered, l) {
			out.NowAnswered = append(out.NowAnswered, l)
		}
	}
	return out
}

// liveOnly are the reasons a control is left that only the site's own
// scripts can lift: a widget that opens, loads or uploads through them.
var liveOnly = map[string]bool{"listbox-not-opened": true, "driver-unavailable": true, "binding-unavailable": true, "needs-native-upload": true}

// scriptedOnly reports a control left because the site's scripts did not run:
// a custom list that offered no option (one that never opened is asked
// without its options, as an asynchronous search is, so the matcher finds no
// entry for it or an answer that cannot take it, and neither says what the
// bank holds), or a group of scripted choice buttons whose state never moved
// after the click.
func scriptedOnly(r apply.CheckedRow, why string) bool {
	switch r.Kind {
	case "custom-listbox":
		return why == "option-not-found" || r.Options == 0
	case "radio-group":
		return why == "write-not-committed"
	}
	return false
}

// classify sorts the required controls of the page by the check stage's
// verdict: a control the verdict names is left with the engine's reason (or
// the verdict's code when the engine wrote it), or needs the live page when
// that reason is a widget the site's scripts drive; every other required
// control is answered. A file control needs the live page: the replay attaches
// no document, and so does a text box that bears a file control's label: it
// is that upload widget's own state (a Dropzone keeps the uploaded file's
// address in one), which the site's stylesheet hides and the replay, loading
// no stylesheet, reads as a question. A control the engine reads as hidden is
// not asked by the form and is not counted.
func classify(rows []apply.CheckedRow, v gates.ReadinessResult) []Field {
	open := map[string][]string{}
	var order []string
	uploads := map[string]bool{}
	for _, r := range rows {
		if r.Kind == "file" {
			uploads[r.Label] = true
		}
	}
	for _, f := range v.Failures {
		if _, ok := open[f.Label]; !ok {
			order = append(order, f.Label)
		}
		open[f.Label] = append(open[f.Label], f.Code)
	}
	var out []Field
	for _, r := range rows {
		codes := open[r.Label]
		why := strings.TrimPrefix(r.Why, "abstained:")
		if len(codes) == 0 && (!r.Required || why == "hidden") {
			continue
		}
		f := Field{Label: r.Label, Kind: r.Kind, Status: statusAnswered}
		switch {
		case r.Kind == "file" && (len(codes) > 0 || r.Outcome != "filled"):
			f.Status = statusNeedsLive
		case len(codes) == 0:
		case codes[0] == gates.CodeRequiredUnanswered && (liveOnly[why] || scriptedOnly(r, why) ||
			r.Kind == "text" && why == "no-entry" && uploads[r.Label]):
			f.Status = statusNeedsLive
		case codes[0] == gates.CodeRequiredUnanswered && r.Why != "":
			f.Status = statusLeft + r.Why
		default:
			f.Status = statusLeft + codes[0]
		}
		if len(codes) > 0 {
			open[r.Label] = codes[1:]
		}
		out = append(out, f)
	}
	// What the verdict names that no control row carries (an unreadable page).
	for _, l := range order {
		for _, code := range open[l] {
			out = append(out, Field{Label: l, Status: statusLeft + code})
		}
	}
	return out
}

// revision is the commit the replay was built from, marked when the tree
// had changes.
func revision() string {
	rev, dirty := applaunch.VCSRevision()
	if rev != "" && dirty {
		rev += "-modified"
	}
	return rev
}

// finish writes results.json and RESULTS.md under dir and, given a baseline,
// prints what changed. It returns 1 when a control the baseline answered is
// no longer answered.
func finish(w io.Writer, dir string, res Results, baselinePath string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(raw, '\n'), 0o600); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "RESULTS.md"), []byte(render(res)), 0o600); err != nil {
		return 0, err
	}
	fmt.Fprintf(w, "replayed %d run(s); results in %s\n", len(res.Runs), dir)
	if baselinePath == "" {
		return 0, nil
	}
	b, err := os.ReadFile(baselinePath)
	if err != nil {
		return 0, err
	}
	var base Results
	if err := json.Unmarshal(b, &base); err != nil {
		return 0, fmt.Errorf("the baseline is not a results file: %w", err)
	}
	if base.Schema != schemaVersion {
		return 0, fmt.Errorf("the baseline has schema %d, this build writes %d", base.Schema, schemaVersion)
	}
	regs, imps := compare(base, res)
	for _, l := range imps {
		fmt.Fprintln(w, "improvement", l)
	}
	for _, l := range regs {
		fmt.Fprintln(w, "regression", l)
	}
	fmt.Fprintf(w, "%d regression(s), %d improvement(s) against %s\n", len(regs), len(imps), baselinePath)
	if len(regs) > 0 {
		return 1, nil
	}
	return 0, nil
}

// compare lines up the fields of the runs both results replayed, by label and
// occurrence, and lists the controls the baseline answered that are not
// answered now, and the reverse.
func compare(base, now Results) (regressions, improvements []string) {
	key := func(fs []Field) map[string]string {
		m, seen := map[string]string{}, map[string]int{}
		for _, f := range fs {
			m[f.Label+"\x00"+strconv.Itoa(seen[f.Label])] = f.Status
			seen[f.Label]++
		}
		return m
	}
	was := map[string]map[string]string{}
	for _, r := range base.Runs {
		if r.Error == "" {
			was[r.RunID] = key(r.Fields)
		}
	}
	for _, r := range now.Runs {
		before, ok := was[r.RunID]
		if !ok {
			continue
		}
		after := key(r.Fields)
		for _, k := range sortedKeys(before, after) {
			label, _, _ := strings.Cut(k, "\x00")
			b, a := before[k], after[k]
			line := fmt.Sprintf("%s %q: %s -> %s", r.RunID, label, cmpStatus(b), cmpStatus(a))
			switch {
			case b == statusAnswered && a != statusAnswered:
				regressions = append(regressions, line)
			case b != statusAnswered && a == statusAnswered:
				improvements = append(improvements, line)
			}
		}
	}
	return regressions, improvements
}

func cmpStatus(s string) string {
	if s == "" {
		return "absent"
	}
	return s
}

func sortedKeys(ms ...map[string]string) []string {
	var keys []string
	for _, m := range ms {
		for k := range m {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// render is RESULTS.md: a table by ATS, the left fields by reason, then one
// line per run.
func render(res Results) string {
	type tally struct{ forms, required, answered, left, live, failed int }
	byATS := map[string]*tally{}
	reasons := map[string]int{}
	for _, r := range res.Runs {
		ats := cmp.Or(r.ATS, "unknown")
		t := byATS[ats]
		if t == nil {
			t = &tally{}
			byATS[ats] = t
		}
		t.forms++
		if r.Error != "" {
			t.failed++
		}
		for _, f := range r.Fields {
			t.required++
			switch {
			case f.Status == statusAnswered:
				t.answered++
			case f.Status == statusNeedsLive:
				t.live++
			default:
				t.left++
				reasons[strings.TrimPrefix(f.Status, statusLeft)]++
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Form replay\n\nGenerated %s, engine %s", res.Generated, cmp.Or(res.Engine, "unknown"))
	if res.Revision != "" {
		fmt.Fprintf(&b, ", revision %s", res.Revision)
	}
	b.WriteString(".\n\n| ATS | forms | not replayed | required | answered | left | needs live page |\n|---|---|---|---|---|---|---|\n")
	var atsNames []string
	for k := range byATS {
		atsNames = append(atsNames, k)
	}
	sort.Strings(atsNames)
	for _, k := range atsNames {
		t := byATS[k]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d |\n", k, t.forms, t.failed, t.required, t.answered, t.left, t.live)
	}
	if len(reasons) > 0 {
		b.WriteString("\n| left because | fields |\n|---|---|\n")
		var rs []string
		for k := range reasons {
			rs = append(rs, k)
		}
		sort.Slice(rs, func(i, j int) bool {
			return reasons[rs[i]] > reasons[rs[j]] || reasons[rs[i]] == reasons[rs[j]] && rs[i] < rs[j]
		})
		for _, k := range rs {
			fmt.Fprintf(&b, "| %s | %d |\n", k, reasons[k])
		}
	}
	b.WriteString("\n## Runs\n\n")
	for _, r := range res.Runs {
		if r.Error != "" {
			fmt.Fprintf(&b, "- %s (%s): not replayed (%s)\n", r.RunID, cmp.Or(r.ATS, "unknown"), r.Error)
			continue
		}
		var ans, left, live int
		for _, f := range r.Fields {
			switch {
			case f.Status == statusAnswered:
				ans++
			case f.Status == statusNeedsLive:
				live++
			default:
				left++
			}
		}
		fmt.Fprintf(&b, "- %s (%s): %d required, %d answered, %d left, %d need the live page; %d blocked request(s)", r.RunID, cmp.Or(r.ATS, "unknown"), len(r.Fields), ans, left, live, r.Blocked)
		if r.OriginalLeft > 0 {
			fmt.Fprintf(&b, "; answers %d of the %d the run left", len(r.NowAnswered), r.OriginalLeft)
			if len(r.NowAnswered) > 0 {
				q := make([]string, len(r.NowAnswered))
				for i, l := range r.NowAnswered {
					q[i] = strconv.Quote(l)
				}
				b.WriteString(": " + strings.Join(q, ", "))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
