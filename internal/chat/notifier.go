package chat

import "sync"

type Notifier struct {
	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

func NewNotifier() *Notifier {
	return &Notifier{waiters: make(map[string][]chan struct{})}
}

func (n *Notifier) Subscribe(runID string) (<-chan struct{}, func()) {
	n.mu.Lock()
	ch := make(chan struct{})
	n.waiters[runID] = append(n.waiters[runID], ch)
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		waiters := n.waiters[runID]
		for index, waiter := range waiters {
			if waiter == ch {
				n.waiters[runID] = append(waiters[:index], waiters[index+1:]...)
				if len(n.waiters[runID]) == 0 {
					delete(n.waiters, runID)
				}
				return
			}
		}
	}
}

func (n *Notifier) Notify(runID string) {
	n.mu.Lock()
	waiters := n.waiters[runID]
	delete(n.waiters, runID)
	n.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}
