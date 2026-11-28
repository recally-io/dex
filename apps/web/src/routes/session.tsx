import type { BotSettings as Settings } from "../lib/bot-scope";
import { useBotSettings, useBotId } from "../lib/bot-scope";
import { useEffect, useMemo, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowDown, Clock, GitBranch, Lock, Send } from "lucide-react";
import type { Attachment, UserItem, MessageInput, Session as SessionInfo } from "../api/generated/types.gen";
import {
  getSessionOptions,
  getHealthOptions,
  getMainSessionOptions,
  sendMessageMutation,
  abortSessionMutation,
  continueSessionMutation,
  compactSessionMutation,
  forkSessionMutation,
  withdrawMessageMutation,
  listModelsOptions,
  listSessionsOptions,
  listBotsOptions,
  listBotRunnersOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { mergeHistory, retryMessage } from "../lib/session-state";
import { useSessionStream } from "../lib/session-stream";
import { errorText, refreshSessions, refreshBoardAttention } from "../lib/queries";
import { botModel, findModel } from "../lib/bot";
import { useApp } from "../lib/app-state";
import { useAttachments } from "../lib/attachments";
import { sessionKind, sessionTime } from "../lib/sessions";
import { SessionActivity } from "../components/session-activity";
import { SessionSettings } from "../components/session-settings";
import { useRunnerSendBlock } from "../components/session-runner";
import { sessionDraftEvent, putSessionDraft, takeSessionDraft } from "../lib/session-draft";
import { Composer, type SendMode } from "../components/composer";
import { hasContext, useClearContext, LoadEarlierButton } from "../components/timeline-actions";
import { QuestionCard } from "../components/bot/question-card";
import { ThreadContext } from "../components/bot/thread";
import { ThreadPanel } from "../components/bot/thread-panel";
import type { ThreadRef } from "../lib/threads";
import {
  RetryNotice,
  Transcript,
  type MessageActions,
  type QueuedActions,
} from "../components/bot/transcript";
import type { SlashCommand } from "../components/slash-commands";
import { ModelPicker } from "../components/model-picker";
import { Failure, Loading } from "../components/feedback";
import { MainPane, Note } from "../components/page";
import { Button, buttonVariants } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { cn, newId } from "../lib/utils";

export function SessionPage({ sessionId }: { sessionId?: number }) {
  const botId = useBotId();
  const settings = useBotSettings(botId);

  return settings.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : settings.isError ? (
    <MainPane>
      <div className="mx-auto max-w-[720px] px-4 py-10 sm:px-8">
        <Failure
          title="Could not load settings"
          error={settings.error}
          retry={() => void settings.refetch()}
        />
      </div>
    </MainPane>
  ) : sessionId ? (
    <Session key={sessionId} sessionId={sessionId} settings={settings.data} />
  ) : (
    <MainTimeline settings={settings.data} />
  );
}

export function MainTimeline({ settings }: { settings: Settings }) {
  const botId = useBotId();
  const main = useQuery(getMainSessionOptions({ path: { botId } }));

  return main.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : main.isError ? (
    <MainPane>
      <Failure title="Could not open the timeline" error={main.error} retry={() => void main.refetch()} />
    </MainPane>
  ) : (
    <Session key={main.data.id} sessionId={main.data.id} settings={settings} main />
  );
}

// A child session links to the session that started it. The parent is Main or a session.
// A parent can belong to another bot, which then names it.
function ParentLink({
  parentId,
  parentBotId,
  parentBotName,
}: {
  parentId: number;
  parentBotId?: string;
  parentBotName?: string;
}) {
  const own = useBotId();
  const botId = parentBotId ?? own;
  const parent = useQuery(getSessionOptions({ path: { botId, sessionId: parentId } }));
  const title = parent.data ? (parent.data.main ? "Main" : parent.data.title || "a session") : "…";
  const name = parentBotName ? `${title} in ${parentBotName}` : title;

  const className = cn(
    buttonVariants({ variant: "ghost", size: "sm" }),
    "max-w-40 gap-1 px-1.5 text-muted-foreground",
  );

  const label = `Started by ${name}`;

  const content = (
    <>
      <GitBranch aria-hidden="true" />
      <span className="hidden truncate sm:inline">
        {parentBotName ? `${parentBotName} · ${title}` : title}
      </span>
    </>
  );

  return parent.data?.main ? (
    <Link to="/b/$botId" params={{ botId }} className={className} title={label} aria-label={label}>
      {content}
    </Link>
  ) : (
    <Link
      to="/b/$botId/s/$sessionId"
      params={{ botId, sessionId: String(parentId) }}
      className={className}
      title={label}
      aria-label={label}
    >
      {content}
    </Link>
  );
}

/**
 * The session title and a compact link to its source session. The full source stays in the label
 * and tooltip so that it does not take space from the title.
 */
export function SessionTitle({
  session,
  heading,
  mainTitle,
}: {
  session: SessionInfo;
  heading: boolean;
  mainTitle: string;
}) {
  const switchable = ["Main", "Session"].includes(sessionKind(session));

  return (
    <>
      <h1
        id={heading ? "page-title" : undefined}
        tabIndex={-1}
        className="min-w-0 truncate text-[13px] font-medium outline-none"
      >
        {session.main ? mainTitle : session.title || "New session"}
      </h1>
      {switchable ? null : <OriginPill session={session} />}
      {!session.main && switchable && session.parentId !== undefined ? (
        <ParentLink
          parentId={session.parentId}
          parentBotId={session.parentBotId}
          parentBotName={session.parentBotName}
        />
      ) : null}
    </>
  );
}

function ForkOrigin({ origin }: { origin: NonNullable<SessionInfo["forkedFrom"]> }) {
  const botId = useBotId();
  const name = origin.main ? "Main" : origin.title || "a session";

  return (
    <div className="flex items-start gap-2.5 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] leading-relaxed text-muted-foreground shadow-card">
      <GitBranch className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <p>
        Forked from{" "}
        {origin.main ? (
          <Link to="/b/$botId" params={{ botId }} className="font-medium text-foreground underline">
            {name}
          </Link>
        ) : (
          <Link
            to="/b/$botId/s/$sessionId"
            params={{ botId, sessionId: String(origin.id) }}
            className="font-medium text-foreground underline"
          >
            {name}
          </Link>
        )}
        . The original session is unchanged. Files on disk are not rolled back.
      </p>
    </div>
  );
}

function OriginPill({ session }: { session: SessionInfo }) {
  const botId = useBotId();

  if (!["web", "automation"].includes(session.channel))
    return (
      <Badge variant="secondary" className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <Send className="size-[11px]" /> {session.channel} · read-only
      </Badge>
    );

  if (session.channel === "automation") {
    const pill = (
      <Badge variant="secondary" className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <Clock className="size-[11px]" /> Automation
      </Badge>
    );

    return session.automationId ? (
      <Link
        to="/b/$botId/automations/$automationId"
        params={{ botId, automationId: session.automationId }}
        className="shrink-0 rounded-full hover:opacity-80"
      >
        {pill}
      </Link>
    ) : (
      pill
    );
  }

  return null;
}

function Session({
  sessionId,
  settings,
  main = false,
}: {
  sessionId: number;
  settings: Settings;
  main?: boolean;
}) {
  const botId = useBotId();

  const { session, state, setState, queued, setQueued, connection, streamError, reconnect } =
    useSessionStream(sessionId);

  const readOnly = session.data?.readOnly ?? true;

  const refetchSession = session.refetch;

  // The live stream carries native events only. The question changes in the control store when its answer
  // ends, so the page polls while an answer waits.
  const answerWaiting = session.data?.question?.status === "answered";

  useEffect(() => {
    if (!answerWaiting) return;

    const timer = setInterval(() => void refetchSession(), 2000);

    return () => clearInterval(timer);
  }, [answerWaiting, refetchSession]);

  const sessions = useQuery({
    ...listSessionsOptions({ path: { botId } }),
    enabled: !readOnly,
    refetchInterval: 3000,
  });

  // Main can stop only its own run. Other parents can also stop eligible children.
  const isMain = main || session.data?.main === true;

  const activeChildren =
    !isMain &&
    (sessions.data?.sessions.some(
      (child) => child.parentId === sessionId && child.stoppedWithParent && child.busy,
    ) ??
      false);

  const health = useQuery(getHealthOptions());
  const memory = health.data ? `${health.data.dataDir}/memory` : undefined;
  const app = useApp();
  const [localDraft, setLocalDraft] = useState("");
  const draft = main ? app.composer.text : localDraft;
  const setDraft = main ? app.setComposerText : setLocalDraft;

  useEffect(() => {
    function receiveDraft() {
      const text = takeSessionDraft(botId, sessionId);

      if (text) setDraft((prior) => (prior ? `${prior}\n\n${text}` : text));
    }

    receiveDraft();
    window.addEventListener(sessionDraftEvent, receiveDraft);

    return () => window.removeEventListener(sessionDraftEvent, receiveDraft);
  }, [botId, sessionId, setDraft]);

  const [jump, setJump] = useState(false);
  const [thread, setThread] = useState<ThreadRef | null>(null);

  const threads = useMemo(
    () => ({ open: setThread, children: state.children, connection }),
    [state.children, connection],
  );

  const [announcement, setAnnouncement] = useState("");
  const clear = useClearContext(sessionId, state.running, () => setAnnouncement("Context cleared."));
  const wasRunning = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const failedMessage = useRef<{ body: MessageInput; attachments: Attachment[] } | null>(null);
  const send = useMutation(sendMessageMutation());
  const models = useQuery(listModelsOptions());
  const files = useAttachments(main);
  // A session with its own model sends with that model, not the bot's.
  const own = session.data?.settings;

  const model = own?.modelId
    ? findModel(own.provider ?? "", own.modelId, models.data?.models)
    : botModel(settings.bot, models.data?.models);

  const fallback = findModel(
    settings.bot.fallbackProvider,
    settings.bot.fallbackModelId,
    models.data?.models,
  );

  const modelReady = model.ok || fallback.ok;

  // Withdrawing an answer opens its question again.
  const withdraw = useMutation({ ...withdrawMessageMutation(), onSuccess: () => void refetchSession() });

  // The native queue is the source of the list: only messages that the stream shows as queued can be withdrawn.
  const queuedActions: QueuedActions = {
    ids: new Set(state.items.flatMap((item) => (item.kind === "user" && item.queued ? [item.id] : []))),
    pending: withdraw.isPending,
    // Edit does not keep the queue position. The text returns to the box and sends again as a new message.
    edit: (id) =>
      withdraw.mutate(
        { path: { botId, sessionId, messageId: id } },
        {
          onSuccess: (message) => {
            setDraft((prior) => (prior.trim() ? `${message.text}\n\n${prior}` : message.text));
            files.restore(message.attachments);
          },
        },
      ),
    remove: (id) => withdraw.mutate({ path: { botId, sessionId, messageId: id } }),
  };

  const abort = useMutation({
    ...abortSessionMutation(),
    onSuccess: async () => {
      if (!isMain) setQueued([]);
      await Promise.all([refreshSessions(botId), refetchSession()]);
    },
  });

  // Announce the start and the end of a run once each, not each streamed token.
  useEffect(() => {
    if (state.running && !wasRunning.current) setAnnouncement("The bot is working.");

    if (!state.running && wasRunning.current) {
      setAnnouncement("The bot finished.");
      // A finished run may have asked a question, or consumed the answer to one.
      void refetchSession();
    }

    wasRunning.current = state.running;
  }, [state.running, refetchSession]);

  const items = [
    ...state.items,
    ...queued.filter((item) => !state.items.some((remote) => remote.id === item.id)),
  ];

  useEffect(() => {
    const container = scroller.current;
    const transcript = container?.firstElementChild;

    if (!container || !transcript) return;

    const observer = new ResizeObserver(() => {
      if (pinned.current) container.scrollTop = container.scrollHeight;
    });

    observer.observe(transcript);
    observer.observe(container);

    return () => observer.disconnect();
  }, []);

  const blank =
    !items.length &&
    !state.notifications?.entries.length &&
    state.historyBefore === undefined &&
    connection === "ready";

  const bots = useQuery(listBotsOptions());

  const channelsRunning = bots.data?.bots.find((item) => item.id === botId)?.activity?.runningChannels ?? 0;

  const recent = (sessions.data?.sessions ?? [])
    .filter((item) => !item.main && !item.archivedAt && item.id !== sessionId && item.botId === undefined)
    .sort((a, b) => b.updatedAt - a.updatedAt)
    .slice(0, 3);

  const runnerBlock = useRunnerSendBlock(sessionId);

  const reason = !modelReady
    ? "Choose an available model before you send."
    : connection !== "ready"
      ? "dex is reconnecting to the bot…"
      : runnerBlock;

  const continueRun = useMutation({
    ...continueSessionMutation(),
    onSuccess: () => void refreshSessions(botId),
  });

  const compact = useMutation(compactSessionMutation());
  const failedCompact = useRef<{ sessionId: number; instructions: string; id: string } | null>(null);
  const pending = send.isPending || abort.isPending || continueRun.isPending || compact.isPending;
  const navigate = useNavigate();
  const fork = useMutation(forkSessionMutation());

  const failedFork = useRef<{
    sessionId: number;
    entryId: number;
    before: boolean;
    runnerId?: string;
    id: string;
  } | null>(null);

  const runners = useQuery(listBotRunnersOptions({ path: { botId } }));
  const runnerList = runners.data?.runners ?? [];
  const runsIn = session.data?.runnerId || runnerList.find((env) => env.default)?.id;

  // A fork opens as its own Session. Edit in new session carries the message text, not its attachments.
  const messageActions: MessageActions = {
    pending: fork.isPending,
    forkTargets: runnerList.filter((env) => env.id !== runsIn),
    fork: (entryId, before, text, runnerId) => {
      const prior = failedFork.current;

      const id =
        prior?.sessionId === sessionId &&
        prior.entryId === entryId &&
        prior.before === before &&
        prior.runnerId === runnerId
          ? prior.id
          : newId();

      const body = { id, entryId, before, runnerId };
      failedFork.current = { ...body, sessionId };

      fork.mutate(
        { path: { botId, sessionId }, body },
        {
          onSuccess: async (created) => {
            failedFork.current = null;

            if (text) putSessionDraft(botId, created.sessionId, text);

            await refreshSessions(botId);
            await navigate({
              to: "/b/$botId/s/$sessionId",
              params: { botId, sessionId: String(created.sessionId) },
            });
          },
        },
      );
    },
  };

  const error = send.error ?? abort.error ?? continueRun.error ?? compact.error ?? fork.error;
  const empty = !hasContext(items);

  const commands: SlashCommand[] = [
    {
      name: "clear",
      aliases: ["new"],
      args: "[handoff]",
      help: "Start a new context. Text after the command is carried into it.",
      unavailable: empty ? "The context is already empty." : undefined,
      run: (handoff) => clear.submit(handoff),
    },
    {
      name: "compact",
      args: "[what to keep]",
      help: "Summarize older messages to free context. Text after the command says what to keep.",
      // The composer is disabled without an available model, so that case never reaches here.
      unavailable: empty ? "There is nothing to compact yet." : undefined,
      run: async (instructions) => {
        const prior = failedCompact.current;

        const id = prior?.sessionId === sessionId && prior.instructions === instructions ? prior.id : newId();
        failedCompact.current = { sessionId, instructions, id };

        await compact.mutateAsync({
          path: { botId, sessionId },
          body: { id, instructions },
        });
        failedCompact.current = null;

        return true;
      },
    },
  ];

  async function sendText(
    text: string,
    attachments: Attachment[],
    answersQuestionId?: string,
    mode: SendMode = "followUp",
  ) {
    abort.reset();
    continueRun.reset();
    compact.reset();
    fork.reset();
    const ids = attachments.map((a) => a.id);
    const failed = failedMessage.current?.body;

    // A transport retry keeps its ID. A retry of a failed bot turn gets a new ID.
    const body: MessageInput =
      failed?.text === text && failed.attachments?.join() === ids.join()
        ? failed
        : { id: newId(), text, attachments: ids, answersQuestionId, mode };

    failedMessage.current = { body, attachments };

    const local: UserItem = {
      kind: "user",
      id: body.id,
      text,
      attachments,
      queued: true,
      steer: body.mode === "steer",
    };

    setQueued((previous) => [...previous, local]);

    try {
      await send.mutateAsync({ path: { botId, sessionId }, body });
      failedMessage.current = null;
      setDraft((current) => (current.trim() === text ? "" : current));
      files.sent(ids);
      await refreshSessions(botId);

      // The question card shows the saved answer. A plain message leaves the question as it was.
      if (answersQuestionId) await Promise.all([refetchSession(), refreshBoardAttention()]);
    } catch (cause) {
      setQueued((previous) => previous.filter((item) => item.id !== body.id));

      // A refused answer means the question closed meanwhile.
      if (answersQuestionId) void refetchSession();

      throw cause;
    }
  }

  function retryText(index: number) {
    const item = state.items[index];

    // The run already holds the message, so native continue answers it without a second copy.
    if (item?.kind === "error" && item.failure) {
      abort.reset();
      continueRun.mutate({ path: { botId, sessionId }, body: { id: newId() } });

      return;
    }

    const previous = retryMessage(state.items, index);

    if (previous?.kind === "user") {
      failedMessage.current = null;
      void sendText(previous.text, previous.attachments ?? []).catch(() => {});
    }
  }

  const running = state.running;

  const composer = readOnly ? null : (
    <>
      {withdraw.error ? (
        <div role="alert" className="mb-3">
          <Note tone="warn">
            {/already started/.test(errorText(withdraw.error))
              ? "It already started, so it can no longer be changed."
              : "Could not withdraw the message."}
          </Note>
        </div>
      ) : null}
      {session.data?.question ? (
        <QuestionCard
          key={session.data.question.id}
          question={session.data.question}
          pending={send.isPending}
          answer={(text) => void sendText(text, [], session.data?.question?.id).catch(() => {})}
        />
      ) : null}
      {abort.isSuccess ? (
        <div role="status" className="mb-3">
          <Note>Stop requested. Earlier actions are not undone. Processes and remote work may continue.</Note>
        </div>
      ) : null}
      {error ? (
        <div className="mb-3">
          <Failure
            title="Could not complete this action"
            error={error}
            retry={() => {
              if (send.isError && failedMessage.current)
                void sendText(failedMessage.current.body.text, failedMessage.current.attachments).catch(
                  () => {},
                );
              else if (abort.isError && abort.variables) abort.mutate(abort.variables);
            }}
          />
        </div>
      ) : null}
      {clear.refused ? (
        <div role="alert" className="mb-3">
          <Note tone="warn">
            <strong className="font-medium">Context not cleared.</strong> {settings.bot.name} is working. Wait
            for the reply or stop it, then try again.
          </Note>
        </div>
      ) : null}
      {clear.error ? (
        <div className="mb-3">
          <Failure title="Could not clear context" error={clear.error} />
        </div>
      ) : null}
      <Composer
        controls={
          <div className="flex min-w-0 items-center gap-1">
            {session.data &&
              (session.data.main ? (
                <SessionActivity sessionId={session.data.id} writable={!session.data.readOnly} />
              ) : (
                <SessionSettings session={session.data} settings={settings} started={!blank} />
              ))}
          </div>
        }
        commands={commands}
        fix={
          modelReady ? null : settings.providers.length ? (
            <ModelPicker
              trigger={
                <button type="button" className="font-medium text-primary underline">
                  Choose a model
                </button>
              }
            />
          ) : (
            <Link className="font-medium text-primary underline" to="/settings/providers">
              Add a provider
            </Link>
          )
        }
        files={files}
        running={running}
        stoppable={
          isMain
            ? connection === "ready" && running && Boolean(state.runTarget)
            : running ||
              activeChildren ||
              items.some((item) => item.kind === "user" && item.queued) ||
              (connection !== "ready" && session.data?.busy === true)
        }
        pending={pending}
        disabledReason={reason}
        placeholder={`Message ${settings.bot.name}`}
        autoFocus
        onSend={(text, mode) => sendText(text, files.ready, undefined, mode)}
        onStop={() => {
          if (!sessionId) return;

          if (isMain) {
            if (connection !== "ready" || !running || !state.runTarget) return;

            abort.mutate({ path: { botId, sessionId }, query: { runTarget: state.runTarget } });
          } else {
            abort.mutate({ path: { botId, sessionId } });
          }
        }}
        text={draft}
        onTextChange={setDraft}
      />
    </>
  );

  const transcript =
    sessionId && session.isPending ? (
      <div className="flex h-60 items-center justify-center">
        <Loading />
      </div>
    ) : sessionId && session.isError ? (
      <div className="flex flex-col items-start gap-4">
        <Failure
          title="Could not load this session"
          error={session.error}
          retry={() => void session.refetch()}
        />
        <Link className="text-[13px] font-medium text-primary underline" to="/b/$botId" params={{ botId }}>
          Open timeline
        </Link>
      </div>
    ) : (
      <>
        {readOnly ? (
          <div className="flex items-start gap-2.5 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] leading-relaxed text-muted-foreground shadow-card">
            <Lock className="mt-0.5 size-3.5 shrink-0" />
            <p>{readOnlyCopy(session.data?.channel)}</p>
          </div>
        ) : null}
        {connection !== "ready" ? (
          <div
            role="alert"
            className="flex flex-col gap-2 rounded-md bg-orange-tint px-3.5 py-3 text-[12.5px]"
          >
            <div className="flex items-center gap-2 font-medium text-foreground">
              <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-orange" />
              {connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the bot…"}
            </div>
            <p className="pl-3.5 text-muted-foreground [overflow-wrap:anywhere]">
              {streamError || "Messages are paused until the stream returns. Your transcript stays here."}
            </p>
            <div className="pl-3.5">
              <Button size="xs" variant="secondary" onClick={() => reconnect.current?.()}>
                Reconnect
              </Button>
            </div>
          </div>
        ) : null}
        {session.data?.forkedFrom ? <ForkOrigin origin={session.data.forkedFrom} /> : null}
        <LoadEarlierButton
          sessionId={sessionId}
          before={state.historyBefore}
          beforeNotification={state.notifications?.before}
          onPage={(page, beforeNotification) =>
            setState((previous) => mergeHistory(previous, page, beforeNotification))
          }
        />
        <Transcript
          sessionId={sessionId}
          items={items}
          notifications={state.notifications?.entries}
          running={running}
          retry={retryText}
          canRetry={!readOnly && !reason && !pending}
          memory={memory}
          queuedActions={readOnly ? undefined : queuedActions}
          messageActions={readOnly ? undefined : messageActions}
        />
        {state.retry ? <RetryNotice retry={state.retry} /> : null}
        {running &&
        !state.retry &&
        !items.some((item) => item.kind === "tool" && item.status === "running") &&
        items.at(-1)?.kind !== "assistant" &&
        items.at(-1)?.kind !== "thinking" ? (
          <div className="flex min-h-6 items-center">
            <Loading />
          </div>
        ) : null}
        {blank && readOnly ? (
          <p className="my-auto py-12 text-center text-[14px] text-muted-foreground">{`No messages yet.`}</p>
        ) : null}
      </>
    );

  const status = (
    <div className="sr-only" role="status" aria-live="polite">
      {announcement}
    </div>
  );

  return (
    <section className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background">
      {readOnly && session.data ? (
        <div className="flex shrink-0 justify-end border-b border-border px-3 py-1">
          <SessionActivity sessionId={session.data.id} writable={false} />
        </div>
      ) : null}
      <ThreadContext.Provider value={threads}>
        <div className="relative flex min-h-0 flex-1">
          <div className="relative flex min-w-0 flex-1 flex-col">
            <div
              ref={scroller}
              className={cn("min-h-0 flex-1 overflow-y-auto overscroll-contain", blank && "hidden")}
              onScroll={() => {
                const el = scroller.current;

                if (el) {
                  pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
                  setJump(!pinned.current);
                }
              }}
            >
              <div className="mx-auto flex min-h-full w-full max-w-[760px] flex-col justify-end gap-3 px-4 py-8 sm:px-8">
                {transcript}
              </div>
            </div>
            {readOnly ? null : (
              <div
                className={cn(
                  "shrink-0 px-3 pb-3 sm:px-8 sm:pb-5",
                  blank && "flex min-h-0 flex-1 items-center",
                )}
              >
                <div className="relative mx-auto w-full max-w-[760px]">
                  {blank ? (
                    <div className="mb-4 px-2">
                      <p className="text-[20px] leading-tight font-semibold tracking-[-0.01em] text-foreground">
                        {`What should ${settings.bot.name} do?`}
                      </p>
                      <p className="mt-1.5 text-[13px] leading-relaxed text-muted-foreground">
                        {session.data?.main
                          ? null
                          : "This session has its own context. It does not see the main timeline. "}
                        Tools run on this computer without a sandbox.
                      </p>
                    </div>
                  ) : null}
                  {jump ? (
                    <Button
                      size="icon-lg"
                      variant="secondary"
                      aria-label="Jump to latest"
                      title="Jump to latest"
                      className="absolute -top-12 right-0 rounded-full shadow-card"
                      onClick={() => {
                        pinned.current = true;
                        setJump(false);
                        scroller.current?.scrollTo({
                          top: scroller.current.scrollHeight,
                          behavior: "smooth",
                        });
                      }}
                    >
                      <ArrowDown className="size-4" />
                    </Button>
                  ) : null}
                  {composer}
                  {blank ? (
                    <div className="mt-6">
                      <p className="mb-1 px-2 text-[12px] font-medium text-muted-foreground">
                        This bot so far
                      </p>
                      <ul className="text-[13px]">
                        <li className="flex items-baseline gap-3 border-t border-border px-2 py-2 first:border-t-0">
                          <span className="min-w-0 flex-1 text-muted-foreground">Model</span>
                          <span className={model.ok ? "text-foreground" : "text-muted-foreground"}>
                            {model.ok ? model.label : "None chosen"}
                          </span>
                        </li>
                        <li className="flex items-baseline gap-3 border-t border-border px-2 py-2">
                          <span className="min-w-0 flex-1 text-muted-foreground">Channels</span>
                          <span className="text-foreground">
                            {channelsRunning ? `${channelsRunning} connected` : "None connected"}
                          </span>
                        </li>
                        <li className="flex items-baseline gap-3 border-t border-border px-2 py-2">
                          <span className="min-w-0 flex-1 text-muted-foreground">Sessions</span>
                          <span className="text-foreground tabular-nums">
                            {sessions.data?.sessions.length ?? 0}
                          </span>
                        </li>
                      </ul>
                    </div>
                  ) : null}
                  {blank && recent.length ? (
                    <div className="mt-6">
                      <p className="mb-1 px-2 text-[12px] font-medium text-muted-foreground">
                        Recent in this bot
                      </p>
                      <ul>
                        {recent.map((item) => (
                          <li key={item.id}>
                            <Link
                              to="/b/$botId/s/$sessionId"
                              params={{ botId, sessionId: String(item.id) }}
                              className="flex items-baseline gap-3 rounded-md px-2 py-2 hover:bg-accent"
                            >
                              <span className="min-w-0 flex-1 truncate text-[13px] text-foreground">
                                {item.title || "New session"}
                              </span>
                              <span className="shrink-0 text-[12px] text-muted-foreground tabular-nums">
                                {sessionTime(item.updatedAt)}
                              </span>
                            </Link>
                          </li>
                        ))}
                      </ul>
                    </div>
                  ) : null}
                </div>
              </div>
            )}
          </div>
          {thread ? <ThreadPanel thread={thread} onClose={() => setThread(null)} /> : null}
        </div>
      </ThreadContext.Provider>
      {status}
    </section>
  );
}

function readOnlyCopy(channel?: SessionInfo["channel"]) {
  const names = { telegram: "Telegram", wechat: "WeChat", feishu: "Feishu" };

  if (channel === "telegram" || channel === "wechat" || channel === "feishu")
    return `You can read this conversation here. Reply in ${names[channel]}.`;

  return channel === "automation" ? "You can read this run here." : "You can read this conversation here.";
}
