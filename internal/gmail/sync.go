package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Sync bounds.
const (
	MaxMessagesPerRun = 4000
	FetchBatch        = 200
)

// Syncer runs the staged incremental sync over All Mail. Only one sync,
// resync or reclassification runs per workspace at a time: the in-process
// slot is the fast path and the claim in the database (see Repo.beginRun)
// holds across Syncer objects and processes.
type Syncer struct {
	Client   *Client
	Repo     *Repo
	Classify ClassifyFunc
	// Version is the classifier version stored with every decision.
	Version string
	// Linker is optional; without it the link stage is skipped.
	Linker Linker
	// PostSync runs best effort after a fully covered, successful run and can
	// never change the run's status.
	PostSync func(ctx context.Context)
	// Ended runs after every recorded run, whatever its outcome, so the mail
	// views follow a sync the dashboard did not start (the schedule's).
	Ended func()

	// Cap and Batch default to the package constants; tests lower them.
	Cap, Batch int
	// Queries defaults to BootstrapQueries.
	Queries []string

	mu      sync.Mutex
	running bool
}

func (s *Syncer) cap() int   { return orDefault(s.Cap, MaxMessagesPerRun) }
func (s *Syncer) batch() int { return orDefault(s.Batch, FetchBatch) }

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func (s *Syncer) queries() []string {
	if len(s.Queries) > 0 {
		return s.Queries
	}
	return BootstrapQueries
}

// Running reports whether a run is in progress in this process.
func (s *Syncer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// acquire takes the single-run slot.
func (s *Syncer) acquire() (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil, ErrAlreadyRunning
	}
	s.running = true
	return func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }, nil
}

// Recover marks runs left running by a process that is gone, or running longer
// than StaleRunAfter, as failed. A run held by a live process is left alone.
// Call it once at server start and at command start.
func (s *Syncer) Recover(ctx context.Context) error { return s.Repo.interruptAbandoned(ctx) }

