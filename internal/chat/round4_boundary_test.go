package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestRound3FinalBoundaryBatch(t *testing.T) {
	for _, withdrawFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("withdrawFirst=", withdrawFirst), func(t *testing.T) {
			r := startNative(t, "echo")
			parent, source := taskParent(t, r)
			entered, release := make(chan struct{}), make(chan struct{})
			releaseRun := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseRun)
			provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "review-boundary", Models: []ai.FauxModelDefinition{{ID: "model"}}, TokensPerSecond: 10000})
			provider.SetResponses([]ai.FauxResponseStep{
				ai.FauxResponseFactory(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
					close(entered)
					<-release
					return ai.FauxAssistantMessage(ai.FauxContentString("root answer"), ai.FauxAssistantMessageOptions{}), nil
				}),
				ai.FauxAssistantMessage(ai.FauxContentString("one batch answer"), ai.FauxAssistantMessageOptions{}),
			})
			r.agents.Catalog.Models.SetProvider(provider.Provider())
			child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Batch", Message: "root", Provider: "review-boundary", ModelID: "model"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			turns := []string{}
			for i, mode := range []string{"steer", "steer", "queue"} {
				call := source
				call.Key = fmt.Sprintf("bot/batch-%d", i)
				if _, err := r.service.ToolSessionSend(t.Context(), call, engine.SessionSendArgs{ID: child.ID, Message: fmt.Sprintf("input %d", i), Mode: mode}); err != nil {
					t.Fatal(err)
				}
				id := turnID(call.Key)
				turns = append(turns, id)
				if err := r.service.admit(t.Context(), taskInput(t, r, id)); err != nil {
					t.Fatal(err)
				}
			}
			if withdrawFirst {
				id, _ := strconv.ParseInt(child.ID, 10, 64)
				if _, err := r.service.Withdraw(t.Context(), "bot", id, turns[0]); err != nil {
					t.Fatal(err)
				}
			}
			releaseRun()
			awaitReceipt(t, r, turnID(source.Key))
			for i, id := range turns {
				turn := awaitReceipt(t, r, id)
				if i == 0 && withdrawFirst {
					continue
				}
				if turn.Status != "done" || turn.Answer.String != "one batch answer" {
					t.Fatalf("not one native batch: %+v", turn)
				}
			}
			rows, err := r.conn.QueryContext(t.Context(), `SELECT id FROM submissions WHERE conversation_id=? AND kind='task_result' ORDER BY rowid`, parent)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(ids) != 2 {
				t.Fatalf("root plus one QueueAll batch must send 2 results, got %d: %v", len(ids), ids)
			}
		})
	}
}

func TestRound3MixedParentStopDuringMaintenance(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Maintenance", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, turnID(source.Key))
	call := source
	call.Key = "bot/queued-parent"
	if _, err := r.service.ToolSessionSend(t.Context(), call, engine.SessionSendArgs{ID: child.ID, Message: "parent queued"}); err != nil {
		t.Fatal(err)
	}
	childID, _ := strconv.ParseInt(child.ID, 10, 64)
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "queued-user", ConversationID: childID, Content: "user queued", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.service.ReleaseMaintenance(context.Background()) })
	err = r.service.Stop(t.Context(), "bot", parent)
	if !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("expected an explicit maintenance error: %v", err)
	}
	for _, id := range []string{source.SourceRequestID, turnID(call.Key), "queued-user"} {
		if got := receipt(t, r, id); got.Status != "pending" {
			t.Fatalf("failed Stop committed partial control changes: %+v", got)
		}
	}
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.service.Stop(t.Context(), "bot", parent); err != nil {
		t.Fatal(err)
	}
	if got := receipt(t, r, turnID(call.Key)); got.Reason.String != "stopped" {
		t.Fatalf("retry did not stop parent input: %+v", got)
	}
	if got := receipt(t, r, "queued-user"); got.Status != "pending" {
		t.Fatalf("retry withdrew user input: %+v", got)
	}
	t.Log("ErrMaintenance rolls back Stop; an explicit retry after release stops only parent input")
}

func TestRound3UserInputAfterStopSnapshotSurvives(t *testing.T) {
	r := startNative(t, "bash")
	parent, source := taskParent(t, r)
	marker := filepath.Join(r.dir, "root-started")
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Race", Message: "touch " + marker + "; sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := strconv.ParseInt(child.ID, 10, 64)
	if err := r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	snapshot, allowAbort := make(chan struct{}), make(chan struct{})
	signalOnce := sync.OnceFunc(func() { close(snapshot); <-allowAbort })
	r.service.changed = func(id int64) {
		if id == childID {
			signalOnce()
		}
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- r.service.Stop(t.Context(), "bot", parent) }()
	<-snapshot
	c, err := r.q.GetConversation(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := r.service.Post(t.Context(), "bot", c.ChannelKey, "", "late-user", "echo user-survived")
		sent <- err
	}()
	waitFor(t, func() bool {
		_, err := r.q.GetSubmission(t.Context(), "late-user")
		return !errors.Is(err, sql.ErrNoRows) && err == nil
	})
	close(allowAbort)
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	got := awaitReceipt(t, r, "late-user")
	if got.Status != "done" || got.Answer.String != "bash: user-survived" {
		t.Fatalf("late user input was lost: %+v", got)
	}
}
