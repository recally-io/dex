package chat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const approve = "Archive Coder"

// archiveFixture is a caller bot with a Main input from a person, and a second bot "coder".
func archiveFixture(t *testing.T) (*nativeTest, int64, engine.TaskContext) {
	t.Helper()
	r := startNative(t, "echo")
	if err := r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "coder", Name: "Coder", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "default", Name: "Dex", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	chatID, source := taskParent(t, r)
	return r, chatID, source
}

func archivedAt(t *testing.T, r *nativeTest, id string) bool {
	t.Helper()
	row, err := r.q.GetAgent(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.ArchivedAt.Valid
}

func askArchive(t *testing.T, r *nativeTest, source engine.TaskContext) engine.QuestionInfo {
	t.Helper()
	q, err := r.service.ToolAskArchive(t.Context(), source, "coder", "Coder", "It has no work left")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// answer sends an answer through the receipt path with the given origin.
func answer(t *testing.T, r *nativeTest, questionID, origin, text string) error {
	t.Helper()
	_, err := r.service.enqueue(t.Context(), "bot", MainKey, "", origin, Message{ID: "answer-" + origin + "-" + text, Content: text, AnswersQuestion: questionID})
	return err
}

func TestOwnerApprovalArchivesBotBeforeTheModelReadsTheAnswer(t *testing.T) {
	// Given a question that asks to archive a bot, and the bot is still active.
	r, _, source := archiveFixture(t)
	q := askArchive(t, r, source)
	if len(q.Options) != 2 || q.Options[0] != approve || q.Options[1] != "Cancel" || archivedAt(t, r, "coder") {
		t.Fatalf("question %+v, archived=%v", q, archivedAt(t, r, "coder"))
	}

	// When the owner picks the approve option in the web card.
	if err := answer(t, r, q.ID, "web", approve); err != nil {
		t.Fatal(err)
	}

	// Then the bot is archived, and the input that the model reads names the outcome.
	if !archivedAt(t, r, "coder") {
		t.Fatal("the bot is not archived")
	}
	row, err := r.q.GetQuestion(t.Context(), q.ID)
	if err != nil || row.Outcome.String != `dex: Approved. Bot "Coder" is archived.` {
		t.Fatalf("question %+v %v", row, err)
	}
	sub, err := r.q.GetSubmission(t.Context(), row.AnswerSubmissionID.String)
	if err != nil || !strings.HasSuffix(sub.Content, "\n\n"+row.Outcome.String) || !strings.Contains(sub.Content, "\n"+approve+"\n") {
		t.Fatalf("content %q %v", sub.Content, err)
	}
	// The card shows the owner's words without the outcome line.
	view, err := r.service.questionView(t.Context(), row)
	if err != nil || view.Answer != approve {
		t.Fatalf("view %+v %v", view, err)
	}
}

func TestOwnerAnswersThatDoNotApproveLeaveTheBotActive(t *testing.T) {
	for _, c := range []struct{ name, origin, text string }{
		{"cancel option", "web", "Cancel"},
		{"different case", "web", "archive coder"},
		{"extra words", "web", "Archive Coder please"},
		{"trailing space", "web", approve + " "},
		{"task origin", "task", approve},
		{"automation origin", "automation", approve},
		{"dream origin", "dream", approve},
		{"unknown origin", "unknown", approve},
		{"empty origin", "", approve},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Given an open question that asks to archive a bot.
			r, _, source := archiveFixture(t)
			q := askArchive(t, r, source)

			// When the answer does not equal the approve option, or it is not from a person.
			if err := answer(t, r, q.ID, c.origin, c.text); err != nil {
				t.Fatal(err)
			}

			// Then the bot stays active, and the model reads "not approved".
			if archivedAt(t, r, "coder") {
				t.Fatal("the bot was archived")
			}
			row, err := r.q.GetQuestion(t.Context(), q.ID)
			if err != nil || row.Outcome.String != `dex: Not approved. Bot "Coder" is not archived.` {
				t.Fatalf("question %+v %v", row, err)
			}
		})
	}
}

func TestPlainQuestionWithTheSameWordsArchivesNothing(t *testing.T) {
	// Given ask_user with the text and options of an archive question.
	r, _, source := archiveFixture(t)
	q, err := r.service.ToolAskUser(t.Context(), source, engine.AskUserArgs{Question: `Archive bot "Coder"?`, Options: []string{approve, "Cancel"}})
	if err != nil {
		t.Fatal(err)
	}

	// When the owner picks "Archive Coder", then the answer is plain text and control acts on nothing.
	if err := answer(t, r, q.ID, "web", approve); err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetQuestion(t.Context(), q.ID)
	if err != nil || row.Action.Valid || row.Outcome.Valid || archivedAt(t, r, "coder") {
		t.Fatalf("question %+v %v archived=%v", row, err, archivedAt(t, r, "coder"))
	}
}

func TestBotArchiveToolReplayNeverArchives(t *testing.T) {
	// Given a saved archive question.
	r, _, source := archiveFixture(t)
	first := askArchive(t, r, source)

	// When PiG replays the call, also with another bot, then it gets the same question and nothing is archived.
	replay, err := r.service.ToolAskArchive(t.Context(), source, "default", "Dex", "another reason")
	if err != nil || replay.ID != first.ID || replay.Text != first.Text {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if archivedAt(t, r, "coder") || archivedAt(t, r, "default") {
		t.Fatal("a replay archived a bot")
	}

	// When the owner approves, then a later replay still changes nothing more.
	if err := answer(t, r, first.ID, "web", approve); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ToolAskArchive(t.Context(), source, "coder", "Coder", "again"); err != nil {
		t.Fatalf("replay after approval: %v", err)
	}
	var questions int
	if err := r.conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM questions").Scan(&questions); err != nil || questions != 1 {
		t.Fatalf("questions %d %v", questions, err)
	}
	if archivedAt(t, r, "default") {
		t.Fatal("the default bot is archived")
	}
}

