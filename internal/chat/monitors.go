package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const (
	minMonitorDelay        = 60 * time.Second
	maxMonitorHorizon      = 7 * 24 * time.Hour
	maxMonitorChainDepth   = 6
	maxActiveMonitors      = 32
	monitorFireGraceWindow = time.Hour
)

func monitorInfo(row sqlc.Monitor) engine.MonitorInfo {
	info := engine.MonitorInfo{
		ID:         row.ID,
		DueAt:      row.DueAt,
		TimeZone:   row.TimeZone,
		Prompt:     row.Prompt,
		State:      row.State,
		ChainDepth: int(row.ChainDepth),
		CreatedAt:  row.CreatedAt,
		Mode:       "prompt",
		MaxRuns:    int(row.MaxRuns), Runs: int(row.Runs), ExpiresAt: row.ExpiresAt, Outcome: row.Outcome,
	}
	if row.CheckCommand != "" {
		info.Mode = "script"
	}
	return info
}

func (s *Service) ToolMonitorSet(ctx context.Context, r engine.TaskContext, a engine.MonitorSetArgs) (engine.MonitorInfo, error) {
	if strings.TrimSpace(a.Prompt) == "" {
		return engine.MonitorInfo{}, errors.New("monitor_set requires non-empty prompt")
	}
	if (a.At == "" && a.DelaySeconds <= 0) || (a.At != "" && a.DelaySeconds > 0) {
		return engine.MonitorInfo{}, errors.New("monitor_set requires exactly one of at or delaySeconds")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if !monitorConversation(conv) {
		return engine.MonitorInfo{}, errors.New("monitors require an active Main or Chat session")
	}
	if existing, err := s.q.GetMonitorByOperationKey(ctx, r.Key); err == nil {
		return monitorInfo(existing), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.MonitorInfo{}, err
	}

	tz := cmp.Or(strings.TrimSpace(a.TimeZone), "UTC")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return engine.MonitorInfo{}, fmt.Errorf("invalid timeZone %q: %w", tz, err)
	}
	now := s.now()
	var dueAt time.Time
	if a.DelaySeconds > int(maxMonitorHorizon/time.Second) {
		return engine.MonitorInfo{}, errors.New("monitor delay exceeds 7 days")
	}
	if a.DelaySeconds > 0 {
		dueAt = now.Add(time.Duration(a.DelaySeconds) * time.Second)
	} else {
		dueAt, err = parseMonitorTimestamp(a.At, loc)
		if err != nil {
			return engine.MonitorInfo{}, err
		}
	}
	delay := dueAt.Sub(now)
	if delay < minMonitorDelay {
		return engine.MonitorInfo{}, fmt.Errorf("monitor delay must be at least %d seconds", int(minMonitorDelay.Seconds()))
	}
	if delay > maxMonitorHorizon {
		return engine.MonitorInfo{}, fmt.Errorf("monitor due time cannot exceed %d days", int(maxMonitorHorizon/(24*time.Hour)))
	}

	maxRuns := cmp.Or(a.MaxRuns, 1)
	if maxRuns < 1 || maxRuns > 100 || len(a.CheckCommand) > 8192 {
		return engine.MonitorInfo{}, errors.New("monitor allows 1–100 runs and an 8192-byte check command")
	}
	script := strings.TrimSpace(a.CheckCommand)
	if (maxRuns == 1 && a.IntervalSeconds != 0) || (maxRuns > 1 && (a.IntervalSeconds < 60 || a.IntervalSeconds > 604800)) {
		return engine.MonitorInfo{}, errors.New("set intervalSeconds (60–604800) only for multiple runs")
	}
	var expiresAt int64
	if a.ExpiresAt != "" {
		expires, err := parseMonitorTimestamp(a.ExpiresAt, loc)
		if err != nil {
			return engine.MonitorInfo{}, err
		}
		if !expires.After(dueAt) || expires.Sub(now) > maxMonitorHorizon {
			return engine.MonitorInfo{}, errors.New("monitor expiry must be after its first check and within 7 days")
		}
		expiresAt = expires.UnixMilli()
	} else if maxRuns > 1 || script != "" {
		if delay >= maxMonitorHorizon {
			return engine.MonitorInfo{}, errors.New("first check must precede the 7-day expiry")
		}
		expiresAt = now.Add(maxMonitorHorizon).UnixMilli()
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	conv, err = q.GetConversation(ctx, conv.ID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if !monitorConversation(conv) || conv.ExecutionID.Int64 != r.ConversationID {
		return engine.MonitorInfo{}, errMonitorTarget
	}
	source, err := q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if source.ConversationID != conv.ID || source.Reason.String == "stopped" || source.Reason.String == "withdrawn" {
		return engine.MonitorInfo{}, errMonitorTarget
	}
	fromUser := source.Kind == "message" && userOrigin(source.Origin)
	if (script != "" || maxRuns > 1) && (!fromUser || source.Status != "pending") {
		return engine.MonitorInfo{}, errors.New("a script or repeating monitor needs a current user input")
	}
	rootRequestID := source.ID
	var chainDepth int64
	if !fromUser {
		prior, perr := monitorForEvent(ctx, q, source.ID)
		switch {
		case perr == nil && prior.ConversationID == conv.ID:
			rootRequestID, chainDepth = prior.RootRequestID, prior.ChainDepth+1
		case perr != nil && !errors.Is(perr, sql.ErrNoRows):
			return engine.MonitorInfo{}, perr
		default:
			chainDepth = 1
			if latest, lerr := q.LatestUserSubmissionInConversation(ctx, conv.ID); lerr == nil {
				rootRequestID = latest
			} else if !errors.Is(lerr, sql.ErrNoRows) {
				return engine.MonitorInfo{}, lerr
			}
		}
	}
	if chainDepth > maxMonitorChainDepth {
		return engine.MonitorInfo{}, fmt.Errorf("follow-up monitor chain depth limit (%d) reached; wait for user input before scheduling another monitor", maxMonitorChainDepth)
	}
	targetEnv, targetRev, err := monitorEnvironment(ctx, q, conv)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := monitorActor(ctx, q, conv, rootRequestID); err != nil {
		return engine.MonitorInfo{}, err
	}

	activeCount, err := q.CountScheduledMonitors(ctx, conv.ID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if activeCount >= maxActiveMonitors {
		return engine.MonitorInfo{}, fmt.Errorf("conversation already has %d scheduled monitors (limit %d)", activeCount, maxActiveMonitors)
	}
	id := uuid.NewV7().String()
	nowMs := now.UnixMilli()
	created, err := q.CreateMonitor(ctx, sqlc.CreateMonitorParams{
		ID:                        id,
		OperationKey:              r.Key,
		AgentID:                   conv.AgentID,
		ConversationID:            conv.ID,
		RootRequestID:             rootRequestID,
		SourceSubmissionID:        source.ID,
		ChainDepth:                chainDepth,
		DueAt:                     dueAt.UnixMilli(),
		TimeZone:                  tz,
		Prompt:                    a.Prompt,
		CreatedAt:                 nowMs,
		UpdatedAt:                 nowMs,
		CheckCommand:              script,
		IntervalSeconds:           int64(a.IntervalSeconds),
		MaxRuns:                   int64(maxRuns),
		ExpiresAt:                 expiresAt,
		TargetExecutionID:         conv.ExecutionID.Int64,
		TargetEnvironment:         targetEnv,
		TargetEnvironmentRevision: targetRev,
		TargetCwd:                 conv.Cwd,
	})
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	var row sqlc.Monitor
	if created == 0 {
		row, err = q.GetMonitorByOperationKey(ctx, r.Key)
	} else {
		row, err = q.GetMonitor(ctx, id)
	}
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.MonitorInfo{}, err
	}
	return monitorInfo(row), nil
}

func (s *Service) ToolMonitorList(ctx context.Context, r engine.TaskContext) ([]engine.MonitorInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListMonitorsByConversation(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.MonitorInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, monitorInfo(row))
	}
	return out, nil
}

func (s *Service) ToolMonitorCancel(ctx context.Context, r engine.TaskContext, id string) (engine.MonitorInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.CancelMonitor(ctx, sqlc.CancelMonitorParams{
		ID:             id,
		ConversationID: conv.ID,
		UpdatedAt:      s.now().UnixMilli(),
	}); err != nil {
		return engine.MonitorInfo{}, err
	}
	row, err := q.GetMonitor(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ConversationID != conv.ID) {
		return engine.MonitorInfo{}, errors.New("monitor not found")
	}
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.MonitorInfo{}, err
	}
	return monitorInfo(row), nil
}

