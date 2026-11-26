package worktask

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

const BoardTokenPrefix = "dex_bt_"
const ExternalLeaseTTL = 2 * time.Minute

var (
	ErrClaimLost      = errors.New("claim_lost")
	ErrBoardToken     = errors.New("invalid Board Token")
	ErrDispatchExists = errors.New("dispatch already recorded; inspect the Task before starting another Process")
)

type ExternalParams struct {
	Harness, MachineID, Hostname, WorkspacePath, GitBranch, ExternalSessionID string
	LeaseSeconds                                                              int64
	DispatchID                                                                string
}

type ExternalIssue struct {
	Execution  sqlc.TaskExecution
	BoardToken string
}

func leaseMillis(seconds int64) (int64, error) {
	switch {
	case seconds == 0:
		return ExternalLeaseTTL.Milliseconds(), nil
	case seconds < 1 || seconds > 3600:
		return 0, ErrInvalid
	default:
		return seconds * 1000, nil
	}
}

func (s *Service) AttachExternalExecution(ctx context.Context, actor Actor, origin Origin, key string, p ExternalParams) (out ExternalIssue, err error) {
	return s.issueExternal(ctx, actor, origin, key, "", p)
}

func (s *Service) ResumeExternalExecution(ctx context.Context, actor Actor, origin Origin, id string, p ExternalParams) (out ExternalIssue, err error) {
	return s.issueExternal(ctx, actor, origin, "", id, p)
}

// DispatchExternalExecution checks the Task binding as well as the Runner binding on resume.
func (s *Service) DispatchExternalExecution(ctx context.Context, actor Actor, origin Origin, key, resumeID string, p ExternalParams) (ExternalIssue, error) {
	if p.DispatchID == "" || p.MachineID == "" || p.WorkspacePath == "" {
		return ExternalIssue{}, ErrInvalid
	}
	return s.issueExternal(ctx, actor, origin, key, resumeID, p)
}

func (s *Service) issueExternal(ctx context.Context, actor Actor, origin Origin, key, id string, p ExternalParams) (ExternalIssue, error) {
	ttl, err := leaseMillis(p.LeaseSeconds)
	if err != nil {
		return ExternalIssue{}, err
	}
	if id == "" || p.Harness != "" {
		switch p.Harness {
		case "claude-code", "codex", "opencode", "custom":
		default:
			return ExternalIssue{}, ErrInvalid
		}
	}
	var out ExternalIssue
	err = s.write(ctx, func(l *Ledger, now int64) error {
		var e sqlc.TaskExecution
		var task sqlc.WorkTask
		var err error
		if id != "" {
			e, err = l.q.GetExternalExecution(ctx, id)
			if err != nil {
				return err
			}
			task, err = boardTask(ctx, l.q, e.TaskID)
		} else {
			task, err = boardTask(ctx, l.q, key)
		}
		if err != nil {
			return err
		}
		if err := authorize(ctx, l.q, actor, origin, task, ActionDispatchExternal); err != nil {
			return err
		}
		if !IsOpenStatus(task.Status) {
			return ErrConflict
		}
		if key != "" && id != "" {
			target, err := boardTask(ctx, l.q, key)
			if err != nil {
				return err
			}
			if target.ID != task.ID {
				return ErrConflict
			}
		}
		if p.DispatchID != "" {
			exists, err := l.q.GetTaskDispatch(ctx, p.DispatchID)
			if err != nil {
				return err
			}
			if exists {
				return ErrDispatchExists
			}
		}
		machine := p.MachineID
		if id != "" {
			machine = e.MachineID
		}
		var enrollment string
		if machine != "" {
			enrollment, err = l.q.GetRunnerCredential(ctx, machine)
			if errors.Is(err, sql.ErrNoRows) || err == nil && enrollment == "" {
				return ErrConflict
			}
			if err != nil {
				return err
			}
		}
		if id == "" {
			meta, err := json.Marshal(struct {
				Hostname string `json:"hostname,omitempty"`
			}{p.Hostname})
			if err != nil {
				return err
			}
			e, err = l.q.CreateExternalExecution(ctx, sqlc.CreateExternalExecutionParams{
				ID: uuid.NewV7().String(), TaskID: task.ID, BotID: task.AssigneeBotID, Harness: p.Harness, MachineID: machine,
				HostMetaJson: string(meta), WorkspacePath: p.WorkspacePath, GitBranch: p.GitBranch, ExternalSessionID: p.ExternalSessionID,
				LeaseExpiresAt: externalTime(now + ttl), At: externalTime(now), DispatchID: p.DispatchID, RunnerEnrollment: enrollment, LeaseTtlMs: ttl,
			})
			if err != nil {
				return err
			}
		} else {
			if enrollment != e.RunnerEnrollment || p.MachineID != "" && p.MachineID != machine ||
				p.Harness != "" && p.Harness != e.Harness || p.WorkspacePath != "" && p.WorkspacePath != e.WorkspacePath ||
				p.ExternalSessionID != "" && p.ExternalSessionID != e.ExternalSessionID {
				return ErrConflict
			}
			if p.DispatchID != "" && (p.Harness != e.Harness || p.MachineID != machine || p.WorkspacePath != e.WorkspacePath || p.ExternalSessionID == "" || p.ExternalSessionID != e.ExternalSessionID) {
				return ErrConflict
			}
			if p.LeaseSeconds == 0 {
				ttl = e.LeaseTtlMs
			}
			dispatch := e.DispatchID
			if p.DispatchID != "" {
				dispatch = p.DispatchID
			}
			e, err = l.q.ResumeExternalExecution(ctx, sqlc.ResumeExternalExecutionParams{
				ID: id, BotID: task.AssigneeBotID, LeaseExpiresAt: externalTime(now + ttl), At: externalTime(now), DispatchID: dispatch, LeaseTtlMs: ttl,
			})
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			if err := l.q.RevokeBoardTokens(ctx, sqlc.RevokeBoardTokensParams{ExecutionID: e.ID, At: externalTime(now)}); err != nil {
				return err
			}
		}
		token := BoardTokenPrefix + rand.Text()
		if err := l.q.InsertBoardToken(ctx, sqlc.InsertBoardTokenParams{TokenHash: tokenHash(token), TaskID: task.ID, ExecutionID: e.ID, LeaseEpoch: e.LeaseEpoch, CreatedAt: now, DispatchID: p.DispatchID}); err != nil {
			return err
		}
		if err := l.recordExternalExecution(ctx, actor, task, e, "", now); err != nil {
			return err
		}
		out = ExternalIssue{Execution: e, BoardToken: token}
		return nil
	})
	if err != nil {
		return ExternalIssue{}, err
	}
	return out, nil
}

