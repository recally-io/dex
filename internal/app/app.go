// Package app connects business storage, native execution, HTTP, and channels.
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
	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/scheduler"
	"github.com/recally-io/dex/internal/server"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/sysops"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/vault"
	"github.com/recally-io/dex/internal/worktask"
	worktasktools "github.com/recally-io/dex/internal/worktask/tools"
)

// DefaultAgentID is the agent that the web UI talks to.
const DefaultAgentID = "default"

// Config configures an App.
type Config struct {
	DataDir string
	Home    string
	ToolEnv map[string]string
	// LoginEnv captures the login shell environment for tools, started from ToolEnv; nil keeps
	// ToolEnv.
	LoginEnv  func(ctx context.Context, base map[string]string) (map[string]string, error)
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
	// Background sets the limits of background commands; a zero field takes its default.
	Background background.Limits
	// ExecEnv overrides the agent execution environment backend; nil uses the multi-driver manager.
	ExecEnv execenv.Provider
	// SSHProgram replaces the ssh executable used by pi-env in tests.
	SSHProgram string
	// E2BAPIURL and E2BEnvdURL replace the E2B Cloud and sandbox envd endpoints in tests.
	E2BAPIURL  string
	E2BEnvdURL string
	// Token is the access token that the API requires. Empty means the stored token when
	// RequireToken is set, and no authentication otherwise.
	Token        string
	RequireToken bool
	// Plugins are the optional sources of this build. cmd/dex lists them. The owner turns each
	// one on or off in plugin_settings, and dex starts only the enabled ones.
	Plugins []plugin.Factory
	// System operations hooks (optional overrides for tests and cmd/dex re-exec).
	Executable         string
	ReleaseBase        string
	Logs               *sysops.LogBuffer
	RequestRestart     func(execPath string)
	SystemWaitInterval time.Duration
	SystemWaitTimeout  time.Duration
	SystemRestartDelay time.Duration
}

