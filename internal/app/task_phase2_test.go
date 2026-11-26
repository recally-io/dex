package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/api"
)

func TestUserSplitsTaskAndFindsOwnerReview(t *testing.T) {
	t.Parallel()
	// Given a Task on the Board.
	d := startDex(t, t.TempDir(), "")
	const tasks = "/api/tasks"
	var parent, upstream, downstream api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Release", "assigneeBotId": "default"}, &parent, http.StatusCreated)
	parentPath := tasks + "/" + parent.Key

	// When the Owner creates two subtasks and makes the second depend on the first.
	d.must(t, http.MethodPost, parentPath+"/split", map[string]string{"title": "Build", "status": "in_progress", "reviewer": "owner"}, &upstream, http.StatusCreated)
	d.must(t, http.MethodPost, parentPath+"/split", map[string]any{"title": "Publish", "dependsOn": []string{upstream.Key}}, &downstream, http.StatusCreated)
	if !downstream.Blocked || downstream.ReviewerKind != "" {
		t.Fatalf("Owner-created Publish should have a dependency and no Reviewer: %+v", downstream)
	}
	d.must(t, http.MethodPost, tasks+"/"+upstream.Key+"/dependencies", map[string]string{"dependsOn": downstream.Key}, nil, http.StatusBadRequest)
	var parentDetail api.TaskDetail
	d.must(t, http.MethodGet, parentPath, nil, &parentDetail)
	if len(parentDetail.Subtasks) != 2 {
		t.Fatalf("subtasks = %+v", parentDetail.Subtasks)
	}

	// Then Build appears in Board until approval, which also unblocks Publish.
	upstreamPath := tasks + "/" + upstream.Key
	d.must(t, http.MethodPatch, upstreamPath, map[string]string{"status": "in_review"}, &upstream)
	var work api.BoardAttention
	d.must(t, http.MethodGet, "/api/board/attention", nil, &work)
	if len(work.TaskReviews) != 1 || work.TaskReviews[0].Key != upstream.Key {
		t.Fatalf("Owner reviews = %+v", work.TaskReviews)
	}
	d.must(t, http.MethodPost, upstreamPath+"/review", map[string]any{"decision": "approve", "reviewSeq": upstream.ReviewSeq}, &upstream)
	var detail api.TaskDetail
	d.must(t, http.MethodGet, tasks+"/"+downstream.Key, nil, &detail)
	if detail.Task.Blocked {
		t.Fatalf("Publish still blocked after approval: %+v", detail.Blockers)
	}
	work = api.BoardAttention{}
	d.must(t, http.MethodGet, "/api/board/attention", nil, &work)
	if len(work.TaskReviews) != 0 {
		t.Fatalf("completed review still in Board: %+v", work.TaskReviews)
	}
	d.must(t, http.MethodDelete, tasks+"/"+downstream.Key+"/dependencies/"+upstream.Key, nil, nil, http.StatusNoContent)
}

func TestUserDelegatesTaskToChildSessionWithScopedTools(t *testing.T) {
	t.Parallel()
	// Given a Task input to Main that starts a linked child on the faux tool model.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	const tasks = "/api/tasks"
	var task, unrelated api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Verify release", "assigneeBotId": "default"}, &task, http.StatusCreated)
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Unrelated work", "assigneeBotId": "default"}, &unrelated, http.StatusCreated)
	path := tasks + "/" + task.Key
	const note = "Child verified the release."
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", main.ID), map[string]string{
		"id": uuid.NewV7().String(), "taskId": task.Id,
		"text": "session_new " + childJSON(t, map[string]any{
			"title": "Verify", "provider": "faux", "modelId": "tool",
			"message": "task_post " + childJSON(t, map[string]string{"id": task.Key, "content": note, "kind": "artifact"}),
		}),
	}, nil, http.StatusAccepted)

	// When the delegated turn finishes, its artifact appears on the linked Task.
	var detail api.TaskDetail
	eventually(t, "child posts an artifact on its Task", func() (bool, any) {
		d.must(t, http.MethodGet, path, nil, &detail)
		for _, entry := range detail.Entries {
			if entry.Kind == "artifact" && entry.Content == note {
				return true, entry
			}
		}
		return false, detail.Entries
	})
	if len(detail.Sessions) != 1 {
		t.Fatalf("linked Sessions = %+v", detail.Sessions)
	}
	child := detail.Sessions[0].ConversationId
	w := d.watch(t, child)
	w.until(t, "child is idle", func(tr *transcript) bool { return !tr.running })

	// Then the child reads its Task, but cannot read another Task or change status.
	check := func(tool, key, fragment string) {
		t.Helper()
		args := map[string]string{"id": key}
		if tool == "task_update" {
			args["status"] = "in_progress"
		}
		id := d.send(t, child, tool+" "+childJSON(t, args))
		w.until(t, "answer to "+id, func(tr *transcript) bool {
			if tr.running {
				return false
			}
			for i, item := range tr.items {
				if item.ID == id {
					for _, reply := range tr.items[i+1:] {
						if reply.Kind == "assistant" && strings.Contains(reply.Text, fragment) {
							return true
						}
					}
				}
			}
			return false
		})
	}
	check("task_read", task.Key, note)
	check("task_read", unrelated.Key, "no Task visible")
	check("task_update", task.Key, "tool error:")
	d.must(t, http.MethodGet, path, nil, &detail)
	if detail.Task.Status != task.Status {
		t.Fatalf("child changed Task status to %s", detail.Task.Status)
	}
}
