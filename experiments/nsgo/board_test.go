package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func boardTestGateway(t *testing.T) {
	t.Helper()
	if os.Getenv("DEX_NSGO_BOARD_TEST") != "1" || os.Getenv("DEX_NSGO_POC_TEST") != "1" {
		t.Skip("set DEX_NSGO_BOARD_TEST=1 and DEX_NSGO_POC_TEST=1 only with the dedicated test gateway")
	}
	target, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" ||
		target.Port() == "19487" || target.Port() == "19520" || target.Port() == "19521" || target.Port() == "19532" || target.Port() == "19533" {
		t.Fatal("DEX_NSGO_TEST_URL must point to the dedicated disposable loopback gateway")
	}
}

func TestUserCreatesAndUpdatesTaskFromBotBoard(t *testing.T) {
	boardTestGateway(t)
	t.Parallel()
	const timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	title := "Prepare release notes " + uuid.New().String()[:8]
	note := "Keep this note on the task."
	message := "Send this update to the assignee."
	call := func(method, path string, body, out any) {
		t.Helper()
		if err := requestJSON(ctx, method, path, body, out); err != nil {
			t.Fatal(err)
		}
	}

	// Given a global Board with a review for a second Bot.
	botName := "Board probe " + uuid.New().String()[:8]
	var assignee struct {
		ID string `json:"id"`
	}
	call(http.MethodPost, "/api/bots", map[string]string{"name": botName, "provider": "faux", "modelId": "echo"}, &assignee)
	var roster struct {
		Bots []botListItem `json:"bots"`
	}
	call(http.MethodGet, "/api/bots", nil, &roster)
	assigneeIndex := -1
	for i, bot := range roster.Bots {
		if bot.ID == assignee.ID {
			assigneeIndex = i
			break
		}
	}
	if assigneeIndex < 0 {
		t.Fatalf("fresh Bot %q not in the active roster", assignee.ID)
	}
	var review boardTask
	call(http.MethodPost, "/api/tasks", map[string]string{"title": "Review " + title, "assigneeBotId": assignee.ID, "reviewerKind": "owner", "status": "in_progress"}, &review)
	call(http.MethodPatch, "/api/tasks/"+url.PathEscape(review.Key), map[string]string{"status": "in_review"}, nil)

	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		key := core.NewSignal(cx, "")
		chat := core.NewSignal(cx, "")
		return core.View{Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0)}, Children: []core.Node{
			core.Show{When: func() bool { return key.Get() == "" }, Then: func(scope *core.Scope) core.Node {
				return boardPage(scope, "default", func(value string) { key.Set(value) })
			}},
			core.Show{When: func() bool { return key.Get() != "" && chat.Get() == "" }, Then: func(scope *core.Scope) core.Node {
				return taskPage(scope, "default", key.Get(), func(id string, session int, _ string) {
					if id == assignee.ID && session == 0 {
						chat.Set("Assignee Main")
					}
				}, func() { key.Set("") })
			}},
			core.Text{ContentFunc: chat.Get},
		}}
	}, nsgotest.Size(390, 820))
	app.WaitFor("id:board-assignee", timeout)
	app.WaitFor("id:board-group-needs", timeout)
	app.Press("id:board-approve-" + review.Key)
	app.WaitForGone("id:board-approve-"+review.Key, timeout)
	app.Press("id:board-completed")
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted("id:board-open-"+review.Key)) == 1 })

	// When the user picks a different assignee and adds a task, Board keeps its place.
	app.Press("id:board-assignee")
	option := "id:board-assignee-" + strconv.Itoa(assigneeIndex)
	if !app.Exists(option) {
		app.Scroll("role:listbox", core.Points(32*assigneeIndex))
	}
	app.Press(option)
	app.Press("id:board-task-title")
	app.Commit("id:board-task-title", title)
	app.Press("id:board-create")
	app.WaitForGone("id:board-create", timeout)
	app.WaitFor("id:board-group-active", timeout)
	var tasks struct {
		Tasks []boardTask `json:"tasks"`
	}
	call(http.MethodGet, "/api/tasks?includeClosed=true", nil, &tasks)
	var key string
	for _, item := range tasks.Tasks {
		if item.Title == title {
			if item.Assignee != assignee.ID {
				t.Fatalf("task assignee = %q, want %q", item.Assignee, assignee.ID)
			}
			key = item.Key
		}
	}
	if key == "" {
		t.Fatal("the newly added task was not returned by Dex")
	}
	app.WaitFor("id:board-open-"+key, timeout)
	app.Press("id:board-open-" + key)
	app.WaitFor("id:task-status-picker", timeout)
	app.Press("id:task-composer")
	app.Commit("id:task-composer", note)
	app.Press("id:task-note")
	app.WaitFor("text:Note saved.", timeout)
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted("text:"+note)) == 1 })

	// A concurrent edit blocks a stale status change. A second attempt uses the fresh revision.
	renamed := title + " revised"
	call(http.MethodPatch, "/api/tasks/"+url.PathEscape(key), map[string]string{"title": renamed}, nil)
	app.Press("id:task-status-picker")
	app.Press("id:task-status-in_progress")
	app.WaitUntil(timeout, func() bool { return app.Find("id:task-title").Text == renamed })
	if got := app.Find("id:task-status").Text; got != "Ready" {
		t.Fatalf("stale status update changed the task to %q", got)
	}
	app.Press("id:task-status-picker")
	app.Press("id:task-status-in_progress")
	app.WaitUntil(timeout, func() bool { return app.Find("id:task-status").Text == "In Progress" })
	app.Press("id:task-composer")
	app.Commit("id:task-composer", message)
	app.Press("id:task-send")
	app.WaitFor("text:Message sent to the assignee's Main.", timeout)
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted("text:"+message)) == 1 })

	// Then the user can return to Board, reopen the task, and open Main without a fake link.
	app.Press("id:task-back-board")
	app.WaitFor("id:board-open-"+key, timeout)
	app.Press("id:board-open-" + key)
	app.WaitFor("id:task-no-sessions", timeout)
	app.Press("id:task-open-main")
	app.WaitFor("text:Assignee Main", timeout)
}

