package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResizableSemaphoreResizeUnderLoad(t *testing.T) {
	sem := NewResizableSemaphore(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var active atomic.Int64
	var completed atomic.Int64
	var maxActive atomic.Int64

	var wg sync.WaitGroup
	workers := 16
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if err := sem.Acquire(ctx, 1); err != nil {
					return
				}
				cur := active.Add(1)
				for {
					m := maxActive.Load()
					if cur <= m || maxActive.CompareAndSwap(m, cur) {
						break
					}
				}

				time.Sleep(2 * time.Millisecond)

				active.Add(-1)
				sem.Release(1)
				completed.Add(1)
			}
		}()
	}

	resizerDone := make(chan struct{})
	go func() {
		defer close(resizerDone)
		limits := []int64{2, 6, 1, 8, 3, 5, 2, 4}
		for _, lim := range limits {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				sem.Resize(lim)
			}
		}
	}()

	<-resizerDone
	cancel()
	wg.Wait()

	if sem.Current() != 0 {
		t.Errorf("expected 0 permits in use at end, got %d", sem.Current())
	}
	if completed.Load() == 0 {
		t.Errorf("no workers completed")
	}
}

func TestResizableSemaphoreContextCancellation(t *testing.T) {
	sem := NewResizableSemaphore(1)
	if err := sem.Acquire(context.Background(), 1); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := sem.Acquire(ctx, 1); err == nil {
		t.Errorf("expected acquire to fail on cancelled context")
	}

	sem.Release(1)
	if sem.Current() != 0 {
		t.Errorf("expected 0 current permits, got %d", sem.Current())
	}

	if err := sem.Acquire(context.Background(), 1); err != nil {
		t.Errorf("expected successful acquire after release: %v", err)
	}
	sem.Release(1)
}
