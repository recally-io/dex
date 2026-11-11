package artifact

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

type Service struct {
	db      *sql.DB
	dataDir string
}

func New(db *sql.DB, dataDir string) *Service { return &Service{db: db, dataDir: dataDir} }

func artifactInfo(a sqlc.Artifact) engine.ArtifactInfo {
	return engine.ArtifactInfo{ID: a.ID, Name: a.Name, Size: a.Size, ConversationID: a.ConversationID, DisplayConversationID: a.DisplayConversationID}
}

// Deliver binds a snapshot to the actual source and, for a task, its parent.
func (s *Service) Deliver(ctx context.Context, caller engine.TaskContext, path string) (engine.ArtifactInfo, error) {
	q := sqlc.New(s.db)
	prior, err := q.GetArtifactByOperation(ctx, sqlc.GetArtifactByOperationParams{OperationKey: caller.Key, AgentID: caller.AgentID})
	if err == nil {
		return artifactInfo(prior), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return engine.ArtifactInfo{}, err
	}
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: caller.AgentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	parent := conv.ID
	switch {
	case conv.ChannelKey == "main", strings.HasPrefix(conv.ChannelKey, "chat:"), strings.HasPrefix(conv.ChannelKey, "automation:"):
	case strings.HasPrefix(conv.ChannelKey, "task:"):
		// A conversation from before child sessions. Its parent link comes from the migration.
	default:
		return engine.ArtifactInfo{}, errors.New("this conversation cannot deliver files")
	}
	if conv.ParentID.Valid {
		// Only a turn that the parent sent delivers to the parent. A user turn delivers in this
		// conversation, like any chat.
		source, err := q.GetSubmission(ctx, caller.SourceRequestID)
		if err != nil {
			return engine.ArtifactInfo{}, err
		}
		if source.ParentRequestID.Valid {
			owner, err := q.GetConversation(ctx, conv.ParentID.Int64)
			if err != nil {
				return engine.ArtifactInfo{}, err
			}
			// The snapshot is in the home of this agent. A parent in another bot cannot serve it, so a
			// child of another bot delivers in its own conversation.
			if owner.AgentID == conv.AgentID {
				parent = owner.ID
			}
		}
	}
	if _, err := q.GetActiveArtifactInput(ctx, sqlc.GetActiveArtifactInputParams{ID: caller.SourceRequestID, ConversationID: conv.ID}); err != nil {
		return engine.ArtifactInfo{}, errors.Join(errors.New("delivery requires an active input"), err)
	}

	snapshot, err := Snapshot(agent.Home(s.dataDir, caller.AgentID), path)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	keepSnapshot := true
	defer func() {
		if keepSnapshot {
			_ = Remove(agent.Home(s.dataDir, caller.AgentID), snapshot.ID)
		}
	}()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q = sqlc.New(tx)
	// Take the writer before reading, so concurrent deliveries cannot upgrade a stale WAL snapshot.
	locked, err := q.LockArtifactInput(ctx, sqlc.LockArtifactInputParams{ID: caller.SourceRequestID, ConversationID: conv.ID})
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	prior, err = q.GetArtifactByOperation(ctx, sqlc.GetArtifactByOperationParams{OperationKey: caller.Key, AgentID: caller.AgentID})
	if err == nil {
		return artifactInfo(prior), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return engine.ArtifactInfo{}, err
	}
	if locked != 1 {
		return engine.ArtifactInfo{}, errors.New("delivery requires an active input")
	}
	params := sqlc.CreateArtifactParams{ID: snapshot.ID, AgentID: caller.AgentID, ConversationID: conv.ID, DisplayConversationID: parent, OperationKey: caller.Key, Name: snapshot.Name, Size: snapshot.Size, CreatedAt: time.Now().UnixMilli()}
	if err := q.CreateArtifact(ctx, params); err != nil {
		return engine.ArtifactInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.ArtifactInfo{}, err
	}
	keepSnapshot = false
	return engine.ArtifactInfo{ID: snapshot.ID, Name: snapshot.Name, Size: snapshot.Size, ConversationID: conv.ID, DisplayConversationID: parent}, nil
}

func (s *Service) Get(ctx context.Context, agentID string, conversationID int64, id string) (sqlc.Artifact, error) {
	return sqlc.New(s.db).GetArtifact(ctx, sqlc.GetArtifactParams{ID: id, AgentID: agentID, ConversationID: conversationID, DisplayConversationID: conversationID})
}

func (s *Service) List(ctx context.Context, agentID string, conversationID int64) ([]sqlc.Artifact, error) {
	return sqlc.New(s.db).ListArtifacts(ctx, sqlc.ListArtifactsParams{AgentID: agentID, ConversationID: conversationID, DisplayConversationID: conversationID})
}
