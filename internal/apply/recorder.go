package apply

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/answerbank/learn"
	"applysmith/internal/browser"
	"applysmith/internal/engine"
	"applysmith/internal/lazyregexp"
)

// The recorder's bounds. The sessions are few and short because each one holds
// a DevTools session on a tab the user is working in.
const (
	handoffEvery    = 1500 * time.Millisecond
	handoffMaxTabs  = 3
	handoffLifetime = 2 * time.Hour
	handoffFailures = 8
	// manualFillKind is the upload kind of the manual-fill corpus.
	manualFillKind = learn.UploadManualFill
)

//go:embed recorder.js
var recorderArmJS string

//go:embed recorder_read.js
var recorderReadJS string

// recorderDisarmJS takes what the recorder buffered out of the page of a tab a
// run has taken back: the run's own DevTools input is as trusted to the page as
// the user's. The recorder left in its world records nothing without a buffer,
// and an entry it seals under the ended session's key opens under no other.
const recorderDisarmJS = `(function(){try{sessionStorage.removeItem("__ajhr")}catch(x){}return true})()`

// recorderArmScript arms the recorder with a session's key. The key is part of
// the script, so the browser layer can recognise it by its shape.
func recorderArmScript(key []byte) string {
	return recorderArmJS + "(" + strconv.Quote(hex.EncodeToString(key)) + ")"
}

var armTailRE = lazyregexp.New(`^\("[0-9a-f]{64}"\)$`)

// isRecorderArm recognises the arming script with any key, and nothing else.
func isRecorderArm(script string) bool {
	rest, ok := strings.CutPrefix(script, recorderArmJS)
	return ok && armTailRE.MatchString(rest)
}

// recorderReadScript is the one read of the recorder's buffer. The cursor is
// part of the expression, so the browser layer can recognise it by its shape.
func recorderReadScript(epoch string, seq int64) string {
	return recorderReadJS + "(" + strconv.Quote(epoch) + "," + strconv.FormatInt(seq, 10) + ")"
}

var readTailRE = lazyregexp.New(`^\("[a-z0-9]{0,16}",[0-9]{1,15}\)$`)

// isRecorderRead recognises the read script with any cursor, and nothing else.
func isRecorderRead(expr string) bool {
	rest, ok := strings.CutPrefix(expr, recorderReadJS)
	return ok && readTailRE.MatchString(rest)
}

// RecorderScripts are the only scripts the browser layer lets the observer of
// a kept tab run.
func RecorderScripts() browser.ObserverScripts {
	return browser.ObserverScripts{IsArm: isRecorderArm, IsRead: isRecorderRead}
}

// TabObserver is the observer session on one kept tab.
type TabObserver interface {
	Install(ctx context.Context, script string) error
	Evaluate(ctx context.Context, expr string) (json.RawMessage, error)
	Release()
}

// Observable is a tab that can be watched once it has been kept.
type Observable interface {
	Observe(ctx context.Context) (TabObserver, error)
}

type recorderEntry struct {
	Epoch        string `json:"epoch"`
	Seq          int64  `json:"seq"`
	Question     string `json:"question"`
	Kind         string `json:"kind"`
	InputType    string `json:"inputType"`
	Autocomplete string `json:"autocomplete"`
	Value        string `json:"value"`
	OptionLabel  string `json:"optionLabel"`
	Prior        string `json:"prior"`
	Skipped      bool   `json:"skipped"`
	URL          string `json:"url"`
	// FrameURL is the address of the same-origin iframe the control was in,
	// empty for the top page.
	FrameURL string `json:"frameUrl"`
	// Options are the labels of the control's options, at most 25, and
	// Section the captions of the form sections around it, outermost first.
	Options []string `json:"options"`
	Section []string `json:"section"`
}

type recorderBuffer struct {
	Armed   bool            `json:"armed"`
	Epoch   string          `json:"epoch"`
	Next    int64           `json:"next"`
	Entries []recorderEntry `json:"entries"`
}

// sealedEntry is a buffered entry as the page's storage holds it: its JSON text
// and the hex HMAC-SHA256 of that text under the session's key. N is a copy of
// its sequence number that the read pages by; only D is believed.
type sealedEntry struct {
	N int64  `json:"n"`
	D string `json:"d"`
	M string `json:"m"`
}

type sealedBuffer struct {
	Armed   bool          `json:"armed"`
	Epoch   string        `json:"epoch"`
	Next    int64         `json:"next"`
	Entries []sealedEntry `json:"entries"`
}

