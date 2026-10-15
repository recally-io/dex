// Package app wires control: the database, the worker process, the chat service, the HTTP
// handler, and the Telegram bot.
package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/dex/internal/automation"
	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/internal/db"
	"github.com/vaayne/dex/internal/db/sqlc"
	"github.com/vaayne/dex/internal/dream"
	"github.com/vaayne/dex/internal/live"
	"github.com/vaayne/dex/internal/scheduler"
	"github.com/vaayne/dex/internal/server"
	"github.com/vaayne/dex/internal/settings"
	"github.com/vaayne/dex/internal/telegram"
	"github.com/vaayne/dex/internal/upload"
	"github.com/vaayne/dex/internal/worker"
	"github.com/vaayne/dex/internal/workerbin"
)

// DefaultAgentID is the agent that the web UI talks to.
const DefaultAgentID = "default"

// Config configures an App.
type Config struct {
	DataDir  string // control.sqlite and agents/<id>/
	CacheDir string // the installed worker binary; also the worker's working directory
	// WorkerEnv is the worker's complete environment, without DEX_DATA_DIR. Use WorkerEnv() for
	// the default.
	WorkerEnv []string
	// TelegramAPIURL replaces https://api.telegram.org, for example for a local Bot API server.
	TelegramAPIURL string
	// Version is the version that GET /api/health reports.
	Version string
	// DreamEvery is the shortest time between two dreams; zero means 24 hours.
	DreamEvery time.Duration
	// AutomationCheck is the time between two checks for due automations; zero means 1 minute.
	AutomationCheck time.Duration
}

// App is a running control process.
type App struct {
	db      *sql.DB
	handler http.Handler
	bots    *bots
	jobs    *scheduler.Scheduler
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Start opens the database, makes sure the default agent exists, and starts the worker and, when
// the settings turn it on, the Telegram bot. The agent has no model until the user chooses one in
// the settings.
func Start(ctx context.Context, cfg Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	conn, err := db.Open(ctx, filepath.Join(cfg.DataDir, "control.sqlite"))
	if err != nil {
		return nil, err
	}
	q := sqlc.New(conn)
	err = q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{
		ID: DefaultAgentID, Name: "Dex", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	path, err := worker.Install(cfg.CacheDir, workerbin.Gzip)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	jobs, err := scheduler.New()
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		chats *chat.Service
		prefs *settings.Service
		hub   *live.Hub
		bots  = &bots{ctx: runCtx}
	)
	w := worker.New(worker.Config{
		Path:      path,
		Dir:       cfg.CacheDir,
		Env:       append(cfg.WorkerEnv, "DEX_DATA_DIR="+cfg.DataDir),
		OnSettled: func(ctx context.Context, s worker.Settled) { chats.HandleSettled(ctx, s) },
		OnChat:    func(ev worker.ChatStream) { hub.HandleChat(ev) },
		OnExit:    func() { hub.WorkerExited() },
		Init: func(ctx context.Context, call worker.CallFunc) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			// The worker still runs; requests to providers without credentials fail.
			errs, err := prefs.ApplyProviders(ctx, call)
			if err != nil {
				slog.Error("give the providers to the worker", "err", err)
			}
			for id, msg := range errs {
				slog.Error("list the models of a provider", "provider", id, "err", msg)
			}
		},
		OnStart: func(ctx context.Context) { chats.Replay(ctx) },
	})
	chats = chat.New(q, w, time.Now, bots.Deliver, func(id int64) { hub.Changed(id) })
	hub = live.New(q, chats, w, DefaultAgentID)
	bots.chats = chats
	prefs = settings.New(q, w.Call, DefaultAgentID, cfg.TelegramAPIURL, bots.Set)
	tg, err := prefs.TelegramConfig(ctx)
	if err != nil {
		cancel()
		return nil, errors.Join(err, conn.Close())
	}

	automations := automation.New(q, chats, DefaultAgentID, time.Now)
	a := &App{db: conn, bots: bots, jobs: jobs, cancel: cancel, handler: server.New(server.Config{
		Chats: chats, Automations: automations, Settings: prefs, Live: hub, AgentID: DefaultAgentID, DataDir: cfg.DataDir, Version: cfg.Version,
		// The worker runs the agent's tools in this workspace.
		Uploads: upload.New(filepath.Join(cfg.DataDir, "agents", DefaultAgentID, "workspace")),
	})}
	a.wg.Go(func() { _ = w.Run(runCtx) })
	a.wg.Go(func() { chats.Run(runCtx) })
	every := cmp.Or(cfg.DreamEvery, 24*time.Hour)
	dreamer := dream.New(q, chats, DefaultAgentID, every, time.Now)
	err = errors.Join(
		jobs.Every("dream", min(every, time.Hour), dreamer.Check),
		jobs.Every("automations", cmp.Or(cfg.AutomationCheck, time.Minute), automations.Check),
	)
	if err != nil {
		cancel()
		a.wg.Wait()
		return nil, errors.Join(err, conn.Close())
	}
	jobs.Start()
	bots.Set(tg) //nolint:contextcheck // the bot runs until Close, not for the life of ctx
	return a, nil
}

// Handler serves the API and the web UI.
func (a *App) Handler() http.Handler { return a.handler }

// Close stops the worker and the bot, waits for the worker to close its storage, and closes the
// database.
func (a *App) Close() error {
	a.cancel()
	err := a.jobs.Close()
	a.bots.Set(nil)
	a.wg.Wait()
	return errors.Join(err, a.db.Close())
}

// bots runs the Telegram bot of the current settings, if any.
type bots struct {
	ctx   context.Context
	chats telegram.Submitter

	mu   sync.Mutex
	bot  *telegram.Bot
	stop func() // stops bot and waits for it
}

// Set stops the running bot and starts one for cfg, or none when cfg is nil. The callers serialize
// Set: the settings service holds its lock, and Start and Close run alone.
func (b *bots) Set(cfg *telegram.Config) {
	b.mu.Lock()
	stop := b.stop
	b.bot, b.stop = nil, nil
	b.mu.Unlock()
	// Wait outside the lock: the old bot can wait for the worker, whose reader calls Deliver.
	if stop != nil {
		stop()
	}
	if cfg == nil {
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	bot := telegram.New(*cfg, b.chats, DefaultAgentID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bot.Run(ctx)
	}()
	b.mu.Lock()
	b.bot, b.stop = bot, func() { cancel(); <-done }
	b.mu.Unlock()
}

// Deliver sends a reply through the running bot. Without a bot, a Telegram reply stays only in
// control.sqlite.
func (b *bots) Deliver(ctx context.Context, channelKey string, reply chat.Reply) {
	b.mu.Lock()
	bot := b.bot
	b.mu.Unlock()
	if bot != nil {
		bot.Deliver(ctx, channelKey, reply)
	} else if strings.HasPrefix(channelKey, "telegram:") {
		slog.Warn("telegram is off; the reply was not sent", "channel", channelKey)
	}
}

// WorkerEnv selects the variables of environ that the worker needs: the basic process variables
// and DEX_FAKE_MODEL. Provider credentials come from the settings, never from the environment.
func WorkerEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "HOME", "TMPDIR", "LANG", "USER", "SHELL", "DEX_FAKE_MODEL":
			env = append(env, kv)
		}
	}
	return env
}
