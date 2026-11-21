package settings

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/recally-io/dex/internal/db/sqlc"
)

var sourceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidWebhookSourceID reports whether id can name a Webhook source.
func ValidWebhookSourceID(id string) bool { return sourceID.MatchString(id) }

// SaveWebhookSource replaces the write-only secret and the rule of one signed Webhook source.
// taskAgentID creates Tasks for unmatched events. holdUnmatched keeps them for the owner instead.
func (s *Service) SaveWebhookSource(ctx context.Context, id, secret, taskAgentID string, holdUnmatched bool) error {
	if !ValidWebhookSourceID(id) || len(secret) < 32 || len(secret) > 256 {
		return fmt.Errorf("%w: use a source ID of lowercase letters, digits, and dashes, and a secret of 32 to 256 bytes", ErrInvalid)
	}
	hold := int64(0)
	if holdUnmatched {
		hold = 1
	}
	n, err := s.q.SaveWebhookSource(ctx, sqlc.SaveWebhookSourceParams{ID: id, Secret: secret, TaskAgentID: sql.NullString{String: taskAgentID, Valid: taskAgentID != ""}, HoldUnmatched: hold})
	if err == nil && n == 0 {
		return fmt.Errorf("%w: select an active agent for new Tasks", ErrInvalid)
	}
	return err
}

// WebhookSource reads the configuration of a source for delivery verification only.
func (s *Service) WebhookSource(ctx context.Context, id string) (sqlc.WebhookSource, error) {
	return s.q.GetWebhookSource(ctx, id)
}

// DeleteWebhookSource removes the secret and disables new deliveries. Recorded events stay.
func (s *Service) DeleteWebhookSource(ctx context.Context, id string) error {
	if !ValidWebhookSourceID(id) {
		return ErrInvalid
	}
	return s.q.DeleteWebhookSource(ctx, id)
}
