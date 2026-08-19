package resilience

import "context"

type Bulkhead struct {
	semaphore chan struct{}
}

func NewBulkhead(limit int) *Bulkhead {
	if limit < 1 {
		limit = 1
	}
	return &Bulkhead{semaphore: make(chan struct{}, limit)}
}

func (b *Bulkhead) Acquire(ctx context.Context) error {
	select {
	case b.semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bulkhead) Release() {
	<-b.semaphore
}
