package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/worktask"
)

func acceptIssue(ctx context.Context, store *worktask.Service, repositoryID int64, receiptKey string, body []byte, rule Rule) (*worktask.Receipt, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Issue struct {
			author
			ID        int64     `json:"id"`
			Number    int64     `json:"number"`
			Title     string    `json:"title"`
			Body      string    `json:"body"`
			URL       string    `json:"html_url"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Join(ErrPayload, err)
	}
	if payload.Repository.ID != repositoryID {
		return nil, ErrPayload
	}
	if payload.Action != "opened" {
		return nil, nil
	}
	issue := payload.Issue
	if issue.ID <= 0 || issue.Number <= 0 || issue.CreatedAt.UnixMilli() <= 0 || strings.TrimSpace(issue.Title) == "" {
		return nil, ErrPayload
	}
	number := strconv.FormatInt(issue.Number, 10)
	content := []rune(strings.TrimSpace(issue.Title + "\n\n" + issue.Body))
	event := worktask.Event{
		Namespace: Namespace(repositoryID), ReceiptKey: receiptKey, ChangeKey: "issue.opened:" + strconv.FormatInt(issue.ID, 10), Type: "issue.opened",
		Objects: []worktask.Object{{Type: "issue", Key: number}}, OccurredAt: issue.CreatedAt.UnixMilli(),
		Content: string(content[:min(len(content), 32000)]), SourceURL: issue.URL,
		AuthorLogin: issue.User.Login, AuthorAssociation: issue.Association, RecordOnly: !issue.trusted(), HoldUnmatched: rule.HoldUnmatched,
	}
	var receipt worktask.Receipt
	var err error
	if rule.IssueAgentID == "" {
		receipt, err = store.Accept(ctx, event)
	} else {
		receipt, err = store.AcceptCreating(ctx, event, rule.IssueAgentID, "GitHub issue #"+number)
	}
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}
