package api

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLockHeldCreatesNothing(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing", ".applysmith")
	if held, err := LockHeld(state); held || err != nil {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if _, err := os.Stat(filepath.Dir(state)); !os.IsNotExist(err) {
		t.Errorf("LockHeld created a directory: %v", err)
	}
}

func TestLockHeldSeesAHolderAndLeavesAStaleFileAlone(t *testing.T) {
	state := t.TempDir()
	l, err := AcquireLock(state)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := LockHeld(state); !held || err != nil {
		t.Errorf("while held: held=%v err=%v", held, err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if held, err := LockHeld(state); held || err != nil {
		t.Errorf("after release: held=%v err=%v", held, err)
	}
	if _, err := os.Stat(LockPath(state)); err != nil {
		t.Errorf("the stale lock file must stay for the stale-lock report: %v", err)
	}
}

// A probe holds the lock for an instant; it must not make a server that starts
// at the same moment report that someone else holds the workspace.
func TestProbingDoesNotMakeAcquireFail(t *testing.T) {
	state := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = LockHeld(state)
				}
			}
		}()
	}
	defer func() { close(stop); wg.Wait() }()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		l, err := AcquireLock(state)
		if err != nil {
			t.Fatalf("acquire during a probe: %v", err)
		}
		if err := l.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

// A document left by an earlier server names an address nobody answers on, so
// it must not be reported as the running server.
func TestStaleDocumentIsNotReportedAsARunningServer(t *testing.T) {
	state := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	stale, err := AcquireLock(state)
	if err != nil {
		t.Fatal(err)
	}
	info := LockInfo{PID: os.Getpid(), Port: port, PathKey: "000000000000", Origin: "http://127.0.0.1:" + strconv.Itoa(port)}
	if err := stale.WriteInfo(info); err != nil {
		t.Fatal(err)
	}
	// Another holder now has the lock while the old document is still there.
	_, err = AcquireLock(state)
	if !IsStartupCode(err, CodeWorkspaceLocked) {
		t.Fatalf("want workspace_locked, got %v", err)
	}
	if strings.Contains(err.Error(), info.Origin) {
		t.Errorf("the stale origin is reported: %v", err)
	}
}

func TestHeldErrorNamesARealServerOnlyWhenItAnswers(t *testing.T) {
	h := newHarness(t)
	_, err := AcquireLock(h.ws.StateDir())
	if !IsStartupCode(err, CodeServerAlreadyRunning) || !strings.Contains(err.Error(), h.srv.Origin()) {
		t.Fatalf("%v", err)
	}
}

func TestLockDocumentIsNeverEmptyWhileItIsRefreshed(t *testing.T) {
	state := t.TempDir()
	l, err := AcquireLock(state)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	info := LockInfo{PID: 1, Port: 1, PathKey: "k", Origin: "http://127.0.0.1:1", LaunchCode: strings.Repeat("a", 43)}
	if err := l.WriteInfo(info); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan int)
	go func() {
		bad := 0
		for {
			select {
			case <-stop:
				done <- bad
				return
			default:
				if _, err := ReadLockInfo(state); err != nil {
					bad++
				}
			}
		}
	}()
	for i := range 300 {
		info.LaunchCode = strings.Repeat(string(rune('a'+i%26)), 43)
		if i%7 == 0 {
			info.LaunchCode = ""
		}
		if err := l.WriteInfo(info); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	if bad := <-done; bad != 0 {
		t.Errorf("a reader saw an unreadable document %d times", bad)
	}
}

// lockHelperEnv makes the test binary hold a workspace lock until it is killed.
const lockHelperEnv = "APPLYSMITH_TEST_LOCK_HOLDER"

func TestScheduler_Lock_StaleLockTakenOver(t *testing.T) {
	if dir := os.Getenv(lockHelperEnv); dir != "" {
		l, err := AcquireLock(dir)
		if err != nil {
			fmt.Println("failed", err)
			os.Exit(2)
		}
		_ = l.WriteInfo(LockInfo{PID: os.Getpid(), Port: 1, PathKey: "000000000000", Origin: "http://127.0.0.1:1"})
		fmt.Println("held")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	state := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScheduler_Lock_StaleLockTakenOver$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+state)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("the holder did not take the lock: %q %v", line, err)
	}
	if _, err := AcquireLock(state); !IsStartupCode(err, CodeWorkspaceLocked) {
		t.Fatalf("a live holder was displaced: %v", err)
	}
	// The holder dies without releasing anything; its lock file stays behind.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if held, err := LockHeld(state); held || err != nil {
		t.Fatalf("a dead holder still holds the lock: %v %v", held, err)
	}
	l, err := AcquireLock(state)
	if err != nil {
		t.Fatalf("the stale lock was not taken over: %v", err)
	}
	defer l.Release()
	if _, err := ReadLockInfo(state); err == nil {
		t.Error("the dead holder's document survived the takeover")
	}
}
