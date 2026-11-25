package chat

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/runner"
)

const gatedEnvironment = "gated-environment"

type gatedOpen struct {
	runner.Provider
	entered, release chan struct{}
	once             sync.Once
	id               string
}

func (g *gatedOpen) Stamp(ctx context.Context, agent, id string) (string, error) {
	if id == g.id {
		return gatedEnvironment, nil
	}
	return g.Provider.Stamp(ctx, agent, id)
}

func (g *gatedOpen) Open(ctx context.Context, agent, id string) (runner.Handle, error) {
	if id != g.id {
		return g.Provider.Open(ctx, agent, id)
	}
	first := false
	g.once.Do(func() { first = true; close(g.entered) })
	if first {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-g.release:
			return nil, errors.New("first environment open failed")
		}
	}
	h, err := g.Provider.Open(ctx, agent, runner.LocalID)
	if err != nil {
		return nil, err
	}
	return gatedHandle{h}, nil
}

type gatedHandle struct{ runner.Handle }

func (gatedHandle) Stamp() string { return gatedEnvironment }

func TestUserRejectModeInputWaitsForEnvironmentReset(t *testing.T) {
	// Given an idle Session with an existing execution and an environment reset in progress.
	r := startNative(t, "echo")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := r.service.Post(ctx, "bot", MainKey, "", "before-reset", "before reset"); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "before-reset")
	entered := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	done := make(chan error, 1)
	go func() {
		done <- r.service.ResetEnvironment(ctx, "bot", func(ctx context.Context) error {
			close(entered)
			select {
			case <-released:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("environment reset did not start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// When the user sends reject-mode input, the reset must not reject it.
	const request = "during-reset"
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{ID: request, Content: "during reset", WhenBusy: "reject"}); err != nil {
		t.Fatalf("send during environment reset: %v", err)
	}
	if got := receipt(t, r, request); got.Status != "pending" || got.AdmittedAt.Valid {
		t.Fatalf("input must wait for environment reset: %+v", got)
	}

	// Then the same input runs after the reset ends.
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	if got := awaitReceipt(t, r, request); got.Status != "done" || got.Answer.String != "echo: during reset" {
		t.Fatalf("input after environment reset: %+v", got)
	}
}

func TestUserColdBotAdmitsLocalSessionWhileDefaultOpenWaits(t *testing.T) {
	// Given a cold Bot with a default environment that stalls on its first open.
	gate := &gatedOpen{entered: make(chan struct{}), release: make(chan struct{})}
	r := startNative(t, "echo", func(p runner.Provider) runner.Provider { gate.Provider = p; return gate })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	local, err := r.service.Create(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.SetEnvironment(ctx, "bot", local.ID, runner.LocalID); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	release := sync.OnceFunc(func() { close(gate.release) })
	defer release()
	mainDone := make(chan error, 1)
	go func() {
		_, err := r.service.Post(ctx, "bot", MainKey, "", "waiting-default", "default")
		mainDone <- err
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("Main did not reach default environment open")
	}

	// When the user sends input to the local Session, it admits before Main can proceed.
	sendCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	_, err = r.service.Post(sendCtx, "bot", local.ChannelKey, "", "independent-local", "local")
	stop()
	if err != nil || !receipt(t, r, "independent-local").AdmittedAt.Valid {
		t.Fatalf("local Session did not admit while default open stalled: %v", err)
	}
	if got := awaitReceipt(t, r, "independent-local"); got.Answer.String != "echo: local" {
		t.Fatalf("local Session did not answer: %+v", got)
	}
	if receipt(t, r, "waiting-default").AdmittedAt.Valid {
		t.Fatal("Main admitted before the default environment was ready")
	}

	// Then Main retries the same input after the first open fails.
	release()
	if err := <-mainDone; err != nil {
		t.Fatal(err)
	}
	if got := awaitReceipt(t, r, "waiting-default"); got.Answer.String != "echo: default" {
		t.Fatalf("Main did not answer after retry: %+v", got)
	}
}

func TestUserSameBotSessionsAdmitWhileChildWaitsForEnvironment(t *testing.T) {
	// Given a warm bot and a child whose first admission waits on its environment.
	gate := &gatedOpen{entered: make(chan struct{}), release: make(chan struct{}), id: gatedEnvironment}
	r := startNative(t, "context", func(p runner.Provider) runner.Provider { gate.Provider = p; return gate })
	parent, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, SessionNewArgs{Title: "Child", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(child.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), `UPDATE conversations SET environment_id = ? WHERE id = ?`, gatedEnvironment, id); err != nil {
		t.Fatal(err)
	}
	conv, err := r.q.GetConversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if conv.EnvironmentID != gatedEnvironment {
		t.Fatalf("child environment was not set: %+v", conv)
	}
	other, err := r.service.Create(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runReconciler(t, r)
	release := sync.OnceFunc(func() { close(gate.release) })
	defer release()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("child did not reach environment open")
	}

	// When more input reaches the child, Main and another session can admit first.
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: "second", ConversationID: id, Content: "second", Origin: "web", Producer: ProducerUser, Attachments: "null", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		id           int64
		key, request string
	}{{parent, MainKey, "main-independent"}, {other.ID, other.ChannelKey, "chat-independent"}} {
		sendCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		got, err := r.service.Post(sendCtx, "bot", target.key, "", target.request, target.request)
		stop()
		if err != nil || got != target.id || !receipt(t, r, target.request).AdmittedAt.Valid {
			t.Fatalf("%s did not admit before child release: id=%d err=%v", target.request, got, err)
		}
		if got := awaitReceipt(t, r, target.request); got.Status != "done" {
			t.Fatalf("%s did not run before child release: %+v", target.request, got)
		}
	}
	first := turnID(source.Key)
	if receipt(t, r, first).AdmittedAt.Valid || receipt(t, r, "second").AdmittedAt.Valid {
		t.Fatal("child request overtook stalled admission")
	}

	// Then a failed attempt retries the same receipt, before the second input.
	release()
	if got := awaitReceipt(t, r, first); got.Status != "done" || got.Answer.String != `context: ["first"]` {
		t.Fatalf("first receipt: %+v", got)
	}
	if got := awaitReceipt(t, r, "second"); got.Status != "done" || got.Answer.String != `context: ["first","second"]` {
		t.Fatalf("second receipt: %+v", got)
	}
	if _, err := r.q.GetSubmissionBySource(ctx, sql.NullString{String: "result:" + first, Valid: true}); err != nil {
		t.Fatal(err)
	}
}
