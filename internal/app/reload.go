package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/engine"
)

// reloadStale reloads each open agent whose extension files, shell rc files, or 1Password
// settings changed. Reload needs all agents idle, so a busy engine waits for a later run. An
// invalid configuration is not tried again until its files change.
func reloadStale(native *engine.Agents, chats *chat.Service) func(context.Context) {
	refused := map[string]string{} // agent ID -> stamp that failed validation
	return func(ctx context.Context) {
		for id, stamp := range native.StaleExtensions() {
			if refused[id] == stamp {
				continue
			}
			err := chats.ReloadExtensions(ctx, id)
			switch {
			case err == nil:
				delete(refused, id)
				slog.Info("reloaded extensions after a change", "agent", id)
			case errors.Is(err, engine.ErrInvalidInput):
				refused[id] = stamp
				slog.Warn("extension change not applied; fix the configuration", "agent", id)
			}
		}
	}
}
