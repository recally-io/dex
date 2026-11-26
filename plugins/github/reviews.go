// Package github converts GitHub deliveries and REST records into Task events.
package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
)

// MaxPayload is the maximum size of a webhook delivery or review page.
const MaxPayload = 2 << 20

var (
	// ErrSignature means the Webhook does not have a valid SHA-256 signature.
	ErrSignature = errors.New("invalid GitHub webhook signature")
	// ErrPayload means the source record is invalid or is from a different repository.
	ErrPayload = errors.New("invalid GitHub review payload")
)

type author struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Association string `json:"author_association"`
}

func (a author) trusted() bool {
	return a.User.Login != "" && (a.Association == "OWNER" || a.Association == "MEMBER" || a.Association == "COLLABORATOR")
}

type review struct {
	author
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	Commit      string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	URL         string    `json:"html_url"`
}

type reviewWebhook struct {
	Action     string `json:"action"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	PullRequest struct {
		Number int64 `json:"number"`
	} `json:"pull_request"`
	Review review `json:"review"`
}

// Namespace uses the repository ID so a rename does not break Task links.
func Namespace(repositoryID int64) string {
	return "github.com/repository/" + strconv.FormatInt(repositoryID, 10)
}

const ReviewSubmitted = "review.submitted"

// PullCoordinates decodes a linked GitHub PR's stable identifiers.
func PullCoordinates(namespace string, object tasksvc.Object) (repositoryID, pullNumber int64, ok bool) {
	if object.Type != "pull_request" {
		return 0, 0, false
	}
	repository, ok := strings.CutPrefix(namespace, "github.com/repository/")
	if !ok {
		return 0, 0, false
	}
	repositoryID, repoErr := strconv.ParseInt(repository, 10, 64)
	pullNumber, pullErr := strconv.ParseInt(object.Key, 10, 64)
	return repositoryID, pullNumber, repoErr == nil && repositoryID > 0 && pullErr == nil && pullNumber > 0
}

// PullRequest returns the object to link when a Task starts tracking a PR.
func PullRequest(number int64) tasksvc.Object {
	return tasksvc.Object{Type: "pull_request", Key: strconv.FormatInt(number, 10)}
}

// Rule is the source rule of one repository. The Webhook and Pull use the same rule.
type Rule struct {
	// IssueBotID creates a Task under this bot for an opened issue.
	IssueBotID string
	// HoldUnmatched keeps an event that matches no Task for the owner.
	HoldUnmatched bool
}

// AcceptWebhook verifies a delivery and commits supported events before returning its receipt.
// A valid but unsupported delivery returns no receipt.
func AcceptWebhook(ctx context.Context, store *tasksvc.Service, repositoryID int64, secret []byte, delivery, kind, signature string, body []byte, rule Rule) (*tasksvc.Receipt, error) {
	if len(body) > MaxPayload || repositoryID <= 0 || delivery == "" {
		return nil, ErrPayload
	}
	if !plugin.ValidSignature(secret, signature, body) {
		return nil, ErrSignature
	}
	if kind == "issues" && (rule.IssueBotID != "" || rule.HoldUnmatched) {
		return acceptIssue(ctx, store, repositoryID, "webhook:"+delivery, body, rule)
	}
	if kind == "check_suite" {
		return acceptCheckSuite(ctx, store, repositoryID, "webhook:"+delivery, body)
	}
	if kind != "pull_request_review" {
		return nil, nil
	}
	var payload reviewWebhook
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Join(ErrPayload, err)
	}
	if payload.Repository.ID != repositoryID {
		return nil, ErrPayload
	}
	if payload.Action != "submitted" {
		return nil, nil
	}
	return acceptReview(ctx, store, repositoryID, payload.PullRequest.Number, "webhook:"+delivery, payload.Review, rule.HoldUnmatched)
}

// AcceptReviewPage stores a page from the authenticated List Reviews API for the given PR.
// A caller can advance its cursor only after this returns successfully. A retry is safe after a partial write.
func AcceptReviewPage(ctx context.Context, store *tasksvc.Service, repositoryID, pullNumber int64, body []byte, rule Rule) ([]tasksvc.Receipt, error) {
	receipts, _, err := acceptReviewPage(ctx, store, repositoryID, pullNumber, body, rule)
	return receipts, err
}

func acceptReviewPage(ctx context.Context, store *tasksvc.Service, repositoryID, pullNumber int64, body []byte, rule Rule) ([]tasksvc.Receipt, int, error) {
	if len(body) > MaxPayload || repositoryID <= 0 || pullNumber <= 0 {
		return nil, 0, ErrPayload
	}
	var reviews []review
	if err := json.Unmarshal(body, &reviews); err != nil {
		return nil, 0, errors.Join(ErrPayload, err)
	}
	if reviews == nil {
		return nil, 0, ErrPayload
	}
	var receipts []tasksvc.Receipt
	for _, item := range reviews {
		receipt, err := acceptReview(ctx, store, repositoryID, pullNumber, "pull:"+reviewKey(item.ID), item, rule.HoldUnmatched)
		if err != nil {
			return nil, 0, err
		}
		if receipt != nil {
			receipts = append(receipts, *receipt)
		}
	}
	return receipts, len(reviews), nil
}

func reviewKey(id int64) string { return "review:" + strconv.FormatInt(id, 10) }

func acceptReview(ctx context.Context, store *tasksvc.Service, repositoryID, pullNumber int64, receiptKey string, item review, hold bool) (*tasksvc.Receipt, error) {
	state := strings.ToLower(item.State)
	switch state {
	case "approved", "changes_requested", "commented":
	default:
		// Pull can see pending or dismissed reviews. Neither is a submitted review event.
		return nil, nil
	}
	if item.ID <= 0 || pullNumber <= 0 || item.SubmittedAt.UnixMilli() <= 0 {
		return nil, ErrPayload
	}
	content := "GitHub review: " + state
	if item.Body != "" {
		content += "\n\n" + item.Body
	}
	chars := []rune(content)
	result, err := store.Accept(ctx, tasksvc.Event{
		Namespace: Namespace(repositoryID), ReceiptKey: receiptKey, ChangeKey: reviewKey(item.ID), Type: ReviewSubmitted,
		Objects: []tasksvc.Object{PullRequest(pullNumber)}, Revision: item.Commit,
		Content: string(chars[:min(len(chars), 32000)]), SourceURL: item.URL, OccurredAt: item.SubmittedAt.UnixMilli(),
		AuthorLogin: item.User.Login, AuthorAssociation: item.Association, RecordOnly: !item.trusted(), HoldUnmatched: hold,
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
