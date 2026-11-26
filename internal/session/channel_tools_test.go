package session

import (
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCanManageChannelsFromWebAndPairedPrivateSession(t *testing.T) {
	// Given a live web input on Main.
	r := startNative(t, "echo")
	id, caller := taskParent(t, r)
	caller.RunInputIDs = []string{caller.SourceRequestID}
	if got, err := r.service.ChannelToolCaller(t.Context(), caller); err != nil || got != id {
		t.Fatalf("web input: session=%d error=%v", got, err)
	}
	// When the owner uses a private chat, alone or in a shared run, the tools stay available.
	for _, origin := range []string{"telegram:7", "feishu:ou_1", "wechat:wx"} {
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
			ID: origin, SessionID: id, Content: "manage channels", Attachments: "null", Origin: origin, Producer: ProducerUser, Kind: "message", CreatedAt: 2,
		}); err != nil {
			t.Fatal(err)
		}
		caller.RunInputIDs = []string{caller.SourceRequestID, origin}
		if got, err := r.service.ChannelToolCaller(t.Context(), caller); err != nil || got != id {
			t.Errorf("mixed web and %s input: session=%d error=%v", origin, got, err)
		}
		private := caller
		private.SourceRequestID = origin
		private.RunInputIDs = []string{origin}
		if got, err := r.service.ChannelToolCaller(t.Context(), private); err != nil || got != id {
			t.Errorf("private %s input: session=%d error=%v", origin, got, err)
		}
	}
}