func tokenHash(token string) string       { return fmt.Sprintf("%x", sha256.Sum256([]byte(token))) }
func externalTime(ms int64) sql.NullInt64 { return sql.NullInt64{Int64: ms, Valid: true} }

func boardToken(ctx context.Context, q *sqlc.Queries, bearer string) (sqlc.TaskExecutionToken, error) {
	if !strings.HasPrefix(bearer, BoardTokenPrefix) {
		return sqlc.TaskExecutionToken{}, ErrBoardToken
	}
	token, err := q.GetBoardToken(ctx, tokenHash(bearer))
	if errors.Is(err, sql.ErrNoRows) {
		return token, ErrBoardToken
	}
	if err == nil && token.RevokedAt.Valid {
		return token, ErrClaimLost
	}
	return token, err
}

func (s *Service) ExternalReadTask(ctx context.Context, bearer string, after, limit int64) (sqlc.WorkTask, []sqlc.WorkTaskEntry, error) {
	if after < 0 || limit < 0 || limit > 100 {
		return sqlc.WorkTask{}, nil, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sqlc.WorkTask{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	token, err := boardToken(ctx, q, bearer)
	if err != nil {
		return sqlc.WorkTask{}, nil, err
	}
	e, err := q.GetExternalExecution(ctx, token.ExecutionID)
	if err != nil {
		return sqlc.WorkTask{}, nil, err
	}
	if e.TaskID != token.TaskID || e.LeaseEpoch != token.LeaseEpoch || e.State != "running" || e.LeaseExpiresAt.Int64 <= time.Now().UnixMilli() {
		return sqlc.WorkTask{}, nil, ErrClaimLost
	}
	task, err := boardTask(ctx, q, token.TaskID)
	if err != nil {
		return task, nil, err
	}
	entries, err := q.ListExternalTaskEntries(ctx, sqlc.ListExternalTaskEntriesParams{TaskID: task.ID, AfterSeq: after, PageSize: limit})
	return task, entries, err
}

type ExternalHeartbeat struct {
	LeaseSeconds                 int64
	GitBranch, ExternalSessionID *string
}

func externalString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func (s *Service) ExternalHeartbeat(ctx context.Context, bearer string, p ExternalHeartbeat) (sqlc.TaskExecution, error) {
	ttl, err := leaseMillis(p.LeaseSeconds)
	if err != nil {
		return sqlc.TaskExecution{}, err
	}
	if p.GitBranch != nil && strings.Contains(*p.GitBranch, bearer) || p.ExternalSessionID != nil && strings.Contains(*p.ExternalSessionID, bearer) {
		return sqlc.TaskExecution{}, ErrInvalid
	}
	change := sqlc.WriteExternalExecutionParams{Heartbeat: externalTime(1), Branch: externalString(p.GitBranch), SessionID: externalString(p.ExternalSessionID)}
	if p.LeaseSeconds != 0 {
		change.TtlMs = externalTime(ttl)
	}
	return s.writeExternal(ctx, bearer, change, nil)
}

func (s *Service) ExternalPostEntry(ctx context.Context, bearer string, p PostEntryParams) (entry sqlc.WorkTaskEntry, err error) {
	if bearer != "" {
		p.Content = strings.ReplaceAll(p.Content, bearer, "[redacted]")
	}
	_, err = s.writeExternal(ctx, bearer, sqlc.WriteExternalExecutionParams{}, func(l *Ledger, task sqlc.WorkTask, e sqlc.TaskExecution, now int64) error {
		entry, err = l.PostEntry(ctx, ExternalAgent(e.ID), OriginUser, task, p, now)
		return err
	})
	return entry, err
}

func (s *Service) ExternalFinish(ctx context.Context, bearer, outcome, summary string) (sqlc.TaskExecution, error) {
	if outcome != "completed" && outcome != "failed" {
		return sqlc.TaskExecution{}, ErrInvalid
	}
	if summary != "" {
		var err error
		if bearer != "" {
			summary = strings.ReplaceAll(summary, bearer, "[redacted]")
		}
		if summary, err = Content(summary); err != nil {
			return sqlc.TaskExecution{}, err
		}
	}
	return s.writeExternal(ctx, bearer, sqlc.WriteExternalExecutionParams{NewState: externalString(&outcome)}, func(l *Ledger, t sqlc.WorkTask, e sqlc.TaskExecution, now int64) error {
		return l.recordExternalExecution(ctx, ExternalAgent(e.ID), t, e, summary, now)
	})
}

func (s *Service) writeExternal(ctx context.Context, bearer string, p sqlc.WriteExternalExecutionParams, record func(*Ledger, sqlc.WorkTask, sqlc.TaskExecution, int64) error) (sqlc.TaskExecution, error) {
	var e sqlc.TaskExecution
	err := s.write(ctx, func(l *Ledger, now int64) error {
		token, err := boardToken(ctx, l.q, bearer)
		if err != nil {
			return err
		}
		p.ID, p.TaskID, p.Epoch, p.At = token.ExecutionID, token.TaskID, token.LeaseEpoch, externalTime(now)
		e, err = l.q.WriteExternalExecution(ctx, p)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimLost
		}
		if err != nil || record == nil {
			return err
		}
		task, err := boardTask(ctx, l.q, token.TaskID)
		if err != nil {
			return err
		}
		return record(l, task, e, now)
	})
	if err != nil {
		return sqlc.TaskExecution{}, err
	}
	return e, nil
}

func (l *Ledger) recordExternalExecution(ctx context.Context, actor Actor, t sqlc.WorkTask, e sqlc.TaskExecution, summary string, now int64) error {
	meta, err := json.Marshal(struct {
		ExecutionID string `json:"executionId"`
		State       string `json:"state"`
		Epoch       int64  `json:"leaseEpoch"`
	}{e.ID, e.State, e.LeaseEpoch})
	if err != nil {
		return err
	}
	content := "External run " + e.State
	if summary != "" {
		content += ": " + summary
	}
	if _, err := l.record(ctx, actor, t, sqlc.InsertWorkTaskEntryParams{Kind: KindExecution, MetaJson: string(meta), Content: content, CreatedAt: now}); err != nil {
		return err
	}
	reason := ""
	switch e.State {
	case "failed":
		reason = WakeExecutionFailed
	case "stale":
		reason = WakeExecutionStale
	}
	if reason != "" {
		_, err = l.queueWake(ctx, t.AssigneeBotID, t, reason, now)
	}
	return err
}

func (s *Service) ListBoardExecutions(ctx context.Context, key, machine, kind, state string) ([]sqlc.ListBoardExecutionsRow, error) {
	var taskID string
	if key != "" {
		task, err := boardTask(ctx, s.q, key)
		if err != nil {
			return nil, err
		}
		taskID = task.ID
	}
	return s.q.ListBoardExecutions(ctx, sqlc.ListBoardExecutionsParams{TaskID: taskID, MachineIDFilter: machine, KindFilter: kind, StateFilter: state})
}
