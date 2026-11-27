package main

import (
	"testing"
)

func TestUserFindsConversationByBotNameAndTitle(t *testing.T) {
	t.Parallel()

	// Given that two lists include the same cross-bot conversation.
	bots := []botListItem{{ID: "ops", Name: "Ops 中文"}, {ID: "docs", Name: "文档 Bot"}}
	lists := map[string][]workspaceSearchSession{
		"ops":  {{chatSession: chatSession{ID: 7, BotID: "docs", Title: "部署记录", ArchivedAt: 1}, UpdatedAt: 20}},
		"docs": {{chatSession: chatSession{ID: 7, Title: "部署记录", ArchivedAt: 1}, UpdatedAt: 20}, {chatSession: chatSession{ID: 8, Title: "产品手册"}, UpdatedAt: 30}},
	}

	// When the user searches using parts of both the Bot name and the title.
	main, conversations := workspaceSearchMatches(bots, lists, "BOT 部署")

	// Then the only match opens once on its owner Bot, including archived sessions.
	if len(main) != 0 || len(conversations) != 1 || conversations[0].Bot != "docs" || conversations[0].ID != 7 ||
		conversations[0].Title != "部署记录" || !conversations[0].Archived {
		t.Fatalf("unexpected matches: main=%+v, conversations=%+v", main, conversations)
	}
	main, conversations = workspaceSearchMatches(bots, lists, "文档")
	if len(main) != 1 || main[0].ID != 0 || main[0].Bot != "docs" || main[0].Title != "文档 Bot" ||
		len(conversations) != 2 || conversations[0].ID != 8 {
		t.Fatalf("unexpected Bot name matches: main=%+v, conversations=%+v", main, conversations)
	}
}