// unseal keeps the entries whose seal holds under the session's key: one a page
// script wrote or changed is dropped. A copy of an entry opens to the same
// epoch and sequence number, so to the same event id, and the receiver keeps
// one.
func unseal(key []byte, b sealedBuffer) recorderBuffer {
	out := recorderBuffer{Armed: b.Armed, Epoch: b.Epoch, Next: b.Next}
	for _, se := range b.Entries {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(se.D))
		sum, err := hex.DecodeString(se.M)
		var e recorderEntry
		if err != nil || !hmac.Equal(sum, mac.Sum(nil)) || json.Unmarshal([]byte(se.D), &e) != nil {
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// WatchSpec describes one kept tab to watch.
type WatchSpec struct {
	TabID string
	// RunID is the session id of every entry and Report the number of the
	// evaluated report; the tracker row's own number is a different id space.
	RunID  string
	Report int64
	Tab    Observable
	// Applied reports whether the run's report has become applied; nil never.
	Applied func(ctx context.Context) (bool, error)
	// Ended is told why the session ended: "tab-closed", "applied", "expired",
	// "failures", "stopped" or "taken-back".
	Ended func(reason string)
	// writes are what the run wrote from answer-bank entries, so an edit of one
	// names its entry; a watch resumed after a restart has none.
	writes []bankWrite
}

// Watcher learns how the user finishes a tab a stopped run kept. It exists only
// where the server builds one: a test or a command-line run has none, so none
// opens a socket to a lane. A failure here ends the recording of that tab and
// touches neither the run nor its ledger.
type Watcher struct {
	// Ctx outlives the runs: sessions end with it.
	Ctx    context.Context
	Poster Poster
	// Every, Max, Lifetime and Failures replace the defaults in tests.
	Every    time.Duration
	Max      int
	Lifetime time.Duration
	Failures int
	Now      func() time.Time
	Log      func(msg string, args ...any)

	mu       sync.Mutex
	sessions map[string]*tabWatch
	wg       sync.WaitGroup
}

// tabWatch is one tab's session: closing stop ends it, and done is closed once
// it has ended.
type tabWatch struct {
	stop, done chan struct{}
	stopping   bool
}

func (w *Watcher) every() time.Duration {
	if w.Every > 0 {
		return w.Every
	}
	return handoffEvery
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watcher) logf(msg string, args ...any) {
	if w.Log != nil {
		w.Log(msg, args...)
	}
}

// Start begins watching a tab. It returns false when the tab is already
// watched or the cap of sessions is reached; nothing it does can fail a run.
func (w *Watcher) Start(spec WatchSpec) bool {
	if w == nil || w.Poster == nil || spec.Tab == nil || spec.TabID == "" {
		return false
	}
	max := w.Max
	if max == 0 {
		max = handoffMaxTabs
	}
	w.mu.Lock()
	if w.sessions == nil {
		w.sessions = map[string]*tabWatch{}
	}
	if w.sessions[spec.TabID] != nil || len(w.sessions) >= max {
		w.mu.Unlock()
		return false
	}
	tw := &tabWatch{stop: make(chan struct{}), done: make(chan struct{})}
	w.sessions[spec.TabID] = tw
	w.mu.Unlock()
	w.wg.Go(func() {
		defer func() {
			_ = recover() // a recorder fault ends its own session only
			w.mu.Lock()
			delete(w.sessions, spec.TabID)
			w.mu.Unlock()
			close(tw.done)
		}()
		reason := w.watch(spec, tw.stop)
		if spec.Ended != nil {
			spec.Ended(reason)
		}
	})
	return true
}

// Stop ends the watch on a tab a run takes back: what the user did up to now is
// read one last time, and Stop returns once the session has ended. A tab that
// is not watched returns at once.
func (w *Watcher) Stop(tabID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	tw := w.sessions[tabID]
	if tw != nil && !tw.stopping {
		tw.stopping = true
		close(tw.stop)
	}
	w.mu.Unlock()
	if tw != nil {
		<-tw.done
	}
}

// Active is the number of tabs being watched.
func (w *Watcher) Active() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.sessions)
}

// Wait blocks until every session has ended.
func (w *Watcher) Wait() {
	if w != nil {
		w.wg.Wait()
	}
}

// recorderSession is the state of one tab's recording; its key is held only by
// this process and the recorder's world.
type recorderSession struct {
	w     *Watcher
	spec  WatchSpec
	obs   TabObserver
	key   []byte
	armed bool
	epoch string
	seq   int64
	fails int
	names entryNames
}

