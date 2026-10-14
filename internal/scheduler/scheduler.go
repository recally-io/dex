// Package scheduler runs background jobs of control on a schedule. It hides the scheduling library,
// so the other packages only name a job, its period, and its task.
//
// A job only decides whether to start durable work, such as a dream; the durable state, not the
// schedule, prevents duplicate work. The scheduler does not run missed jobs again after a sleep or a
// restart, so each task must check the durable state itself.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
)

// Scheduler runs jobs until Close.
type Scheduler struct {
	s gocron.Scheduler
}

// New returns a Scheduler that starts no job before Start.
func New() (*Scheduler, error) {
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create scheduler: %w", err)
	}
	return &Scheduler{s: s}, nil
}

// Every runs task at Start and then once in each period. When a run is still busy at the next
// time, that time is skipped. The task's context is canceled at Close.
func (s *Scheduler) Every(name string, period time.Duration, task func(context.Context)) error {
	_, err := s.s.NewJob(
		gocron.DurationJob(period),
		gocron.NewTask(task),
		gocron.WithName(name),
		gocron.WithStartAt(gocron.WithStartImmediately()),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		return fmt.Errorf("schedule %s: %w", name, err)
	}
	return nil
}

// Start starts the jobs.
func (s *Scheduler) Start() { s.s.Start() }

// Close cancels the running tasks and waits for them to return.
func (s *Scheduler) Close() error { return s.s.Shutdown() }
