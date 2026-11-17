package apply

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/tracker"
)

// scriptedObserver answers the recorder's reads from a script and notes every
// script it is asked to run. A read's entries are sealed under the key the
// recorder was last armed with, as the recorder in the page seals them.
type scriptedObserver struct {
	mu       sync.Mutex
	exprs    []string
	reads    int
	key      []byte
	onRead   func(n int) (json.RawMessage, error)
	armErr   error
	released bool
}

func (o *scriptedObserver) Install(_ context.Context, script string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !isRecorderArm(script) {
		return browserErr(browser.CodeNotAllowed)
	}
	o.exprs = append(o.exprs, "arm")
	o.key, _ = hex.DecodeString(script[len(script)-66 : len(script)-2])
	return o.armErr
}

func (o *scriptedObserver) Evaluate(_ context.Context, expr string) (json.RawMessage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !isRecorderRead(expr) {
		return nil, browserErr(browser.CodeNotAllowed)
	}
	o.exprs = append(o.exprs, strings.TrimPrefix(expr, recorderReadJS))
	o.reads++
	raw, err := o.onRead(o.reads)
	var b recorderBuffer
	if err != nil || json.Unmarshal(raw, &b) != nil {
		return raw, err
	}
	out := sealedBuffer{Armed: b.Armed, Epoch: b.Epoch, Next: b.Next}
	for _, e := range b.Entries {
		e.Epoch = cmp.Or(e.Epoch, b.Epoch)
		d, _ := json.Marshal(e)
		mac := hmac.New(sha256.New, o.key)
		mac.Write(d)
		out.Entries = append(out.Entries, sealedEntry{N: e.Seq, D: string(d), M: hex.EncodeToString(mac.Sum(nil))})
	}
	return json.Marshal(out)
}

func (o *scriptedObserver) Release() {
	o.mu.Lock()
	o.released = true
	o.mu.Unlock()
}

func (o *scriptedObserver) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.exprs)
}

func (o *scriptedObserver) readCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reads
}

func buffer(armed bool, epoch string, entries ...recorderEntry) json.RawMessage {
	next := int64(0)
	if n := len(entries); n > 0 {
		next = entries[n-1].Seq
	}
	b, _ := json.Marshal(recorderBuffer{Armed: armed, Epoch: epoch, Next: next, Entries: entries})
	return b
}

func entry(seq int64, q, kind, value, prior string) recorderEntry {
	return recorderEntry{Seq: seq, Question: q, Kind: kind, InputType: "text", Value: value, Prior: prior, URL: "https://apply.example.test/form"}
}

// observed is a tab whose observer is a scripted one.
type observed struct {
	obs *scriptedObserver
	err error
}

func (t observed) Observe(context.Context) (TabObserver, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.obs, nil
}

type posts struct {
	mu  sync.Mutex
	got []LearnEvent
	err error
}

func (p *posts) PostEvents(_ context.Context, ev []LearnEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.got = append(p.got, ev...)
	return nil
}

func (p *posts) events() []LearnEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.got)
}

func runWatcher(t *testing.T, w *Watcher, spec WatchSpec) (reason string) {
	t.Helper()
	done := make(chan string, 1)
	spec.Ended = func(r string) { done <- r }
	if spec.TabID == "" {
		spec.TabID = "tab-1"
	}
	if spec.RunID == "" {
		spec.RunID = "run_20270310T090000.000_abc123"
	}
	if !w.Start(spec) {
		t.Fatal("the session did not start")
	}
	select {
	case reason = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session never ended")
	}
	w.Wait()
	return reason
}

func fastWatcher(p Poster) *Watcher {
	return &Watcher{Ctx: context.Background(), Poster: p, Every: 2 * time.Millisecond}
}

