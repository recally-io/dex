// Package scheduler runs background jobs of control on a schedule.
//
// A job only decides whether to start durable work, such as a dream; the durable state, not the
// schedule, prevents duplicate work. The scheduler does not run missed jobs again after a sleep or a
// restart, so each task must check the durable state itself.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Scheduler runs jobs until Close.
type Scheduler struct {
	mu     sync.Mutex
	jobs   []func(context.Context)
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a Scheduler that starts no job before Start.
func New() *Scheduler {
	return &Scheduler{}
}

// Every runs task at Start and then once in each period. When a run is still busy at the next
// time, that time is skipped. The task's context is canceled at Close.
func (s *Scheduler) Every(name string, period time.Duration, task func(context.Context)) error {
	if period <= 0 {
		return fmt.Errorf("schedule %s: duration must be positive", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, func(ctx context.Context) {
		task(ctx)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				task(ctx)
			}
		}
	})
	return nil
}

// Start starts the jobs with ctx as the base context until Close or ctx cancellation.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	for _, job := range s.jobs {
		s.wg.Go(func() { job(ctx) })
	}
}

// Close cancels the running tasks and waits for them to return.
func (s *Scheduler) Close() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}
