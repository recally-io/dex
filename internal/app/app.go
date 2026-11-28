// Package app connects business storage, native execution, HTTP, and channels.
package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/scheduler"
	"github.com/recally-io/dex/internal/server"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/sysops"
	tasksvc "github.com/recally-io/dex/internal/task"
	tasktools "github.com/recally-io/dex/internal/task/tools"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/vault"
)

// DefaultBotID is the bot that the web UI talks to.
const DefaultBotID = "default"

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
	// SkillCatalogURL and McpRegistryURL set catalog origins for tests.
	SkillCatalogURL string
	McpRegistryURL  string
	// FeishuAPIURL and FeishuAccountsURL replace the platform boundaries in tests.
	FeishuAPIURL      string
	FeishuAccountsURL string
	// Wechat API and CDN overrides connect integration tests to local HTTP servers.
	WechatAPIURL     string
	WechatCDNURL     string
	WechatHTTPClient *http.Client
	// Version is the version that GET /api/health reports.
	Version string
	// WebHandler serves the web UI at /. If nil, no web UI is served.
	WebHandler http.Handler
	// DreamEvery is the shortest time between two dreams; zero means 24 hours.
	DreamEvery time.Duration
	// AutomationCheck is the time between two checks for due automations; zero means 1 minute.
	AutomationCheck time.Duration
	// Background sets the limits of background commands; a zero field takes its default.
	Background background.Limits
	// ExecEnv overrides the Bot's Runner backend. A nil value uses the default manager.
	ExecEnv runner.Provider
	// SSHProgram replaces the ssh executable used by the SSH Runner in tests.
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
	dataLock   *os.File
	db         *sql.DB
	engine     *engine.Bots
	envs       *runner.Manager
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
	lock, err := os.OpenFile(filepath.Join(cfg.DataDir, ".dex.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock: %w", err)
	}
	// The CLI and native app can select the same directory. Only one may own
	// recovery, channel jobs, and the bot execution stores at a time.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(fmt.Errorf("workspace is already open in Dex: %s", cfg.DataDir), closeErr)
		}
		return nil, errors.Join(fmt.Errorf("lock workspace: %w", err), closeErr)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, releaseWorkspaceLock(lock))
		}
	}()
	conn, err := db.Open(ctx, filepath.Join(cfg.DataDir, "control.sqlite"))
	if err != nil {
		return nil, err
	}
	q := sqlc.New(conn)
	err = q.CreateBotIfMissing(ctx, sqlc.CreateBotIfMissingParams{
		ID: DefaultBotID, Name: "Dex", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	jobs := scheduler.New()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		artifacts    *artifact.Service
		sessions     *session.Service
		routineTools *automation.ToolService
		prefs        *settings.Service
		botsService  *bot.Service
		hub          *live.Hub
		bots         *channelRegistry
	)
	bg := background.New(conn, time.Now, cfg.Background, func(id int64) { sessions.WakeSession(id) })
	// A previous run owned the processes of running commands. This must precede any new command.
	if err := bg.Recover(ctx); err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, conn.Close())
	}
	tasks := tasksvc.New(conn, func(id int64) { sessions.WakeSession(id) })
	if err := tasks.ExtendActiveLeasesOnStartup(ctx, time.Now(), tasksvc.ExternalLeaseTTL); err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, conn.Close())
	}
	taskEvents := new(tasksvc.Broker)
	tasks.SetOnEntry(taskEvents.Publish)
	plugins, available, pluginErrors, err := startPlugins(ctx, q, cfg.Plugins, plugin.Deps{DB: conn, Tasks: tasks})
	if err != nil {
		cancel()
		jobs.Close()
		return nil, errors.Join(err, conn.Close())
	}
	// These tools bind to services that exist only after the engine.
	botTools := &bot.Tools{}
	var channelTools bot.ChannelTools
	taskToolService := tasktools.New(conn)
	taskToolService.ConfigureTasks(tasks)
	taskTools := taskToolService.Handlers(pluginTools(plugins))
	deliverArtifact := func(ctx context.Context, caller engine.TaskContext, path string) (artifact.Delivery, error) {
		result, err := artifacts.Deliver(ctx, caller, path)
		if err != nil {
			return result, err
		}
		_, file, err := artifacts.Open(caller.BotID, result.Name)
		if err != nil {
			return result, err
		}
		defer file.Close()
		queued, err := bots.Bot(caller.BotID).QueueFile(ctx, caller, result.Name, file)
		if queued {
			result.ChannelDelivery = "queued"
		}
		return result, err
	}
	envs := runner.NewManager(runner.ManagerConfig{
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
		SessionRunner: func(ctx context.Context, ref engine.Ref) (string, error) {
			return sessions.SessionRunner(ctx, ref)
		},
		FakeModel:       cfg.FakeModel,
		FakeInputSuffix: session.BoundRoutineCheckSuffix,
		Extensions: func(rt engine.ExtensionRuntime) engine.ExtensionSet {
			id := rt.BotID
			sessionHandlers := session.Handlers{
				SetProject: sessions.ToolSetProject,
				Sessions: session.Tools{
					New: sessions.ToolSessionNew, Send: sessions.ToolSessionSend, Stop: sessions.ToolSessionStop,
					Fork: sessions.ToolSessionFork, List: sessions.ToolSessionList, Read: sessions.ToolSessionRead, Search: sessions.ToolSessionSearch,
				},
				Runners: func(ctx context.Context, c engine.TaskContext) ([]engine.RunnerInfo, error) {
					return prefs.SessionRunners(ctx, c.BotID)
				},
			}
			outbox := session.OutboxTools{NotifyUser: sessions.ToolNotifyUser, ReportParent: sessions.ToolReportParent, RecentOutbox: sessions.ToolRecentOutbox}
			botHandlers := botTools.Handlers()
			optional := []*engine.Extension{
				session.FrontDeskExtension(), session.OutboxExtension(id, outbox), session.ChildOutboxExtension(id, outbox),
				session.AskUserExtension(id, sessions.ToolAskUser), bot.ReadExtension(id, botHandlers), bot.ManageExtension(id, botHandlers),
			}
			optional = append(optional, bot.ChannelExtensions(id, bot.ChannelTools{
				Read: func(ctx context.Context, c engine.TaskContext, view, bot string) (any, error) {
					return channelTools.Read(ctx, c, view, bot)
				},
				Request: func(ctx context.Context, c engine.TaskContext, bot, op, target string) (any, error) {
					return channelTools.Request(ctx, c, bot, op, target)
				},
			})...)
			mainTaskTools := taskTools
			mainTaskTools.Dispatch = func(ctx context.Context, c engine.TaskContext, args tasktools.DispatchArgs) (tasktools.DispatchResult, error) {
				return taskToolService.DispatchExternal(ctx, c, args, rt, sessions.ToolProcessStart)
			}
			optional = append(optional, tasktools.TaskExtension(id, mainTaskTools), session.ChildExtension(),
				tasktools.ChildTaskExtension(id, taskTools),
				session.DreamMessageExtension(id, sessions.ToolReadDreamMessage), session.RoutineCheckInstructions())
			return engine.ExtensionSet{
				Before: map[string][]*engine.Extension{
					"tool-search": {session.CheckExtension(id, sessions.ToolReportCheck)},
					"vault": {
						automation.ToolExtension(id, automation.ToolHandlers{
							List: routineTools.List, Get: routineTools.Get, Create: routineTools.Create, Update: routineTools.Update, Run: routineTools.Run,
						}),
						session.Extension(id, sessionHandlers), session.ReadExtension(id, sessionHandlers),
						session.MonitorExtension(rt, session.MonitorTools{
							StartProcess: sessions.ToolProcessStart, ListProcesses: sessions.ToolProcessList, StopProcess: sessions.ToolProcessStop,
							Set: sessions.ToolMonitorSet, List: sessions.ToolMonitorList, Cancel: sessions.ToolMonitorCancel,
						}),
					},
					"memory": {session.WorkLoopExtension(), artifact.Extension(rt, deliverArtifact)},
				},
				Optional: optional,
			}
		},
		InputExtensions:   inputExtensions,
		HideToolArguments: bot.IsChannelTool,
		FallbackModel: func(ctx context.Context, botID string) engine.ModelRef {
			row, err := q.GetBot(ctx, botID)
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
	sessions = session.New(conn, native, time.Now, func(key string) { bots.Notify(key) }, func(id int64) {
		hub.Changed(id)
		if taskEvents.HasSubscribers() {
			linked, err := q.HasTaskSession(runCtx, id)
			if err != nil && runCtx.Err() == nil {
				slog.Error("check Task conversation", "err", err)
			}
			if linked {
				taskEvents.Publish(0)
			}
		}
	})
	sessions.ConfigureTasks(tasks)
	sessions.ConfigureBackground(bg)
	automations := automation.New(q, sessions, time.Now)
	automations.ConfigureRunnerStatus(envs.Status)
	// Check admits due Automations one at a time. An offline SSH host can delay
	// the others by this timeout. Probe in parallel if many Automations use SSH.
	automations.ConfigureProbe(func(ctx context.Context, botID, runnerID string) error {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		h, err := envs.Open(ctx, botID, runnerID)
		if err != nil {
			return err
		}
		return h.Close(context.WithoutCancel(ctx))
	})
	routineTools = automation.NewToolService(conn, automations, sessions.WakeSession)
	hub = live.New(sessions, native)
	bots = &channelRegistry{ctx: runCtx, db: conn, sessions: sessions, managers: make(map[string]*botChannels)}
	sessions.ConfigureChannelReceipts(bots.reactionIntake, bots.reactionAdmission)

	a := &App{db: conn, engine: native, envs: envs, bots: bots, jobs: jobs, background: bg, cancel: cancel}
	defer func() { //nolint:contextcheck // Shutdown joins the app lifetime.
		if err != nil {
			err = errors.Join(err, a.Close())
		}
	}()
	setTelegram := func(botID string, config *telegram.Config) {
		channels := bots.Bot(botID)
		uploads := upload.New(bot.Home(cfg.DataDir, botID))
		if config == nil {
			channels.Set("telegram", nil) //nolint:contextcheck // Manager owns the app lifetime, not the settings request.
			if err := channels.CleanupTelegramAlbums(runCtx, uploads); err != nil {
				slog.Warn("telegram: album cleanup", "err", err)
			}
			return
		}
		config.Uploads = uploads
		config.Speech = prefs.SpeechEndpoint
		channels.Set("telegram", telegram.New(*config, channels)) //nolint:contextcheck // Manager owns the app lifetime, not the settings request.
	}
	prefs = settings.New(q, native.Catalog, native, envs)
	if err := prefs.ApplyOnePassword(ctx); err != nil {
		return nil, err
	}
	if err := sessions.RecoverSessions(ctx); err != nil {
		return nil, err
	}
	discovery, err := prefs.RegisterProviders(runCtx)
	if err != nil {
		return nil, err
	}
	channelMu := new(sync.Mutex)
	telegramSetup := telegram.NewSetup(q, conn, channelMu, cfg.TelegramAPIURL, vault.New(cfg.DataDir), setTelegram, func(id string) channel.Status {
		return bots.Status(id, "telegram")
	})
	if err := telegramSetup.LoadManager(ctx); err != nil {
		slog.Warn("telegram: could not open the manager credential")
	}
	botsService = bot.New(q, conn, channelMu, prefs.CheckModel)
	botsService.BindTelegram(telegramSetup)
	botsService.RunningChannelCount = bots.RunningCount
	botsService.OnRunnerChanged = func(changeCtx context.Context, botID, oldEnvID, oldSandboxID string) {
		_ = native.CloseBot(changeCtx, botID)
		if err := envs.ReleaseBot(changeCtx, botID, oldEnvID, oldSandboxID); err != nil {
			slog.Warn("release the earlier Runner of a bot", "agent", botID, "err", err)
		}
	}
	*botTools = bot.Tools{Bots: botsService, Sessions: sessions, Extensions: native.Extensions}
	channelTools = botsService.ChannelToolHandlers(sessions)
	// An approved archive commits in a session receipt; the channels stop right after it.
	sessions.OnBotArchived = func(id string) {
		if err := bg.StopBot(runCtx, id); err != nil {
			slog.Error("stop background commands of an archived bot", "agent", id, "err", err)
		}
		if err := native.CloseBot(runCtx, id); err != nil && !errors.Is(err, engine.ErrBusy) {
			slog.Warn("close runtime resources of an archived bot", "agent", id, "err", err)
		}
		if err := envs.ResetBot(runCtx, id); err != nil {
			slog.Warn("cleanup sandbox of an archived bot", "agent", id, "err", err)
		}
		if err := botsService.StopChannels(runCtx, id); err != nil {
			slog.Error("stop channels of an archived bot", "agent", id, "err", err)
		}
	}
	sessions.ConfigureSessions(prefs.CheckSession)
	var feishuSetup *feishu.SetupService
	setFeishu := func(botID string, config *feishu.Config) { //nolint:contextcheck // The manager owns the bot lifetime.
		manager := bots.Bot(botID).Manager
		uploads := upload.New(bot.Home(cfg.DataDir, botID))
		if config == nil {
			manager.Set("feishu", nil)
			return
		}
		manager.Set("feishu", feishu.New(*config, manager, uploads, func(ctx context.Context, owner, code string) (bool, error) {
			return feishuSetup.Pair(ctx, botID, owner, code)
		}))
	}
	feishuSetup = feishu.NewSetup(q, conn, channelMu, cfg.FeishuAPIURL, cfg.FeishuAccountsURL, setFeishu, func(id string) channel.Status {
		return bots.Status(id, "feishu")
	})
	botsService.BindFeishu(feishuSetup)
	wxOptions := wechat.Options{APIURL: cfg.WechatAPIURL, CDNURL: cfg.WechatCDNURL, HTTPClient: cfg.WechatHTTPClient, Version: cfg.Version}
	setWechat := func(botID string, config *wechat.Config) { //nolint:contextcheck // bot lifetime belongs to the manager
		manager := bots.Bot(botID).Manager
		uploads := upload.New(bot.Home(cfg.DataDir, botID))
		if config == nil {
			manager.Set("wechat", nil)
			return
		}
		manager.Set("wechat", wechat.New(*config, wxOptions, q, manager, uploads))
	}
	wechatSetup := wechat.NewSetup(q, conn, channelMu, wxOptions, setWechat, func(id string) channel.Status {
		return bots.Status(id, "wechat")
	})
	botsService.BindWechat(wechatSetup)
	activeBots, err := q.ListBots(ctx)
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
		AcquireMaintenance: sessions.AcquireMaintenance,
		ReleaseMaintenance: sessions.ReleaseMaintenance,
		RequestRestart:     cfg.RequestRestart,
		IsBusyErr:          func(err error) bool { return errors.Is(err, session.ErrMaintenanceBusy) },
		WaitInterval:       cfg.SystemWaitInterval,
		WaitTimeout:        cfg.SystemWaitTimeout,
		RestartDelay:       cfg.SystemRestartDelay,
	})
	a.sysops = sys

	forRunnerBots := func(ctx context.Context, id string, fn func(string)) {
		bots, err := q.ListActiveBotsByRunner(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("find bots that use the Runner", "runner", id, "err", err)
			}
			return
		}
		for _, bot := range bots {
			fn(bot.ID)
		}
	}
	// Restore the default even when no input is waiting for the machine.
	envs.Runners().OnOnline(func(id string) {
		forRunnerBots(runCtx, id, func(botID string) { native.MarkRestorable(runCtx, botID) })
		sessions.RetryNow()
	})
	a.handler = server.New(server.Config{
		WebHandler:      cfg.WebHandler,
		ChannelWebhooks: http.HandlerFunc(bots.webhook),
		Telegram:        telegramSetup,
		Feishu:          feishuSetup,
		Wechat:          wechatSetup,
		SkillCatalogURL: cfg.SkillCatalogURL,
		McpRegistryURL:  cfg.McpRegistryURL,
		Tasks:           tasks,
		TaskEvents:      taskEvents,
		Plugins:         plugins, AvailablePlugins: available, PluginErrors: pluginErrors,
		Engine: native,
		Envs:   envs,
		RunnerChanged: func(ctx context.Context, id string) {
			forRunnerBots(context.WithoutCancel(ctx), id, func(botID string) {
				native.MarkStale(botID, "Runner settings")
			})
			native.MarkRunnerChanged(id)
		},
		Runners:     envs.Runners(),
		System:      sys,
		ReleaseBase: cfg.ReleaseBase,
		Sessions:    sessions, Automations: automations, Settings: prefs, Bots: botsService, Artifacts: artifacts, Live: hub, DataDir: cfg.DataDir, Version: cfg.Version, Token: token,
	})
	a.wg.Go(func() { sessions.Run(runCtx) })
	a.wg.Go(func() { tasks.RunLeaseWatchdog(runCtx) })
	a.wg.Go(func() { runReloads(runCtx, native, sessions) })
	a.wg.Go(func() {
		select {
		case <-discovery.Done():
			sessions.Wake()
		case <-runCtx.Done():
		}
	})
	every := cmp.Or(cfg.DreamEvery, 24*time.Hour)
	dreamer := dream.New(q, sessions.Post, every, time.Now)
	if err := jobs.Every("dream", min(every, time.Hour), dreamer.Check); err != nil {
		return nil, err
	}
	if err := jobs.Every("automations", cmp.Or(cfg.AutomationCheck, time.Minute), automations.Check); err != nil {
		return nil, err
	}
	if err := jobs.Every("followup-monitors", min(cmp.Or(cfg.AutomationCheck, 5*time.Second), 5*time.Second), func(ctx context.Context) {
		if err := sessions.FireDueMonitors(ctx); err != nil && ctx.Err() == nil {
			slog.Error("fire due followup monitors", "err", err)
		}
	}); err != nil {
		return nil, err
	}
	if err := jobs.Every("channel-webhook-prune", time.Hour, func(ctx context.Context) {
		cutoff := time.Now().Add(-(channel.WebhookRetention + channel.WebhookFutureSkew)).UnixMilli()
		if err := q.PruneHandledChannelWebhooks(ctx, cutoff); err != nil && ctx.Err() == nil {
			slog.Warn("prune handled channel webhooks", "err", err)
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
	if err := jobs.Every("telegram-manager", time.Second, telegramSetup.ManagerPoll()); err != nil {
		return nil, err
	}
	for _, bot := range activeBots {
		tg, err := telegramSetup.Config(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		wx, err := wechatSetup.Config(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		fs, err := feishuSetup.Config(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		if tg == nil || db.CanStartChannel(ctx, q, bot.ID) {
			setTelegram(bot.ID, tg)
		}
		setFeishu(bot.ID, fs)
		if wx == nil || db.CanStartChannel(ctx, q, bot.ID) {
			setWechat(bot.ID, wx)
		}
	}
	jobs.Start(runCtx)
	a.dataLock = lock
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
	err := errors.Join(a.engine.Close(context.Background()), a.envs.Close(context.Background()), a.db.Close())
	if a.dataLock != nil {
		err = errors.Join(err, releaseWorkspaceLock(a.dataLock))
	}
	return err
}

func releaseWorkspaceLock(file *os.File) error {
	// A concurrent fork can hold the descriptor until it execs. Release the
	// lock before close so reopening does not wait for that process.
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
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
func runReloads(ctx context.Context, native *engine.Bots, sessions *session.Service) {
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
		// One signal can represent multiple bots. A concurrent signal can be
		// consumed here because the snapshot below includes its dirty bots.
		select {
		case <-native.ReloadWake():
		default:
		}
		var batch sync.WaitGroup
		for _, id := range native.ReadyReloads() {
			batch.Go(func() { _ = sessions.ReloadDirtyExtensions(ctx, id) })
		}
		batch.Wait()
	}
}
