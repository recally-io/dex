package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserPureBackgroundRunNeverAutoSendsAndMixedRunRoutesToLastUser(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()
	conv, _ := taskParent(t, r)
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}

	// Given a pure background submission (sub_session) with a completed native answer.
	bgID := "result:child-1"
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             bgID,
		ConversationID: conv,
		Content:        `{"task-result":{"session":"2","title":"Worker","turn":"child-1","outcome":"done","answer":"ok"}}`,
		Attachments:    "null",
		Origin:         "telegram:42",
		Producer:       ProducerSubSession,
		Kind:           "task_result",
		CreatedAt:      10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(ctx, bgID, engine.Outcome{Status: "done", Answer: "internal review", AnswerID: 100, RunInputIDs: []string{bgID}}); err != nil {
		t.Fatal(err)
	}

	// Then no automatic Outbox row is created for a pure background run.
	var count int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, bgID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pure background run wrote %d outbox rows, want 0", count)
	}

	// Given a mixed run containing Telegram user input A and a background result B.
	tgID := "tg:123:42:10"
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             tgID,
		ConversationID: conv,
		Content:        "check status",
		Attachments:    "[]",
		Origin:         "telegram:42",
		Producer:       ProducerUser,
		ReplyChannel:   "telegram",
		ReplyPeer:      "42",
		ChannelContext: `{"accountId":"123","senderId":"42"}`,
		Kind:           "message",
		CreatedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	bgMixedID := "result:child-2"
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             bgMixedID,
		ConversationID: conv,
		Content:        `{"task-result":{"session":"2","title":"Worker","turn":"child-2","outcome":"done","answer":"ok"}}`,
		Attachments:    "null",
		Origin:         "telegram:42",
		Producer:       ProducerSubSession,
		Kind:           "task_result",
		CreatedAt:      21,
	}); err != nil {
		t.Fatal(err)
	}
	mixedOutcome := engine.Outcome{Status: "done", Answer: "mixed answer", AnswerID: 200, RunInputIDs: []string{tgID, bgMixedID}}
	if err := r.service.settle(ctx, bgMixedID, mixedOutcome); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(ctx, tgID, mixedOutcome); err != nil {
		t.Fatal(err)
	}

	// Then exactly one Outbox row is written to telegram:42 from the winning user input.
	var peer, body string
	if err := r.conn.QueryRowContext(ctx, `SELECT peer, body FROM outbox_messages WHERE source_submission_id = ?`, tgID).Scan(&peer, &body); err != nil {
		t.Fatalf("missing outbox for mixed run: %v", err)
	}
	if peer != "telegram:42" || body != "mixed answer" {
		t.Fatalf("mixed outbox = (%q, %q), want (telegram:42, mixed answer)", peer, body)
	}
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, bgMixedID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("background input in mixed run wrote %d outbox rows, want 0", count)
	}

	// Given an answer whose run membership cannot be verified (AnswerID > 0, empty RunInputIDs).
	unverifiedID := "tg:123:42:11"
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             unverifiedID,
		ConversationID: conv,
		Content:        "mystery",
		Attachments:    "[]",
		Origin:         "telegram:42",
		Producer:       ProducerUser,
		ReplyChannel:   "telegram",
		ReplyPeer:      "42",
		ChannelContext: `{"accountId":"123","senderId":"42"}`,
		Kind:           "message",
		CreatedAt:      30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(ctx, unverifiedID, engine.Outcome{Status: "done", Answer: "unverified", AnswerID: 300}); err != nil {
		t.Fatal(err)
	}
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, unverifiedID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unverified run membership wrote %d outbox rows, want 0", count)
	}
}

