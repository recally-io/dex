package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

var (
	ErrProjectForbidden = errors.New("only Chat sessions can set a project directory")
	ErrProjectInvalid   = errors.New("invalid project directory")
)

// projectPath validates paths at the API and tool boundary. Empty restores the workspace.
func projectPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrProjectInvalid, err)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrProjectInvalid)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProjectInvalid, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: path must be a directory", ErrProjectInvalid)
	}
	return path, nil
}

// SetProject saves control state first. API changes apply on the next submit.
func (s *Service) SetProject(ctx context.Context, agentID string, id int64, path string) (string, error) {
	conv, err := s.q.GetConversation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.AgentID != agentID) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(conv.ChannelKey, "chat:") {
		return "", ErrProjectForbidden
	}
	path, err = projectPath(path)
	if err != nil {
		return "", err
	}
	if err := s.q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{Cwd: path, ID: id, AgentID: agentID}); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Service) ToolSetProject(ctx context.Context, source engine.TaskContext, path string) (string, error) {
	conv, err := s.taskCaller(ctx, source)
	if err != nil {
		return "", err
	}
	return s.SetProject(ctx, source.AgentID, conv.ID, path)
}
