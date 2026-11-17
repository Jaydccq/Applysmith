//go:build browser

package apply

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/ledger"
)

// A run that dies after it took the press claim is a real process death: the
// test binary starts itself as a child with its own Chrome, the child exits
// without any cleanup at the chosen point, and the parent reads what the
// database holds.

const childExit = 97

// chromeSlots bounds the Chromes the children launch at once; launching many
// on a busy machine times out.
var chromeSlots = make(chan struct{}, 2)

type childTB struct {
	dir string
	fns []func()
}

func (c *childTB) Helper()          {}
func (c *childTB) Cleanup(f func()) { c.fns = append(c.fns, f) }
func (c *childTB) TempDir() string  { return c.dir }
func (c *childTB) Fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "child: "+format+"\n", a...)
	os.Exit(2)
}

func childMain() int {
	dir, url, die := os.Getenv("AJ_DIR"), os.Getenv("AJ_URL"), os.Getenv("AJ_DIE")
	chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	root := filepath.Join(dir, "chrome")
	inst, err := browser.Open(ctx, browser.Options{
		Chrome: chrome, ProfileDir: filepath.Join(root, "applysmith-apply-e2e-profile"), StateDir: filepath.Join(root, "state"),
		DownloadsDir: filepath.Join(root, "downloads"), Environ: os.Environ(),
	}.WithHostRules(mockHostRules).WithHeadless())
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		return 2
	}
	watchdog(inst.PID())
	exit := func() { os.Exit(childExit) }
	hk := Hooks{}
	switch die {
	case "claim":
		hk.AfterClaim = exit
	case "press":
		hk.AfterPress = exit
	}
	tb := &childTB{dir: dir}
	g := newE2E(tb, inst.Lane(browser.LaneApply), dir, hk)
	run := g.runner.RunOne(ctx, submitInput(url))
	fmt.Fprintf(os.Stderr, "child: the run ended without dying: %+v\n", run.Outcome)
	return 3
}

func TestE2E_Apply_ProcessDeathAfterClaimOrPressLeavesADurableUncertainRowAndNoRetry(t *testing.T) {
	for _, kind := range adapters {
		for _, die := range []string{"claim", "press"} {
			t.Run(kind+"/"+die, func(t *testing.T) {
				t.Parallel()
				chromeSlots <- struct{}{}
				t.Cleanup(func() { <-chromeSlots })
				inst, st := sharedBrowser(t)
				lane := inst.Lane(browser.LaneApply)
				b := st.add(&board{kind: kind, marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1))})
				url := st.postingURL(b)
				dir := t.TempDir()
				g := newE2E(t, lane, dir, Hooks{})
				g.seedPosting(t, url)
				g.st.Close()

				// Chrome outlives the child that launched it (the product leaves it
				// for the next start to adopt). The test never does: whatever happens,
				// its process group is killed, and no Chrome of this profile remains.
				chromeRoot := filepath.Join(dir, "chrome")
				t.Cleanup(func() { reapChrome(t, chromeRoot) })
				cmd := exec.Command(os.Args[0])
				cmd.Env = append(os.Environ(), "AJ_APPLY_CHILD=1", "AJ_DIR="+dir, "AJ_URL="+url, "AJ_DIE="+die)
				out, err := cmd.CombinedOutput()
				reapChrome(t, chromeRoot)
				ee, ok := err.(*exec.ExitError)
				if !ok || ee.ExitCode() != childExit {
					t.Fatalf("the child did not die at the hook: %v\n%s", err, out)
				}
				childPID := cmd.ProcessState.Pid()

				g2 := newE2E(t, lane, dir, Hooks{})
				var state string
				var pid int
				if err := g2.st.Read().QueryRow(`SELECT state, pid FROM submissions`).Scan(&state, &pid); err != nil {
					t.Fatalf("no claim row survived the death: %v", err)
				}
				if state != "submitting" || pid != childPID {
					t.Fatalf("row %s pid %d, want submitting by %d", state, pid, childPID)
				}
				if g2.count(t, `SELECT count(*) FROM apply_runs WHERE outcome_kind = 'running'`) != 1 {
					t.Fatal("the dead run's record is not there")
				}
				// The restart: the crash sweep and the run sweep.
				n, err := g2.ledger.SweepStartup(context.Background(), ledger.ProcessRunning)
				if err != nil || n != 1 {
					t.Fatalf("swept %d, %v", n, err)
				}
				if n, err := SweepRuns(context.Background(), g2.st, time.Now()); err != nil || n != 1 {
					t.Fatalf("swept runs %d, %v", n, err)
				}
				if state, _, _ := g2.ledgerState(t); state != "may-have-submitted" {
					t.Fatalf("ledger %s", state)
				}
				want := 0
				if die == "press" {
					want = 1
				}
				if got := b.presses(); got != want {
					t.Fatalf("the server saw %d presses, want %d", got, want)
				}
				// Nothing retries on its own, and a new run is refused until the user resubmits.
				next := g2.runner.RunOne(context.Background(), submitInput(url))
				if next.Outcome.Kind != OutcomeStopped || next.Outcome.Class != ClassPosting || !strings.Contains(next.Outcome.Reason, "resubmit") {
					t.Fatalf("next run: %+v", next.Outcome)
				}
				if got := b.presses(); got != want {
					t.Fatalf("a blocked run pressed: %d presses", got)
				}
				if g2.count(t, `SELECT count(*) FROM gate_decisions WHERE gate = 'press_claim' AND verdict = 'allow'`) != 1 {
					t.Fatal("the claim decision of the dead run was not audited")
				}
			})
		}
	}
}

// reapChrome kills the Chrome a child started and returns only when every
// process of its profile is gone, so the TempDir removal that follows never
// races a dying browser that still writes into the profile.
func reapChrome(t *testing.T, root string) {
	t.Helper()
	if !stopChrome(root, 30*time.Second) {
		t.Errorf("a Chrome with the profile %s is still running", root)
	}
}
