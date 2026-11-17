// Package flight shares one execution among concurrent callers that ask for
// the same thing.
package flight

import (
	"context"
	"sync"
)

// Group shares one execution among concurrent callers with the same key. The
// shared work is detached from any single caller's cancellation, so one caller
// giving up does not fail the others. The zero Group is ready to use.
type Group struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done   chan struct{}
	val    any
	err    error
	joined int
}

// Do returns what fn returns for key, running fn once for every caller that
// asks while it runs. fn receives the first caller's context without its
// cancellation. A caller whose own context ends is answered with that
// context's error at once, and the work goes on for the others.
func (g *Group) Do(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*call{}
	}
	c, running := g.calls[key]
	if running {
		c.joined++
	} else {
		c = &call{done: make(chan struct{})}
		g.calls[key] = c
		detached := context.WithoutCancel(ctx)
		go func() {
			c.val, c.err = fn(detached)
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(c.done)
		}()
	}
	g.mu.Unlock()
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Joined counts the callers that joined an execution still running, so a test
// can wait for them instead of sleeping.
func (g *Group) Joined() (n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		n += c.joined
	}
	return n
}
