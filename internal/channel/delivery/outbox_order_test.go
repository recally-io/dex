package delivery

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

type orderTestTelegram struct {
	attempts []string
	fail     error
}

func (*orderTestTelegram) Run(ctx context.Context) error { return ctx.Err() }
func (*orderTestTelegram) Parts(reply channel.Reply) []channel.Part {
	return []channel.Part{{Text: reply.Answer}}
}
func (b *orderTestTelegram) Send(_ context.Context, _, id string, _ channel.Part) (channel.Receipt, error) {
	b.attempts = append(b.attempts, id)
	if err := b.fail; err != nil {
		b.fail = nil
		return channel.Receipt{}, err
	}
	return channel.Receipt{MessageID: id}, nil
}

func TestUserChannelRepliesKeepInputOrderThroughStopAndRetry(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m := New(ctx, conn, nil, "bot")
	t.Cleanup(m.Close)
	bot := &orderTestTelegram{}
	exec := func(stmt string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	flush := func(want ...string) {
		t.Helper()
		bot.attempts = nil
		if err := m.flush(ctx, "telegram", bot); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(bot.attempts, want) {
			t.Fatalf("send attempts = %v, want %v", bot.attempts, want)
		}
	}

	// Given the second answer is ready before the first input has settled.
	exec(`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO sessions (id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('bot', '123:fixture', '42,43');
INSERT INTO submissions (id, session_id, content, status, origin, created_at, channel_context) VALUES
    ('first', 1, 'first', 'pending', 'telegram:42', 1, '{"accountId":"123"}'),
    ('second', 1, 'second', 'done', 'telegram:42', 2, '{"accountId":"123"}'),
    ('other', 1, 'other', 'done', 'telegram:43', 3, '{"accountId":"123"}');
INSERT INTO outbox_messages (id, event_id, agent_id, session_id, source_submission_id, kind, peer, channel_context, body, created_at)
VALUES ('second', 'reply:bot:second', 'bot', 1, 'second', 'reply', 'telegram:42', '{"accountId":"123"}', 'second answer', 2),
       ('other', 'reply:bot:other', 'bot', 1, 'other', 'reply', 'telegram:43', '{"accountId":"123"}', 'other answer', 3);`)
	flush("other")

	// When Stop replaces the first answer with a reply that needs a retry,
	// the later answer waits. Once the retry is due, both go out in input order.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	if err := q.InsertStoppedOutboxForSession(ctx, sqlc.InsertStoppedOutboxForSessionParams{AgentID: "bot", SessionID: 1, Now: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.StopSessionSubmissions(ctx, sqlc.StopSessionSubmissionsParams{SessionID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	bot.fail = errors.New("temporary Telegram failure")
	flush("first")
	exec(`UPDATE outbox_messages SET retry_at = 9223372036854775807 WHERE id = 'first' AND retry_at > 0`)
	flush()
	exec(`UPDATE outbox_messages SET retry_at = 0 WHERE id = 'first'`)
	flush("first", "second")

	// Then settled silent inputs, revoked inputs, and terminal delivery failures
	// do not stop a new answer, even if a failed reply's input is still pending.
	exec(`INSERT INTO submissions (id, session_id, content, status, origin, created_at, channel_revoked) VALUES
    ('silent', 1, 'silent', 'done', 'telegram:42', 5, 0),
    ('revoked', 1, 'revoked', 'pending', 'telegram:42', 6, 1),
    ('failed', 1, 'failed', 'pending', 'telegram:42', 7, 0),
    ('last', 1, 'last', 'done', 'telegram:42', 8, 0);
INSERT INTO outbox_messages (id, event_id, agent_id, session_id, source_submission_id, kind, peer, channel_context, body, created_at)
VALUES ('failed', 'reply:bot:failed', 'bot', 1, 'failed', 'reply', 'telegram:42', '{"accountId":"123"}', 'failed answer', 7),
       ('last', 'reply:bot:last', 'bot', 1, 'last', 'reply', 'telegram:42', '{"accountId":"123"}', 'last answer', 8);`)
	bot.fail = channel.ErrPermanent
	flush("failed")
	flush("last")
}
