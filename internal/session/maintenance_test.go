package session

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserMaintenanceReportsClosedEngine(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	if err := r.bots.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcquireMaintenance(t.Context()); !errors.Is(err, engine.ErrClosed) || errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("closed engine reported as busy: %v", err)
	}
}

func TestUserMaintenanceDoesNotReleaseAnUnconfirmedPause(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	if err := r.bots.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcquireMaintenance(t.Context()); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("native gate: %v", err)
	}
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bots.Create(t.Context(), "bot", engine.Options{}); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("maintenance released another holder: %v", err)
	}
	r.bots.Resume()
}

func TestUserMaintenanceRenewalKeepsAdmissionPaused(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bots.Create(t.Context(), "bot", engine.Options{}); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("renewal reopened native admission: %v", err)
	}
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUserMaintenanceRejectsWebInputWithoutSavingIt(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "rejected-web", "wait"); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("maintenance rejection: %v", err)
	}
	if _, err := r.q.GetSubmission(t.Context(), "rejected-web"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected input was saved: %v", err)
	}
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "rejected-web", "retry"); err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, "rejected-web"); sub.Answer.String != "echo: retry" {
		t.Fatalf("retry: %+v", sub)
	}
}
