package app_test

import (
	"net/http"
	"testing"

	"github.com/recally-io/dex/internal/channel/pairing"
)

func TestUserCanSelectOnlyTheirAuthorizedChannelTarget(t *testing.T) {
	t.Parallel()
	// Given two bots, only one with a paired Feishu owner.
	d := startDex(t, t.TempDir(), "")
	bot := d.createCoder(t)
	if _, err := sessionDB(t, d).ExecContext(t.Context(), `INSERT INTO feishu_settings (bot_id, app_id, app_secret, brand, owner_id) VALUES (?,'app','test-secret','feishu','owner')`, bot.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/bots/" + bot.ID + "/notification-targets"
	// When the user lists eligible targets and selects one as primary.
	var list pairing.NotificationTargets
	d.must(t, http.MethodGet, path, nil, &list)
	if len(list.Targets) != 1 || list.Targets[0].Peer != "feishu:owner" {
		t.Fatalf("targets=%+v", list)
	}
	id := list.Targets[0].ID
	body := map[string]string{"targetId": id}
	d.must(t, http.MethodPut, path, body, nil)
	// Then another bot cannot use that target and the owner sees it as primary.
	d.must(t, http.MethodPut, "/api/bots/default/notification-targets", body, nil, http.StatusBadRequest)
	d.must(t, http.MethodGet, path, nil, &list)
	if list.PrimaryID != id {
		t.Fatalf("primary=%+v", list)
	}
}
