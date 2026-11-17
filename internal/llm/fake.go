package llm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// Step is one scripted response of a fixture.
type Step struct {
	Text string
	// Fail scripts a failure of any fallback kind instead of text.
	Fail    Kind
	Status  int
	Latency time.Duration
	// Finish overrides the finish reason, "stop" by default.
	Finish string
}

// Fixture answers fake-provider calls for a task. Steps are consumed one per
// call and the last one repeats.
type Fixture struct {
	Task string
	// Model limits the fixture to one model id; empty matches any.
	Model string
	// Contains matches a substring of the prompt; Hash matches the hex SHA-256
	// of the prompt. At least one of them must be set.
	Contains string
	Hash     string
	Steps    []Step
}

// Fake is the deterministic, offline provider. It never invents a success: a
// call is answered by a scripted fixture or by an installed generator, and
// fails otherwise.
type Fake struct {
	mu          sync.Mutex
	fixtures    []*fixtureState
	generate    Generator
	testFixture bool
}

// Generator answers a prompt for a task when no scripted fixture matches. It
// must be deterministic: the same prompt always gives the same text.
type Generator func(task, prompt string) (text string, ok bool)

// SetGenerator installs the fallback that serves prompts no fixture matches,
// such as the evaluation fixture generators. Scripted fixtures win.
func (f *Fake) SetGenerator(g Generator) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generate = g
}

type fixtureState struct {
	Fixture
	next int
}

// NewFake returns a fake provider answering from the fixtures.
func NewFake(fixtures ...Fixture) *Fake {
	f := &Fake{}
	for _, fx := range fixtures {
		f.fixtures = append(f.fixtures, &fixtureState{Fixture: fx})
	}
	return f
}

// addTestFixture scripts the fixed test prompt of `applysmith llm test` after any
// fixture the caller registered, so a scripted failure for it still wins.
func (f *Fake) addTestFixture() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.testFixture {
		return
	}
	f.testFixture = true
	f.fixtures = append(f.fixtures, &fixtureState{Fixture: Fixture{Task: testTask, Hash: PromptHash(testPrompt), Steps: []Step{{Text: "ok"}}}})
}

// PromptHash is the identifier a Hash fixture matches and a missing-fixture
// error names; it reveals nothing of the prompt.
func PromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func (f *Fake) step(task, model, prompt string) (Step, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := PromptHash(prompt)
	for _, fx := range f.fixtures {
		if fx.Task != task || (fx.Model != "" && fx.Model != model) {
			continue
		}
		if !(fx.Contains != "" && strings.Contains(prompt, fx.Contains)) && !(fx.Hash != "" && fx.Hash == h) {
			continue
		}
		if len(fx.Steps) == 0 {
			continue
		}
		s := fx.Steps[min(fx.next, len(fx.Steps)-1)]
		fx.next++
		return s, true
	}
	if f.generate != nil {
		if text, ok := f.generate(task, prompt); ok {
			return Step{Text: text}, true
		}
	}
	return Step{}, false
}

// fakeTokens is a deterministic function of text length.
func fakeTokens(s string) int { return (len(s) + 3) / 4 }

func (f *Fake) complete(ctx context.Context, a *attempt, in attemptInput) (completion, error) {
	full := in.system + "\n\n" + in.prompt
	st, ok := f.step(in.task, a.cand.model, in.prompt)
	if !ok {
		return completion{}, &Error{Kind: KindFakeFixture,
			Message: fmt.Sprintf("no fixture for task %s and prompt %s", in.task, PromptHash(in.prompt)[:12])}
	}
	if st.Latency > 0 {
		t := time.NewTimer(st.Latency)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return completion{}, ctx.Err()
		}
	}
	if st.Fail != "" {
		return completion{}, &Error{Kind: st.Fail, Status: st.Status, Message: "scripted failure"}
	}
	finish := st.Finish
	if finish == "" {
		finish = "stop"
	}
	in.emit(st.Text)
	return completion{text: st.Text, finish: finish, usage: Usage{Input: fakeTokens(full), Output: fakeTokens(st.Text)}}, nil
}

// fakeVector is the deterministic 64-dimension embedding of a text.
func fakeVector(text string) []float32 {
	v := make([]float32, 64)
	var norm float64
	for i := range v {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", i, text)))
		x := float64(int32(binary.BigEndian.Uint32(sum[:4]))) / math.MaxInt32
		v[i] = float32(x)
		norm += x * x
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(norm))
	}
	return v
}
