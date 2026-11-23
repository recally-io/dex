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
	// Lock order: maintenanceMu, then admissionMu. Confirm the lease in the
	// same boundary as receipt insertion; native admission owns its own gate.
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
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
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
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
	return s.reloadExtensions(ctx, agentID, s.engine.Reload)
}

func (s *Service) ReloadDirtyExtensions(ctx context.Context, agentID string) error {
	return s.reloadExtensions(ctx, agentID, s.engine.ReloadDirty)
}

func (s *Service) reloadExtensions(ctx context.Context, agentID string, reload func(context.Context, string) error) error {
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return err
	}
	defer func() { unlock(); s.Wake() }()
	return reload(ctx, agentID)
}

// ResetEnvironment verifies agentID is idle, closes its active runtime resources and background
// commands, and invokes reset to tear down remote sandbox state.
func (s *Service) ResetEnvironment(ctx context.Context, agentID string, reset func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return err
	}
	defer func() { unlock(); s.Wake() }()

	pending, err := s.q.CountPendingAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if pending > 0 {
		return ErrConversationBusy
	}
	if s.engine != nil {
		if err := s.engine.CloseAgent(ctx, agentID); err != nil {
			return err
		}
	}
	if s.background != nil {
		if err := s.background.StopAgent(ctx, agentID); err != nil {
			return err
		}
	}
	if reset != nil {
		return reset(ctx)
	}
	return nil
}
