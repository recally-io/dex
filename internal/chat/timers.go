package chat

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const (
	minTimerDelay        = 60 * time.Second
	maxTimerHorizon      = 7 * 24 * time.Hour
	maxTimerChainDepth   = 6
	maxActiveTimers      = 32
	timerFireGraceWindow = time.Hour
)

func timerInfo(row sqlc.FollowupTimer) engine.TimerInfo {
	return engine.TimerInfo{
		ID:         row.ID,
		DueAt:      row.DueAt,
		TimeZone:   row.TimeZone,
		Prompt:     row.Prompt,
		State:      row.State,
		ChainDepth: int(row.ChainDepth),
		CreatedAt:  row.CreatedAt,
	}
}

func (s *Service) ToolTimerSet(ctx context.Context, r engine.TaskContext, a engine.TimerSetArgs) (engine.TimerInfo, error) {
	if strings.TrimSpace(a.Prompt) == "" {
		return engine.TimerInfo{}, errors.New("timer_set requires non-empty prompt")
	}
	if (a.At == "" && a.DelaySeconds <= 0) || (a.At != "" && a.DelaySeconds > 0) {
		return engine.TimerInfo{}, errors.New("timer_set requires exactly one of at or delaySeconds")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if conv.ParentID.Valid {
		return engine.TimerInfo{}, errors.New("child sessions cannot schedule follow-up timers")
	}
	if existing, err := s.q.GetFollowupTimerByOperationKey(ctx, r.Key); err == nil {
		return timerInfo(existing), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.TimerInfo{}, err
	}

	tz := cmp.Or(strings.TrimSpace(a.TimeZone), "UTC")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return engine.TimerInfo{}, fmt.Errorf("invalid timeZone %q: %w", tz, err)
	}
	now := s.now()
	var dueAt time.Time
	if a.DelaySeconds > 0 {
		dueAt = now.Add(time.Duration(a.DelaySeconds) * time.Second)
	} else {
		dueAt, err = parseTimerTimestamp(a.At, loc)
		if err != nil {
			return engine.TimerInfo{}, err
		}
	}
	delay := dueAt.Sub(now)
	if delay < minTimerDelay {
		return engine.TimerInfo{}, fmt.Errorf("timer delay must be at least %d seconds", int(minTimerDelay.Seconds()))
	}
	if delay > maxTimerHorizon {
		return engine.TimerInfo{}, fmt.Errorf("timer due time cannot exceed %d days", int(maxTimerHorizon/(24*time.Hour)))
	}

	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if source.ConversationID != conv.ID {
		return engine.TimerInfo{}, errors.New("timer source belongs to another conversation")
	}

	rootRequestID := source.ID
	var chainDepth int64
	if source.Kind == "message" && isUserOrigin(source.Origin) {
		chainDepth = 0
	} else if prior, perr := s.q.GetFollowupTimerByFireSubmission(ctx, sql.NullString{String: source.ID, Valid: true}); perr == nil {
		rootRequestID = prior.RootRequestID
		chainDepth = prior.ChainDepth + 1
	} else if !errors.Is(perr, sql.ErrNoRows) {
		return engine.TimerInfo{}, perr
	} else {
		if latest, lerr := s.q.LatestUserSubmissionInConversation(ctx, conv.ID); lerr == nil {
			rootRequestID = latest
		} else if !errors.Is(lerr, sql.ErrNoRows) {
			return engine.TimerInfo{}, lerr
		}
		chainDepth = 1
	}
	if chainDepth > maxTimerChainDepth {
		return engine.TimerInfo{}, fmt.Errorf("follow-up timer chain depth limit (%d) reached; wait for user input before scheduling another timer", maxTimerChainDepth)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.RequireActive(ctx, q, conv.AgentID); err != nil {
		return engine.TimerInfo{}, err
	}
	activeCount, err := q.CountScheduledFollowupTimers(ctx, conv.ID)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if activeCount >= maxActiveTimers {
		return engine.TimerInfo{}, fmt.Errorf("conversation already has %d scheduled timers (limit %d)", activeCount, maxActiveTimers)
	}
	id := uuid.NewV7().String()
	nowMs := now.UnixMilli()
	created, err := q.CreateFollowupTimer(ctx, sqlc.CreateFollowupTimerParams{
		ID:                 id,
		OperationKey:       r.Key,
		AgentID:            conv.AgentID,
		ConversationID:     conv.ID,
		RootRequestID:      rootRequestID,
		SourceSubmissionID: source.ID,
		ChainDepth:         chainDepth,
		DueAt:              dueAt.UnixMilli(),
		TimeZone:           tz,
		Prompt:             a.Prompt,
		CreatedAt:          nowMs,
		UpdatedAt:          nowMs,
	})
	if err != nil {
		return engine.TimerInfo{}, err
	}
	var row sqlc.FollowupTimer
	if created == 0 {
		row, err = q.GetFollowupTimerByOperationKey(ctx, r.Key)
	} else {
		row, err = q.GetFollowupTimer(ctx, id)
	}
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.TimerInfo{}, err
	}
	return timerInfo(row), nil
}

