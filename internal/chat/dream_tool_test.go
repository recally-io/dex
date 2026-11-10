package chat

import (
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserDreamUsesNativeToolToReadItsEvidence(t *testing.T) {
	// Given a dream with a clipped source record in the real control database.
	r := startNative(t, "tool")
	ctx := t.Context()
	ref, err := r.agents.Create(ctx, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "tool"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO conversations (id, agent_id, channel_key, title, created_at) VALUES (101, 'bot', 'chat:original', '', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, title, created_at, execution_id) VALUES (102, 'bot', 'dream:run', '', 1, ?)`,
		`INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at, settled_at) VALUES ('original', 101, 'correction', '[]', NULL, 'web', 'message', 'done', 1, 2)`,
		`INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at) VALUES ('run', 102, ?, '[]', NULL, 'dream', 'message', 'pending', 3)`,
	} {
		var err error
		switch {
		case strings.Contains(query, "execution_id"):
			_, err = r.conn.ExecContext(ctx, query, ref.ID)
		case strings.Contains(query, "'run'"):
			_, err = r.conn.ExecContext(ctx, query, dream.Header+`{"message_id":"original"}`+"\n")
		default:
			_, err = r.conn.ExecContext(ctx, query)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// When the actual native model invokes the dream-only tool.
	sub, err := r.agents.Submit(ctx, ref, engine.Input{RequestID: "run", Origin: "dream", Kind: "message", Source: "dex:bot/dream/run", Content: `read_dream_message {"messageId":"original","field":"user","offset":0}`, Model: engine.ModelRef{Provider: "faux", ModelID: "tool"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := sub.Wait(ctx)
	// Then the tool resolves the real source receipt and returns the original evidence.
	if err != nil || outcome.Status != "done" || !strings.Contains(outcome.Answer, "correction") {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestUserDreamReadsOnlyItsOwnIncludedMessage(t *testing.T) {
	// Given a settled long message and a dream containing its truncated record.
	r := startNative(t, "echo")
	ctx := t.Context()
	long := strings.Repeat("记", 4000) + "CORRECTION" + strings.Repeat("忆", 100)
	for _, query := range []string{
		`INSERT INTO conversations (id, agent_id, channel_key, title, created_at) VALUES (101, 'bot', 'chat:original', '', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, title, created_at, execution_id) VALUES (102, 'bot', 'dream:run', '', 1, 102)`,
		`INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at, settled_at) VALUES ('original', 101, ?, '[]', NULL, 'web', 'message', 'done', 1, 2)`,
		`INSERT INTO submissions (id, conversation_id, content, attachments, source_key, origin, kind, status, created_at) VALUES ('run', 102, ?, '[]', NULL, 'dream', 'message', 'pending', 3)`,
	} {
		var err error
		switch {
		case strings.Contains(query, "'original'"):
			_, err = r.conn.ExecContext(ctx, query, long)
		case strings.Contains(query, "'run'"):
			_, err = r.conn.ExecContext(ctx, query, dream.Header+`{"message_id":"original"}`+"\n")
		default:
			_, err = r.conn.ExecContext(ctx, query)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	caller := engine.TaskContext{AgentID: "bot", ConversationID: 102, SourceRequestID: "run"}
	// When the dream requests the next page at a Unicode character boundary.
	page, err := r.service.ToolReadDreamMessage(ctx, caller, engine.DreamMessageRequest{MessageID: "original", Field: "user", Offset: 4000})
	if err != nil || !strings.HasPrefix(page.Text, "CORRECTION") || page.HasMore {
		t.Fatalf("page = %+v, %v", page, err)
	}
	// Then other agents, non-dream callers, and messages not in the digest are denied.
	for _, tc := range []struct {
		caller engine.TaskContext
		id     string
	}{
		{engine.TaskContext{AgentID: "other", ConversationID: 102, SourceRequestID: "run"}, "original"},
		{engine.TaskContext{AgentID: "bot", ConversationID: 101, SourceRequestID: "run"}, "original"},
		{caller, "run"},
	} {
		if _, err := r.service.ToolReadDreamMessage(ctx, tc.caller, engine.DreamMessageRequest{MessageID: tc.id, Field: "user"}); err == nil {
			t.Fatalf("unexpected access: %+v", tc)
		}
	}
}
