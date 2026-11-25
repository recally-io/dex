package chat

import (
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserChannelSteerCannotBorrowWebOwnerToolAccess(t *testing.T) {
	// Given a live web input on Main.
	r := startNative(t, "echo")
	id, caller := taskParent(t, r)
	caller.RunInputIDs = []string{caller.SourceRequestID}
	if got, err := r.service.ChannelToolCaller(t.Context(), caller); err != nil || got != id {
		t.Fatalf("web input: session=%d error=%v", got, err)
	}
	// When a paired channel adds input to that run, then the entire run loses access.
	for _, origin := range []string{"telegram:7", "feishu:ou_1", "wechat:wx"} {
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
			ID: origin, ConversationID: id, Content: "manage channels", Attachments: "null", Origin: origin, Kind: "message", CreatedAt: 2,
		}); err != nil {
			t.Fatal(err)
		}
		caller.RunInputIDs = []string{caller.SourceRequestID, origin}
		if _, err := r.service.ChannelToolCaller(t.Context(), caller); !errors.Is(err, errChannelToolCaller) {
			t.Errorf("mixed web and %s input: %v", origin, err)
		}
	}
}