// Run performs one sync and returns its record. A failed run is returned with
// a nil error: the record carries the failure. An error means the run could
// not even be recorded or another run is in progress.
func (s *Syncer) Run(ctx context.Context, trig Trigger) (*RunRecord, error) {
	if err := s.Repo.guardDemo(ctx); err != nil {
		return nil, err
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return s.runLocked(ctx, trig)
}

func (s *Syncer) runLocked(ctx context.Context, trig Trigger) (*RunRecord, error) {
	cursor, _, hasCursor := s.Repo.Cursor(ctx)
	mode := ModeIncr
	if !hasCursor {
		mode = ModeBootstrap
	}
	rec, err := s.Repo.beginRun(ctx, trig, mode, s.Version, nil)
	if err != nil {
		return nil, err
	}
	return s.execute(ctx, rec, cursor)
}

// execute runs the stages of a run whose claim is held, over one IMAP session.
func (s *Syncer) execute(ctx context.Context, rec *RunRecord, cursor string) (*RunRecord, error) {
	if s.Ended != nil {
		defer s.Ended()
	}
	rec.HistoryStart = cursor
	sess, listErr := s.Client.open(ctx, nil)
	var src *source
	if listErr == nil {
		defer sess.close()
		src, listErr = s.list(ctx, sess, rec, cursor)
	}
	if listErr != nil {
		rec.fail(StageList, listErr)
		rec.Counts.Failed++
	} else {
		rec.markOK(StageList)
		s.process(ctx, sess, rec, src)
	}
	return s.finish(ctx, rec, src, listErr)
}

// mark is a place in All Mail: its UIDVALIDITY and the last UID read.
type mark struct{ validity, uid uint32 }

func (m mark) String() string { return fmt.Sprintf("%d:%d", m.validity, m.uid) }

func parseMark(s string) (mark, bool) {
	v, u, ok := strings.Cut(s, ":")
	if !ok {
		return mark{}, false
	}
	validity, err1 := strconv.ParseUint(v, 10, 32)
	uid, err2 := strconv.ParseUint(u, 10, 32)
	if err1 != nil || err2 != nil || validity == 0 {
		return mark{}, false
	}
	return mark{uint32(validity), uint32(uid)}, true
}

// source is what the list stage produced.
type source struct {
	ids       []string          // to fetch, in order
	uids      map[string]uint32 // the All Mail UID of each id
	newest    string            // incremental: the mark the cursor moves to
	drained   bool
	resume    string // resume token to persist when not drained
	start     string // bootstrap: the mark read before listing
	bootstrap bool
}

// resumeState continues a capped bootstrap: the query it stopped in and the
// UIDs below which that query goes on, under one UIDVALIDITY.
type resumeState struct {
	Validity   uint32 `json:"validity,omitempty"`
	QueryIndex int    `json:"queryIndex,omitempty"`
	Below      uint32 `json:"below,omitempty"`
}

func parseResume(tok string) resumeState {
	var r resumeState
	if tok != "" {
		_ = json.Unmarshal([]byte(tok), &r)
	}
	return r
}

// list reads the UIDs above the cursor. A cursor that no longer applies (All
// Mail's UIDVALIDITY changed) is dropped and the bootstrap searches run again.
func (s *Syncer) list(ctx context.Context, sess *session, rec *RunRecord, cursor string) (*source, error) {
	if cursor == "" {
		return s.listBootstrap(ctx, sess)
	}
	m, ok := parseMark(cursor)
	if ok && m.validity == sess.cur.validity {
		return s.listSince(ctx, sess, m)
	}
	rec.Mode, rec.FallbackUsed = ModeFallback, true
	if err := s.Repo.SetState(ctx, map[string]string{keyCursor: "", keyCursorUpdated: "", keyResumeToken: "", keyBootstrapStart: "",
		keyCursorResetAt: stamp(s.Repo.now())}); err != nil {
		return nil, err
	}
	return s.listBootstrap(ctx, sess)
}

func (s *Syncer) listBootstrap(ctx context.Context, sess *session) (*source, error) {
	repo := s.Repo
	src := &source{bootstrap: true, drained: true}
	// The mark is read before anything is listed, so mail that arrives
	// during a long bootstrap is read by the next incremental run.
	start, ok := parseMark(repo.stateOr(ctx, keyBootstrapStart))
	if !ok || start.validity != sess.cur.validity {
		start = mark{sess.cur.validity, sess.cur.next - 1}
		if err := repo.SetState(ctx, map[string]string{keyBootstrapStart: start.String(), keyResumeToken: ""}); err != nil {
			return nil, err
		}
	}
	src.start = start.String()
	resume := parseResume(repo.stateOr(ctx, keyResumeToken))
	if resume.Validity != start.validity {
		resume = resumeState{}
	}
	seen := map[uint32]bool{}
	var uids []uint32
	qs := s.queries()
collect:
	for qi := resume.QueryIndex; qi < len(qs); qi++ {
		found, err := sess.searchRaw(qs[qi])
		if err != nil {
			return nil, err
		}
		// Newest first, as Gmail lists.
		for i := len(found) - 1; i >= 0; i-- {
			u := found[i]
			if u > start.uid || seen[u] || qi == resume.QueryIndex && resume.Below != 0 && u >= resume.Below {
				continue
			}
			if len(uids) >= s.cap() {
				src.drained = false
				tok, _ := json.Marshal(resumeState{Validity: start.validity, QueryIndex: qi, Below: u + 1})
				src.resume = string(tok)
				break collect
			}
			seen[u] = true
			uids = append(uids, u)
		}
	}
	return src, s.identify(sess, src, uids)
}

// identify reads the Gmail id of each listed UID, keeping the order. A UID
// expunged since the search is left out.
func (s *Syncer) identify(sess *session, src *source, uids []uint32) error {
	ids, err := sess.ids(uids)
	if err != nil {
		return err
	}
	src.uids = make(map[string]uint32, len(ids))
	for _, u := range uids {
		if id := ids[u]; id != "" {
			src.ids = append(src.ids, id)
			src.uids[id] = u
		}
	}
	return nil
}

// listSince lists the UIDs above the cursor, oldest first. The cap counts
// messages not yet decided, so a capped run always makes progress: the
// cursor moves to the last UID it took and the next run goes on from there.
func (s *Syncer) listSince(ctx context.Context, sess *session, from mark) (*source, error) {
	src := &source{drained: true, newest: from.String()}
	uids, err := sess.searchAfter(from.uid)
	if err != nil {
		return nil, err
	}
	if err := s.identify(sess, src, uids); err != nil {
		return nil, err
	}
	known, err := s.Repo.existingDecisions(ctx, s.Version, src.ids)
	if err != nil {
		return nil, err
	}
	last, fresh := from.uid, 0
	for i, id := range src.ids {
		if !known[id] {
			if fresh >= s.cap() {
				src.drained = false
				src.ids = src.ids[:i]
				break
			}
			fresh++
		}
		last = src.uids[id]
	}
	if src.drained && len(uids) > 0 {
		last = uids[len(uids)-1]
	}
	src.newest = mark{from.validity, last}.String()
	return src, nil
}

// process fetches, classifies and stores the listed messages in batches. Each
// batch commits before the next is fetched, so an interrupted run keeps what
// it finished and the next run lists the rest again.
func (s *Syncer) process(ctx context.Context, sess *session, rec *RunRecord, src *source) {
	repo := s.Repo
	rec.Counts.Listed = len(src.ids)
	skip, err := repo.tombstoned(ctx, src.ids)
	if err != nil {
		rec.fail(StageFetch, err)
		return
	}
	known, err := repo.existingDecisions(ctx, s.Version, src.ids)
	if err != nil {
		rec.fail(StageFetch, err)
		return
	}
	var want []string
	for _, id := range src.ids {
		switch {
		case skip[id]:
		case known[id]:
			rec.Counts.Deduplicated++
		default:
			want = append(want, id)
		}
	}
	mailbox := repo.Mailbox(ctx)
	for start := 0; start < len(want); start += s.batch() {
		batch := want[start:min(start+s.batch(), len(want))]
		uids := make([]uint32, len(batch))
		for i, id := range batch {
			uids[i] = src.uids[id]
		}
		got, err := sess.messages(uids)
		if err != nil {
			rec.Counts.Failed++
			rec.fail(StageFetch, err)
			return
		}
		var fetched []*Message
		var gone []string
		for i, id := range batch {
			if m := got[uids[i]]; m != nil {
				fetched = append(fetched, m)
			} else {
				gone = append(gone, id)
			}
		}
		rec.Counts.Fetched += len(fetched)
		scored, err := s.classifyBatch(fetched, mailbox)
		if err != nil {
			rec.Counts.Failed++
			rec.fail(StageClassify, err)
			return
		}
		rec.Counts.Classified += len(scored)
		stored, review, err := repo.storeBatch(ctx, rec.ID, s.Version, scored, gone, false)
		if err != nil {
			rec.fail(StageStore, err)
			return
		}
		rec.Counts.Stored += stored
		rec.Counts.Review += review
		rec.Counts.Tombstoned += len(gone)
		for _, sm := range scored {
			if sm.verdict.Kind == VerdictIgnored {
				rec.Counts.Ignored++
			}
		}
		rec.Counts.Deduplicated += len(scored) - stored
	}
	rec.markOK(StageFetch)
	rec.markOK(StageClassify)
	rec.markOK(StageStore)
	s.link(ctx, rec)
}

func (s *Syncer) classifyBatch(msgs []*Message, mailbox string) (out []scoredMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("classifier panicked: %v", r)
		}
	}()
	for _, m := range msgs {
		out = append(out, scoredMessage{msg: m, verdict: s.Classify(m, mailbox)})
	}
	return out, nil
}

