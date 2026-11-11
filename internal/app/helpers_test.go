package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func taskJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type resultSubmissionState struct {
	status, content, answer, reason string
}

func taskTurn(t *testing.T, d *dex, chatID int64, text string) string {
	t.Helper()
	id := d.send(t, chatID, text)
	w := d.watch(t, chatID)
	w.until(t, "answer to "+id, func(tr *transcript) bool {
		for i, entry := range tr.items {
			if entry.ID == id && entry.Kind == "user" {
				return !tr.running && len(tr.items) > i+1 && tr.items[len(tr.items)-1].Kind == "assistant"
			}
		}
		return false
	})
	return w.tr.items[len(w.tr.items)-1].Text
}

func taskTool(t *testing.T, d *dex, chatID int64, name string, args any) string {
	t.Helper()
	d.setModel(t, "tool")
	return taskTurn(t, d, chatID, name+" "+taskJSON(t, args))
}

func taskGate(t *testing.T, dir, name string) string {
	t.Helper()
	base := filepath.Join(dir, name)
	// The shell polls a gate until its deadline; cleanup can always release it.
	t.Cleanup(func() { _ = os.WriteFile(base+".gate", nil, 0o600) })
	return fmt.Sprintf("printf 'real artifact\\n' > '%s.output'; echo $$ > '%s.pid'; deadline=$((SECONDS+45)); until test -f '%s.gate'; do if (( SECONDS >= deadline )); then echo 'gate deadline exceeded' >&2; exit 1; fi; sleep 0.05; done; echo 'gate released'", base, base, base)
}

func waitTaskFile(t *testing.T, path string) string {
	t.Helper()
	var data []byte
	eventually(t, "file "+path, func() (bool, any) {
		var err error
		data, err = os.ReadFile(path)
		return err == nil && len(data) > 0, fmt.Sprintf("data=%q err=%v", data, err)
	})
	return string(data)
}

func taskPIDGone(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid shell PID %q: %v", data, err)
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitTaskPIDGone(t *testing.T, path string) {
	t.Helper()
	eventually(t, "shell PID disappears", func() (bool, any) { return taskPIDGone(t, path), path })
}

func releaseResultGate(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskResultDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func resultSubmission(t *testing.T, conn *sql.DB, id string) resultSubmissionState {
	t.Helper()
	var row resultSubmissionState
	if err := conn.QueryRowContext(t.Context(), `SELECT status, content, coalesce(answer, ''), coalesce(reason, '') FROM submissions WHERE id = ?`, id).
		Scan(&row.status, &row.content, &row.answer, &row.reason); err != nil {
		t.Fatal(err)
	}
	return row
}

func waitResultSubmission(t *testing.T, conn *sql.DB, id, status string) resultSubmissionState {
	t.Helper()
	var row resultSubmissionState
	eventually(t, fmt.Sprintf("submission %s %s", id, status), func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), `SELECT status, content, coalesce(answer, ''), coalesce(reason, '') FROM submissions WHERE id = ?`, id).Scan(&row.status, &row.content, &row.answer, &row.reason)
		return err == nil && row.status == status, fmt.Sprintf("row=%+v err=%v", row, err)
	})
	return row
}
func waitTaskChannelText(t *testing.T, f *fakeTelegram, text string) {
	t.Helper()
	var replies []string
	eventually(t, text, func() (bool, any) {
		for {
			select {
			case reply := <-f.sent:
				replies = append(replies, reply.Text)
				if strings.Contains(reply.Text, text) {
					return true, replies
				}
			default:
				return false, replies
			}
		}
	})
}

// execution is the latest turn of one child session of a parent.
type execution struct {
	ID, Title, Status, Report string
	// Turn is the ID of the latest turn. Its result has the ID ResultID.
	Turn, ResultID string
}

// spawnExecution creates a child session that runs goal on a faux model.
func spawnExecution(t *testing.T, d *dex, parent int64, title, goal, model string) {
	t.Helper()
	d.setModel(t, "spawn")
	_ = taskTurn(t, d, parent, childJSON(t, map[string]any{"title": title, "message": goal, "provider": "faux", "modelId": model}))
}

// waitExecution waits until the latest turn of the child session with this title has the status
// "running" (the turn is pending), "done", or "cancelled" (the turn is unanswered).
func waitExecution(t *testing.T, conn *sql.DB, parent int64, title, status string) execution {
	t.Helper()
	want := map[string]string{"running": "pending", "done": "done", "cancelled": "unanswered"}[status]
	var e execution
	eventually(t, title+" "+status, func() (bool, any) {
		var state string
		err := conn.QueryRowContext(t.Context(), `SELECT CAST(c.id AS TEXT), c.title, s.status, coalesce(s.answer, ''), s.id
FROM conversations c JOIN submissions s ON s.conversation_id = c.id AND s.kind = 'message'
WHERE c.parent_id = ? AND c.title = ? ORDER BY c.id DESC, s.rowid DESC LIMIT 1`, parent, title).Scan(&e.ID, &e.Title, &state, &e.Report, &e.Turn)
		e.Status, e.ResultID = state, "result:"+e.Turn
		return err == nil && state == want, fmt.Sprintf("execution=%+v err=%v", e, err)
	})
	return e
}
