package chat

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserGroupQuestionAnswerCannotCrossAudienceOrActor(t *testing.T) {
	// Given a real public Question whose decision belongs to the initiating paired actor.
	r := startNative(t, "spawn")
	groupSettings(t, r, "bot", "123:synthetic", "42,43")
	identity := ChannelContext{AccountID: "123", SenderID: "42", ChatID: -100, TopicID: 7, MessageID: "1"}
	parent, _ := groupInput(t, r, "bot", "group-question", identity, `{"tool":"group_question","question":"Public choice","options":["Red","Blue"]}`)
	question, err := r.q.GetCurrentQuestion(t.Context(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	// A different topic and another paired member cannot bind their answer to that Question.
	other := identity
	other.TopicID = 8
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:8", "cross-topic-answer", Message{Content: "Red", AnswersQuestion: question.ID, QuestionOnly: true, ChannelContext: other}); !errors.Is(err, ErrQuestionClosed) {
		t.Fatalf("answer crossed topic: %v", err)
	}
	other = identity
	other.SenderID = "43"
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "other-actor-answer", Message{Content: "Red", AnswersQuestion: question.ID, QuestionOnly: true, ChannelContext: other}); !errors.Is(err, ErrQuestionClosed) {
		t.Fatalf("answer borrowed another actor: %v", err)
	}
	// The original sender's answer is a normal native input in the same audience.
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:-100:7", "valid-group-answer", Message{Content: "Blue", AnswersQuestion: question.ID, QuestionOnly: true, ChannelContext: identity}); err != nil {
		t.Fatal(err)
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "valid-group-answer", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	answer := awaitReceipt(t, r, id)
	if answer.Status != "done" || answer.ConversationID != parent.ID || !strings.Contains(answer.Answer.String, "Answer to: Public choice\nBlue") {
		t.Fatalf("answer lost its native audience: %+v", answer)
	}
}