// watch runs one session to its end and reports why it ended.
func (w *Watcher) watch(spec WatchSpec, stop <-chan struct{}) string {
	ctx := w.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	obs, err := spec.Tab.Observe(ctx)
	if err != nil {
		w.logf("a kept tab could not be observed", "error", err.Error())
		return endReason(err)
	}
	defer obs.Release()
	s := &recorderSession{w: w, spec: spec, obs: obs, key: make([]byte, 32), names: entryNames{writes: spec.writes}}
	_, _ = rand.Read(s.key)
	life := w.Lifetime
	if life == 0 {
		life = handoffLifetime
	}
	deadline := w.now().Add(life)
	maxFails := w.Failures
	if maxFails == 0 {
		maxFails = handoffFailures
	}
	tick := time.NewTicker(w.every())
	defer tick.Stop()
	for {
		err := s.drain(ctx)
		switch {
		case err == nil:
			s.fails = 0
		case isTabClosed(err):
			return "tab-closed"
		default:
			s.fails++
			if s.fails >= maxFails {
				w.logf("a kept tab's recording ended after failed reads", "failures", s.fails)
				return "failures"
			}
		}
		if spec.Applied != nil {
			if done, aerr := spec.Applied(ctx); aerr == nil && done {
				_ = s.drain(ctx) // the last drain
				return "applied"
			}
		}
		if !w.now().Before(deadline) {
			_ = s.drain(ctx)
			return "expired"
		}
		select {
		case <-ctx.Done():
			return "stopped"
		case <-stop:
			_ = s.drain(ctx) // the last drain
			return "taken-back"
		case <-tick.C:
		}
	}
}

func isTabClosed(err error) bool {
	code, _ := browser.CodeOf(err)
	return code == browser.CodeTabClosed
}

func endReason(err error) string {
	if isTabClosed(err) {
		return "tab-closed"
	}
	return "failures"
}

// drain arms the recorder with the session's key on the first drain and
// whenever the page does not hold it (a navigation drops the script, and the
// next read finds it gone), reads what it buffered since the last read and
// posts the entries it sealed.
func (s *recorderSession) drain(ctx context.Context) error {
	if !s.armed {
		if err := s.arm(ctx); err != nil {
			return err
		}
	}
	raw, err := s.obs.Evaluate(ctx, recorderReadScript(s.epoch, s.seq))
	if err != nil {
		return err
	}
	var sealed sealedBuffer
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return fmt.Errorf("the recorder's buffer is unreadable: %w", err)
	}
	if !sealed.Armed {
		if err := s.arm(ctx); err != nil {
			return err
		}
	}
	if len(sealed.Entries) == 0 {
		if sealed.Epoch != s.epoch {
			s.epoch, s.seq = sealed.Epoch, 0
		}
		return nil
	}
	if events := s.events(unseal(s.key, sealed)); len(events) > 0 {
		// An event the receiver cannot take even alone is not retried.
		if _, _, err := PostBatches(ctx, s.w.Poster, events); err != nil {
			return err
		}
	}
	s.epoch, s.seq = sealed.Epoch, sealed.Entries[len(sealed.Entries)-1].N
	return nil
}

func (s *recorderSession) arm(ctx context.Context) error {
	if err := s.obs.Install(ctx, recorderArmScript(s.key)); err != nil {
		return err
	}
	s.armed = true
	return nil
}

// events turns the buffered entries into manual-fill events. An edit of a
// control whose value is never kept is posted without any value, options or
// section, and a value that reaches this point for such a control, by its
// question or by its options, is discarded here too. An entry names no
// selector, so the control it changed is told by its page and question: an
// edit that replaced the value the run wrote there from an answer-bank entry
// names that entry.
func (s *recorderSession) events(buf recorderBuffer) []LearnEvent {
	now := s.w.now().UnixMilli()
	out := make([]LearnEvent, 0, len(buf.Entries))
	for _, e := range buf.Entries {
		page, host := pageOf(e.URL)
		if host == "" || strings.TrimSpace(e.Question) == "" {
			continue
		}
		ev := engine.LearningEvent{
			EventID: eventUUID(fmt.Sprintf("%s|handoff|%s|%d", s.spec.RunID, e.Epoch, e.Seq)), Kind: manualFillKind,
			Host: host, URL: page, Question: e.Question, ControlKind: e.Kind, InputType: e.InputType, Autocomplete: e.Autocomplete,
			Time: now, TrustedInput: true, FrameURL: framePath(e.FrameURL),
		}
		if !e.Skipped && learn.Classify(e.Question, e.Kind, e.InputType, e.Autocomplete, e.Options) == learn.WithheldNone {
			ev.Before, ev.After, ev.OptionLabel = e.Prior, e.Value, e.OptionLabel
			ev.Options, ev.Section = e.Options, e.Section
		}
		q := learn.Normalize(e.Question)
		named := s.names.entryOf(ev, func(w bankWrite) bool { return w.page == page && learn.Normalize(w.question) == q })
		out = append(out, LearnEvent{LearningEvent: ev, ReportNumber: s.spec.Report, RunID: s.spec.RunID, EntryID: named})
	}
	return out
}

// framePath reduces a frame's address as pageOf does; a blank or srcdoc frame
// keeps its about: address, which carries nothing else.
func framePath(raw string) string {
	if raw == "about:blank" || raw == "about:srcdoc" {
		return raw
	}
	page, _ := pageOf(raw)
	return page
}

// pageOf reduces a page address to scheme, host and path and returns its host.
func pageOf(raw string) (page, host string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
	return u.String(), strings.ToLower(u.Hostname())
}