func TestHandoff_Recorder_ReinstallsAfterNavigation(t *testing.T) {
	t.Run("a navigation drops the script and the next read arms it again", func(t *testing.T) {
		obs := &scriptedObserver{}
		obs.onRead = func(n int) (json.RawMessage, error) {
			switch n {
			case 1: // freshly kept: the session armed the recorder before it read
				return buffer(true, ""), nil
			case 2:
				return buffer(true, "e1", entry(1, "Sponsorship", "radio-group", "No", "Yes")), nil
			case 3: // the page navigated: script gone, the buffer survives
				return buffer(false, "e1", entry(2, "City", "text", "Springfield", "")), nil
			}
			return nil, browserErr(browser.CodeTabClosed)
		}
		p := &posts{}
		if reason := runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}}); reason != "tab-closed" {
			t.Fatalf("ended by %q", reason)
		}
		seen := obs.seen()
		var arms int
		for _, e := range seen {
			if e == "arm" {
				arms++
			}
		}
		if arms != 2 {
			t.Fatalf("armed %d times: %v", arms, seen)
		}
		// The cursor travels in the read: the second read asks past entry 1.
		if !slices.Contains(seen, `("e1",1)`) || !slices.Contains(seen, `("e1",2)`) {
			t.Fatalf("reads %v", seen)
		}
		got := p.events()
		if len(got) != 2 || got[0].After != "No" || got[0].Before != "Yes" || got[1].After != "Springfield" {
			t.Fatalf("posted %+v", got)
		}
		if !obs.released {
			t.Fatal("the observer was not released")
		}
	})

	t.Run("only the recorder's own scripts are recognised as reads and arms", func(t *testing.T) {
		key := strings.Repeat("ab", 32)
		arm := recorderArmScript([]byte(strings.Repeat("\xab", 32)))
		for _, c := range []struct {
			script    string
			read, arm bool
		}{
			{recorderReadScript("", 0), true, false},
			{recorderReadScript("ab12cd34", 41), true, false},
			{arm, false, true},
			{"", false, false},
			{recorderReadJS, false, false},
			{recorderReadJS + `("x",1);alert(1)`, false, false},
			{recorderReadJS + `("a b",1)`, false, false},
			{recorderReadJS + `("e",-1)`, false, false},
			{"document.title", false, false},
			{recorderArmJS, false, false},
			{arm + ";alert(1)", false, false},
			{recorderArmJS + `("` + key + `",alert(1))`, false, false},
		} {
			if isRecorderRead(c.script) != c.read || isRecorderArm(c.script) != c.arm {
				t.Errorf("%.60q: read %v arm %v", c.script, isRecorderRead(c.script), isRecorderArm(c.script))
			}
		}
	})
	t.Run("a buffer whose epoch changed is read from the start", func(t *testing.T) {
		obs := &scriptedObserver{}
		obs.onRead = func(n int) (json.RawMessage, error) {
			switch n {
			case 1:
				return buffer(true, "old", entry(7, "A", "text", "x", "")), nil
			case 2:
				return buffer(true, "new", entry(1, "B", "text", "y", "")), nil
			}
			return nil, browserErr(browser.CodeTabClosed)
		}
		p := &posts{}
		runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}})
		seen := obs.seen()
		if !slices.Contains(seen, `("old",7)`) || len(p.events()) != 2 {
			t.Fatalf("reads %v posted %d", seen, len(p.events()))
		}
	})
}

