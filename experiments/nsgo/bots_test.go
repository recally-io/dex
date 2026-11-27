//go:build !js

package main

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestUserCreatesBotAndRenamesItWithoutLosingNewInstructions(t *testing.T) {
	if os.Getenv("DEX_NSGO_BOTS_TEST") != "1" {
		t.Skip("set DEX_NSGO_BOTS_TEST=1 only with a disposable Dex gateway")
	}
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" || os.Getenv("DEX_NSGO_TEST_URL") == "" {
		t.Fatal("set DEX_NSGO_POC_TEST=1 and DEX_NSGO_TEST_URL to the disposable gateway")
	}
	const timeout = 22 * time.Second
	name := "验收 " + uuid.New().String()[:8]
	newName := name + " 新名称"
	const instructions = "由另一个客户端保存的指令。"

	// Given an active bot list backed by a disposable Dex gateway.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		picked := core.NewSignal(cx, "")
		return core.Show{When: func() bool { return picked.Get() == "" }, Then: func(cx *core.Scope) core.Node {
			open := core.NewSignal(cx, false)
			return core.View{Style: core.Style{FlexGrow: 1}, Children: []core.Node{
				ui.Button{Text: "新建 Bot", TestID: "bots-new", OnPress: func() { open.Set(true) }},
				botsPage(cx, "default", func(id, _ string) { picked.Set(id) }),
				core.Show{When: open.Get, Then: func(cx *core.Scope) core.Node {
					return botCreateDialog(cx, func() { open.Set(false) }, func(id, _ string) { picked.Set(id) })
				}},
			}}
		}, Else: func(cx *core.Scope) core.Node {
			return botSettingsPage(cx, picked.Get(), func(string) {})
		}}
	})
	app.WaitFor("id:bots-open-default", timeout)

	// When the user opens, cancels, and reopens the form, it starts with no draft.
	if len(app.QueryMounted("id:bots-create-name")) != 0 {
		t.Fatal("the creation form is visible before the user opens it")
	}
	app.Press("id:bots-new")
	app.Press("id:bots-create-name")
	app.Commit("id:bots-create-name", "这份草稿不保存")
	app.Press("id:bots-create-cancel")
	app.WaitForGone("id:bots-create-name", timeout)
	app.Press("id:bots-new")
	if got := app.Find("id:bots-create-name").Text; got != "" {
		t.Fatalf("reopened form kept canceled draft %q", got)
	}

	// The user then creates a bot and edits only its name.
	app.Press("id:bots-create-name")
	app.Commit("id:bots-create-name", name)
	app.Press("id:bots-create")
	app.WaitFor("id:bot-settings-name", timeout)
	app.Clear("id:bot-settings-name")
	app.Commit("id:bot-settings-name", newName)

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	var bots struct {
		Bots []botListItem `json:"bots"`
	}
	if err := requestJSON(ctx, http.MethodGet, "/api/bots", nil, &bots); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, bot := range bots.Bots {
		if bot.Name == name {
			id = bot.ID
			break
		}
	}
	if id == "" || id == "default" {
		t.Fatalf("created bot ID = %q", id)
	}
	path := botPath(id)
	var settings botSettingsValues
	if err := requestJSON(ctx, http.MethodGet, path, nil, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Instructions = instructions
	if err := requestJSON(ctx, http.MethodPut, path, settings, nil); err != nil {
		t.Fatal(err)
	}
	app.Press("id:bot-settings-save")

	// Then the renamed bot retains the latest instructions from the other client.
	app.WaitFor("text:设置已保存", timeout)
	if err := requestJSON(ctx, http.MethodGet, path, nil, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Name != newName || settings.Instructions != instructions {
		t.Fatalf("saved name = %q, instructions = %q", settings.Name, settings.Instructions)
	}
}
