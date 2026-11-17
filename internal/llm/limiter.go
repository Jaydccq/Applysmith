package llm

import (
	"container/list"
	"context"
	"sync"
)

// limiter caps in-flight calls of one provider and serves waiters in arrival
// order.
type limiter struct {
	mu      sync.Mutex
	cap     int
	inUse   int
	waiters list.List // of chan struct{}
}

// acquire blocks until a slot is free or ctx ends. The returned func releases
// the slot.
func (l *limiter) acquire(ctx context.Context, capacity int) (func(), error) {
	l.mu.Lock()
	l.cap = capacity
	if l.inUse < l.cap && l.waiters.Len() == 0 {
		l.inUse++
		l.mu.Unlock()
		return l.release, nil
	}
	ch := make(chan struct{})
	el := l.waiters.PushBack(ch)
	l.mu.Unlock()

	select {
	case <-ch:
		return l.release, nil
	case <-ctx.Done():
		l.mu.Lock()
		select {
		case <-ch:
			// Granted just as the wait ended; hand the slot on.
			l.mu.Unlock()
			l.release()
		default:
			l.waiters.Remove(el)
			l.mu.Unlock()
		}
		return nil, ctx.Err()
	}
}

func (l *limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inUse--
	for l.inUse < l.cap && l.waiters.Len() > 0 {
		front := l.waiters.Front()
		l.waiters.Remove(front)
		l.inUse++
		close(front.Value.(chan struct{}))
	}
}

func (r *Router) limiterFor(id string) *limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.limiters[id]
	if !ok {
		l = &limiter{}
		r.limiters[id] = l
	}
	return l
}
