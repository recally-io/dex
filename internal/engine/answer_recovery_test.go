package engine

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserDoneNativeInputWithoutAnswerFailsSettlement(t *testing.T) {
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "damaged", "hello", "echo"))
	if err := a.Reset(t.Context(), ref, "valid-reset", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Corrupt one completed native input. A reset is a valid done write without an Answer.
	db, err := sql.Open("sqlite", filepath.Join(config.DataDir, "agents", "alpha", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `UPDATE submissions SET record = json_remove(record, '$.answer') WHERE json_extract(record, '$.requestId') = 'damaged'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cold := coreEngine(t, config)
	reset, err := cold.RecoveryOutcome(t.Context(), ref, "valid-reset")
	if err != nil || reset.State != RecoverySettled || reset.Outcome.Status != "done" {
		t.Fatalf("valid reset: %+v, %v", reset, err)
	}
	if _, err := cold.RecoveryOutcome(t.Context(), ref, "damaged"); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("cold recovery accepted damaged input: %v", err)
	}
	replay, err := cold.Submit(t.Context(), ref, Input{RequestID: "damaged"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := replay.Wait(t.Context()); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("replay accepted damaged input: %+v, %v", outcome, err)
	}
}
