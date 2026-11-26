package session

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// TimelineNotification is a read-only projection, not a delivery receipt.
type TimelineNotification struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

type NotificationPage struct {
	Notifications          []TimelineNotification `json:"notifications"`
	NextBeforeNotification string                 `json:"nextBeforeNotification,omitempty"`
	Position               int64                  `json:"-"`
}

// TimelineNotifications reads newest first. A positive limit reads a page; a
// negative limit drains changes after the stream's last read position.
func (s *Service) TimelineNotifications(ctx context.Context, botID string, sessionID int64, before string, after int64, limit int) (NotificationPage, error) {
	size := limit
	if limit > 0 {
		size++
	}
	rows, err := s.q.ListTimelineNotifications(ctx, sqlc.ListTimelineNotificationsParams{
		BotID: botID, SessionID: sessionID, BeforeID: before, AfterPosition: after, PageSize: int64(size),
	})
	if err != nil {
		return NotificationPage{}, err
	}
	page := NotificationPage{Position: after}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		page.NextBeforeNotification = rows[limit-1].ID
	}
	if len(rows) > 0 {
		page.Position = rows[0].Position
	}
	page.Notifications = make([]TimelineNotification, len(rows))
	for i, row := range rows {
		page.Notifications[i] = TimelineNotification{ID: row.ID, Text: row.Body, At: row.CreatedAt}
	}
	return page, nil
}