// link runs the link stage. Its failure is recorded without undoing anything
// already committed.
func (s *Syncer) link(ctx context.Context, rec *RunRecord) {
	if s.Linker == nil {
		if st := rec.stage(StageLink); st != nil {
			st.Status = StageSkipped
		}
		return
	}
	n, err := s.Linker.Link(ctx, rec.ID)
	if err != nil {
		rec.fail(StageLink, err)
		return
	}
	rec.Counts.Linked += n
	rec.markOK(StageLink)
}

// finish decides coverage, moves the cursor as the last step and saves the run.
// A complete bootstrap sets the cursor to the mark it read first; a successful
// incremental run moves it to the last UID it took, capped or not.
func (s *Syncer) finish(ctx context.Context, rec *RunRecord, src *source, listErr error) (*RunRecord, error) {
	now := s.Repo.now().UTC()
	rec.EndedAt = &now
	if rec.FailedStage == "" {
		rec.Status = "succeeded"
	} else {
		rec.Status = "failed"
	}
	// A skipped link stage is not a failure.
	allOK := rec.FailedStage == ""
	rec.CoverageComplete = listErr == nil && src != nil && src.drained && allOK
	if src != nil {
		rec.HistoryEnd = src.newest
		rec.ResumeToken = src.resume
	}
	// The end is written even when the caller gave up: a row left running
	// under this live process would refuse every later run.
	end := context.WithoutCancel(ctx)
	// A connection made while the run read mail may be another account's
	// (Account.save), so this run's place is not written over it.
	issued, _ := parseStamp(s.Repo.stateOr(end, keyConnectedAt))
	err := s.Repo.saveRun(end, rec, func(tx *sql.Tx) error {
		if src == nil || issued.After(rec.StartedAt) {
			return nil
		}
		kv := map[string]string{}
		switch {
		case src.bootstrap && rec.CoverageComplete:
			kv[keyCursor] = src.start
			kv[keyCursorUpdated] = stamp(now)
			kv[keyResumeToken], kv[keyBootstrapStart] = "", ""
		case src.bootstrap:
			kv[keyResumeToken] = src.resume
		case allOK:
			kv[keyCursor] = src.newest
			if rec.CoverageComplete {
				kv[keyCursorUpdated] = stamp(now)
			}
		}
		return setState(end, tx, kv)
	})
	if err != nil {
		return rec, err
	}
	if rec.Status == "succeeded" && rec.CoverageComplete && s.PostSync != nil {
		func() {
			defer func() { _ = recover() }()
			s.PostSync(ctx)
		}()
	}
	return rec, nil
}
