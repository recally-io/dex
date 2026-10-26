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

// ProjectPath validates paths at the API and tool boundary. Empty restores the workspace.
func ProjectPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%w: home directory is unavailable", ErrProjectInvalid)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrProjectInvalid)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: directory is unavailable", ErrProjectInvalid)
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
	path, err = ProjectPath(path)
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

// RecentProjects reads activity once and filters paths that no longer exist.
func (s *Service) RecentProjects(ctx context.Context, agentID string) ([]string, error) {
	paths, err := s.q.ListRecentProjects(ctx, agentID)
	if err != nil {
		return nil, err
	}
	projects := []string{}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			projects = append(projects, path)
			if len(projects) == 8 {
				break
			}
		}
	}
	return projects, nil
}
