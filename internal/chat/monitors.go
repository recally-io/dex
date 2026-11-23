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
		ID:         "schedule-" + row.ID,
		Kind:       "schedule",
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
		return engine.MonitorInfo{}, errors.New("monitor_set_schedule requires non-empty prompt")
	}
	if (a.At == "" && a.DelaySeconds <= 0) || (a.At != "" && a.DelaySeconds > 0) {
		return engine.MonitorInfo{}, errors.New("monitor_set_schedule requires exactly one of at or delaySeconds")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}

	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
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
	if a.Env != nil && script == "" {
		return engine.MonitorInfo{}, errors.New("env requires checkCommand")
	}
	checkEnv, err := json.Marshal(a.Env)
	if err != nil {
		return engine.MonitorInfo{}, fmt.Errorf("encode monitor env: %w", err)
	}
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
	if !activeMonitorTarget(conv) || conv.ExecutionID.Int64 != r.ConversationID {
		return engine.MonitorInfo{}, errMonitorTarget
	}
	source, err := q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if source.ConversationID != conv.ID || source.Reason.String == "stopped" || source.Reason.String == "withdrawn" {
		return engine.MonitorInfo{}, errMonitorTarget
	}
	rootRequestID, chainDepth, err := monitorChain(ctx, q, source)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	targetEnv, targetRev, err := monitorEnvironment(ctx, q, conv)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := monitorActor(ctx, q, conv, rootRequestID); err != nil {
		return engine.MonitorInfo{}, err
	}

	if err := checkMonitorCapacity(ctx, q, conv.ID); err != nil {
		return engine.MonitorInfo{}, err
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
		CheckEnv:                  string(checkEnv),
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
	if s.background != nil {
		commands, err := s.background.List(ctx, conv.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range commands {
			out = append(out, backgroundInfo(row))
		}
	}
	return out, nil
}

func (s *Service) ToolMonitorCancel(ctx context.Context, r engine.TaskContext, id string) (engine.MonitorInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	kind, rawID, _ := strings.Cut(id, "-")
	switch kind {
	case "command":
		return s.cancelCommandMonitor(ctx, conv.ID, rawID)
	case "schedule":
		return s.cancelMonitor(ctx, conv.AgentID, conv.ID, rawID)
	default:
		return engine.MonitorInfo{}, ErrNotFound
	}
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
	return s.cancelMonitor(ctx, agentID, chatID, monitorID)
}

func (s *Service) cancelMonitor(ctx context.Context, agentID string, chatID int64, monitorID string) (engine.MonitorInfo, error) {
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	defer func() { unlock(); s.Wake() }()
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	if s.leaseUntil.Load() != 0 {
		return engine.MonitorInfo{}, engine.ErrMaintenance
	}
	row, err := s.q.GetMonitor(ctx, monitorID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (row.ConversationID != chatID || row.AgentID != agentID)) {
		return engine.MonitorInfo{}, ErrNotFound
	}
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	conv, err := s.q.GetConversation(ctx, chatID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	events, err := s.q.ListPendingMonitorEvents(ctx, row.ID)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	ref := engine.Ref{AgentID: agentID, ID: cmp.Or(row.TargetExecutionID, conv.ExecutionID.Int64), Scope: conv.ExecutionScope}
	var withdrawn []string
	for _, id := range events {
		if ref.ID != 0 {
			outcome, err := s.engine.Withdraw(ctx, ref, id)
			if err != nil {
				return engine.MonitorInfo{}, err
			}
			if outcome == engine.Placed {
				continue
			}
		}
		withdrawn = append(withdrawn, id)
	}
	// Commit only after native withdrawal. A failed write can retry against the same request IDs.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UnixMilli()
	for _, id := range withdrawn {
		if _, err := q.WithdrawSubmission(ctx, sqlc.WithdrawSubmissionParams{ID: id, Now: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
			return engine.MonitorInfo{}, err
		}
	}
	if _, err := q.CancelMonitor(ctx, sqlc.CancelMonitorParams{ID: row.ID, ConversationID: chatID, UpdatedAt: now}); err != nil {
		return engine.MonitorInfo{}, err
	}
	row, err = q.GetMonitor(ctx, monitorID)
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

func activeMonitorTarget(c sqlc.Conversation) bool {
	return c.ExecutionScope == "" && c.ExecutionID.Valid && !c.ArchivedAt.Valid && c.AbortPending == 0
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
	if root.ConversationID != conv.ID || root.ChannelRevoked != 0 {
		return errMonitorTarget
	}
	if root.Kind != "message" || !userOrigin(root.Origin) {
		return nil
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
	if c.AgentID != row.AgentID || !activeMonitorTarget(c) {
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

func checkMonitorCapacity(ctx context.Context, q *sqlc.Queries, conversationID int64) error {
	scheduled, err := q.CountScheduledMonitors(ctx, conversationID)
	if err != nil {
		return err
	}
	commands, err := q.ListRunningBackgroundIDs(ctx, conversationID)
	if err != nil {
		return err
	}
	if scheduled+int64(len(commands)) >= maxActiveMonitors {
		return fmt.Errorf("session already has %d active Monitors", maxActiveMonitors)
	}
	return nil
}

// Follow command receipts to the first non-command source. Schedule rows already record depth.
func monitorChain(ctx context.Context, q *sqlc.Queries, source sqlc.Submission) (string, int64, error) {
	var depth int64
	for depth <= maxMonitorChainDepth {
		if source.Kind == "message" && userOrigin(source.Origin) {
			return source.ID, depth, nil
		}
		if source.Kind == "task_result" && source.SourceKey.String == source.ID {
			if event, ok := strings.CutPrefix(source.ID, "background:"); ok {
				id, _, _ := strings.Cut(event, ":")
				command, err := q.GetBackgroundCommand(ctx, id)
				if err != nil {
					return "", 0, err
				}
				if command.ConversationID != source.ConversationID {
					return "", 0, errMonitorTarget
				}
				source, err = q.GetSubmission(ctx, command.SourceRequestID)
				if err != nil {
					return "", 0, err
				}
				depth++
				continue
			}
			prior, err := monitorForEvent(ctx, q, source.ID)
			if err == nil && prior.ConversationID == source.ConversationID {
				depth += prior.ChainDepth + 1
				if depth <= maxMonitorChainDepth {
					return prior.RootRequestID, depth, nil
				}
				break
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", 0, err
			}
		}
		depth++
		if depth <= maxMonitorChainDepth {
			return source.ID, depth, nil
		}
	}
	return "", 0, fmt.Errorf("follow-up monitor chain depth limit (%d) reached; wait for user input before setting another Monitor", maxMonitorChainDepth)
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

// AuthorizeMonitor rechecks a follow-up immediately before native admission.
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