func TestUserWebSteerDuringTelegramRunKeepsAnswerInWebAndPreservesOrderAcrossRestart(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	r := openNativeTest(t, dataDir, "bash", nil)
	ctx := t.Context()
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{
		AgentID:   "bot",
		AppID:     "cli_1",
		AppSecret: "sec",
		OwnerID:   "ou_1",
		Brand:     "feishu",
	}); err != nil {
		t.Fatal(err)
	}

	// Given Telegram A starts a run paused by a file gate, Web B steers into the same run,
	// and Feishu C queues for the next run.
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	gateCmd := "touch '" + started + "'; while [ ! -f '" + release + "' ]; do sleep 0.05; done; echo tg-a"

	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:100", Message{
		Content:        gateCmd,
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Telegram A bash tool to start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	tgReceipt, err := r.service.LookupSource(ctx, "bot", "telegram:123:100")
	if err != nil {
		t.Fatal(err)
	}
	tgID := tgReceipt.ID

	webID := "web:0194f2c0-0000-7000-8000-000000000099"
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{ID: webID, Content: "echo web-b", WhenBusy: "steer"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcceptMessage(ctx, "bot", "feishu:ou_1", "feishu:cli_1:200", Message{
		Content:        "echo feishu-c",
		ChannelContext: ChannelContext{AccountID: "cli_1", SenderID: "ou_1"},
	}); err != nil {
		t.Fatal(err)
	}
	feishuReceipt, err := r.service.LookupSource(ctx, "bot", "feishu:cli_1:200")
	if err != nil {
		t.Fatal(err)
	}
	feishuID := feishuReceipt.ID

	// When A's gate is released, A and B settle together, then C is reconciled and runs in the next run.
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{tgID, webID} {
		waitSubSettled(t, r, id)
	}
	conv, err := r.q.GetConversationByKey(ctx, sqlc.GetConversationByKeyParams{AgentID: "bot", ChannelKey: MainKey})
	if err != nil {
		t.Fatal(err)
	}
	r.service.deliverSession(ctx, conv.ID, feishuID)
	waitSubSettled(t, r, feishuID)

	// Then A+B's shared answer stays in Web (0 outbox rows for A and B), while C sends 1 outbox row to Feishu.
	var abCount int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id IN (?, ?)`, tgID, webID).Scan(&abCount); err != nil {
		t.Fatal(err)
	}
	if abCount != 0 {
		t.Fatalf("Telegram A + Web steer B wrote %d outbox rows, want 0 (last user was Web)", abCount)
	}
	var feishuPeer string
	if err := r.conn.QueryRowContext(ctx, `SELECT peer FROM outbox_messages WHERE source_submission_id = ?`, feishuID).Scan(&feishuPeer); err != nil {
		t.Fatalf("missing feishu outbox for C: %v", err)
	}
	if feishuPeer != "feishu:ou_1" {
		t.Fatalf("feishu outbox peer = %q, want feishu:ou_1", feishuPeer)
	}

	// And cold RecoveryOutcome on a fresh engine instance recovers exact RunInputIDs = [A, B] (C excluded).
	r.close()
	cold := newColdEngine(t, dataDir)
	rec, err := cold.RecoveryOutcome(ctx, engine.Ref{AgentID: "bot", ID: conv.ExecutionID.Int64}, tgID)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{tgID, webID}
	if rec.State != engine.RecoverySettled || !slices.Equal(rec.Outcome.RunInputIDs, wantOrder) {
		t.Fatalf("cold RecoveryOutcome = (%q, %v), want (settled, %v)", rec.State, rec.Outcome.RunInputIDs, wantOrder)
	}
}

func TestUserDelegationPermanentAdmissionRejectionReportsOnceToParentWithoutChannelOutbox(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()
	parentConv, parentReq := taskParent(t, r)
	childConv, err := r.q.CreateChildConversation(ctx, sqlc.CreateChildConversationParams{
		AgentID:    "bot",
		ChannelKey: "chat:child-reject",
		Title:      "Rejected Child",
		ParentID:   sql.NullInt64{Int64: parentConv, Valid: true},
		CreatedAt:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnID := "bot/delegation-reject-1"
	if _, err := r.q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{
		ID:              turnID,
		ConversationID:  childConv.ID,
		Content:         "run task",
		WhenBusy:        "followUp",
		ParentRequestID: sql.NullString{String: parentReq.SourceRequestID, Valid: true},
		CreatedAt:       11,
	}); err != nil {
		t.Fatal(err)
	}

	// When the delegated turn is permanently rejected before any native run is created.
	if err := r.service.settle(ctx, turnID, engine.Outcome{Status: "unanswered", Reason: "no model configured"}); err != nil {
		t.Fatal(err)
	}

	// Then the parent receives exactly one failure result derived from the delegation ID and no direct channel outbox is written.
	res, err := r.q.GetSubmission(ctx, "result:"+turnID)
	if err != nil {
		t.Fatalf("expected parent failure result: %v", err)
	}
	if res.ConversationID != parentConv || res.Producer != ProducerSubSession || res.ReplyChannel != "" || res.ReplyPeer != "" {
		t.Fatalf("parent result = (conv=%d producer=%q reply=%q:%q), want (conv=%d sub_session '')", res.ConversationID, res.Producer, res.ReplyChannel, res.ReplyPeer, parentConv)
	}
	var outboxCount int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, turnID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("delegation rejection wrote %d direct channel outbox rows, want 0", outboxCount)
	}
}

func waitSubSettled(t *testing.T, r *nativeTest, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		sub, err := r.q.GetSubmission(t.Context(), id)
		if err == nil && sub.Status != "pending" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for submission %s to settle", id)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newColdEngine(t *testing.T, dataDir string) *engine.Agents {
	t.Helper()
	a, err := engine.New(engine.Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return a
}