func TestHandoff_Recorder_SensitiveControlsPostedValueSkipped(t *testing.T) {
	// The page's own check said nothing about these (Skipped false): the chain
	// discards the value for each by what the control is.
	cases := []struct {
		name string
		e    recorderEntry
	}{
		{"password", recorderEntry{Question: "Account password", Kind: "text", InputType: "password", Value: "hunter2"}},
		{"one-time code", recorderEntry{Question: "Enter the code we sent you", Kind: "text", Autocomplete: "one-time-code", Value: "123456"}},
		{"card number", recorderEntry{Question: "Card number", Kind: "text", Value: "4111111111111111"}},
		{"national identifier", recorderEntry{Question: "Social Security Number", Kind: "text", Value: "000-00-0000"}},
		{"profile email", recorderEntry{Question: "Email address", Kind: "text", InputType: "email", Value: "alex@example.com"}},
		{"profile phone", recorderEntry{Question: "Phone number", Kind: "text", InputType: "tel", Value: "+1 555 0100"}},
		{"profile name", recorderEntry{Question: "Full name", Kind: "text", Value: "Alex Example"}},
		{"date of birth", recorderEntry{Question: "Date of birth", Kind: "text", Value: "1990-01-02"}},
		{"self-identification", recorderEntry{Question: "Gender", Kind: "select", Value: "Female", OptionLabel: "Female", Prior: "Male"}},
		// A pick whose question says nothing a set of self-identification options does not.
		{"self-identification by its options", recorderEntry{Question: "Which of these describes you?", Kind: "custom-listbox", Value: "Asian", OptionLabel: "Asian",
			Options: []string{"Asian", "Black or African American", "Hispanic or Latino", "White", "Two or more races"}, Section: []string{"Voluntary Self-Identification"}}},
		{"payment label", recorderEntry{Question: "Bank account number", Kind: "text", Value: "000123456"}},
		{"the page said so", recorderEntry{Question: "Favourite colour", Kind: "text", Value: "green", Prior: "blue", Skipped: true}},
	}
	for i, c := range cases {
		c.e.Seq = int64(i + 1)
		c.e.URL = "https://apply.example.test/form"
		cases[i] = c
	}
	// One ordinary answer travels with its value, options and section, so the
	// test also proves the rule is not "drop everything".
	plain := entry(int64(len(cases)+1), "Years of Go experience", "select", "6", "5")
	plain.OptionLabel, plain.Options, plain.Section = "6", []string{"4", "5", "6"}, []string{"Experience"}
	var all []recorderEntry
	for _, c := range cases {
		all = append(all, c.e)
	}
	all = append(all, plain)
	obs := &scriptedObserver{}
	obs.onRead = func(n int) (json.RawMessage, error) {
		if n == 1 {
			return buffer(true, "e1", all...), nil
		}
		return nil, browserErr(browser.CodeTabClosed)
	}
	p := &posts{}
	runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}, Report: 12})
	got := p.events()
	if len(got) != len(all) {
		t.Fatalf("%d events for %d edits: the edit of a skipped control must still be noted", len(got), len(all))
	}
	for i, c := range cases {
		ev := got[i]
		if ev.After != "" || ev.Before != "" || ev.OptionLabel != "" || ev.Options != nil || ev.Section != nil {
			t.Errorf("%s: a value reached the receiver: %+v", c.name, ev.LearningEvent)
		}
		if ev.Question == "" || ev.Kind != manualFillKind {
			t.Errorf("%s: the edit was not noted: %+v", c.name, ev.LearningEvent)
		}
	}
	if last := got[len(got)-1]; last.After != "6" || last.Before != "5" || !slices.Equal(last.Options, plain.Options) || !slices.Equal(last.Section, plain.Section) {
		t.Errorf("an ordinary answer lost its value: %+v", last.LearningEvent)
	}
}

