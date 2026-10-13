package tasks

import (
	"context"
	"sync"
)

// slrSemaphore limits how many SLR downloads run at once. Downloads that cannot
// acquire a slot stay in the "queued" state, which the Downloads view shows.
type slrSemaphore struct {
	mu     sync.Mutex
	cond   *sync.Cond
	limit  int
	active int
}

func newSLRSemaphore() *slrSemaphore {
	s := &slrSemaphore{limit: 1}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *slrSemaphore) setLimit(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	s.limit = n
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *slrSemaphore) acquire(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Wake the waiter if the context is cancelled while queued.
	stop := context.AfterFunc(ctx, func() { s.cond.Broadcast() })
	defer stop()

	for s.active >= s.limit {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.cond.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.active++
	return nil
}

func (s *slrSemaphore) release() {
	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

var slrSem = newSLRSemaphore()
