package channel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"
)

// WebhookRetention covers Telegram's 24-hour update hold and all four Feishu
// retries, whose longest interval is 6 hours. Pending events are not pruned.
const WebhookRetention = 7 * 24 * time.Hour

// Webhook identifies an active HTTP receiver and verifies its platform requests.
// Implementations must not perform platform I/O from Decode.
type Webhook struct {
	Account string
	Decode  func(http.Header, []byte) (WebhookEvent, int)
}

// WebhookEvent contains either an event for durable intake or a URL challenge.
type WebhookEvent struct {
	ID        string
	Payload   []byte
	Challenge string
}

// WebhookInbox drains accepted callbacks and recognizes replays after rollback.
type WebhookInbox interface {
	DrainWebhookInbox(ctx context.Context, platform, account string, continuous bool, receive func(context.Context, []byte) error) error
	HandledWebhook(ctx context.Context, platform, account, eventID string) (bool, error)
}

// MatchWebhookSecret compares fixed-length hashes so the comparison does not
// disclose the expected secret's length.
func MatchWebhookSecret(got, want string) bool {
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
