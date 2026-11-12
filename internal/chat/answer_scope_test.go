package chat

import (
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserAnswerDedupIsScopedToChild(t *testing.T) {
	r := startNative(t, "echo")
	if err := r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "other", Name: "Other", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	var answers []int64
	for _, agent := range []string{"bot", "other"} {
		parent, err := r.service.Main(t.Context(), agent)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := r.service.execution(t.Context(), parent.ID, agent, engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"})
		if err != nil {
			t.Fatal(err)
		}
		source := agent + "-source"
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: source, ConversationID: parent.ID, Content: "delegate", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		call := engine.TaskContext{AgentID: agent, ConversationID: ref.ID, SourceRequestID: source, Key: agent + "/scope"}
		if _, err := r.service.ToolSessionNew(t.Context(), call, engine.SessionNewArgs{Title: "Child", Message: "same"}); err != nil {
			t.Fatal(err)
		}
		turn := turnID(call.Key)
		if err := r.service.admit(t.Context(), taskInput(t, r, turn)); err != nil {
			t.Fatal(err)
		}
		awaitReceipt(t, r, turn)
		var count int
		if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE id = ? AND kind = 'task_result'`, "result:"+turn).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("agent %s did not receive its child result", agent)
		}
		result := receipt(t, r, "result:"+turn)
		if !result.NativeAnswerID.Valid || !result.ResultChildID.Valid {
			t.Fatalf("result has no answer key: %+v", result)
		}
		answers = append(answers, result.NativeAnswerID.Int64)
	}
	if answers[0] != answers[1] {
		t.Fatalf("test requires equal native answer IDs in separate stores: %v", answers)
	}
}