func TestUserOpensAndDismissesAnUpdateFromFilteredBoard(t *testing.T) {
	boardTestGateway(t)
	t.Parallel()
	const timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	call := func(method, path string, body, out any) {
		t.Helper()
		if err := requestJSON(ctx, method, path, body, out); err != nil {
			t.Fatal(err)
		}
	}

	// Given a changed result from a real run and a task for another Bot.
	var bot struct {
		ID string `json:"id"`
	}
	label := "Release check " + uuid.New().String()[:8]
	call(http.MethodPost, "/api/bots", map[string]string{"name": label, "provider": "faux", "modelId": "tool"}, &bot)
	var other boardTask
	call(http.MethodPost, "/api/tasks", map[string]string{"title": "Other Bot task " + label, "assigneeBotId": "default", "status": "ready"}, &other)
	var automation struct {
		ID string `json:"id"`
	}
	path := "/api/bots/" + url.PathEscape(bot.ID) + "/automations"
	call(http.MethodPost, path, map[string]any{"name": label, "prompt": `automation_report {"outcome":"changed","summary":"The changelog is ready."}`,
		"schedule": map[string]string{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": false}, &automation)
	path += "/" + url.PathEscape(automation.ID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := requestJSON(ctx, http.MethodDelete, path, nil, nil); err != nil {
			t.Errorf("remove test automation: %v", err)
		}
	})
	var run struct {
		ID string `json:"id"`
	}
	call(http.MethodPost, path+"/run", nil, &run)
	if run.ID == "" {
		t.Fatal("the real automation did not return a run")
	}
	wait, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	ticks := time.NewTicker(100 * time.Millisecond)
	defer ticks.Stop()
	for {
		attention, err := boardRead[boardAttention](wait, "/api/board/attention")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, result := range attention.Results {
			if result.ID == run.ID {
				found = true
				break
			}
		}
		if found {
			break
		}
		select {
		case <-ticks.C:
		case <-wait.Done():
			t.Fatalf("run %s has no Board result", run.ID)
		}
	}

	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		result := core.NewSignal(cx, "")
		filter := core.NewSignal(cx, bot.ID)
		return core.Dynamic(func(scope *core.Scope) core.Node {
			id, botFilter := result.Get(), filter.Get()
			return core.Untracked(scope, func() core.Node {
				if id != "" {
					return boardResultPage(scope, id, func() { result.Set("") }, nil)
				}
				return boardPage(scope, "default", func(string) {}, boardNavigation{
					FilterBot: botFilter, ClearFilter: func() { filter.Set("") }, OpenResult: result.Set,
				})
			})
		})
	}, nsgotest.Size(1280, 800), nsgotest.Idiom(core.IdiomDesktop))
	app.WaitFor("id:board-group-updates", timeout)
	if n := len(app.QueryMounted("id:board-open-" + other.Key)); n != 0 {
		t.Fatalf("filtered Board shows %d rows for the other Bot", n)
	}

	// When the user expands the update, views the result, and marks it done.
	app.Press("id:board-update-" + automation.ID)
	app.Press("text:View result")
	if got := app.WaitFor("id:board-result-summary", timeout).Text; got != "The changelog is ready." {
		t.Fatalf("result summary = %q", got)
	}
	app.WaitFor("id:board-result-done", timeout)
	app.Press("id:board-result-done")
	app.WaitForGone("id:board-result-done", timeout)
	app.Press("id:board-result-back")
	app.WaitFor("id:board-empty", timeout)

	// Then returning to the global view brings the other Bot's task back.
	app.Press("id:board-clear-filter")
	app.WaitFor("id:board-open-"+other.Key, timeout)
}
