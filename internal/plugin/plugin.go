// Package plugin is the seam between the core and the optional sources that run in the dex process.
// A plugin lives under plugins/<name>. The core never imports a plugin; cmd/dex selects them.
package plugin

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"time"

	"github.com/recally-io/dex/internal/worktask"
)

// Deps are the core services that a plugin can use.
type Deps struct {
	DB        *sql.DB
	WorkTasks *worktask.Service
}

// Factory builds a plugin when dex starts.
type Factory func(Deps) Plugin

// Plugin is what one source adds to dex. A zero field adds nothing.
type Plugin struct {
	// Name is the path segment of the plugin routes. It has only lowercase ASCII letters.
	Name string
	// Hooks serves /hooks/<Name>/ without the access token. HookLimit is its largest request body.
	Hooks     http.Handler
	HookLimit int64
	// API serves /api/plugins/<Name>/ behind the access token.
	API   http.Handler
	Jobs  []Job
	Tools []worktask.LinkTool
	// Migrations holds migrations/*.sql, the schema of the plugin. See Migrate.
	Migrations fs.FS
}

// Job runs on the core scheduler. A job checks durable state, so a missed run is safe.
type Job struct {
	Name  string
	Every time.Duration
	Run   func(context.Context)
}
