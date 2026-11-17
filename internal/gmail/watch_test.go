package gmail

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/imaptest"
)

// watching starts a watcher over the sync env with test timings and stops it
// at the end of the test.
func (s *syncEnv) watching(hold time.Duration) *Watcher {
	s.t.Helper()
	svc := &Service{Client: s.client, Account: s.account, Repo: s.repo}
	w := &Watcher{Client: s.client, Syncer: s.sy, Repo: s.repo, Readable: svc.Readable,
		Hold: hold, Settle: 5 * time.Millisecond, Spacing: 5 * time.Millisecond, Retry: 20 * time.Millisecond, RetryCap: 50 * time.Millisecond, Recheck: 20 * time.Millisecond}
	w.Start()
	s.t.Cleanup(w.Stop)
	return w
}

// eventually fails the test unless ok turns true within ten seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// goneRound waits until a watcher that is not reading has gone round its loop:
// a wake is taken only by a pause, so after two are taken the watcher has made
// at least one whole check in between and chose to wait again.
func goneRound(t *testing.T, w *Watcher) {
	t.Helper()
	for range 2 {
		w.Wake()
		eventually(t, "the watcher takes a wake", func() bool { return len(w.wakeup()) == 0 })
	}
}

func (s *syncEnv) stored(id string) bool {
	var n int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_decisions WHERE message_id = ?`, id).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n > 0
}

func TestWatcher_SyncsMailAsGmailAnnouncesIt(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	runs := s.count("gmail_runs")
	// A long hold: only Gmail's EXISTS can end the IDLE in time.
	s.watching(time.Minute)
	eventually(t, "the watcher catches up and idles", func() bool {
		return s.count("gmail_runs") == runs+1 && len(s.imap.Commands("IDLE")) > 0
	})
	s.addJob("b1", 2000)
	// The cursor moves last, after the message is stored.
	eventually(t, "the cursor moves past the new mail", func() bool { return s.cursor() == "7:4" })
	if !s.stored("b1") {
		t.Fatal("the new mail was not stored")
	}
	var trig string
	if err := s.st.Read().QueryRow(`SELECT trigger_kind FROM gmail_runs ORDER BY id DESC LIMIT 1`).Scan(&trig); err != nil || trig != "scheduled" {
		t.Fatalf("trigger = %q, %v", trig, err)
	}
	for _, c := range s.imap.Commands("") {
		if name := strings.Fields(c)[0]; !strings.Contains(" CAPABILITY LOGIN LIST EXAMINE UID IDLE LOGOUT ", " "+name+" ") {
			t.Fatalf("the server was sent %q", c)
		}
	}
}

// A first sync capped below the mailbox goes on run after run until it read
// everything, and then the watcher idles instead of syncing again.
func TestWatcher_GoesOnUntilACappedFirstSyncReadEverything(t *testing.T) {
	s := newSyncEnv(t)
	for i, id := range []string{"a1", "a2", "a3"} {
		s.addJob(id, int64(1000+i))
	}
	s.search("Q1", "a1", "a2", "a3")
	s.sy.Cap = 1
	s.watching(time.Minute)
	eventually(t, "the first sync reads everything", func() bool { return s.cursor() == "7:3" })
	// With a minute's hold only new mail ends the IDLE, so the runs counted
	// once it idles are all it makes.
	eventually(t, "the watcher idles", func() bool { return len(s.imap.Commands("IDLE")) > 0 })
	if runs := s.count("gmail_runs"); runs != 3 {
		t.Fatalf("runs = %d, want 3 capped runs and no more", runs)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if !s.stored(id) {
			t.Fatalf("%s was not stored", id)
		}
	}
}

func TestWatcher_SignsInAgainAfterTheConnectionDrops(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.watching(time.Minute)
	eventually(t, "the watcher idles", func() bool { return len(s.imap.Commands("IDLE")) > 0 })
	logins := len(s.imap.Commands("LOGIN"))
	s.imap.Drop()
	s.addJob("b1", 2000)
	eventually(t, "the mail after the drop is stored", func() bool { return s.stored("b1") })
	if n := len(s.imap.Commands("LOGIN")); n <= logins {
		t.Fatalf("logins = %d, want more than %d", n, logins)
	}
}

// idleWaiters counts the goroutines go-imap starts to wait for the end of an
// IDLE. One lives from the server's go-ahead until the wait's stop is closed.
func idleWaiters() int {
	buf := make([]byte, 4<<20)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), "responses.(*Idle).Handle.func1")
}

// An IDLE that ends because the connection broke releases go-imap's waiting
// goroutine, as one that ends in order does.
func TestWatcher_ADroppedIdleLeavesNoGoroutineWaiting(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	w := s.watching(time.Minute)
	eventually(t, "the watcher idles", func() bool { return idleWaiters() == 1 })
	s.imap.Drop()
	s.addJob("b1", 2000)
	eventually(t, "the mail after the drop is stored", func() bool { return s.stored("b1") })
	w.Stop()
	eventually(t, "no goroutine waits on an ended IDLE", func() bool { return idleWaiters() == 0 })
}

func TestWatcher_WaitsForAnAppPassword(t *testing.T) {
	e := newEnv(t)
	s := &syncEnv{env: e, sy: &Syncer{Client: e.client, Repo: e.repo, Classify: jobClassify, Version: testVersion, Queries: []string{"Q1"}}}
	s.addJob("a1", 1000)
	s.search("Q1", "a1")
	w := s.watching(time.Minute)
	goneRound(t, w)
	if n := len(s.imap.Commands("")); n != 0 {
		t.Fatalf("%d commands reached the server before a password was stored", n)
	}
	e.connect()
	w.Wake()
	eventually(t, "the first sync after connecting stores the mail", func() bool { return s.stored("a1") })
}

func TestWatcher_StopsSigningInOnceGmailRefusesThePassword(t *testing.T) {
	s := newSyncEnv(t)
	s.imap.Refuse("Invalid credentials (Failure)")
	w := s.watching(time.Minute)
	eventually(t, "the refusal is recorded", func() bool { return s.refused.Load() == 1 })
	goneRound(t, w)
	if n := len(s.imap.Commands("LOGIN")); n != 1 {
		t.Fatalf("LOGIN sent %d times, want once", n)
	}
}

// Gmail's draft autosave moves UIDNEXT without leaving mail above the
// cursor; the watcher syncs once for it and not again.
func TestWatcher_SyncsOnceForMailThatIsGoneAgain(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	s.watching(30 * time.Millisecond)
	eventually(t, "the watcher catches up and idles", func() bool { return len(s.imap.Commands("IDLE")) > 0 })
	runs := s.count("gmail_runs")
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "d1", Subject: "draft", MS: 2000})
	s.imap.Remove("d1")
	eventually(t, "a sync reads the change", func() bool { return s.count("gmail_runs") == runs+1 })
	// Each 30ms IDLE ends in a look at UIDNEXT; two more IDLEs mean it looked
	// and did not sync.
	idles := len(s.imap.Commands("IDLE"))
	eventually(t, "the watcher idles twice more", func() bool { return len(s.imap.Commands("IDLE")) >= idles+2 })
	if n := s.count("gmail_runs"); n != runs+1 {
		t.Fatalf("%d syncs after one change, want 1", n-runs)
	}
	if c := s.cursor(); c != "7:3" {
		t.Fatalf("cursor = %q, want 7:3", c)
	}
}

func TestWatcher_NeverReadsADemoWorkspace(t *testing.T) {
	e, stack := demoStack(t)
	stack.Watcher.Recheck = 10 * time.Millisecond
	stack.Watcher.Start()
	defer stack.Watcher.Stop()
	goneRound(t, stack.Watcher)
	if n := len(e.imap.Commands("")); n != 0 {
		t.Fatalf("%d commands reached the server from a demo workspace", n)
	}
}

func TestWatcher_StopEndsAnIdle(t *testing.T) {
	s := newSyncEnv(t)
	s.bootstrapped()
	w := s.watching(time.Hour)
	eventually(t, "the watcher idles", func() bool { return len(s.imap.Commands("IDLE")) > 0 })
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not end the IDLE")
	}
	eventually(t, "the connection is closed", func() bool { return s.imap.Connections() == 0 })
}