// App is a running control process.
type App struct {
	db         *sql.DB
	engine     *engine.Agents
	envs       *execenv.Manager
	sysops     *sysops.Manager
	handler    http.Handler
	bots       *channelRegistry
	jobs       *scheduler.Scheduler
	background *background.Manager
	cancel     context.CancelFunc
	wg         sync.WaitGroup
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
	warnLegacyMCPFiles(cfg.DataDir)
	err = q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{
		ID: DefaultAgentID, Name: "Dex", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if err := migrateAgentInstructions(ctx, conn, cfg); err != nil {
		slog.Warn("import legacy bot instructions", "err", err)
	}
	jobs := scheduler.New()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		artifacts     *artifact.Service
		chats         *chat.Service
		routineTools  *automation.ToolService
		prefs         *settings.Service
		agentsService *agent.Service
		hub           *live.Hub
		bots          *channelRegistry
	)
	bg := background.New(conn, time.Now, cfg.Background, func(id int64) { chats.WakeConversation(id) })
	// A previous run owned the processes of running commands. This must precede any new command.
	if err := bg.Recover(ctx); err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, conn.Close())
	}
	workTasks := worktask.New(conn, func(id int64) { chats.WakeConversation(id) })
	plugins, available, pluginErrors, err := startPlugins(ctx, q, cfg.Plugins, plugin.Deps{DB: conn, WorkTasks: workTasks})
	if err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, conn.Close())
	}
	tools := engine.TaskTools{
		AuthorizeGroup: func(ctx context.Context, c engine.TaskContext) error { return chats.AuthorizeGroup(ctx, c) },
		GroupQuestion: func(ctx context.Context, c engine.TaskContext, args engine.AskUserArgs) (engine.QuestionInfo, error) {
			return chats.ToolGroupQuestion(ctx, c, args)
		},
		GroupSession: func(ctx context.Context, c engine.TaskContext, args engine.GroupSessionArgs) (engine.GroupSessionResult, error) {
			return chats.ToolGroupSession(ctx, c, args)
		},
		DeliverGroupFile: func(ctx context.Context, caller engine.TaskContext, name string, reader io.Reader) error {
			queued, err := bots.Agent(caller.AgentID).QueueFile(ctx, caller, name, reader)
			if err != nil {
				return err
			}
			if !queued {
				return errors.New("group file delivery needs its active top-level Telegram source")
			}
			return nil
		},
		ReadDreamMessage: func(ctx context.Context, c engine.TaskContext, request engine.DreamMessageRequest) (engine.DreamMessagePage, error) {
			return chats.ToolReadDreamMessage(ctx, c, request)
		},
		DeliverArtifact: func(ctx context.Context, caller engine.TaskContext, path string) (engine.ArtifactInfo, error) {
			result, err := artifacts.Deliver(ctx, caller, path)
			if err != nil {
				return result, err
			}
			_, file, err := artifacts.Open(caller.AgentID, result.Name)
			if err != nil {
				return result, err
			}
			defer file.Close()
			queued, err := bots.Agent(caller.AgentID).QueueFile(ctx, caller, result.Name, file)
			if queued {
				result.ChannelDelivery = "queued"
			}
			return result, err
		},
		ReportCheck: func(ctx context.Context, c engine.TaskContext, r engine.CheckReport) error {
			return chats.ToolReportCheck(ctx, c, r)
		},
		SetProject: func(ctx context.Context, c engine.TaskContext, path string) (string, error) {
			return chats.ToolSetProject(ctx, c, path)
		},
		Sessions: engine.SessionTools{
			New: func(ctx context.Context, c engine.TaskContext, a engine.SessionNewArgs) (engine.SessionRef, error) {
				return chats.ToolSessionNew(ctx, c, a)
			},
			Send: func(ctx context.Context, c engine.TaskContext, a engine.SessionSendArgs) (engine.SessionRef, error) {
				return chats.ToolSessionSend(ctx, c, a)
			},
			Stop: func(ctx context.Context, c engine.TaskContext, id string) (engine.SessionRef, error) {
				return chats.ToolSessionStop(ctx, c, id)
			},
			Fork: func(ctx context.Context, c engine.TaskContext, a engine.SessionForkArgs) (engine.SessionRef, error) {
				return chats.ToolSessionFork(ctx, c, a)
			},
			List: func(ctx context.Context, c engine.TaskContext, a engine.SessionListArgs) ([]engine.SessionInfo, error) {
				return chats.ToolSessionList(ctx, c, a)
			},
			Read: func(ctx context.Context, c engine.TaskContext, a engine.SessionReadArgs) (engine.SessionPage, error) {
				return chats.ToolSessionRead(ctx, c, a)
			},
			Search: func(ctx context.Context, c engine.TaskContext, a engine.SessionSearchArgs) ([]engine.SessionMatch, error) {
				return chats.ToolSessionSearch(ctx, c, a)
			},
		},
		AskUser: func(ctx context.Context, c engine.TaskContext, a engine.AskUserArgs) (engine.QuestionInfo, error) {
			return chats.ToolAskUser(ctx, c, a)
		},
		Outbox: engine.OutboxTools{
			NotifyUser: func(ctx context.Context, c engine.TaskContext, a engine.NotifyUserArgs) (string, error) {
				return chats.ToolNotifyUser(ctx, c, a)
			},
			ReportParent: func(ctx context.Context, c engine.TaskContext, a engine.ReportParentArgs) (string, error) {
				return chats.ToolReportParent(ctx, c, a)
			},
			RecentOutbox: func(ctx context.Context, ref engine.Ref) ([]engine.OutboxSummary, error) {
				return chats.ToolRecentOutbox(ctx, ref)
			},
		},
		Environments: func(ctx context.Context, c engine.TaskContext) ([]engine.EnvironmentInfo, error) {
			return prefs.SessionEnvironments(ctx, c.AgentID)
		},
		AuthorizeMonitor: func(ctx context.Context, c engine.TaskContext) error { return chats.AuthorizeMonitor(ctx, c) },
		Monitors: engine.MonitorTools{
			Command: func(ctx context.Context, c engine.TaskContext, a engine.MonitorCommandArgs) (engine.MonitorInfo, error) {
				return chats.ToolMonitorCommand(ctx, c, a)
			},
			Set: func(ctx context.Context, c engine.TaskContext, a engine.MonitorSetArgs) (engine.MonitorInfo, error) {
				return chats.ToolMonitorSet(ctx, c, a)
			},
			List: func(ctx context.Context, c engine.TaskContext) ([]engine.MonitorInfo, error) {
				return chats.ToolMonitorList(ctx, c)
			},
			Cancel: func(ctx context.Context, c engine.TaskContext, id string) (engine.MonitorInfo, error) {
				return chats.ToolMonitorCancel(ctx, c, id)
			},
		},
	}
	routines := engine.RoutineTools{
		List: func(ctx context.Context, c engine.TaskContext) ([]engine.RoutineInfo, error) {
			return routineTools.List(ctx, c)
		},
		Get: func(ctx context.Context, c engine.TaskContext, id string) (engine.RoutineInfo, error) {
			return routineTools.Get(ctx, c, id)
		},
		Create: func(ctx context.Context, c engine.TaskContext, p engine.RoutinePatch) (engine.RoutineInfo, error) {
			return routineTools.Create(ctx, c, p)
		},
		Update: func(ctx context.Context, c engine.TaskContext, p engine.RoutinePatch) (engine.RoutineInfo, error) {
			return routineTools.Update(ctx, c, p)
		},
		Run: func(ctx context.Context, c engine.TaskContext, id string) (engine.RoutineRun, error) {
			return routineTools.Run(ctx, c, id)
		},
	}
	// These tools bind to services that exist only after the engine.
	botTools := &agent.BotTools{}
	var channelTools engine.ChannelTools
	envs := execenv.NewManager(execenv.ManagerConfig{
		DataDir:    cfg.DataDir,
		SSHHome:    cfg.Home,
		Queries:    q,
		SSHProgram: cfg.SSHProgram,
		E2BAPIURL:  cfg.E2BAPIURL,
		E2BEnvdURL: cfg.E2BEnvdURL,
	})
	execProvider := cfg.ExecEnv
	if execProvider == nil {
		execProvider = envs
	}
	native, err := engine.New(engine.Config{
		MCPServers: engine.NewMCPStore(conn),
		DataDir:    cfg.DataDir,
		Home:       cfg.Home,
		BaseEnv:    cfg.ToolEnv,
		LoginEnv:   cfg.LoginEnv,
		ExecEnv:    execProvider,
		SessionEnvironment: func(ctx context.Context, ref engine.Ref) (string, error) {
			return chats.SessionEnvironment(ctx, ref)
		},
		FakeModel: cfg.FakeModel,
		Tasks:     tools,
		Routines:  routines,
		Bots:      botTools.Handlers(),
		Channels: engine.ChannelTools{
			Read: func(ctx context.Context, c engine.TaskContext, view, bot string) (any, error) {
				return channelTools.Read(ctx, c, view, bot)
			},
			Request: func(ctx context.Context, c engine.TaskContext, bot, op, target string) (any, error) {
				return channelTools.Request(ctx, c, bot, op, target)
			},
		},
		WorkTasks: worktasktools.New(conn).Handlers(pluginTools(plugins)),
		FallbackModel: func(ctx context.Context, agentID string) engine.ModelRef {
			row, err := q.GetAgent(ctx, agentID)
			if err != nil {
				return engine.ModelRef{}
			}
			return engine.ModelRef{Provider: row.FallbackProvider, ModelID: row.FallbackModelID}
		},
	})
	if err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, envs.Close(context.WithoutCancel(ctx)), conn.Close())
	}
	artifacts = artifact.New(cfg.DataDir, native.ReadWorkspaceFile)
	bg.ConfigureEnvOpener(native.ExecEnv)
	chats = chat.New(conn, native, time.Now, func(key string) { bots.Notify(key) }, func(id int64) { hub.Changed(id) })
	chats.ConfigureBackground(bg)
	automations := automation.New(q, chats, time.Now)
	automations.ConfigureEnvironmentStatus(envs.Status)
	// Check admits due routines one at a time, so a dead host delays the others by this timeout;
	// probe in parallel if many routines use SSH hosts.
	automations.ConfigureProbe(func(ctx context.Context, agentID, environmentID string) error {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		h, err := envs.Open(ctx, agentID, environmentID)
		if err != nil {
			return err
		}
		return h.Close(context.WithoutCancel(ctx))
	})
	routineTools = automation.NewToolService(conn, automations, chats.WakeConversation)
	hub = live.New(chats, native)
	bots = &channelRegistry{ctx: runCtx, db: conn, chats: chats, managers: make(map[string]*delivery.Manager)}
	chats.ConfigureChannelReceipts(bots.reactionIntake, bots.reactionAdmission)

	a := &App{db: conn, engine: native, envs: envs, bots: bots, jobs: jobs, background: bg, cancel: cancel}
	defer func() { //nolint:contextcheck // Shutdown joins the app lifetime.
		if err != nil {
			err = errors.Join(err, a.Close())
		}
	}()
	setTelegram := func(agentID string, config *telegram.Config) {
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("telegram", nil) //nolint:contextcheck // Manager owns the app lifetime, not the settings request.
			if err := manager.CleanupTelegramAlbums(runCtx, uploads); err != nil {
				slog.Warn("telegram: album cleanup", "err", err)
			}
			return
		}
		config.Uploads = uploads
		config.Speech = prefs.SpeechEndpoint
		config.Watch = func(ctx context.Context, chatID int64) (io.ReadCloser, error) { return hub.Open(ctx, agentID, chatID) }
		manager.Set("telegram", telegram.New(*config, manager)) //nolint:contextcheck // Manager owns the app lifetime, not the settings request.
	}
	prefs = settings.New(q, native.Catalog, native, envs)
	if err := prefs.ApplyOnePassword(ctx); err != nil {
		return nil, err
	}
	if err := chats.RecoverSessions(ctx); err != nil {
		return nil, err
	}
	discovery, err := prefs.RegisterProviders(runCtx)
	if err != nil {
		return nil, err
	}
	agentsService = agent.New(q, prefs.CheckModel, cfg.TelegramAPIURL, setTelegram)
	if err := agentsService.ConfigureTelegramVault(ctx, vault.New(cfg.DataDir)); err != nil {
		slog.Warn("telegram: could not open the manager credential")
	}
	agentsService.OnEnvironmentChanged = func(changeCtx context.Context, agentID, oldEnvID, oldSandboxID string) {
		_ = native.CloseAgent(changeCtx, agentID)
		if err := envs.ReleaseAgent(changeCtx, agentID, oldEnvID, oldSandboxID); err != nil {
			slog.Warn("release the earlier execution environment of a bot", "agent", agentID, "err", err)
		}
	}
	*botTools = agent.BotTools{Agents: agentsService, Chats: chats, Extensions: native.Extensions}
	channelTools = agentsService.ChannelToolHandlers(chats)
	// An approved archive commits in a chat receipt; the channels stop right after it.
	chats.OnBotArchived = func(id string) {
		if err := bg.StopAgent(runCtx, id); err != nil {
			slog.Error("stop background commands of an archived bot", "agent", id, "err", err)
		}
		if err := native.CloseAgent(runCtx, id); err != nil && !errors.Is(err, engine.ErrBusy) {
			slog.Warn("close runtime resources of an archived bot", "agent", id, "err", err)
		}
		if err := envs.ResetAgent(runCtx, id); err != nil {
			slog.Warn("cleanup sandbox of an archived bot", "agent", id, "err", err)
		}
		if err := agentsService.StopChannels(runCtx, id); err != nil {
			slog.Error("stop channels of an archived bot", "agent", id, "err", err)
		}
	}
	chats.ConfigureSessions(prefs.CheckChat)
	setFeishu := func(agentID string, config *feishu.Config) { //nolint:contextcheck // The manager owns the bot lifetime.
		manager := bots.Agent(agentID)
		uploads := upload.New(agent.Home(cfg.DataDir, agentID))
		if config == nil {
			manager.Set("feishu", nil)
			return
		}
		manager.Set("feishu", feishu.New(*config, manager, func(ctx context.Context, chatID int64) (io.ReadCloser, error) { return hub.Open(ctx, agentID, chatID) }, uploads, func(ctx context.Context, owner, code string) (bool, error) {
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

	logs := cfg.Logs
	if logs == nil {
		logs = sysops.NewLogBuffer(0)
	}
	sys := sysops.New(sysops.Config{
		DB:                 conn,
		DataDir:            cfg.DataDir,
		Version:            cfg.Version,
		Executable:         cfg.Executable,
		ReleaseBase:        cfg.ReleaseBase,
		Logs:               logs,
		AcquireMaintenance: chats.AcquireMaintenance,
		ReleaseMaintenance: chats.ReleaseMaintenance,
		RequestRestart:     cfg.RequestRestart,
		IsBusyErr:          func(err error) bool { return errors.Is(err, chat.ErrMaintenanceBusy) },
		WaitInterval:       cfg.SystemWaitInterval,
		WaitTimeout:        cfg.SystemWaitTimeout,
		RestartDelay:       cfg.SystemRestartDelay,
	})
	a.sysops = sys

	forEnvironmentBots := func(ctx context.Context, id string, fn func(string)) {
		bots, err := q.ListActiveAgentsByEnvironment(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("find bots that use the environment", "environment", id, "err", err)
			}
			return
		}
		for _, bot := range bots {
			fn(bot.ID)
		}
	}
	// Restore the default even when no input is waiting for the machine.
	envs.Runners().OnOnline(func(id string) {
		forEnvironmentBots(runCtx, id, func(agentID string) { native.MarkRestorable(runCtx, agentID) })
		chats.RetryNow()
	})
	a.handler = server.New(server.Config{
		WorkTasks: workTasks,
		Plugins:   plugins, AvailablePlugins: available, PluginErrors: pluginErrors,
		Engine: native,
		Envs:   envs,
		EnvironmentChanged: func(ctx context.Context, id string) {
			forEnvironmentBots(context.WithoutCancel(ctx), id, func(agentID string) {
				native.MarkStale(agentID, "execution environment settings")
			})
			native.MarkEnvironmentChanged(id)
		},
		Runners:     envs.Runners(),
		System:      sys,
		ReleaseBase: cfg.ReleaseBase,
		Chats:       chats, Automations: automations, Settings: prefs, Agents: agentsService, Artifacts: artifacts, Live: hub, DataDir: cfg.DataDir, Version: cfg.Version, Token: token,
	})
	a.wg.Go(func() { chats.Run(runCtx) })
	a.wg.Go(func() { runReloads(runCtx, native, chats) })
	a.wg.Go(func() {
		select {
		case <-discovery.Done():
			chats.Wake()
		case <-runCtx.Done():
		}
	})
	every := cmp.Or(cfg.DreamEvery, 24*time.Hour)
	dreamer := dream.New(q, chats.Post, every, time.Now)
	if err := jobs.Every("dream", min(every, time.Hour), dreamer.Check); err != nil {
		return nil, err
	}
	if err := jobs.Every("automations", cmp.Or(cfg.AutomationCheck, time.Minute), automations.Check); err != nil {
		return nil, err
	}
	if err := jobs.Every("followup-monitors", min(cmp.Or(cfg.AutomationCheck, 5*time.Second), 5*time.Second), func(ctx context.Context) {
		if err := chats.FireDueMonitors(ctx); err != nil && ctx.Err() == nil {
			slog.Error("fire due followup monitors", "err", err)
		}
	}); err != nil {
		return nil, err
	}
	for _, p := range plugins {
		for _, job := range p.Jobs {
			if err := jobs.Every(p.Name+"/"+job.Name, job.Every, job.Run); err != nil {
				return nil, err
			}
		}
	}
	if err := jobs.Every("telegram-manager", time.Second, agentsService.TelegramManagerPoll()); err != nil {
		return nil, err
	}
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
	jobs.Start(runCtx)
	return a, nil
}

// Handler serves the API and the web UI.
func (a *App) Handler() http.Handler { return a.handler }

// Close stops business services, closes native execution, and closes business storage.
func (a *App) Close() error {
	a.sysops.Close()
	a.jobs.Close()
	a.bots.Close()
	a.cancel()
	a.wg.Wait()
	a.background.Close()
	return errors.Join(a.engine.Close(context.Background()), a.envs.Close(context.Background()), a.db.Close())
}

// ToolEnv selects the process values that tools inherit when the login shell is not available.
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

// runReloads waits for a change or settlement. The one-shot delay groups writes
// and lets the Harness finish its turn before the idle check.
func runReloads(ctx context.Context, native *engine.Agents, chats *chat.Service) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-native.ReloadWake():
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		// One signal can represent multiple agents. A concurrent signal can be
		// consumed here because the snapshot below includes its dirty agents.
		select {
		case <-native.ReloadWake():
		default:
		}
		var batch sync.WaitGroup
		for _, id := range native.ReadyReloads() {
			batch.Go(func() { _ = chats.ReloadDirtyExtensions(ctx, id) })
		}
		batch.Wait()
	}
}
