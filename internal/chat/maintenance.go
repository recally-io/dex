package chat

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

var ErrMaintenanceBusy = errors.New("maintenance is busy")

// AcquireMaintenance pauses native admission for at most two minutes.
func (s *Service) AcquireMaintenance(ctx context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	return s.acquire(ctx)
}

func (s *Service) acquire(ctx context.Context) error {
	// Lock order: maintenanceMu, then deliveryMu. Keep gate closure and confirmed
	// lease publication in one boundary with submit and abort dispatch.
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	until := s.now().Add(2 * time.Minute).UnixMilli()
	if s.leaseUntil.Load() == 0 {
		if err := s.engine.Quiesce(ctx); err != nil {
			if errors.Is(err, engine.ErrBusy) {
				return errors.Join(ErrMaintenanceBusy, err)
			}
			return err
		}
	}
	s.leaseUntil.Store(until)
	if s.maintenanceTimer != nil {
		s.maintenanceTimer.Stop()
	}
	s.maintenanceTimer = time.AfterFunc(2*time.Minute, func() {
		s.maintenanceMu.Lock()
		defer s.maintenanceMu.Unlock()
		if s.leaseUntil.Load() == until {
			_ = s.release()
		}
	})
	return nil
}

func (s *Service) ReleaseMaintenance(_ context.Context) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	return s.release()
}

func (s *Service) release() error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.leaseUntil.Swap(0) == 0 {
		return nil
	}
	if s.maintenanceTimer != nil {
		s.maintenanceTimer.Stop()
	}
	s.engine.Resume()
	s.Wake()
	return nil
}

// ReloadExtensions orders file reload with business receipt admission.
func (s *Service) ReloadExtensions(ctx context.Context, agentID string) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	return s.engine.Reload(ctx, agentID)
}
