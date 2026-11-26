package session

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
		ID:          bgID,
		SessionID:   conv,
		Content:     `{"session-result":{"session":"2","title":"Worker","turn":"child-1","outcome":"done","answer":"ok"}}`,
		Attachments: "null",
		Origin:      "telegram:42",
		Producer:    ProducerSubSession,
		Kind:        "session_result",
		CreatedAt:   10,
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
		SessionID:      conv,
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
		ID:          bgMixedID,
		SessionID:   conv,
		Content:     `{"session-result":{"session":"2","title":"Worker","turn":"child-2","outcome":"done","answer":"ok"}}`,
		Attachments: "null",
		Origin:      "telegram:42",
		Producer:    ProducerSubSession,
		Kind:        "session_result",
		CreatedAt:   21,
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
		SessionID:      conv,
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
	r := openNativeTest(t, dataDir, "bash")
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
	conv, err := r.q.GetSessionByKey(ctx, sqlc.GetSessionByKeyParams{AgentID: "bot", ChannelKey: MainKey})
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

func TestUserBackToBackTelegramMessagesKeepFilesAndQuestionButtonsAndWebSteerCleansBlobs(t *testing.T) {
	t.Parallel()
	r := startNative(t, "bash")
	ctx := t.Context()
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}

	// Given an active Telegram run holding the session busy so A1 and A2 queue together into one QueueAll run.
	dir := t.TempDir()
	holdStarted := filepath.Join(dir, "hold-started")
	holdRelease := filepath.Join(dir, "hold-release")
	holdCmd := "touch '" + holdStarted + "'; while [ ! -f '" + holdRelease + "' ]; do sleep 0.05; done; echo hold"
	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:1", Message{
		Content:        holdCmd,
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFile(t, holdStarted)

	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:2", Message{
		Content:        "echo a1",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:3", Message{
		Content:        "echo a2",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	a1Receipt, err := r.service.LookupSource(ctx, "bot", "telegram:123:2")
	if err != nil {
		t.Fatal(err)
	}
	a2Receipt, err := r.service.LookupSource(ctx, "bot", "telegram:123:3")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := r.q.GetSessionByKey(ctx, sqlc.GetSessionByKeyParams{AgentID: "bot", ChannelKey: MainKey})
	if err != nil {
		t.Fatal(err)
	}

	// Queue a file snapshot and a question on A1 (the Run's Inputs[0] source input).
	if _, err := r.q.QueueOutboxFile(ctx, sqlc.QueueOutboxFileParams{
		ID:           "file-a1",
		OperationKey: "op-a1",
		SubmissionID: a1Receipt.ID,
		AgentID:      "bot",
		ExecutionID:  conv.ExecutionID,
		Name:         "report.txt",
		MimeType:     "text/plain",
		Body:         []byte("hello report"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.CreateQuestion(ctx, sqlc.CreateQuestionParams{
		ID:                 "q-a1",
		AgentID:            "bot",
		SessionID:          conv.ID,
		SourceSubmissionID: a1Receipt.ID,
		ToolOperationKey:   "op-q-a1",
		Text:               "Proceed?",
		OptionsJson:        `["Yes","No"]`,
		CreatedAt:          100,
	}); err != nil {
		t.Fatal(err)
	}

	// Release hold so A1+A2 execute together in one QueueAll run.
	if err := os.WriteFile(holdRelease, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitSubSettled(t, r, a1Receipt.ID)
	waitSubSettled(t, r, a2Receipt.ID)

	// Prove A1 and A2 actually merged into one QueueAll Run (both share the single answer "bash: a2",
	// and A2 did not write a separate Outbox row).
	a1Sub, err := r.q.GetSubmission(ctx, a1Receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	a2Sub, err := r.q.GetSubmission(ctx, a2Receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a1Sub.Answer.String, "bash: a2") || a2Sub.Answer.String != "bash: a2" {
		t.Fatalf("expected A1 and A2 to share one QueueAll Run answer (bash: a2), got A1=%q A2=%q", a1Sub.Answer.String, a2Sub.Answer.String)
	}
	var a2Outbox int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, a2Receipt.ID).Scan(&a2Outbox); err != nil || a2Outbox != 0 {
		t.Fatalf("A2 outbox rows = %d, err = %v; want 0 (A1 owns the shared Run outbox)", a2Outbox, err)
	}

	// Then A1 owns the Outbox row as kind=question, and the queued file is bound as pending.
	var kind, status string
	if err := r.conn.QueryRowContext(ctx, `SELECT kind, status FROM outbox_messages WHERE source_submission_id = ? AND kind != 'file'`, a1Receipt.ID).Scan(&kind, &status); err != nil {
		t.Fatalf("expected A1 to own question outbox row: %v", err)
	}
	if kind != "question" || status != "pending" {
		t.Fatalf("A1 outbox = (%q, %q), want (question, pending)", kind, status)
	}
	var fileStatus string
	if err := r.conn.QueryRowContext(ctx, `SELECT status FROM outbox_messages WHERE id = 'file:file-a1'`).Scan(&fileStatus); err != nil || fileStatus != "pending" {
		t.Fatalf("file outbox status = %q, err = %v; want pending", fileStatus, err)
	}
	var unbound int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_files WHERE submission_id = ? AND outbox_id IS NULL`, a1Receipt.ID).Scan(&unbound); err != nil || unbound != 0 {
		t.Fatalf("unbound files on A1 = %d, err = %v; want 0", unbound, err)
	}

	// And when Web steer takes over a Telegram run, unbound files queued on the Telegram input are cleaned up.
	tgSteerStarted := filepath.Join(dir, "tg-steer-started")
	tgSteerRelease := filepath.Join(dir, "tg-steer-release")
	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:4", Message{
		Content:        "touch '" + tgSteerStarted + "'; while [ ! -f '" + tgSteerRelease + "' ]; do sleep 0.05; done; echo tg",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFile(t, tgSteerStarted)
	tg4, err := r.service.LookupSource(ctx, "bot", "telegram:123:4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.QueueOutboxFile(ctx, sqlc.QueueOutboxFileParams{
		ID:           "file-tg4",
		OperationKey: "op-tg4",
		SubmissionID: tg4.ID,
		AgentID:      "bot",
		ExecutionID:  conv.ExecutionID,
		Name:         "leak.txt",
		MimeType:     "text/plain",
		Body:         []byte("should be deleted"),
	}); err != nil {
		t.Fatal(err)
	}
	webSteerID := "web:0194f2c0-0000-7000-8000-000000000101"
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{ID: webSteerID, Content: "echo web-takeover", WhenBusy: "steer"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tgSteerRelease, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitSubSettled(t, r, tg4.ID)
	waitSubSettled(t, r, webSteerID)
	var leaked int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_files WHERE submission_id = ?`, tg4.ID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("unbound files after Web takeover = %d, err = %v; want 0", leaked, err)
	}
}

func TestUserChannelClearWritesZeroOutboxRows(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:1", Message{
		Content:        "hello",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := r.service.LookupSource(ctx, "bot", "telegram:123:1")
	if err != nil {
		t.Fatal(err)
	}
	waitSubSettled(t, r, rec.ID)
	var before int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	conv, err := r.q.GetSessionByKey(ctx, sqlc.GetSessionByKeyParams{AgentID: "bot", ChannelKey: MainKey})
	if err != nil {
		t.Fatal(err)
	}
	// When the Telegram user runs /clear, it records a context_reset submission.
	if err := r.service.Clear(ctx, "bot", conv.ID, "clear-1", "telegram:42", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var pending int
		if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE kind = 'context_reset' AND status = 'pending'`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for context_reset to settle")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Then /clear must not create any Outbox row (especially no failed "channel is not paired" row).
	var after int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("channel /clear wrote %d outbox rows, want 0", after-before)
	}
}

func TestUserWebRunSteeredByTelegramRoutesReplyToTelegram(t *testing.T) {
	t.Parallel()
	r := startNative(t, "bash")
	ctx := t.Context()
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "web-started")
	release := filepath.Join(dir, "web-release")
	webID := "web:0194f2c0-0000-7000-8000-000000000201"
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{
		ID:      webID,
		Content: "touch '" + started + "'; while [ ! -f '" + release + "' ]; do sleep 0.05; done; echo web-a",
	}); err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)

	// When Telegram steers into the active Web run, Telegram is the last user input.
	if err := r.service.SubmitMessage(ctx, "bot", "telegram:42", "telegram:123:50", Message{
		Content:        "echo tg-b",
		WhenBusy:       "steer",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}
	tgRec, err := r.service.LookupSource(ctx, "bot", "telegram:123:50")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitSubSettled(t, r, webID)
	waitSubSettled(t, r, tgRec.ID)

	// Then the shared answer routes once to telegram:42 (proving online RunInputIDs = [webID, tgID]).
	var peer string
	if err := r.conn.QueryRowContext(ctx, `SELECT peer FROM outbox_messages WHERE source_submission_id = ?`, tgRec.ID).Scan(&peer); err != nil || peer != "telegram:42" {
		t.Fatalf("telegram steer outbox peer = %q, err = %v; want telegram:42", peer, err)
	}
}

func TestCrashBeforeControlSettleHelper(t *testing.T) {
	if os.Getenv("DEX_CRASH_HELPER") != "1" {
		t.Skip("helper process only")
	}
	dataDir := os.Getenv("DEX_CRASH_DATA_DIR")
	gateDir := os.Getenv("DEX_CRASH_GATE_DIR")
	ctx := context.Background()
	r := openNativeTest(t, dataDir, "bash")
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		AgentID:      "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := r.service.Main(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.service.execution(ctx, c.ID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	conv := c
	tgID := "tg-crash-a"
	webID := "web-crash-b"
	started := filepath.Join(gateDir, "started")
	release := filepath.Join(gateDir, "release")
	gateCmd := "touch '" + started + "'; while [ ! -f '" + release + "' ]; do sleep 0.05; done; echo crash-a"
	// Persist Control pending submissions without settling them, then submit directly to PiG.
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID: tgID, SessionID: conv.ID, Content: gateCmd, Attachments: "[]",
		Origin: "telegram:42", Producer: ProducerUser, ReplyChannel: "telegram", ReplyPeer: "42",
		ChannelContext: `{"accountId":"123","senderId":"42"}`, Kind: "message", CreatedAt: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID: webID, SessionID: conv.ID, Content: "echo crash-b", Attachments: "[]",
		Origin: "web", Producer: ProducerUser, Kind: "message", WhenBusy: "steer", CreatedAt: 11,
	}); err != nil {
		t.Fatal(err)
	}
	model := engine.ModelRef{Provider: "faux", ModelID: "bash"}
	subA, err := r.agents.Submit(ctx, ref, engine.Input{RequestID: tgID, Content: gateCmd, Origin: "telegram:42", Source: "telegram:42", Kind: "message", WhenBusy: "followUp", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)
	subB, err := r.agents.Submit(ctx, ref, engine.Input{RequestID: webID, Content: "echo crash-b", Origin: "web", Source: "web", Kind: "message", WhenBusy: "steer", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	outA, err := subA.Wait(ctx)
	if err != nil || outA.Status != "done" || !slices.Equal(outA.RunInputIDs, []string{tgID, webID}) {
		t.Fatalf("subA wait = (%+v, %v)", outA, err)
	}
	outB, err := subB.Wait(ctx)
	if err != nil || outB.Status != "done" || !slices.Equal(outB.RunInputIDs, []string{tgID, webID}) {
		t.Fatalf("subB wait = (%+v, %v)", outB, err)
	}
	// Simulate hard crash after PiG wrote the answer and before Control settled either submission.
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
}

func TestUserCrashBeforeControlSettlePreservesWebSteerRoutingAfterSIGKILL(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	gateDir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCrashBeforeControlSettleHelper$")
	cmd.Env = append(os.Environ(),
		"DEX_CRASH_HELPER=1",
		"DEX_CRASH_DATA_DIR="+dataDir,
		"DEX_CRASH_GATE_DIR="+gateDir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected helper to terminate via SIGKILL, exited 0:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("unexpected helper error %v:\n%s", err, out)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL, got %v:\n%s", err, out)
	}

	// When Dex restarts after SIGKILL, both Control submissions settle via same-ID replay and stay in Web.
	r := openNativeTest(t, dataDir, "bash")
	ctx := t.Context()
	if err := r.service.RecoverSessions(ctx); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	t.Cleanup(cancelRun)
	go r.service.Run(runCtx)
	r.service.RetryNow()
	waitSubSettled(t, r, "tg-crash-a")
	waitSubSettled(t, r, "web-crash-b")
	for _, id := range []string{"tg-crash-a", "web-crash-b"} {
		sub, err := r.q.GetSubmission(ctx, id)
		if err != nil || sub.Status != "done" {
			t.Fatalf("submission %s after SIGKILL recovery = (%q, %v), want done", id, sub.Status, err)
		}
	}
	var count int
	if err := r.conn.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE source_submission_id IN ('tg-crash-a', 'web-crash-b')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("SIGKILL recovery wrote %d outbox rows, want 0 (last user was Web)", count)
	}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for file %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestUserDelegationPermanentAdmissionRejectionReportsOnceToParentWithoutChannelOutbox(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()
	parentConv, parentReq := taskParent(t, r)
	childConv, err := r.q.CreateChildSession(ctx, sqlc.CreateChildSessionParams{
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
		SessionID:       childConv.ID,
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
	if res.SessionID != parentConv || res.Producer != ProducerSubSession || res.ReplyChannel != "" || res.ReplyPeer != "" {
		t.Fatalf("parent result = (conv=%d producer=%q reply=%q:%q), want (conv=%d sub_session '')", res.SessionID, res.Producer, res.ReplyChannel, res.ReplyPeer, parentConv)
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
