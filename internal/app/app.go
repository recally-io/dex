// Package app wires control: the database, the worker process, the chat service, the HTTP
// handler, and channel bots.
package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/feishu"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/scheduler"
	"github.com/recally-io/dex/internal/server"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/telegram"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/wechat"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
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
	// FeishuAPIURL and FeishuAccountsURL replace the platform boundaries in tests.
	FeishuAPIURL      string
	FeishuAccountsURL string
	// Wechat API and CDN overrides connect integration tests to local HTTP servers.
	WechatAPIURL     string
	WechatCDNURL     string
	WechatHTTPClient *http.Client
	// Version is the version that GET /api/health reports.
	Version string
	// DreamEvery is the shortest time between two dreams; zero means 24 hours.
	DreamEvery time.Duration
	// AutomationCheck is the time between two checks for due automations; zero means 1 minute.
	AutomationCheck time.Duration
	// Token is the access token that the API requires. Empty means the stored token when
	// RequireToken is set, and no authentication otherwise.
	Token        string
	RequireToken bool
}

// App is a running control process.
type App struct {
	db      *sql.DB
	handler http.Handler
	bots    *channelRegistry
	jobs    *scheduler.Scheduler
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Start opens the database, makes sure the default agent exists, and starts the worker and, when
// the settings turn them on, channel bots. The agent has no model until the user chooses one in
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
		bots  *channelRegistry
	)
	w := worker.New(worker.Config{
		Path:      path,
		Dir:       cfg.CacheDir,
		Env:       append(cfg.WorkerEnv, "DEX_DATA_DIR="+cfg.DataDir),
		OnSettled: func(ctx context.Context, s worker.Settled) { chats.HandleSettled(ctx, s) },
		OnTask:    func(ctx context.Context, r worker.TaskRequest) (any, error) { return chats.HandleTask(ctx, r) },
		OnChat:    func(ev worker.ChatStream) { hub.HandleChat(ev) },
		OnExit:    func() { hub.WorkerExited() },
		Init: func(ctx context.Context, call worker.CallFunc) error {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := chats.RecoverTasks(ctx, call); err != nil {
				return err
			}
			if err := chats.RestoreMaintenance(ctx, call); err != nil {
				return err
			}
			// The worker still runs; requests to providers without credentials fail.
			errs, err := prefs.ApplyProviders(ctx, call)
			if err != nil {
				slog.Error("give the providers to the worker", "err", err)
			}
			for id, msg := range errs {
				slog.Error("list the models of a provider", "provider", id, "err", msg)
			}
			return nil
		},
		OnStart: func(ctx context.Context) { chats.Replay(ctx) },
	})
	chats = chat.New(conn, w, time.Now, func(key string) { bots.Notify(key) }, func(id int64) { hub.Changed(id) })
	hub = live.New(q, chats, w)
	bots = &channelRegistry{ctx: runCtx, db: conn, chats: chats, managers: make(map[string]*channel.Manager)}
	setTelegram := func(agentID string, config *telegram.Config) { //nolint:contextcheck // bots live until app shutdown, not the settings request
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("telegram", nil)
			return
		}
		config.Uploads = uploads
		config.Seen = func(ctx context.Context, source string) (bool, error) {
			_, err := q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return err == nil, err
		}
		config.Watch = func(ctx context.Context, source string) (string, io.ReadCloser, error) {
			id, err := q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
			if err != nil {
				return "", nil, err
			}
			sub, err := q.GetSubmission(ctx, id)
			if err != nil {
				return "", nil, err
			}
			if sub.Status != "pending" {
				return "", nil, nil
			}
			stream, err := hub.Open(ctx, agentID, sub.ConversationID)
			return id, stream, err
		}
		config.Report = func(state, reason string) { manager.Report("telegram", channel.Status{State: state, Reason: reason}) }
		manager.Set("telegram", telegram.New(*config, manager))
	}
	prefs = settings.New(q, w.Call, cfg.TelegramAPIURL, setTelegram)
	chats.ConfigureTasks(prefs.CheckChat)
	setFeishu := func(agentID string, config *feishu.Config) { //nolint:contextcheck // The manager owns the bot lifetime.
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("feishu", nil)
			return
		}
		manager.Set("feishu", feishu.New(*config, manager, q, agentStreams{hub, agentID}, uploads, func(ctx context.Context, owner, code string) (bool, error) {
			return prefs.PairFeishu(ctx, agentID, owner, code)
		}))
	}
	prefs.ConfigureFeishu(cfg.FeishuAPIURL, cfg.FeishuAccountsURL, setFeishu)
	prefs.ChannelStatus = bots.Status
	wxOptions := wechat.Options{APIURL: cfg.WechatAPIURL, CDNURL: cfg.WechatCDNURL, HTTPClient: cfg.WechatHTTPClient, Version: cfg.Version}
	setWechat := func(agentID string, config *wechat.Config) { //nolint:contextcheck // bot lifetime belongs to the manager
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("wechat", nil)
			return
		}
		manager.Set("wechat", wechat.New(*config, wxOptions, q, manager, uploads))
	}
	prefs.ConfigureWechat(wxOptions, conn, setWechat)
	agents, err := q.ListAgents(ctx)
	if err != nil {
		cancel()
		return nil, errors.Join(err, conn.Close())
	}
	// The stored token always exists, so `dex token` works before the host changes.
	stored, err := prefs.AccessToken(ctx)
	if err != nil {
		cancel()
		return nil, errors.Join(err, conn.Close())
	}
	token := cfg.Token
	if token == "" && cfg.RequireToken {
		token = stored
	}

	automations := automation.New(q, chats, time.Now)
	a := &App{db: conn, bots: bots, jobs: jobs, cancel: cancel, handler: server.New(server.Config{
		WorkerReady:   func(ctx context.Context) bool { return w.Call(ctx, "ping", struct{}{}, &struct{}{}) == nil },
		Worker:        w.Call,
		RestartWorker: w.Restart,
		Chats:         chats, Automations: automations, Settings: prefs, Live: hub, DataDir: cfg.DataDir, Version: cfg.Version, Token: token,
	})}
	a.wg.Go(func() { _ = w.Run(runCtx) })
	a.wg.Go(func() { chats.Run(runCtx) })
	every := cmp.Or(cfg.DreamEvery, 24*time.Hour)
	dreamer := dream.New(q, chats, every, time.Now)
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
	for _, agent := range agents {
		tg, err := prefs.TelegramConfig(ctx, agent.ID)
		if err != nil {
			return nil, errors.Join(err, a.Close()) //nolint:contextcheck // Shutdown joins the app lifetime, not the request.
		}
		wx, err := prefs.WechatConfig(ctx, agent.ID)
		if err != nil {
			return nil, errors.Join(err, a.Close()) //nolint:contextcheck // Shutdown joins the app lifetime, not the request.
		}
		fs, err := prefs.FeishuConfig(ctx, agent.ID)
		if err != nil {
			return nil, errors.Join(err, a.Close()) //nolint:contextcheck // Shutdown joins the app lifetime, not the request.
		}
		setTelegram(agent.ID, tg)
		setFeishu(agent.ID, fs)
		setWechat(agent.ID, wx)
	}
	return a, nil
}

// Handler serves the API and the web UI.
func (a *App) Handler() http.Handler { return a.handler }

// Close stops the worker and the bot, waits for the worker to close its storage, and closes the
// database.
func (a *App) Close() error {
	a.cancel()
	err := a.jobs.Close()
	a.bots.Close()
	a.wg.Wait()
	return errors.Join(err, a.db.Close())
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

// agentStreams binds channel previews to their agent.
type agentStreams struct {
	hub     *live.Hub
	agentID string
}

func (s agentStreams) Open(ctx context.Context, chatID int64) (io.ReadCloser, error) {
	return s.hub.Open(ctx, s.agentID, chatID)
}
