package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserConnectsTelegramBotsToSeparateAgents(t *testing.T) {
	t.Parallel()
	// Given two bots with separate platform queues and agents with different models.
	first, second := newFakeTelegram(t), newFakeTelegram(t)
	const secondToken = "456:second-secret"
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot"+secondToken+"/") {
			r.URL.Path = strings.Replace(r.URL.Path, secondToken, fakeToken, 1)
			second.serve(w, r)
			return
		}
		first.serve(w, r)
	}))
	defer boundary.Close()
	dir := t.TempDir()
	d := startDex(t, dir, boundary.URL)
	d.setModel(t, "echo")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "shout", "thinkingLevel": "off"}, &b, http.StatusCreated)
	aBase, bBase := "/api/agents/default", "/api/agents/"+b.ID
	for _, config := range []struct{ base, token string }{{aBase, fakeToken}, {bBase, secondToken}} {
		d.must(t, http.MethodPut, config.base+"/channels/telegram", map[string]any{"token": config.token, "allowedUsers": []int64{42}}, nil)
	}
	// When both bots get the same remote peer and update ID, each input reaches its own main.
	for run := range 2 {
		text := fmt.Sprintf("message %d", run)
		first.message(int64(run+1), 42, 7, "A "+text)
		second.message(int64(run+1), 42, 7, "B "+text)
		for _, check := range []struct {
			bot               *fakeTelegram
			base, want, other string
		}{
			{first, aBase, "echo: A " + text, "B " + text},
			{second, bBase, "shout: B " + text, "A " + text},
		} {
			if sent := check.bot.next(t); sent.Text != check.want {
				t.Fatalf("%s reply=%+v want=%s", check.base, sent, check.want)
			}
			var main chatInfo
			d.must(t, http.MethodGet, check.base+"/chats/main", nil, &main)
			snapshot := d.watchPath(t, fmt.Sprintf("%s/chats/%d/events", check.base, main.ID))
			snapshot.until(t, "bot input in owning main", answered(check.want))
			raw, err := json.Marshal(snapshot.tr.items)
			snapshot.cancel()
			if err != nil || strings.Contains(string(raw), check.other) {
				t.Fatalf("crossed timeline=%s err=%v", raw, err)
			}
		}
		if run == 0 {
			// Then the same credential cannot start a second poller, and the conflict names its owner.
			status, body := d.call(t, http.MethodPut, bBase+"/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}})
			if status != http.StatusConflict || !strings.Contains(body, "Dex") {
				t.Fatalf("credential conflict=%d %s", status, body)
			}
			d.stop()
			d = startDex(t, dir, boundary.URL)
		}
	}
	// Global settings expose no channel state and old routes have no aliases.
	var global map[string]any
	d.must(t, http.MethodGet, "/api/settings", nil, &global)
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		if _, ok := global[platform]; ok {
			t.Fatalf("global channel=%s", platform)
		}
		d.must(t, http.MethodDelete, "/api/settings/"+platform, nil, nil, http.StatusNotFound)
	}
	t.Logf("artifact: bot 123 -> default/echo; bot 456 -> %s/shout; restart preserved both; duplicate token=409", b.ID)
}
