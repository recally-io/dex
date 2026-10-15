package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vaayne/dex/internal/worker"
)

func TestUserMaintenanceWorkerFailureIsNotBusy(t *testing.T) {
	// Given a worker that has no running process.
	s := New(nil, worker.New(worker.Config{}), time.Now, nil, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	// When maintenance cannot reach the worker, callers must receive a failure, not busy.
	err := s.AcquireMaintenance(ctx, "test")
	if err == nil || errors.Is(err, ErrMaintenanceBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker failure reported as busy: %v", err)
	}
}