// ListMonitors returns active and recent follow-up monitors of a chat.
func (s *Service) ListMonitors(ctx context.Context, agentID string, chatID int64) ([]engine.MonitorInfo, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListMonitorsByConversation(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.MonitorInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, monitorInfo(row))
	}
	return out, nil
}

// CancelMonitor cancels one scheduled follow-up monitor of a chat.
func (s *Service) CancelMonitor(ctx context.Context, agentID string, chatID int64, monitorID string) (engine.MonitorInfo, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return engine.MonitorInfo{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := q.CancelMonitor(ctx, sqlc.CancelMonitorParams{
		ID:             monitorID,
		ConversationID: chatID,
		UpdatedAt:      s.now().UnixMilli(),
	}); err != nil {
		return engine.MonitorInfo{}, err
	}
	row, err := q.GetMonitor(ctx, monitorID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ConversationID != chatID) {
		return engine.MonitorInfo{}, ErrNotFound
	}
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.MonitorInfo{}, err
	}
	s.signal(chatID)
	return monitorInfo(row), nil
}

func parseMonitorTimestamp(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid monitor timestamp %q (use RFC3339 or YYYY-MM-DDTHH:MM:SS)", raw)
}

var errMonitorTarget = errors.New("monitor target or authority is no longer valid")

