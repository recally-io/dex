//go:build !js

package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func composerActivityTestGateway(t *testing.T) {
	t.Helper()
	if os.Getenv("DEX_NSGO_ACTIVITY_TEST") != "1" {
		t.Skip("set DEX_NSGO_ACTIVITY_TEST=1 only with a disposable Dex gateway")
	}
	target, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" || err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Port() == "19487" {
		t.Fatal("DEX_NSGO_TEST_URL must point to a dedicated loopback Dex with the faux tool model")
	}
}

func TestUserRefreshesAndCancelsMainActivityOnPhone(t *testing.T) {
	composerActivityTestGateway(t)
	t.Parallel()
	const timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	// Given an empty Main on a new Bot in the disposable Dex database.
	var bot struct {
		ID string `json:"id"`
	}
	seed := map[string]string{"name": "活动探针 " + uuid.New().String()[:8], "provider": "faux", "modelId": "tool"}
	if err := requestJSON(ctx, http.MethodPost, "/api/bots", seed, &bot); err != nil {
		t.Fatal(err)
	}
	if bot.ID == "" || bot.ID == "default" {
		t.Fatalf("expected a fresh test Bot, got %q", bot.ID)
	}
	main, err := chatGetSession(ctx, bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return composerActivity(cx, bot.ID, func() chatSession { return main })
	}, nsgotest.Size(360, 740), nsgotest.Idiom(core.IdiomPhone))
	app.Press("id:composer-activity")
	app.WaitFor("text:暂无监控。", timeout)
	app.WaitFor("text:暂无外发记录。", timeout)

	// When a real tool schedules a monitor, refresh shows it and enables cancellation.
	const prompt = "稍后检查构建"
	if err := sendChat(ctx, bot.ID, main.ID, uuid.New().String(), `monitor_set {"delaySeconds":300,"prompt":"`+prompt+`"}`); err != nil {
		t.Fatal(err)
	}
	path := chatSessionPath(bot.ID, main.ID) + "/monitors"
	var monitorID string
	app.WaitUntil(timeout, func() bool {
		var result struct {
			Monitors []struct {
				ID     string `json:"id"`
				Prompt string `json:"prompt"`
				State  string `json:"state"`
			} `json:"monitors"`
		}
		if err := requestJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
			t.Fatal(err)
		}
		for _, monitor := range result.Monitors {
			if monitor.Prompt == prompt && monitor.State == "scheduled" {
				monitorID = monitor.ID
				return true
			}
		}
		return false
	})
	cancelled := false
	t.Cleanup(func() {
		if !cancelled {
			ctx, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer stop()
			if err := requestJSON(ctx, http.MethodDelete, path+"/"+url.PathEscape(monitorID), nil, nil); err != nil {
				t.Errorf("clean up scheduled test monitor: %v", err)
			}
		}
	})
	app.Press("id:composer-activity-refresh")
	app.WaitFor("text:"+prompt, timeout)
	cancelID := "id:composer-activity-cancel-" + monitorID
	app.Press(cancelID)

	// Then the cancellation persists and the same row no longer offers cancellation.
	app.WaitFor("text:监控已取消。", timeout)
	cancelled = true
	app.WaitForGone(cancelID, timeout)
	stateID := "id:composer-activity-monitor-state-" + monitorID
	if got := app.Find(stateID).Text; got != "已取消" {
		t.Fatalf("monitor state = %q, want 已取消", got)
	}
	app.Press("id:composer-activity-close")
	app.WaitForGone("id:composer-activity-panel", timeout)
	app.Press("id:composer-activity")
	if got := app.WaitFor(stateID, timeout).Text; got != "已取消" {
		t.Fatalf("reopened monitor state = %q, want 已取消", got)
	}
	app.WaitFor("text:暂无外发记录。", timeout)
}

func TestUserCanRetryActivityWhenMainIsUnavailable(t *testing.T) {
	composerActivityTestGateway(t)
	t.Parallel()
	const timeout = 12 * time.Second

	// Given the page still has a Main that the real Dex gateway can no longer find.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return composerActivity(cx, "missing-"+uuid.New().String(), func() chatSession {
			return chatSession{Main: true, ID: 1}
		})
	}, nsgotest.Size(1024, 720), nsgotest.Idiom(core.IdiomDesktop))

	// When the user opens the activity popover, both read failures are recoverable.
	app.Press("id:composer-activity")
	app.WaitFor("text:无法读取监控。请刷新活动。", timeout)
	app.WaitFor("text:无法读取发件箱。请刷新活动。", timeout)
	app.Press("id:composer-activity-refresh")

	// Then another failure still does not look like a successful empty list.
	app.WaitFor("text:无法读取发件箱。请刷新活动。", timeout)
	if app.Exists("text:暂无监控。") || app.Exists("text:暂无外发记录。") {
		t.Fatal("failed activity request displayed an empty state")
	}
}
