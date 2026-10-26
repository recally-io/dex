// Package app connects business storage, native execution, HTTP, and channels.
package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
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
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/feishu"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/scheduler"
	"github.com/recally-io/dex/internal/server"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/telegram"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/wechat"
)

// DefaultAgentID is the agent that the web UI talks to.
const DefaultAgentID = "default"

// Config configures an App.
type Config struct {
	DataDir   string
	Home      string
	ToolEnv   map[string]string
	FakeModel bool
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
	engine  *engine.Agents
	handler http.Handler
	bots    *channelRegistry
	jobs    *scheduler.Scheduler
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Start restores task cancellation, applies settings, and starts business services.
func Start(ctx context.Context, cfg Config) (_ *App, err error) {
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
	jobs, err := scheduler.New()
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		chats         *chat.Service
		prefs         *settings.Service
		agentsService *agent.Service
		hub           *live.Hub
		bots          *channelRegistry
	)
	tools := engine.TaskTools{
		SetProject: func(ctx context.Context, c engine.TaskContext, path string) (string, error) {
			return chats.ToolSetProject(ctx, c, path)
		},
		Spawn: func(ctx context.Context, c engine.TaskContext, args engine.SpawnTaskArgs) (engine.TaskInfo, error) {
			t, err := chats.SpawnTask(ctx, c, args)
			return engine.TaskInfo(t), err
		},
		List: func(ctx context.Context, c engine.TaskContext) ([]engine.TaskInfo, error) {
			tasks, err := chats.ToolListTasks(ctx, c)
			if err != nil {
				return nil, err
			}
			out := make([]engine.TaskInfo, len(tasks))
			for i, t := range tasks {
				out[i] = engine.TaskInfo(t)
			}
			return out, nil
		},
		Get: func(ctx context.Context, c engine.TaskContext, id string) (engine.TaskInfo, error) {
			t, err := chats.ToolGetTask(ctx, c, id)
			return engine.TaskInfo(t), err
		},
		Cancel: func(ctx context.Context, c engine.TaskContext, id string) (engine.TaskInfo, error) {
			t, err := chats.ToolCancelTask(ctx, c, id)
			return engine.TaskInfo(t), err
		},
	}
	native, err := engine.New(engine.Config{DataDir: cfg.DataDir, Home: cfg.Home, BaseEnv: cfg.ToolEnv, FakeModel: cfg.FakeModel, Tasks: tools})
	if err != nil {
		cancel()
		return nil, errors.Join(err, jobs.Close(), conn.Close())
	}
	chats = chat.New(conn, native, time.Now, func(key string) { bots.Notify(key) }, func(id int64) { hub.Changed(id) })
	hub = live.New(chats, native)
	bots = &channelRegistry{ctx: runCtx, db: conn, chats: chats, managers: make(map[string]*channel.Manager)}
	a := &App{db: conn, engine: native, bots: bots, jobs: jobs, cancel: cancel}
	defer func() { //nolint:contextcheck // Shutdown joins the app lifetime.
		if err != nil {
			err = errors.Join(err, a.Close())
		}
	}()
	setTelegram := func(agentID string, config *telegram.Config) { //nolint:contextcheck // bots live until app shutdown, not the settings request
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("telegram", nil)
			return
		}
		config.Uploads = uploads
		config.Watch = agentStreams{hub, agentID}.Open
		config.Report = func(state, reason string) { manager.Report("telegram", channel.Status{State: state, Reason: reason}) }
		manager.Set("telegram", telegram.New(*config, manager))
	}
	prefs = settings.New(q, native.Catalog)
	if err := chats.RecoverTasks(ctx); err != nil {
		return nil, err
	}
	discovery, err := prefs.RegisterProviders(runCtx)
	if err != nil {
		return nil, err
	}
	agentsService = agent.New(q, prefs.CheckModel, cfg.TelegramAPIURL, setTelegram)
	chats.ConfigureTasks(prefs.CheckChat)
	setFeishu := func(agentID string, config *feishu.Config) { //nolint:contextcheck // The manager owns the bot lifetime.
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("feishu", nil)
			return
		}
		manager.Set("feishu", feishu.New(*config, manager, agentStreams{hub, agentID}, uploads, func(ctx context.Context, owner, code string) (bool, error) {
			return agentsService.PairFeishu(ctx, agentID, owner, code)
		}))
	}
	agentsService.ConfigureFeishu(cfg.FeishuAPIURL, cfg.FeishuAccountsURL, setFeishu)
	agentsService.ChannelStatus = bots.Status
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
	agentsService.ConfigureWechat(wxOptions, conn, setWechat)
	agents, err := q.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	// The stored token always exists, so `dex token` works before the host changes.
	stored, err := prefs.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	token := cfg.Token
	if token == "" && cfg.RequireToken {
		token = stored
	}

	automations := automation.New(q, chats, time.Now)
	a.handler = server.New(server.Config{
		RuntimeReady: func(ctx context.Context) bool { return native.Health(ctx) == nil },
		Engine:       native,
		Chats:        chats, Automations: automations, Settings: prefs, Agents: agentsService, Live: hub, DataDir: cfg.DataDir, Version: cfg.Version, Token: token,
	})
	a.wg.Go(func() { chats.Run(runCtx) })
	a.wg.Go(func() {
		select {
		case <-discovery.Done():
			chats.Wake()
		case <-runCtx.Done():
		}
	})
	every := cmp.Or(cfg.DreamEvery, 24*time.Hour)
	dreamer := dream.New(q, chats, every, time.Now)
	err = errors.Join(
		jobs.Every("dream", min(every, time.Hour), dreamer.Check),
		jobs.Every("automations", cmp.Or(cfg.AutomationCheck, time.Minute), automations.Check),
	)
	if err != nil {
		return nil, err
	}
	jobs.Start()
	for _, agent := range agents {
		tg, err := agentsService.TelegramConfig(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		wx, err := agentsService.WechatConfig(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		fs, err := agentsService.FeishuConfig(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		setTelegram(agent.ID, tg)
		setFeishu(agent.ID, fs)
		setWechat(agent.ID, wx)
	}
	return a, nil
}

// Handler serves the API and the web UI.
func (a *App) Handler() http.Handler { return a.handler }

// Close stops business services, closes native execution, and closes business storage.
func (a *App) Close() error {
	err := a.jobs.Close()
	a.bots.Close()
	a.cancel()
	a.wg.Wait()
	return errors.Join(err, a.engine.Close(context.Background()), a.db.Close())
}

// ToolEnv selects the process values that tools can inherit.
// Provider credentials enter the model catalog through Settings.
func ToolEnv(environ []string) map[string]string {
	env := make(map[string]string)
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "HOME", "TMPDIR", "LANG", "USER", "SHELL":
			env[name] = value
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
