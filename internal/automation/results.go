package automation

import (
	"context"
	"database/sql"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Result is a settled result that needs the owner's attention.
type Result struct {
	RunID          string `json:"runId"`
	BotID          string `json:"botId"`
	BotName        string `json:"botName"`
	AutomationID   string `json:"automationId"`
	AutomationName string `json:"automationName"`
	SessionID      int64  `json:"sessionId"`
	Outcome        string `json:"outcome"`
	Summary        string `json:"summary"`
	CreatedAt      int64  `json:"createdAt"`
	Read           bool   `json:"read"`
	Dismissed      bool   `json:"dismissed"`
	// RunnerID and Cwd are where the run started.
	RunnerID string `json:"runnerId"`
	Cwd      string `json:"cwd"`
}

// Results reads all bots in one query. Dismissed results are optional for direct links.
func (s *Service) Results(ctx context.Context, includeDismissed bool) ([]Result, error) {
	rows, err := s.q.ListAutomationResults(ctx, includeDismissed)
	if err != nil {
		return nil, err
	}
	items := make([]Result, len(rows))
	for i, r := range rows {
		items[i] = Result{RunID: r.RunID, BotID: r.BotID, BotName: r.BotName, AutomationID: r.AutomationID, AutomationName: r.AutomationName, SessionID: r.SessionID, Outcome: r.Outcome, Summary: r.Summary, CreatedAt: r.CreatedAt, Read: r.ReadAt.Valid, Dismissed: r.DismissedAt.Valid, RunnerID: r.RunnerID, Cwd: r.Cwd}
	}
	return items, nil
}

// ReadResult keeps the first read time on repeated requests.
func (s *Service) ReadResult(ctx context.Context, id string) error {
	n, err := s.q.ReadAutomationResult(ctx, sqlc.ReadAutomationResultParams{ID: id, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissResult keeps the first dismissal time on repeated requests.
func (s *Service) DismissResult(ctx context.Context, id string) error {
	n, err := s.q.DismissAutomationResult(ctx, sqlc.DismissAutomationResultParams{ID: id, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
