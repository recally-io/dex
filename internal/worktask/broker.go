package worktask

import (
	"sync"
)

// Broker sends wake hints. It does not store or order entries. Subscribers
// read the ledger by ID, so a full channel does not lose entries.
type Broker struct {
	mu   sync.Mutex
	subs map[chan int64]struct{}
}

func (b *Broker) Subscribe() (<-chan int64, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan int64, 16)
	if b.subs == nil {
		b.subs = make(map[chan int64]struct{})
	}
	b.subs[ch] = struct{}{}
	return ch, sync.OnceFunc(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, ch)
		close(ch)
	})
}

func (b *Broker) HasSubscribers() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs) > 0
}

// Publish sends an entry ID or zero for a change outside the ledger.
func (b *Broker) Publish(entryID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- entryID:
		default:
		}
	}
}