func TestHandoff_Recorder_EndsOnTabCloseAppliedTwoHoursOrEightFailures(t *testing.T) {
	t.Run("the tab closes", func(t *testing.T) {
		obs := &scriptedObserver{onRead: func(n int) (json.RawMessage, error) {
			if n < 3 {
				return buffer(true, "e"), nil
			}
			return nil, browserErr(browser.CodeTabClosed)
		}}
		if reason := runWatcher(t, fastWatcher(&posts{}), WatchSpec{Tab: observed{obs: obs}}); reason != "tab-closed" {
			t.Fatalf("ended by %q", reason)
		}
	})
	t.Run("the report becomes applied after one last drain", func(t *testing.T) {
		var applied bool
		var mu sync.Mutex
		obs := &scriptedObserver{}
		obs.onRead = func(n int) (json.RawMessage, error) {
			mu.Lock()
			defer mu.Unlock()
			if n == 2 {
				applied = true // the user presses Submit between two reads
			}
			if applied && n >= 3 {
				return buffer(true, "e1", entry(1, "Cover letter", "text", "Attached", "")), nil
			}
			return buffer(true, "e1"), nil
		}
		p := &posts{}
		reason := runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}, Applied: func(context.Context) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return applied, nil
		}})
		if reason != "applied" || len(p.events()) != 1 {
			t.Fatalf("ended by %q with %d posted: the last drain must still be made", reason, len(p.events()))
		}
	})
	t.Run("two hours", func(t *testing.T) {
		clk := &clock{t: t0}
		obs := &scriptedObserver{}
		obs.onRead = func(n int) (json.RawMessage, error) {
			if n == 2 {
				clk.advance(handoffLifetime)
			}
			if n >= 3 {
				return buffer(true, "e1", entry(1, "Notice period", "text", "2 weeks", "")), nil
			}
			return buffer(true, "e1"), nil
		}
		w := fastWatcher(&posts{})
		w.Now = clk.Now
		p := &posts{}
		w.Poster = p
		if reason := runWatcher(t, w, WatchSpec{Tab: observed{obs: obs}}); reason != "expired" || len(p.events()) != 1 {
			t.Fatalf("ended by %q with %d posted", reason, len(p.events()))
		}
	})
	t.Run("eight consecutive failed drains", func(t *testing.T) {
		obs := &scriptedObserver{onRead: func(int) (json.RawMessage, error) { return nil, errors.New("the page is busy") }}
		if reason := runWatcher(t, fastWatcher(&posts{}), WatchSpec{Tab: observed{obs: obs}}); reason != "failures" || obs.readCount() != 8 {
			t.Fatalf("ended by %q after %d reads", reason, obs.readCount())
		}
		// Seven failures and a success start the count again.
		obs = &scriptedObserver{}
		obs.onRead = func(n int) (json.RawMessage, error) {
			switch {
			case n <= 7:
				return nil, errors.New("busy")
			case n == 8:
				return buffer(true, "e"), nil
			case n <= 15:
				return nil, errors.New("busy")
			}
			return nil, browserErr(browser.CodeTabClosed)
		}
		if reason := runWatcher(t, fastWatcher(&posts{}), WatchSpec{Tab: observed{obs: obs}}); reason != "tab-closed" || obs.readCount() != 16 {
			t.Fatalf("ended by %q after %d reads", reason, obs.readCount())
		}
	})
	t.Run("a refusing receiver ends the recording and nothing else", func(t *testing.T) {
		obs := &scriptedObserver{onRead: func(int) (json.RawMessage, error) {
			return buffer(true, "e1", entry(1, "Q", "text", "v", "")), nil
		}}
		p := &posts{err: errors.New("the server is down")}
		if reason := runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}}); reason != "failures" {
			t.Fatalf("ended by %q", reason)
		}
	})
	t.Run("an observer that cannot attach ends at once", func(t *testing.T) {
		if reason := runWatcher(t, fastWatcher(&posts{}), WatchSpec{Tab: observed{err: errors.New("no endpoint")}}); reason != "failures" {
			t.Fatalf("ended by %q", reason)
		}
	})
}

func TestHandoff_Recorder_SessionLimitsOneSessionPerTabThreeTabs(t *testing.T) {
	block := make(chan struct{})
	obs := func() *scriptedObserver {
		return &scriptedObserver{onRead: func(int) (json.RawMessage, error) { <-block; return nil, browserErr(browser.CodeTabClosed) }}
	}
	w := fastWatcher(&posts{})
	for _, id := range []string{"t1", "t2", "t3"} {
		if !w.Start(WatchSpec{TabID: id, RunID: "r", Tab: observed{obs: obs()}}) {
			t.Fatalf("%s did not start", id)
		}
	}
	if w.Start(WatchSpec{TabID: "t4", RunID: "r", Tab: observed{obs: obs()}}) {
		t.Fatal("a fourth session started")
	}
	if w.Start(WatchSpec{TabID: "t1", RunID: "r", Tab: observed{obs: obs()}}) {
		t.Fatal("a second session on one tab started")
	}
	if w.Active() != 3 {
		t.Fatalf("%d sessions", w.Active())
	}
	close(block)
	w.Wait()
	if w.Active() != 0 {
		t.Fatalf("%d sessions left", w.Active())
	}
	var none *Watcher
	if none.Start(WatchSpec{TabID: "x", Tab: observed{}}) || none.Active() != 0 {
		t.Fatal("no watcher is a watcher")
	}
}

