package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserSeesOneReviewFromWebhookAndPull(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	store := worktask.New(conn)
	task, err := store.Create(ctx, "default", "Review the change")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkObject(ctx, "default", task.ID, Namespace(10), PullRequest(2)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWait(ctx, "default", task.ID, worktask.Wait{
		Namespace: Namespace(10), Type: "review.submitted", Object: PullRequest(2), Revision: "abc",
		AfterAt: time.Date(2026, 11, 21, 2, 59, 0, 0, time.UTC).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	const review = `{"id":71,"state":"approved","body":"Looks good.","commit_id":"abc","submitted_at":"2026-11-21T03:00:00Z","html_url":"https://github.com/org/repo/pull/2#pullrequestreview-71"}`
	body := []byte(`{"action":"submitted","repository":{"id":10},"pull_request":{"number":2},"review":` + review + `}`)
	secret := []byte("fixture-only-key")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if _, err := AcceptWebhook(ctx, store, 10, secret, "delivery-1", "pull_request_review", "sha256=00", body, ""); !errors.Is(err, ErrSignature) {
		t.Fatalf("unsigned event: %v", err)
	}
	if _, err := AcceptWebhook(ctx, store, 11, secret, "delivery-1", "pull_request_review", signature, body, ""); !errors.Is(err, ErrPayload) {
		t.Fatalf("wrong repository: %v", err)
	}
	first, err := AcceptWebhook(ctx, store, 10, secret, "delivery-1", "pull_request_review", signature, body, "")
	if err != nil || first == nil || first.TaskID != task.ID || first.Duplicate || first.ResolvedWaits != 1 {
		t.Fatalf("Webhook: %+v, %v", first, err)
	}
	const pending = `{"id":72,"state":"PENDING"}`
	pulled, err := AcceptReviewPage(ctx, store, 10, 2, []byte("["+pending+","+strings.Replace(review, `"approved"`, `"APPROVED"`, 1)+"]"))
	if err != nil || len(pulled) != 1 || pulled[0].EventID != first.EventID || !pulled[0].Duplicate {
		t.Fatalf("Pull: %+v, %v", pulled, err)
	}
	detail, err := store.Detail(ctx, "default", task.ID)
	if err != nil || len(detail.Entries) != 1 || detail.Entries[0].Content != "GitHub review: approved\n\nLooks good." {
		t.Fatalf("timeline: %+v, %v", detail.Entries, err)
	}
}
