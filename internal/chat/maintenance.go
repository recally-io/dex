package chat

import (
	"context"
	"errors"
	"time"
)

var ErrMaintenanceBusy = errors.New("maintenance is busy")

// AcquireMaintenance closes dispatch atomically with checking outstanding submissions.
// A failed acquire holds no gate. A successful lease expires if its owner dies.
func (s *Service) AcquireMaintenance(ctx context.Context, id string) error {
	// Do not hold the dispatch lock while the worker reader processes events.
	if err := s.worker.Call(ctx, "ping", struct{}{}, &struct{}{}); err != nil {
		return errors.Join(ErrMaintenanceBusy, err)
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if s.leaseID != "" && s.now().Before(s.leaseUntil) && s.leaseID != id {
		return errors.Join(ErrMaintenanceBusy, errors.New("another updater holds the lease"))
	}
	if len(s.active) > 0 {
		return errors.Join(ErrMaintenanceBusy, errors.New("a submission is dispatched or running"))
	}
	// Every dispatch reserves its submission under this lock before calling the worker.
	s.leaseID = id
	s.leaseUntil = s.now().Add(2 * time.Minute)
	return nil
}

func (s *Service) ReleaseMaintenance(id string) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if s.leaseID != "" && s.now().Before(s.leaseUntil) && s.leaseID != id {
		return ErrMaintenanceBusy
	}
	s.leaseID = ""
	s.leaseUntil = time.Time{}
	select {
	case s.dispatchWake <- struct{}{}:
	default:
	}
	return nil
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
