package chat

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/worker"
)

var ErrMaintenanceBusy = errors.New("maintenance is busy")

// AcquireMaintenance closes worker admission before checking unfinished work.
// Control keeps the expiry so it can restore the gate after a worker restart.
func (s *Service) AcquireMaintenance(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.reloading {
		return errors.Join(ErrMaintenanceBusy, errors.New("an extension reload is running"))
	}
	return s.acquire(ctx)
}

// AcquireReload takes the lease for an extension reload. Unlike AcquireMaintenance, it refuses a
// lease that another holder has, and no other holder can renew or release the lease until
// ReleaseReload.
func (s *Service) AcquireReload(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.reloading || s.leaseUntil.Load() > s.now().UnixMilli() {
		return errors.Join(ErrMaintenanceBusy, errors.New("maintenance or another reload is running"))
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	s.reloading = true
	return nil
}

// ReleaseReload releases the lease of AcquireReload.
func (s *Service) ReleaseReload(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.reloading = false
	return s.release(ctx)
}

func (s *Service) acquire(ctx context.Context) error {
	// Lock order: maintenanceMu, then deliveryMu. Keep gate closure and confirmed
	// lease publication in one boundary with submit and abort dispatch.
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	until := s.now().Add(2 * time.Minute).UnixMilli()
	var result worker.QuiesceResult
	err := s.worker.Call(ctx, "quiesce", worker.QuiesceParams{ExpiresAt: until}, &result)
	if err == nil && !result.Busy {
		s.leaseUntil.Store(until)
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
	if s.reloading {
		return errors.Join(ErrMaintenanceBusy, errors.New("an extension reload is running"))
	}
	return s.release(ctx)
}

func (s *Service) release(ctx context.Context) error {
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
