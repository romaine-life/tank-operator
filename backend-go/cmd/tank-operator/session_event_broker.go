package main

import "sync"

type sessionEventBroker struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func newSessionEventBroker() *sessionEventBroker {
	return &sessionEventBroker{subscribers: map[string]map[chan struct{}]struct{}{}}
}

func (b *sessionEventBroker) Subscribe(sessionID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subscribers[sessionID] == nil {
		b.subscribers[sessionID] = map[chan struct{}]struct{}{}
	}
	b.subscribers[sessionID][ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subscribers[sessionID], ch)
		if len(b.subscribers[sessionID]) == 0 {
			delete(b.subscribers, sessionID)
		}
		close(ch)
	}
}

func (b *sessionEventBroker) Notify(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers[sessionID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *sessionEventBroker) SubscriberCount(sessionID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers[sessionID])
}
