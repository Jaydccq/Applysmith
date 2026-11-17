package agent

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// MaxAgentRuns is how many runs the Ask Agent may have going at once.
const MaxAgentRuns = 2

// startsRuns names the tools whose call starts a background run.
var startsRuns = []string{"evaluate_job", "scan_portal", "autofill_application", "apply_from_list"}

// QueueNote is the line a waiting start shows on the runs board, and its
// stored record.
type QueueNote interface {
	// Touch tells the board the start is still waiting.
	Touch(ctx context.Context)
	// Release takes the line and the record away once the start has begun,
	// been refused, given up or been told.
	Release(ctx context.Context)
}

// LeftStart is a start that still waited when the server stopped.
type LeftStart struct {
	SessionID, Label string
	Note             QueueNote
}

// RunGate keeps the Ask Agent to MaxAgentRuns runs at once. A start past the
// cap is queued: it is stored and shows on the runs board as working with a
// note, the model is told so, and the call goes ahead by itself when a run
// ends. A start the tool would refuse now (its Conflict) and a start the same
// as one already waiting are answered at once instead; a waiting start that is
// refused or gives up later is told to its conversation, and so, at the next
// start (Settle), is one the server stopped before it began.
type RunGate struct {
	// Active counts the runs conversations started that are still going.
	Active func(ctx context.Context) (int, error)
	// Hold stores a waiting start of a conversation and puts it on the runs
	// board.
	Hold func(ctx context.Context, sessionID, label string) (QueueNote, error)
	// Left lists the starts a stopped server left waiting.
	Left func(ctx context.Context) ([]LeftStart, error)
	// Tell appends a line to a conversation, as a run's completion message is.
	Tell func(ctx context.Context, sessionID, body string)
	// Life bounds a waiting start; it ends when the server stops.
	Life context.Context
	// Poll is how often a waiting start looks for a free slot; Wait is how long
	// it waits before it gives up. Zero takes 2 seconds and 30 minutes.
	Poll, Wait time.Duration

	mu      sync.Mutex
	waiting map[string]bool // tool and canonical arguments of each waiting start

	// starting makes the count and the start that follows it one step. A run
	// counts as going once its start has recorded it, so two starts that found
	// the last slot together would otherwise both take it.
	starting sync.Mutex
}

func (g *RunGate) poll() time.Duration {
	if g.Poll > 0 {
		return g.Poll
	}
	return 2 * time.Second
}

func (g *RunGate) wait() time.Duration {
	if g.Wait > 0 {
		return g.Wait
	}
	return 30 * time.Minute
}

// wrap gates the tools that start runs. A nil gate leaves them as they are.
func (g *RunGate) wrap(tools []Tool) []Tool {
	if g == nil || g.Active == nil {
		return tools
	}
	for i := range tools {
		if slices.Contains(startsRuns, tools[i].Name) && tools[i].Run != nil {
			tools[i].Run = g.gated(tools[i])
		}
	}
	return tools
}

func (g *RunGate) gated(t Tool) ToolFunc {
	run := t.Run
	return func(ctx context.Context, c ToolCall) (ToolResult, error) {
		g.starting.Lock()
		n, err := g.Active(ctx)
		if err != nil || n < MaxAgentRuns {
			defer g.starting.Unlock()
			return run(ctx, c)
		}
		g.starting.Unlock()
		if t.Conflict != nil {
			if err := t.Conflict(ctx, c); err != nil {
				return answerRefusal(err)
			}
		}
		key := t.Name + " " + canonicalArgs(c.Args)
		if !g.claim(key) {
			return ToolResult{Text: "The same start is already queued and begins by itself when one of my runs finishes, so I did not queue it twice.", Clean: true}, nil
		}
		var note QueueNote
		if g.Hold != nil {
			if note, err = g.Hold(ctx, sessionOf(c), t.Label); err != nil {
				g.release(key)
				return ToolResult{}, fmt.Errorf("the start could not be queued: %w", err)
			}
		}
		go g.startWhenFree(t, run, c, note, key)
		return ToolResult{Text: fmt.Sprintf("Queued: %d runs I started are still going, which is the most at once. This start (%s) waits for one to finish and then begins by itself; the Runs page shows it as working with a queued note, and you are told when it ends.", n, startName(t)), Clean: true}, nil
	}
}

func (g *RunGate) claim(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting[key] {
		return false
	}
	if g.waiting == nil {
		g.waiting = map[string]bool{}
	}
	g.waiting[key] = true
	return true
}

func (g *RunGate) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.waiting, key)
}

// startWhenFree waits for a free slot and runs the call. The guard chain already
// allowed the call when it was made; only the start waits. A start that does
// not begin (refused, failed, or no slot within the wait) is told to the
// conversation that made it. One the server stops keeps its stored record and
// board line, which the next start tells and takes away (Settle).
func (g *RunGate) startWhenFree(t Tool, run ToolFunc, c ToolCall, note QueueNote, key string) {
	life := g.Life
	if life == nil {
		life = context.Background()
	}
	ctx, cancel := context.WithTimeout(life, g.wait())
	defer cancel()
	defer g.release(key)
	done := func() {
		if note != nil {
			note.Release(context.WithoutCancel(life))
		}
	}
	tick := time.NewTicker(g.poll())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if life.Err() != nil {
				return
			}
			g.tell(life, sessionOf(c), startName(t), fmt.Sprintf("no run of mine finished within %s, so it gave up; ask again to start it", g.wait()))
			done()
			return
		case <-tick.C:
		}
		if note != nil {
			note.Touch(ctx)
		}
		g.starting.Lock()
		if n, err := g.Active(ctx); err != nil || n >= MaxAgentRuns {
			g.starting.Unlock()
			continue
		}
		rctx, stop := context.WithTimeout(life, startTimeout*4)
		res, err := run(rctx, c)
		stop()
		g.starting.Unlock()
		switch {
		case err != nil:
			g.tell(life, sessionOf(c), startName(t), "it could not be started: "+err.Error())
		case res.RunID == "":
			g.tell(life, sessionOf(c), startName(t), "it was refused: "+strings.TrimSuffix(res.Text, "."))
		}
		done()
		return
	}
}

// Settle tells the conversation of each start a stopped server left waiting
// that it did not begin, and takes the start away. It is never replayed: the
// guard chain decided it in a turn that is over, and nothing is replayed at a
// start (storage.md).
func (g *RunGate) Settle(ctx context.Context) error {
	if g == nil || g.Left == nil {
		return nil
	}
	left, err := g.Left(ctx)
	if err != nil {
		return err
	}
	for _, s := range left {
		g.tell(ctx, s.SessionID, labelName(s.Label), "the server stopped before one of my runs finished; ask again to start it")
		s.Note.Release(ctx)
	}
	return nil
}

// startName names a start by its tool's label without "Starting": "a scan".
func startName(t Tool) string { return labelName(cmp.Or(t.Label, t.Name)) }

func labelName(label string) string {
	return strings.TrimPrefix(strings.ToLower(cmp.Or(label, "a run")), "starting ")
}

// tell says in a conversation why a queued start did not begin.
func (g *RunGate) tell(ctx context.Context, sessionID, name, why string) {
	if g.Tell == nil || sessionID == "" {
		return
	}
	g.Tell(context.WithoutCancel(ctx), sessionID, fmt.Sprintf("The queued start (%s) did not begin: %s.", name, why))
}
