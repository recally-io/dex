package app_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserUpgradeDoesNotResumeRetiredTelegramGroup(t *testing.T) {
	t.Parallel()
	// Given the saved native queue of a pre-scope group chat and a private chat.
	seed := t.TempDir()
	d := startDex(t, seed, "")
	d.setModel(t, "bash")
	work := t.TempDir()
	group := d.newChat(t, taskGate(t, work, "legacy-group"))
	waitTaskFile(t, filepath.Join(work, "legacy-group.pid"))
	marker := filepath.Join(work, "resumed")
	queued := d.send(t, group, fmt.Sprintf("echo replayed > %q", marker))
	d.watch(t, group).until(t, "group follow-up in the native queue", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == queued && it.Queued })
	})
	private := d.newChat(t, "echo private history")
	d.watch(t, private).until(t, "private answer", answered("bash: private history"))
	control := sessionDB(t, d)
	var groupExec, privateExec int64
	for id, native := range map[int64]*int64{group: &groupExec, private: &privateExec} {
		if err := control.QueryRowContext(t.Context(), `SELECT execution_id FROM conversations WHERE id = ?`, id).Scan(native); err != nil {
			t.Fatal(err)
		}
	}
	d.stop()

	// Restore the native store beside a control database at the v0.20.0-rc.6 schema.
	data := t.TempDir()
	nativeDir := filepath.Join(data, "agents", "default")
	if err := os.MkdirAll(nativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(seed, "agents", "default", "state.sqlite"), filepath.Join(nativeDir, "state.sqlite")); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+filepath.Join(data, "control.sqlite")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, os.DirFS("../db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 20261124032920); err != nil {
		t.Fatal(err)
	}
	execSQL(t, old, `INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('default', 'Dex', 'faux', 'bash', 1)`)
	// A separate scoped group store can have the same native ID as the private chat.
	scoped := max(group, private) + 1
	if _, err := old.ExecContext(t.Context(), `INSERT INTO conversations (id, agent_id, channel_key, execution_id, execution_scope, created_at) VALUES
		(?, 'default', 'telegram:-100', ?, '', 1),
		(?, 'default', 'chat:private', ?, '', 1),
		(?, 'default', 'chat:scoped-group', ?, 'tg_123_-100_7', 1)`, group, groupExec, private, privateExec, scoped, privateExec); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), `INSERT INTO submissions (id, conversation_id, content, origin, status, created_at, admitted_at)
		VALUES (?, ?, 'group follow-up', 'telegram:-100', 'pending', 1, 1)`, queued, group); err != nil {
		t.Fatal(err)
	}
	releaseResultGate(t, work, "legacy-group")

	// When the owner starts dex and continues the private chat after the upgrade.
	d = startDex(t, data, "")
	d.send(t, private, "echo private continued")
	w := d.watch(t, private)
	w.until(t, "private answer after upgrade", answered("bash: private continued"))
	for _, id := range []int64{group, scoped} {
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", id), nil, nil, http.StatusNotFound)
	}

	// Then the native group input is withdrawn without running. Its control receipt is sealed.
	reader, err := engine.New(engine.Config{DataDir: data, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(context.WithoutCancel(t.Context())) })
	var outcome engine.Outcome
	eventually(t, "retired group input settles", func() (bool, any) {
		out, err := reader.RecoveryOutcome(t.Context(), engine.Ref{AgentID: "default", ID: groupExec}, queued)
		if err != nil {
			return false, err
		}
		outcome = out.Outcome
		return out.State == engine.RecoverySettled, out
	})
	if outcome.Status != "unanswered" || outcome.Reason != "aborted" {
		t.Fatalf("retired group input was not withdrawn: %+v", outcome)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("retired group input ran: %v", err)
	}
	var status string
	var revoked bool
	if err := old.QueryRowContext(t.Context(), `SELECT status, channel_revoked FROM submissions WHERE id = ?`, queued).Scan(&status, &revoked); err != nil || status != "unanswered" || !revoked {
		t.Fatalf("retired group receipt = %q, revoked = %v: %v", status, revoked, err)
	}
}