func monitorConversation(c sqlc.Conversation) bool {
	return c.ExecutionScope == "" && c.ExecutionID.Valid && !c.ParentID.Valid && !c.ArchivedAt.Valid && c.AbortPending == 0 && Writable(c.ChannelKey)
}

func monitorEnvironment(ctx context.Context, q *sqlc.Queries, c sqlc.Conversation) (string, int64, error) {
	agent, err := q.GetAgent(ctx, c.AgentID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && agent.ArchivedAt.Valid) {
		return "", 0, errMonitorTarget
	}
	if err != nil {
		return "", 0, err
	}
	id := effectiveEnvironment(agent, c.EnvironmentID)
	if id == "local" {
		return id, 0, nil
	}
	env, err := q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, errMonitorTarget
	}
	return id, env.UpdatedAt, err
}

// Check the recorded initiator, not whoever most recently used Main.
func monitorActor(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, rootID string) error {
	root, err := q.GetSubmission(ctx, rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return errMonitorTarget
	}
	if err != nil {
		return err
	}
	if root.ConversationID != conv.ID || root.ChannelRevoked != 0 || root.Kind != "message" {
		return errMonitorTarget
	}
	switch peer, _, _ := strings.Cut(root.Origin, ":"); peer {
	case "web":
		return nil
	case "telegram":
		var actor ChannelContext
		if err := json.Unmarshal([]byte(root.ChannelContext), &actor); err != nil {
			return errMonitorTarget
		}
		allowed, err := TelegramInputAllowed(ctx, q, conv.AgentID, root.Origin, actor)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	case "feishu":
		cfg, err := q.GetFeishuSettings(ctx, conv.AgentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cfg.OwnerID != "" && root.Origin == "feishu:"+cfg.OwnerID && strings.HasPrefix(root.SourceKey.String, "feishu:"+cfg.AppID+":") {
			return nil
		}
	case "wechat":
		cfg, err := q.GetWechatSettings(ctx, conv.AgentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cfg.Owner != "" && root.Origin == "wechat:"+cfg.Owner && strings.HasPrefix(root.SourceKey.String, "wechat:"+cfg.BotID+":") {
			return nil
		}
	}
	return errMonitorTarget
}

func monitorTarget(ctx context.Context, q *sqlc.Queries, row sqlc.Monitor) (sqlc.Conversation, error) {
	c, err := q.GetConversation(ctx, row.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, errMonitorTarget
	}
	if err != nil {
		return c, err
	}
	if c.AgentID != row.AgentID || !monitorConversation(c) {
		return c, errMonitorTarget
	}
	id, rev, err := monitorEnvironment(ctx, q, c)
	if err != nil {
		return c, err
	}
	// Rows from the upgrade predate this binding. They keep the original session selection.
	if row.TargetExecutionID != 0 && (c.ExecutionID.Int64 != row.TargetExecutionID || c.Cwd != row.TargetCwd || id != row.TargetEnvironment || rev != row.TargetEnvironmentRevision) {
		return c, errMonitorTarget
	}
	return c, monitorActor(ctx, q, c, row.RootRequestID)
}

func monitorForEvent(ctx context.Context, q *sqlc.Queries, eventID string) (sqlc.Monitor, error) {
	if key, ok := strings.CutPrefix(eventID, "monitor:"); ok {
		id, _, ok := strings.Cut(key, ":")
		if ok {
			return q.GetMonitor(ctx, id)
		}
	}
	// Event receipts made before the upgrade retain their original request IDs.
	return q.GetMonitorByFireSubmission(ctx, sql.NullString{String: eventID, Valid: true})
}

// AuthorizeMonitor checks queued native follow-ups against current control state.
func (s *Service) AuthorizeMonitor(ctx context.Context, caller engine.TaskContext) error {
	source, err := s.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return err
	}
	// A user request is not a monitor event just because its ID uses the prefix.
	if source.Kind != "task_result" || source.SourceKey.String != source.ID {
		return nil
	}
	row, err := monitorForEvent(ctx, s.q, source.ID)
	if err != nil {
		return err
	}
	if row.State == "cancelled" || source.Status != "pending" || source.ConversationID != row.ConversationID || row.AgentID != caller.AgentID {
		return errMonitorTarget
	}
	conv, err := monitorTarget(ctx, s.q, row)
	if err != nil {
		return err
	}
	if conv.ExecutionID.Int64 != caller.ConversationID {
		return errMonitorTarget
	}
	return nil
}
