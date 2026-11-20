// Package github converts GitHub deliveries and REST records into Task events.
package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/worktask"
)

const maxPayload = 2 << 20

var (
	// ErrSignature means the Webhook does not have a valid SHA-256 signature.
	ErrSignature = errors.New("invalid GitHub webhook signature")
	// ErrPayload means the source record is invalid or is from a different repository.
	ErrPayload = errors.New("invalid GitHub review payload")
)

type review struct {
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

// PullRequest returns the object to link when a Task starts tracking a PR.
func PullRequest(number int64) worktask.Object {
	return worktask.Object{Type: "pull_request", Key: strconv.FormatInt(number, 10)}
}

// AcceptWebhook verifies a submitted review and commits it before returning its receipt.
// A valid but unsupported delivery returns no receipt.
func AcceptWebhook(ctx context.Context, store *worktask.Service, repositoryID int64, secret []byte, delivery, kind, signature string, body []byte) (*worktask.Receipt, error) {
	if len(body) > maxPayload || repositoryID <= 0 || delivery == "" {
		return nil, ErrPayload
	}
	if !validSignature(secret, signature, body) {
		return nil, ErrSignature
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
	return acceptReview(ctx, store, repositoryID, payload.PullRequest.Number, "webhook:"+delivery, payload.Review)
}

func validSignature(secret []byte, signature string, body []byte) bool {
	encoded, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(secret) == 0 || len(encoded) != 2*sha256.Size {
		return false
	}
	digest, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(digest, mac.Sum(nil))
}

// AcceptReviewPage stores a page from the authenticated List Reviews API for the given PR.
// A caller can advance its cursor only after this returns successfully. A retry is safe after a partial write.
func AcceptReviewPage(ctx context.Context, store *worktask.Service, repositoryID, pullNumber int64, body []byte) ([]worktask.Receipt, error) {
	if len(body) > maxPayload || repositoryID <= 0 || pullNumber <= 0 {
		return nil, ErrPayload
	}
	var reviews []review
	if err := json.Unmarshal(body, &reviews); err != nil {
		return nil, errors.Join(ErrPayload, err)
	}
	var receipts []worktask.Receipt
	for _, item := range reviews {
		receipt, err := acceptReview(ctx, store, repositoryID, pullNumber, "pull:"+reviewKey(item.ID), item)
		if err != nil {
			return nil, err
		}
		if receipt != nil {
			receipts = append(receipts, *receipt)
		}
	}
	return receipts, nil
}

func reviewKey(id int64) string { return "review:" + strconv.FormatInt(id, 10) }

func acceptReview(ctx context.Context, store *worktask.Service, repositoryID, pullNumber int64, receiptKey string, item review) (*worktask.Receipt, error) {
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
	result, err := store.Accept(ctx, worktask.Event{
		Namespace: Namespace(repositoryID), ReceiptKey: receiptKey, ChangeKey: reviewKey(item.ID), Type: "review.submitted",
		Objects: []worktask.Object{PullRequest(pullNumber)}, Revision: item.Commit,
		Content: string(chars[:min(len(chars), 32000)]), SourceURL: item.URL, OccurredAt: item.SubmittedAt.UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
