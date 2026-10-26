package automation

import (
	"context"
	"database/sql"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// InboxItem is a settled, non-duplicate result that needs the owner's attention.
type InboxItem struct {
	RunID          string `json:"runId"`
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	AutomationID   string `json:"automationId"`
	AutomationName string `json:"automationName"`
	ChatID         int64  `json:"chatId"`
	Outcome        string `json:"outcome"`
	Summary        string `json:"summary"`
	CreatedAt      int64  `json:"createdAt"`
	Read           bool   `json:"read"`
	Dismissed      bool   `json:"dismissed"`
}

// Inbox reads all bots in one query. Dismissed results are optional for direct links.
func (s *Service) Inbox(ctx context.Context, includeDismissed bool) ([]InboxItem, error) {
	rows, err := s.q.ListInbox(ctx, includeDismissed)
	if err != nil {
		return nil, err
	}
	items := make([]InboxItem, len(rows))
	for i, r := range rows {
		items[i] = InboxItem{RunID: r.RunID, AgentID: r.AgentID, AgentName: r.AgentName, AutomationID: r.AutomationID, AutomationName: r.AutomationName, ChatID: r.ChatID, Outcome: r.Outcome, Summary: r.Summary, CreatedAt: r.CreatedAt, Read: r.ReadAt.Valid, Dismissed: r.DismissedAt.Valid}
	}
	return items, nil
}

// ReadInboxItem keeps the first read time on repeated requests.
func (s *Service) ReadInboxItem(ctx context.Context, id string) error {
	n, err := s.q.ReadInboxItem(ctx, sqlc.ReadInboxItemParams{ID: id, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissInboxItem keeps the first dismissal time on repeated requests.
func (s *Service) DismissInboxItem(ctx context.Context, id string) error {
	n, err := s.q.DismissInboxItem(ctx, sqlc.DismissInboxItemParams{ID: id, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
