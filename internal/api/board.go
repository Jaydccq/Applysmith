package api

import (
	"fmt"
	"slices"
	"sync"

	"applysmith/internal/api/wire"
)

// board keeps the revision of each topic and the stream of notices about them.
type board struct {
	mu   sync.Mutex
	revs map[string]int64
	es   *eventStream
}

func newBoard(epoch string) *board {
	b := &board{revs: map[string]int64{}, es: newEventStream(epoch)}
	for _, t := range wire.Topics {
		b.revs[t] = 0
	}
	return b
}

func (b *board) notify(topic, file string) error {
	if !slices.Contains(wire.Topics, topic) {
		return fmt.Errorf("api: %q is not a board topic", topic)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revs[topic]++
	n := wire.BoardNotice{Topic: topic, Revision: wire.SafeInt(b.revs[topic])}
	if topic == "configuration" {
		n.File = file
	}
	b.es.publish("notice", n)
	return nil
}

func (b *board) snapshot() sseEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	revs := make(map[string]wire.SafeInt, len(b.revs))
	for t, r := range b.revs {
		revs[t] = wire.SafeInt(r)
	}
	ev := newEvent("snapshot", wire.BoardSnapshot{Revisions: revs})
	ev.epoch, ev.id = b.es.epoch, b.es.head()
	return ev
}