func TestHandoff_Recorder_EntriesCarryTheRunAndReportAndOnlyAPathURL(t *testing.T) {
	e := entry(1, "Are you authorised to work?", "radio-group", "Yes", "No")
	e.URL = "https://apply.example.test/form?token=abc#step2"
	e.OptionLabel, e.Epoch = "Yes", "e1"
	obs := &scriptedObserver{}
	obs.onRead = func(n int) (json.RawMessage, error) {
		if n == 1 {
			return buffer(true, "e1", e), nil
		}
		return nil, browserErr(browser.CodeTabClosed)
	}
	p := &posts{}
	runWatcher(t, fastWatcher(p), WatchSpec{Tab: observed{obs: obs}, RunID: "run_20270310T090000.000_abc123", Report: 7})
	got := p.events()
	if len(got) != 1 {
		t.Fatalf("%d events", len(got))
	}
	ev := got[0]
	if ev.URL != "https://apply.example.test/form" || ev.Host != "apply.example.test" {
		t.Errorf("url %q host %q", ev.URL, ev.Host)
	}
	if ev.RunID != "run_20270310T090000.000_abc123" || ev.ReportNumber != 7 || !ev.TrustedInput || ev.Kind != "manual-fill" {
		t.Errorf("event %+v", ev)
	}
	if ev.Question != e.Question || ev.ControlKind != "radio-group" || ev.After != "Yes" || ev.Before != "No" || ev.OptionLabel != "Yes" {
		t.Errorf("event %+v", ev.LearningEvent)
	}
	// The same entry under a buffer epoch a page script changed: the id comes
	// from the sealed epoch, so a copy posted again is a duplicate.
	if again := (&recorderSession{w: fastWatcher(nil), spec: WatchSpec{RunID: "run_20270310T090000.000_abc123"}}).events(recorderBuffer{Epoch: "e9", Entries: []recorderEntry{e}}); again[0].EventID != ev.EventID {
		t.Error("the same entry got another event id: a repeated post would not be a duplicate")
	}
	for _, raw := range []string{"https://user:pw@apply.example.test/a/b?x=1#y", "https://apply.example.test/a/b#y"} {
		if page, _ := pageOf(raw); page != "https://apply.example.test/a/b" {
			t.Errorf("%s -> %s", raw, page)
		}
	}
	for _, bad := range []string{"", "about:blank", "chrome://version/", "file:///etc/hosts"} {
		if page, host := pageOf(bad); page != "" || host != "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

// An edit inside a same-origin iframe keeps the top page as its URL and the
// frame's origin and path as its frame URL.
func TestHandoff_Recorder_FrameEntryCarriesTopURLAndFramePath(t *testing.T) {
	e := entry(1, "City of residence", "text", "Lyon", "")
	e.URL, e.FrameURL = "https://jobs.example.test/apply", "https://jobs.example.test/frame/form?token=abc#s2"
	top := entry(2, "Team", "text", "Platform", "")
	top.URL = "https://jobs.example.test/apply"
	got := (&recorderSession{w: fastWatcher(nil), spec: WatchSpec{RunID: "run_20270310T090000.000_abc123"}}).events(recorderBuffer{Epoch: "e1", Entries: []recorderEntry{e, top}})
	if len(got) != 2 {
		t.Fatalf("%d events", len(got))
	}
	if got[0].URL != "https://jobs.example.test/apply" || got[0].FrameURL != "https://jobs.example.test/frame/form" {
		t.Errorf("url %q frame %q", got[0].URL, got[0].FrameURL)
	}
	if got[1].FrameURL != "" {
		t.Errorf("a top-page edit carries frame %q", got[1].FrameURL)
	}
	for raw, want := range map[string]string{"about:srcdoc": "about:srcdoc", "about:blank": "about:blank", "chrome://x/": "", "https://a.test/f?x#y": "https://a.test/f"} {
		if f := framePath(raw); f != want {
			t.Errorf("%q -> %q", raw, f)
		}
	}
}

func TestHandoff_Recorder_JoinsByReportNotTrackerRow(t *testing.T) {
	g, tr := trackerRig(t)
	ctx := context.Background()
	// Report 7 is the evaluated report; its tracker row has a number of its own.
	score := 4.6
	var res tracker.UpsertResult
	err := g.st.Write(ctx, func(tx *sql.Tx) (err error) {
		res, err = tr.UpsertReportIn(ctx, tx, tracker.Report{Number: 7, Date: "2027-03-10", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal, Score: &score,
			Posting: identity.Posting{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rb := ReportBinding{Number: 7, TrackerID: res.Entry.ID}
	if rb.Number == rb.TrackerID {
		t.Fatalf("the fixture must keep the two id spaces apart (both %d)", rb.Number)
	}
	if got, err := reportApplied(ctx, tr, rb); err != nil || got {
		t.Fatalf("an evaluated row reads as applied: %v %v", got, err)
	}
	// A binding whose tracker row belongs to another report is no join.
	if _, err := tr.SetStatus(ctx, tracker.StatusChange{ID: res.Entry.ID, To: tracker.Applied, Actor: tracker.ActorUser}); err != nil {
		t.Fatal(err)
	}
	if got, err := reportApplied(ctx, tr, rb); err != nil || !got {
		t.Fatalf("the applied row of report 7 does not read as applied: %v %v", got, err)
	}
	if got, _ := reportApplied(ctx, tr, ReportBinding{Number: res.Entry.ID, TrackerID: res.Entry.ID}); got {
		t.Fatal("the row joined by its own number")
	}
	if got, err := reportApplied(ctx, tr, ReportBinding{Number: 7}); err != nil || got {
		t.Fatalf("a binding with no row: %v %v", got, err)
	}

	// The entries carry the report number, never the tracker row's.
	s := &recorderSession{w: fastWatcher(nil), spec: WatchSpec{RunID: "run_x", Report: rb.Number}}
	evs := s.events(recorderBuffer{Epoch: "e", Entries: []recorderEntry{entry(1, "Q", "text", "v", "")}})
	if len(evs) != 1 || evs[0].ReportNumber != 7 {
		t.Fatalf("events %+v", evs)
	}
}

// watchTab is a kept scripted tab that can be observed.
type watchTab struct {
	*scriptTab
	id    string
	obs   *scriptedObserver
	err   error
	asked chan struct{}
}

func (w *watchTab) ID() string { return w.id }
func (w *watchTab) Observe(context.Context) (TabObserver, error) {
	select {
	case w.asked <- struct{}{}:
	default:
	}
	if w.err != nil {
		return nil, w.err
	}
	return w.obs, nil
}

func TestHandoff_WatchStartsOnlyForHumanFinishableStops(t *testing.T) {
	cases := []struct {
		name  string
		out   Outcome
		watch bool
	}{
		{"a bot check", Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: ClassDefect, Reason: "the page served a verification challenge"}, true},
		{"a sign-in", Outcome{Kind: OutcomeStopped, Stage: StageAccount, Class: ClassCredential, Reason: "sign in"}, true},
		{"a required answer only the user holds", Outcome{Kind: OutcomeStopped, Stage: StageCheck, Class: ClassAnswerGap, Reason: "a required answer"}, true},
		{"a closed posting", Outcome{Kind: OutcomeStopped, Stage: StageAccount, Class: ClassPosting, Reason: "posting closed"}, false},
		{"a 404 page under another class", Outcome{Kind: OutcomeStopped, Stage: StageAccount, Class: ClassDefect, Reason: "the page answered HTTP 404"}, false},
		{"no longer accepting", Outcome{Kind: OutcomeStopped, Stage: StageAccount, Class: ClassAnswerGap, Reason: "this job is no longer accepting applications"}, false},
		{"a site that is down", Outcome{Kind: OutcomeStopped, Stage: StageAccount, Class: ClassUnavailable, Reason: "down"}, false},
		{"a run that submitted", Outcome{Kind: OutcomeSubmitted}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &posts{}
			w := &Watcher{Ctx: context.Background(), Poster: p, Every: 2 * time.Millisecond}
			k := &Keeper{Watch: w}
			g := newRig(t, func(d *Deps) { d.Page.Keep = k })
			obs := &scriptedObserver{onRead: func(int) (json.RawMessage, error) { return nil, browserErr(browser.CodeTabClosed) }}
			tab := &watchTab{scriptTab: newScriptTab(appPage()), id: "tab-w", obs: obs, asked: make(chan struct{}, 1)}
			run := &Run{ID: "run_w", Input: Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome: c.out}
			s := &session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: tab}
			s.release(context.Background())
			w.Wait()
			asked := len(tab.asked) > 0
			if asked != c.watch {
				t.Fatalf("watched = %v, want %v", asked, c.watch)
			}
			if c.watch != tab.kept || (!c.watch && !tab.closed) {
				t.Fatalf("kept %v closed %v", tab.kept, tab.closed)
			}
		})
	}
	t.Run("no watcher on the keeper opens nothing", func(t *testing.T) {
		k := &Keeper{}
		g := newRig(t, func(d *Deps) { d.Page.Keep = k })
		tab := &watchTab{scriptTab: newScriptTab(appPage()), id: "t", asked: make(chan struct{}, 1)}
		run := &Run{ID: "run_n", Input: Input{URL: pageURL}, Outcome: Outcome{Kind: OutcomeStopped, Class: ClassDefect, Reason: "x"}}
		(&session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: tab}).release(context.Background())
		if len(tab.asked) != 0 || !tab.kept {
			t.Fatalf("asked %d kept %v", len(tab.asked), tab.kept)
		}
	})
	t.Run("a failing DevTools endpoint never fails the run", func(t *testing.T) {
		w := &Watcher{Ctx: context.Background(), Poster: &posts{}, Every: time.Millisecond}
		g := newRig(t, func(d *Deps) { d.Page.Keep = &Keeper{Watch: w} })
		tab := &watchTab{scriptTab: newScriptTab(appPage()), id: "t", err: errors.New("connection refused"), asked: make(chan struct{}, 1)}
		out := Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: ClassDefect, Reason: "challenge"}
		run := &Run{ID: "run_f", Input: Input{URL: pageURL}, Outcome: out}
		(&session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: tab}).release(context.Background())
		w.Wait()
		if run.Outcome != out || !tab.kept || tab.closed {
			t.Fatalf("outcome %+v kept %v closed %v", run.Outcome, tab.kept, tab.closed)
		}
	})
	t.Run("a tab the user closed is forgotten and its row stops offering the jump", func(t *testing.T) {
		board := &fakeBoard{}
		lane := &fakeLane{live: []string{"tab-c"}}
		k := &Keeper{Board: board, Watch: &Watcher{Ctx: context.Background(), Poster: &posts{}, Every: 2 * time.Millisecond}}
		g := newRig(t, func(d *Deps) { d.Page.Keep = k })
		obs := &scriptedObserver{onRead: func(int) (json.RawMessage, error) { return nil, browserErr(browser.CodeTabClosed) }}
		tab := &watchTab{scriptTab: newScriptTab(appPage()), id: "tab-c", obs: obs, asked: make(chan struct{}, 1)}
		lt := &keptWatch{watchTab: tab, lane: lane}
		run := &Run{ID: "run_c", Input: Input{URL: pageURL}, Outcome: Outcome{Kind: OutcomeStopped, Class: ClassDefect, Reason: "x"}}
		(&session{r: g.r, e: &Exec{r: g.r, Run: run, Ctx: context.Background()}, tab: lt}).release(context.Background())
		k.Watch.Wait()
		eventually(t, 3*time.Second, "the board to be told", func() bool { return board.wasCleared("run_c") })
		if _, ok := k.TabOf("run_c"); ok {
			t.Fatal("the closed tab still counts")
		}
	})
}

// keptWatch is an observable kept tab that also names its lane.
type keptWatch struct {
	*watchTab
	lane *fakeLane
}

func (k *keptWatch) Handle() TabLane { return k.lane }

func browserErr(c browser.Code) error { return &browser.Error{Code: c, Message: string(c)} }
