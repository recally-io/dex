package session

import (
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCanReceiveAnEventOnMultiplePairedChannels(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	conv, _ := taskParent(t, r)
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42,99"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{AgentID: "bot", BotID: "wx", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	// Given one alert with deliveries to three paired destinations.
	for _, target := range []struct{ peer, identity string }{
		{"telegram:42", `{"accountId":"123","senderId":"42"}`},
		{"telegram:99", `{"accountId":"123","senderId":"99"}`},
		{"wechat:owner", `{"accountId":"wx"}`},
	} {
		row := sqlc.InsertOutboxMessageParams{ID: target.peer, EventID: "remind:alert", AgentID: "bot", SessionID: conv, Kind: "proactive", Peer: target.peer, ChannelContext: target.identity, Body: "Alert", Status: "pending"}
		if n, err := r.q.InsertOutboxMessage(ctx, row); err != nil || n != 1 {
			t.Fatalf("enqueue %s: %d, %v", target.peer, n, err)
		}
		row.ID += ":duplicate"
		if n, err := r.q.InsertOutboxMessage(ctx, row); err != nil || n != 0 {
			t.Fatalf("replay %s: %d, %v", target.peer, n, err)
		}
	}
	// When the owner unpairs one peer and later pairs it again.
	for _, users := range []string{"99", "42,99"} {
		if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: users}); err != nil {
			t.Fatal(err)
		}
	}
	// Then cancelled work stays failed. The other destinations stay pending.
	for peer, want := range map[string]string{"telegram:42": "failed", "telegram:99": "pending", "wechat:owner": "pending"} {
		row, err := r.q.GetOutboxMessage(ctx, peer)
		if err != nil || row.Status != want {
			t.Fatalf("%s status = %q, want %q: %v", peer, row.Status, want, err)
		}
	}
}
