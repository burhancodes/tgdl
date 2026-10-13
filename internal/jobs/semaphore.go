package jobs

import (
	"context"
	"sync"
)

type waiter struct {
	n     int64
	ready chan struct{}
}

type ResizableSemaphore struct {
	mu      sync.Mutex
	limit   int64
	cur     int64
	waiters []*waiter
}

func NewResizableSemaphore(limit int64) *ResizableSemaphore {
	if limit < 1 {
		limit = 1
	}
	return &ResizableSemaphore{
		limit: limit,
	}
}

func (s *ResizableSemaphore) Acquire(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	if s.cur+n <= s.limit && len(s.waiters) == 0 {
		s.cur += n
		s.mu.Unlock()
		return nil
	}

	w := &waiter{n: n, ready: make(chan struct{})}
	s.waiters = append(s.waiters, w)
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-w.ready:
			s.cur -= n
			s.notifyWaitersLocked()
			s.mu.Unlock()
			return ctx.Err()
		default:
			for i, it := range s.waiters {
				if it == w {
					s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			return ctx.Err()
		}
	case <-w.ready:
		return nil
	}
}

func (s *ResizableSemaphore) TryAcquire(n int64) bool {
	if n <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur+n <= s.limit && len(s.waiters) == 0 {
		s.cur += n
		return true
	}
	return false
}

func (s *ResizableSemaphore) Release(n int64) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur -= n
	if s.cur < 0 {
		s.cur = 0
	}
	s.notifyWaitersLocked()
}

func (s *ResizableSemaphore) Resize(newLimit int64) {
	if newLimit < 1 {
		newLimit = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = newLimit
	s.notifyWaitersLocked()
}

func (s *ResizableSemaphore) Current() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *ResizableSemaphore) Limit() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

func (s *ResizableSemaphore) notifyWaitersLocked() {
	for len(s.waiters) > 0 {
		w := s.waiters[0]
		if s.cur+w.n <= s.limit {
			s.cur += w.n
			s.waiters = s.waiters[1:]
			close(w.ready)
		} else {
			break
		}
	}
}