func TestBotArchiveRefusesDefaultSelfAndBusyBots(t *testing.T) {
	r, _, source := archiveFixture(t)
	ask := func(key, id string) error {
		source.Key = key
		_, err := r.service.ToolAskArchive(t.Context(), source, id, id, "reason")
		return err
	}

	// Given the default bot, the caller itself, and a missing bot, then the tool refuses to ask.
	if err := ask("bot/default", "default"); !errors.Is(err, db.ErrDefaultAgent) {
		t.Fatalf("default: %v", err)
	}
	if err := ask("bot/self", "bot"); !errors.Is(err, ErrQuestionInvalid) {
		t.Fatalf("self: %v", err)
	}
	if err := ask("bot/missing", "nobody"); !errors.Is(err, db.ErrAgentMissing) {
		t.Fatalf("missing: %v", err)
	}

	// Given a bot with a pending input in any conversation, then the tool refuses to ask.
	busyConversation, err := r.q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "coder", ChannelKey: "chat:busy", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "busy-input", ConversationID: busyConversation.ID, Content: "work", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ask("bot/busy", "coder"); !errors.Is(err, db.ErrAgentBusy) {
		t.Fatalf("busy: %v", err)
	}
	if archivedAt(t, r, "coder") {
		t.Fatal("a busy bot was archived")
	}
}

func TestBotBecomingBusyAfterTheQuestionIsNotArchivedOnApproval(t *testing.T) {
	// Given an open question for an idle bot.
	r, _, source := archiveFixture(t)
	q := askArchive(t, r, source)

	// When the bot gets a running input before the owner approves.
	busyConversation, err := r.q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "coder", ChannelKey: "chat:busy", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "busy-input", ConversationID: busyConversation.ID, Content: "work", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := answer(t, r, q.ID, "web", approve); err != nil {
		t.Fatal(err)
	}

	// Then the answer is stored, the bot stays active, and the model reads why.
	row, err := r.q.GetQuestion(t.Context(), q.ID)
	want := `dex: Approved, but bot "Coder" is not archived: ` + db.ErrAgentBusy.Error() + "."
	if err != nil || row.Outcome.String != want || archivedAt(t, r, "coder") {
		t.Fatalf("outcome %q err=%v archived=%v", row.Outcome.String, err, archivedAt(t, r, "coder"))
	}
}

func TestDecidedActionAnswerCannotBeWithdrawnOrReopened(t *testing.T) {
	// Given an approved answer that archived a bot.
	r, chatID, source := archiveFixture(t)
	q := askArchive(t, r, source)
	if err := answer(t, r, q.ID, "web", approve); err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}

	// When the answer is withdrawn, then Withdraw refuses, because the decision ran.
	if _, err := r.service.Withdraw(t.Context(), "bot", chatID, row.AnswerSubmissionID.String); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("withdraw: %v", err)
	}

	// When a failed run reopens its question, then a decided action question closes instead of opening again.
	if err := reopenQuestion(t.Context(), r.q, row.AnswerSubmissionID.String); err != nil {
		t.Fatal(err)
	}
	row, err = r.q.GetQuestion(t.Context(), q.ID)
	if err != nil || row.Status != "consumed" {
		t.Fatalf("status %q %v", row.Status, err)
	}
}

func TestBotToolCallerNeedsAPersonInMainOrAChat(t *testing.T) {
	r := startNative(t, "echo")
	type conversation struct {
		key, origin string
		change      bool
		want        bool
	}
	var next int64 = 1000
	for _, c := range []conversation{
		{"main", "web", true, true},
		{"chat:one", "telegram:7", true, true},
		{"chat:two", "feishu:ou_1", true, true},
		{"chat:three", "wechat:wx", true, true},
		{"main", "task", true, false},
		{"main", "automation", true, false},
		{"main", "dream", true, false},
		{"main", "unknown", true, false},
		{"task:child", "web", true, false},
		{"automation:run", "automation", true, false},
		{"dream:one", "dream", true, false},
		// Reading is allowed in Main, a Chat, and an automation session.
		{"main", "task", false, true},
		{"automation:run", "automation", false, true},
		{"task:child", "task", false, false},
		{"dream:one", "dream", false, false},
	} {
		next++
		t.Run(fmt.Sprintf("%s %s change=%v", c.key, c.origin, c.change), func(t *testing.T) {
			conv, err := r.q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "bot", ChannelKey: c.key, CreatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.conn.ExecContext(t.Context(), "UPDATE conversations SET execution_id = ? WHERE id = ?", next, conv.ID); err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("input-%d", next)
			if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: id, ConversationID: conv.ID, Content: "x", Attachments: "null", Origin: c.origin, Kind: "message", CreatedAt: 1}); err != nil {
				t.Fatal(err)
			}
			err = r.service.ToolBotCaller(t.Context(), engine.TaskContext{AgentID: "bot", ConversationID: next, SourceRequestID: id}, c.change)
			if (err == nil) != c.want || (err != nil && !errors.Is(err, ErrBotCaller)) {
				t.Fatalf("err=%v want allowed=%v", err, c.want)
			}
		})
	}
}
