package chat

import (
	"context"
	"errors"
	"time"

	"github.com/vaayne/dex/internal/worker"
)

var ErrMaintenanceBusy = errors.New("maintenance is busy")

// AcquireMaintenance closes control dispatch, then atomically closes worker admission
// and checks durable work. A failed acquire holds no gate. Both leases expire.
func (s *Service) AcquireMaintenance(ctx context.Context, id string) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.dispatchMu.Lock()
	if s.leaseID != "" && s.now().Before(s.leaseUntil) && s.leaseID != id {
		s.dispatchMu.Unlock()
		return errors.Join(ErrMaintenanceBusy, errors.New("another updater holds the lease"))
	}
	if len(s.active) > 0 {
		s.dispatchMu.Unlock()
		return errors.Join(ErrMaintenanceBusy, errors.New("a submission is dispatched or running"))
	}
	s.leaseID = id
	s.leaseUntil = s.now().Add(2 * time.Minute)
	params := worker.QuiesceParams{LeaseID: id, ExpiresAt: s.leaseUntil.UnixMilli()}
	s.dispatchMu.Unlock()
	// The stdout reader processes settled events under dispatchMu. Do not hold it here.
	var result worker.QuiesceResult
	err := s.worker.Call(ctx, "quiesce", params, &result)
	if err == nil && !result.Busy {
		return nil
	}
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = s.worker.Call(release, "resume", worker.ResumeParams{LeaseID: id}, &struct{}{})
	s.clearMaintenance()
	return errors.Join(ErrMaintenanceBusy, err, errors.New(result.Reason))
}

func (s *Service) ReleaseMaintenance(ctx context.Context, id string) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.dispatchMu.Lock()
	other := s.leaseID != "" && s.now().Before(s.leaseUntil) && s.leaseID != id
	s.dispatchMu.Unlock()
	if other {
		return ErrMaintenanceBusy
	}
	if err := s.worker.Call(ctx, "resume", worker.ResumeParams{LeaseID: id}, &struct{}{}); err != nil {
		return err
	}
	s.clearMaintenance()
	return nil
}

// RestoreMaintenance runs before a new worker is exposed to watches or replay.
func (s *Service) RestoreMaintenance(ctx context.Context, call worker.CallFunc) error {
	s.dispatchMu.Lock()
	params := worker.QuiesceParams{LeaseID: s.leaseID, ExpiresAt: s.leaseUntil.UnixMilli()}
	s.dispatchMu.Unlock()
	if params.LeaseID == "" || params.ExpiresAt <= s.now().UnixMilli() {
		return nil
	}
	var result worker.QuiesceResult
	if err := call(ctx, "quiesce", params, &result); err != nil {
		return err
	}
	if result.Busy {
		return errors.Join(ErrMaintenanceBusy, errors.New(result.Reason))
	}
	return nil
}
func (s *Service) clearMaintenance() {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.leaseID = ""
	s.leaseUntil = time.Time{}
	select {
	case s.dispatchWake <- struct{}{}:
	default:
	}
}
func (s *Service) beginDispatch(ctx context.Context, id string) bool {
	for {
		s.dispatchMu.Lock()
		wait := s.leaseUntil.Sub(s.now())
		if s.leaseID == "" || wait <= 0 {
			s.active[id] = true
			s.dispatchMu.Unlock()
			return true
		}
		s.dispatchMu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-s.dispatchWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (s *Service) endDispatch(id string) {
	s.dispatchMu.Lock()
	delete(s.active, id)
	s.dispatchMu.Unlock()
}
