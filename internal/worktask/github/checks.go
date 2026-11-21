package github

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/worktask"
)

// CheckSuiteCompleted is the event type of a finished CI check suite.
const CheckSuiteCompleted = "check_suite.completed"

// acceptCheckSuite adds a finished check suite to the timeline of each linked PR. CI is record-only:
// it cannot create a Task, resolve a wait, alert Main, or be held as unmatched.
// GitHub sends no PR for a branch in a fork, so Dex cannot route those suites.
func acceptCheckSuite(ctx context.Context, store *worktask.Service, repositoryID int64, receiptKey string, body []byte) (*worktask.Receipt, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		} `json:"repository"`
		CheckSuite struct {
			ID         int64     `json:"id"`
			HeadSHA    string    `json:"head_sha"`
			Conclusion string    `json:"conclusion"`
			UpdatedAt  time.Time `json:"updated_at"`
			App        struct {
				Slug string `json:"slug"`
			} `json:"app"`
			PullRequests []struct {
				Number int64 `json:"number"`
				Base   struct {
					Repo struct {
						ID int64 `json:"id"`
					} `json:"repo"`
				} `json:"base"`
			} `json:"pull_requests"`
		} `json:"check_suite"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Join(ErrPayload, err)
	}
	if payload.Repository.ID != repositoryID {
		return nil, ErrPayload
	}
	if payload.Action != "completed" {
		return nil, nil
	}
	suite := payload.CheckSuite
	if suite.ID <= 0 || suite.UpdatedAt.UnixMilli() <= 0 {
		return nil, ErrPayload
	}
	var objects []worktask.Object
	for _, pull := range suite.PullRequests {
		// A suite can list a PR in another repository that uses the same commit.
		if pull.Number > 0 && pull.Base.Repo.ID == repositoryID && len(objects) < 16 {
			objects = append(objects, PullRequest(pull.Number))
		}
	}
	if len(objects) == 0 {
		return nil, nil
	}
	url := ""
	if strings.HasPrefix(payload.Repository.HTMLURL, "https://") && suite.HeadSHA != "" {
		url = payload.Repository.HTMLURL + "/commit/" + suite.HeadSHA + "/checks"
	}
	// A rerun completes the same suite again, so the completion time is part of the change key.
	change := "check_suite:" + strconv.FormatInt(suite.ID, 10) + ":" + strconv.FormatInt(suite.UpdatedAt.UnixMilli(), 10)
	receipt, err := store.Accept(ctx, worktask.Event{
		Namespace: Namespace(repositoryID), ReceiptKey: receiptKey, ChangeKey: change, Type: CheckSuiteCompleted,
		Objects: objects, Revision: suite.HeadSHA, Content: "GitHub checks: " + cmp.Or(suite.App.Slug, "unknown app") + " " + cmp.Or(suite.Conclusion, "no conclusion"),
		SourceURL: url, OccurredAt: suite.UpdatedAt.UnixMilli(), AuthorLogin: suite.App.Slug, RecordOnly: true,
	})
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}
