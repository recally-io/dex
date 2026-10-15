package chat

import (
	"context"
	"errors"
	"time"

	"github.com/vaayne/dex/internal/worker"
)

var ErrMaintenanceBusy = errors.New("maintenance is busy")

// AcquireMaintenance closes worker admission before checking unfinished work.
// Control keeps the expiry so it can restore the gate after a worker restart.
func (s *Service) AcquireMaintenance(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	until := s.now().Add(2 * time.Minute).UnixMilli()
	s.leaseUntil.Store(until)
	var result worker.QuiesceResult
	err := s.worker.Call(ctx, "quiesce", worker.QuiesceParams{ExpiresAt: until}, &result)
	if err == nil && !result.Busy {
		return nil
	}
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = s.worker.Call(release, "resume", struct{}{}, &struct{}{})
	s.leaseUntil.Store(0)
	if err != nil {
		return err
	}
	if result.Reason == "" {
		return ErrMaintenanceBusy
	}
	return errors.Join(ErrMaintenanceBusy, errors.New(result.Reason))
}

func (s *Service) ReleaseMaintenance(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if err := s.worker.Call(ctx, "resume", struct{}{}, &struct{}{}); err != nil {
		return err
	}
	s.leaseUntil.Store(0)
	return nil
}

// RestoreMaintenance runs before a new worker is exposed to watches or replay.
func (s *Service) RestoreMaintenance(ctx context.Context, call worker.CallFunc) error {
	until := s.leaseUntil.Load()
	if until <= s.now().UnixMilli() {
		return nil
	}
	var result worker.QuiesceResult
	if err := call(ctx, "quiesce", worker.QuiesceParams{ExpiresAt: until}, &result); err != nil {
		return err
	}
	if result.Busy {
		return ErrMaintenanceBusy
	}
	return nil
}
