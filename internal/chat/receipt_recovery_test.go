package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserReceiptRecoveryKeepsNativeAnswerAfterModelChange(t *testing.T) {
	for _, column := range []string{"admitted_at", "status"} {
		for _, model := range []string{"", "shout"} {
			t.Run(column+"/model="+model, func(t *testing.T) {
				// Given a completed native input whose business receipt cannot be saved.
				r := startNative(t, "bash")
				_, err := r.conn.ExecContext(t.Context(), "CREATE TRIGGER block_receipt BEFORE UPDATE OF "+column+" ON submissions BEGIN SELECT RAISE(FAIL, 'receipt unavailable'); END")
				if err != nil {
					t.Fatal(err)
				}
				action := filepath.Join(r.dir, "actions")
				content := "printf x >> " + action + "; printf 'saved native answer'"
				if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "stable-source", Message{Content: content}); err != nil {
					t.Fatal(err)
				}
				id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "stable-source", Valid: true})
				if err != nil {
					t.Fatal(err)
				}
				business := receipt(t, r, id)
				conv, err := r.q.GetConversation(t.Context(), business.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				ref := engine.Ref{AgentID: "bot", ID: conv.ExecutionID.Int64}
				input := engine.Input{RequestID: id, Content: business.Content, Kind: "message", Origin: "telegram:7", Source: "dex:bot/chat/1", Model: engine.ModelRef{Provider: "faux", ModelID: "bash"}}
				native, err := r.agents.Submit(t.Context(), ref, input)
				if err != nil {
					t.Fatal(err)
				}
				waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				outcome, err := native.Wait(waitCtx)
				if err != nil {
					t.Fatal(err)
				}
				if outcome.Status != "done" || outcome.Answer != "bash: saved native answer" {
					t.Fatalf("precondition: native outcome %+v", outcome)
				}
				waitFor(t, func() bool { _, waiting := r.service.waiting.Load(id); return !waiting })
				before := receipt(t, r, id)
				if before.Status != "pending" || before.AdmittedAt.Valid != (column == "status") {
					t.Fatalf("trigger did not hold expected receipt window: %+v", before)
				}
				// When the user clears or changes the model before receipt reconciliation.
				// SaveAgent permits an empty model and uses this query.
				if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: model, ThinkingLevel: "off"}); err != nil {
					t.Fatal(err)
				}
				if _, err := r.conn.ExecContext(t.Context(), "DROP TRIGGER block_receipt"); err != nil {
					t.Fatal(err)
				}
				err = r.service.reconcileAgent(t.Context(), "bot")
				if err != nil {
					t.Fatal(err)
				}
				got := awaitReceipt(t, r, id)
				// Then the native answer survives, with one execution and one source receipt.
				if got.Status != outcome.Status || got.Answer.String != outcome.Answer {
					t.Fatalf("native outcome was replaced: native=%+v business status=%s answer=%q reason=%q", outcome, got.Status, got.Answer.String, got.Reason.String)
				}
				if !got.AdmittedAt.Valid || got.Reason.Valid || got.Origin != "telegram:7" || got.Content != content {
					t.Fatalf("recovered receipt: %+v", got)
				}
				if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "stable-source", Message{Content: "duplicate"}); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := r.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = 'stable-source'").Scan(&count); err != nil || count != 1 {
					t.Fatalf("source receipts=%d err=%v", count, err)
				}
				data, err := os.ReadFile(action)
				if err != nil || string(data) != "x" {
					t.Fatalf("execution repeated: %q err=%v", data, err)
				}
				history, err := r.agents.History(t.Context(), ref, 1<<62, 100)
				if err != nil {
					t.Fatal(err)
				}
				users := 0
				for _, item := range history.Items {
					if item.Kind == "user" {
						users++
						if item.Text != content || item.Origin != "telegram:7" {
							t.Fatalf("input metadata changed: %+v", item)
						}
					}
				}
				if users != 1 {
					t.Fatalf("native input entries=%d", users)
				}
			})
		}
	}
}
