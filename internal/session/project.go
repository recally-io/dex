package session

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
	"github.com/recally-io/dex/internal/runner"
)

var (
	ErrProjectForbidden = errors.New("only writable sessions can set a project directory")
	ErrProjectInvalid   = runner.ErrInvalidPath
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

// resolveProjectPath validates and normalizes a project path inside an execution environment of
// the bot. An empty runnerID selects the environment of the bot.
func (s *Service) resolveProjectPath(ctx context.Context, botID, runnerID, path string) (string, error) {
	return s.engine.ResolveProjectPath(ctx, botID, runnerID, path)
}

// SetProject saves control state first. API changes apply on the next submit.
func (s *Service) SetProject(ctx context.Context, botID string, id int64, path string) (string, error) {
	conv, err := s.q.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.BotID != botID) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !acceptsInput(conv.ChannelKey, conv.ParentID) {
		return "", ErrProjectForbidden
	}
	path, err = s.resolveProjectPath(ctx, botID, conv.RunnerID, path)
	if err != nil {
		return "", err
	}
	if err := s.q.SetSessionCwd(ctx, sqlc.SetSessionCwdParams{Cwd: path, ID: id, BotID: botID}); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Service) ToolSetProject(ctx context.Context, source engine.TaskContext, path string) (string, error) {
	conv, err := s.toolCaller(ctx, source)
	if err != nil {
		return "", err
	}
	return s.SetProject(ctx, source.BotID, conv.ID, path)
}

// RecentProjects reads activity once and filters paths that do not exist in the environment of the
// bot or in runnerID.
func (s *Service) RecentProjects(ctx context.Context, botID, runnerID string) ([]string, error) {
	paths, err := s.q.ListRecentProjects(ctx, botID)
	if err != nil {
		return nil, err
	}
	return s.engine.FilterProjectPaths(ctx, botID, runnerID, paths, 8)
}