func (s *Service) ToolTimerList(ctx context.Context, r engine.TaskContext) ([]engine.TimerInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListFollowupTimersByConversation(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.TimerInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, timerInfo(row))
	}
	return out, nil
}

func (s *Service) ToolTimerCancel(ctx context.Context, r engine.TaskContext, id string) (engine.TimerInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.CancelFollowupTimer(ctx, sqlc.CancelFollowupTimerParams{
		ID:             id,
		ConversationID: conv.ID,
		UpdatedAt:      s.now().UnixMilli(),
	}); err != nil {
		return engine.TimerInfo{}, err
	}
	row, err := q.GetFollowupTimer(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ConversationID != conv.ID) {
		return engine.TimerInfo{}, errors.New("timer not found")
	}
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.TimerInfo{}, err
	}
	return timerInfo(row), nil
}

// ListTimers returns active and recent follow-up timers of a chat.
func (s *Service) ListTimers(ctx context.Context, agentID string, chatID int64) ([]engine.TimerInfo, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListFollowupTimersByConversation(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.TimerInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, timerInfo(row))
	}
	return out, nil
}

// CancelTimer cancels one scheduled follow-up timer of a chat.
func (s *Service) CancelTimer(ctx context.Context, agentID string, chatID int64, timerID string) (engine.TimerInfo, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return engine.TimerInfo{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.TimerInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.CancelFollowupTimer(ctx, sqlc.CancelFollowupTimerParams{
		ID:             timerID,
		ConversationID: chatID,
		UpdatedAt:      s.now().UnixMilli(),
	}); err != nil {
		return engine.TimerInfo{}, err
	}
	row, err := q.GetFollowupTimer(ctx, timerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ConversationID != chatID) {
		return engine.TimerInfo{}, ErrNotFound
	}
	if err != nil {
		return engine.TimerInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.TimerInfo{}, err
	}
	s.signal(chatID)
	return timerInfo(row), nil
}

// FireDueTimers claims due scheduled timers and delivers them as durable system inputs.
func (s *Service) FireDueTimers(ctx context.Context) error {
	now := s.now()
	due, err := s.q.ListDueFollowupTimers(ctx, now.UnixMilli())
	if err != nil {
		return err
	}
	var errs []error
	for _, timer := range due {
		if err := s.fireOneTimer(ctx, timer, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) fireOneTimer(ctx context.Context, timer sqlc.FollowupTimer, now time.Time) error {
	s.writeMu.Lock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		s.writeMu.Unlock()
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := db.RequireActive(ctx, q, timer.AgentID); err != nil {
		if errors.Is(err, db.ErrAgentArchived) {
			_, _ = q.CancelFollowupTimersByConversation(ctx, sqlc.CancelFollowupTimersByConversationParams{
				ConversationID: timer.ConversationID,
				UpdatedAt:      now.UnixMilli(),
			})
			err = tx.Commit()
		}
		s.writeMu.Unlock()
		return err
	}
	conv, err := q.GetConversation(ctx, timer.ConversationID)
	if err != nil || conv.ArchivedAt.Valid {
		if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.ArchivedAt.Valid) {
			_, _ = q.CancelFollowupTimersByConversation(ctx, sqlc.CancelFollowupTimersByConversationParams{
				ConversationID: timer.ConversationID,
				UpdatedAt:      now.UnixMilli(),
			})
			err = tx.Commit()
		}
		s.writeMu.Unlock()
		return err
	}

	nowMs := now.UnixMilli()
	withinGrace := now.Sub(time.UnixMilli(timer.DueAt)) <= timerFireGraceWindow
	state := "fired"
	subID := "timer:" + timer.ID + ":fire"
	content := fmt.Sprintf("[Follow-up timer due: %s]\n%s", timer.ID, timer.Prompt)
	if !withinGrace {
		state = "expired"
		subID = "timer:" + timer.ID + ":expired"
		content = fmt.Sprintf("[Follow-up timer expired past 1h grace window: %s]\nOriginal prompt: %s", timer.ID, timer.Prompt)
	}
	changed, err := q.MarkFollowupTimerFired(ctx, sqlc.MarkFollowupTimerFiredParams{
		ID:               timer.ID,
		State:            state,
		FireSubmissionID: sql.NullString{String: subID, Valid: true},
		UpdatedAt:        nowMs,
	})
	if err != nil || changed == 0 {
		s.writeMu.Unlock()
		return err
	}
	if _, err := q.CreateTimerSubmission(ctx, sqlc.CreateTimerSubmissionParams{
		ID:             subID,
		ConversationID: timer.ConversationID,
		Content:        content,
		CreatedAt:      nowMs,
		TimerID:        timer.ID,
	}); err != nil {
		s.writeMu.Unlock()
		return err
	}
	if err := tx.Commit(); err != nil {
		s.writeMu.Unlock()
		return err
	}
	s.writeMu.Unlock()

	s.signal(timer.ConversationID)
	s.Wake()
	return nil
}

func parseTimerTimestamp(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timer timestamp %q (use RFC3339 or YYYY-MM-DDTHH:MM:SS)", raw)
}
