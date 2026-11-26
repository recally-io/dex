package tools

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserDelegatesTaskFromMainAndKeepsSessionOwnership(t *testing.T) {
	s, conn := openTools(t)
	ctx := t.Context()
	q := sqlc.New(conn)
	var wakes []int64
	s.ConfigureWorkTasks(worktask.New(conn, func(main int64) {
		if _, err := q.GetUnadmittedTaskWake(ctx, main); err != nil {
			t.Fatalf("wake for Main %d before commit: %v", main, err)
		}
		wakes = append(wakes, main)
	}))
	main := func(key string) engine.TaskContext { return toolCall("assignee", 1, "wake", key) }

	// Given both a Source Task and a root Task the user explicitly created.
	receipt, err := s.tasks.AcceptCreating(ctx, worktask.Event{
		Namespace: "fixture", ReceiptKey: "source", Type: "created", Content: "External work", OccurredAt: 1,
		Objects: []worktask.Object{{Type: "item", Key: "1"}},
	}, "assignee", "External root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(ctx, main("source-split"), WorkTaskSplitArgs{ParentID: receipt.TaskID, Title: "No"}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("split without root authorization: %v", err)
	}
	wakes = nil
	parent, err := s.New(ctx, toolCall("assignee", 1, "user", "root"), WorkTaskNewArgs{Title: "Release"})
	if err != nil {
		t.Fatal(err)
	}

	// When Main splits the Task, it reviews delegated work by default. Self-assigned work has no Reviewer.
	args := WorkTaskSplitArgs{ParentID: parent.Key, Title: "Build", AssigneeBotID: "reviewer"}
	delegated, err := s.Split(ctx, main("split"), args)
	if err != nil || delegated.ReviewerKind != worktask.ActorBot || delegated.ReviewerID != "assignee" {
		t.Fatalf("delegated subtask: %+v, %v", delegated, err)
	}
	if replay, err := s.Split(ctx, main("split"), args); err != nil || replay.ID != delegated.ID || !slices.Equal(wakes, []int64{2}) {
		t.Fatalf("split replay: %+v, %v, wakes = %v", replay, err, wakes)
	}
	self, err := s.Split(ctx, main("self"), WorkTaskSplitArgs{ParentID: parent.Key, Title: "Publish", DependsOn: []string{delegated.Key}})
	if err != nil || self.ReviewerKind != "" || !self.Blocked {
		t.Fatalf("self-assigned subtask: %+v, %v", self, err)
	}
	row, err := q.GetBoardTask(ctx, delegated.ID)
	if err != nil || !row.AuthorizedByRootID.Valid || row.AuthorizedByRootID.String != parent.ID || row.ParentTaskID != (sql.NullString{String: parent.ID, Valid: true}) {
		t.Fatalf("child authorization: %+v, %v", row, err)
	}
	if _, err := s.Update(ctx, main("link"), WorkTaskUpdateArgs{ID: self.Key, Session: "4"}); err != nil {
		t.Fatal(err)
	}

	// Then the parent Assignee can reassign a subtask without taking over its linked Session.
	if _, err := s.Assign(ctx, main("clear-review"), WorkTaskAssignArgs{ID: delegated.Key, AssigneeBotID: "other", Reviewer: "none"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Bot removed an existing Reviewer: %v", err)
	}
	handOff := WorkTaskAssignArgs{ID: self.Key, AssigneeBotID: "other", Note: "Ship after Build."}
	out, err := s.Assign(ctx, main("assign"), handOff)
	if err != nil || out.AssigneeBotID != "other" || out.ReviewerID != "assignee" || !slices.Equal(wakes, []int64{2, 3}) {
		t.Fatalf("handoff: %+v, %v, wakes = %v", out, err, wakes)
	}
	// Main can also delegate a sibling that is currently assigned to a different Bot.
	unreviewed, err := s.Split(ctx, main("no-review"), WorkTaskSplitArgs{ParentID: parent.Key, Title: "Package", AssigneeBotID: "reviewer", Reviewer: "none"})
	if err != nil || unreviewed.ReviewerKind != "" {
		t.Fatalf("subtask without a Reviewer: %+v, %v", unreviewed, err)
	}
	if _, err := s.Read(ctx, main("unreviewed"), WorkTaskReadArgs{ID: unreviewed.Key}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("parent Assignee unexpectedly reads subtask without a role: %v", err)
	}
	if got, err := s.Assign(ctx, main("parent-assign"), WorkTaskAssignArgs{ID: unreviewed.Key, AssigneeBotID: "other"}); err != nil || got.AssigneeBotID != "other" {
		t.Fatalf("parent Assignee could not reassign its subtask: %+v, %v", got, err)
	}
	child, err := q.GetConversation(ctx, 4)
	if err != nil || child.ParentID != (sql.NullInt64{Int64: 1, Valid: true}) {
		t.Fatalf("child Session changed parents: %+v, %v", child, err)
	}
	if detail, err := s.Read(ctx, toolCall("other", 3, "other-user", "read"), WorkTaskReadArgs{ID: self.Key}); err != nil || len(detail.Sessions) != 1 {
		t.Fatalf("new Assignee cannot see existing linked Session: %+v, %v", detail, err)
	}
}

func TestUserChildReadsOnlyBoundTaskAndTrustedUpstreamContext(t *testing.T) {
	s, _ := openTools(t)
	ctx := t.Context()
	main := func(key string) engine.TaskContext { return toolCall("assignee", 1, "user", key) }
	parent, err := s.New(ctx, main("root"), WorkTaskNewArgs{Title: "Release", Note: "Owner requested a release."})
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := s.Split(ctx, main("upstream"), WorkTaskSplitArgs{ParentID: parent.Key, Title: "Build", Note: "Use version 2."})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := s.Split(ctx, main("dependent"), WorkTaskSplitArgs{ParentID: parent.Key, Title: "Publish", DependsOn: []string{upstream.Key}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, main("link"), WorkTaskUpdateArgs{ID: dependent.Key, Session: "4"}); err != nil {
		t.Fatal(err)
	}
	object := worktask.Object{Type: "item", Key: "1"}
	if err := s.tasks.LinkObject(ctx, "assignee", upstream.ID, "fixture", object); err != nil {
		t.Fatal(err)
	}
	const external = "This external text must stay off the model"
	if _, err := s.tasks.Accept(ctx, worktask.Event{
		Namespace: "fixture", ReceiptKey: "update", Type: "updated", Content: external, Objects: []worktask.Object{object}, OccurredAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// When the linked child requests upstream context, the response has trusted entries of both related Tasks.
	child := toolCall("other", 4, "child-user", "child")
	detail, err := s.Read(ctx, child, WorkTaskReadArgs{ID: dependent.Key, IncludeUpstream: true})
	if err != nil || len(detail.Upstream) != 2 || detail.Upstream[0].Key != parent.Key || detail.Upstream[1].Key != upstream.Key {
		t.Fatalf("upstream context: %+v, %v", detail, err)
	}
	data, err := json.Marshal(detail.Upstream)
	if err != nil || !strings.Contains(string(data), "Owner requested a release.") || !strings.Contains(string(data), "Use version 2.") || strings.Contains(string(data), external) || strings.Contains(string(data), `"trust":"external"`) {
		t.Fatalf("untrusted or missing context: %s, %v", data, err)
	}

	// Then the context does not grant direct read or write access to the related Tasks.
	if _, err := s.Read(ctx, child, WorkTaskReadArgs{ID: upstream.Key}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("child directly read unlinked upstream Task: %v", err)
	}
	if _, err := s.Post(ctx, child, WorkTaskPostArgs{ID: upstream.Key, Content: "No"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("child wrote to unlinked upstream Task: %v", err)
	}
	if _, err := s.MyWorkload(ctx, child, struct{}{}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("child could list the Bot's workload: %v", err)
	}
}
