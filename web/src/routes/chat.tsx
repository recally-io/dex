import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useContext, useEffect, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowDown, Clock, GitBranch, Lock, Send } from "lucide-react";
import type { Attachment, UserItem, MessageInput, Chat as ChatInfo } from "../api/generated/types.gen";
import {
  getChatOptions,
  getHealthOptions,
  getMainChatOptions,
  sendMessageMutation,
  abortChatMutation,
  continueChatMutation,
  compactChatMutation,
  forkChatMutation,
  withdrawMessageMutation,
  listModelsOptions,
  listChatsOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { mergeHistory, retryMessage } from "../lib/chat-state";
import { useChatStream } from "../lib/chat-stream";
import { errorText, refreshChats } from "../lib/queries";
import { agentModel, findModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { useAttachments } from "../lib/attachments";
import { activeSessionCount, sessionKind, sessionTime } from "../lib/sessions";
import { SessionSettings } from "../components/session-settings";
import { Artifacts } from "../components/agent/artifacts";
import { chatDraftEvent, putChatDraft, takeChatDraft } from "../lib/chat-draft";
import { Composer, type SendMode } from "../components/composer";
import { hasContext, useClearContext, LoadEarlierButton } from "../components/timeline-actions";
import { QuestionCard } from "../components/agent/question-card";
import {
  RetryNotice,
  Transcript,
  type MessageActions,
  type QueuedActions,
} from "../components/agent/transcript";
import type { SlashCommand } from "../components/slash-commands";
import { ModelPicker } from "../components/model-picker";
import { Failure, Loading } from "../components/feedback";
import { ChatSettingsContext, InPaneContext, MainPane, Note, PageHeader } from "../components/page";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { cn, newId } from "../lib/utils";

export function ChatPage({ chatId }: { chatId?: number }) {
  const agentId = useAgentId();
  const settings = useAgentSettings(agentId);

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
  ) : chatId ? (
    <Chat key={chatId} chatId={chatId} settings={settings.data} />
  ) : (
    <MainTimeline settings={settings.data} />
  );
}

export function MainTimeline({ settings, visible = true }: { settings: Settings; visible?: boolean }) {
  const agentId = useAgentId();
  const main = useQuery(getMainChatOptions({ path: { agentId } }));

  return main.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : main.isError ? (
    <MainPane>
      <Failure title="Could not open the timeline" error={main.error} retry={() => void main.refetch()} />
    </MainPane>
  ) : (
    <Chat key={main.data.id} chatId={main.data.id} settings={settings} main visible={visible} />
  );
}

// A child session links to the session that started it. The parent is Main or a Chat session.
function ParentLink({ parentId }: { parentId: number }) {
  const agentId = useAgentId();
  const parent = useQuery(getChatOptions({ path: { agentId, chatId: parentId } }));
  const name = parent.data ? (parent.data.main ? "Main" : parent.data.title || "a chat") : "…";

  const className =
    "max-w-56 shrink-0 truncate text-[12.5px] text-muted-foreground hover:text-foreground hover:underline";

  return parent.data?.main ? (
    <Link to="/a/$agentId" params={{ agentId }} className={className}>
      Started by {name}
    </Link>
  ) : (
    <Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(parentId) }} className={className}>
      Started by {name}
    </Link>
  );
}

function ForkOrigin({ origin }: { origin: NonNullable<ChatInfo["forkedFrom"]> }) {
  const agentId = useAgentId();
  const name = origin.main ? "Main" : origin.title || "a chat";

  return (
    <div className="flex items-start gap-2.5 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] leading-relaxed text-muted-foreground shadow-card">
      <GitBranch className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <p>
        Forked from{" "}
        {origin.main ? (
          <Link to="/a/$agentId" params={{ agentId }} className="font-medium text-foreground underline">
            {name}
          </Link>
        ) : (
          <Link
            to="/a/$agentId/c/$chatId"
            params={{ agentId, chatId: String(origin.id) }}
            className="font-medium text-foreground underline"
          >
            {name}
          </Link>
        )}
        . The original chat is unchanged. Files on disk are not rolled back.
      </p>
    </div>
  );
}

function OriginPill({ chat }: { chat: ChatInfo }) {
  const agentId = useAgentId();

  if (!["web", "automation"].includes(chat.channel))
    return (
      <Badge variant="secondary" className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <Send className="size-[11px]" /> {chat.channel} · read-only
      </Badge>
    );

  if (chat.channel === "automation") {
    const pill = (
      <Badge variant="secondary" className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <Clock className="size-[11px]" /> Routine
      </Badge>
    );

    return chat.automationId ? (
      <Link
        to="/a/$agentId/automations/$automationId"
        params={{ agentId, automationId: chat.automationId }}
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

function Chat({
  chatId,
  settings,
  main = false,
  visible = true,
}: {
  chatId: number;
  settings: Settings;
  main?: boolean;
  visible?: boolean;
}) {
  const agentId = useAgentId();

  const { chat, state, setState, queued, setQueued, connection, streamError, reconnect } =
    useChatStream(chatId);

  const readOnly = chat.data?.readOnly ?? true;
  const kind = chat.data ? sessionKind(chat.data) : undefined;
  const switchable = kind === "Main" || kind === "Chat";

  const refetchChat = chat.refetch;

  // The live stream carries native events only. The question changes in the control store when its answer
  // ends, so the page polls while an answer waits.
  const answerWaiting = chat.data?.question?.status === "answered";

  useEffect(() => {
    if (!answerWaiting) return;

    const timer = setInterval(() => void refetchChat(), 2000);

    return () => clearInterval(timer);
  }, [answerWaiting, refetchChat]);

  const sessions = useQuery({
    ...listChatsOptions({ path: { agentId } }),
    enabled: !readOnly,
    refetchInterval: 3000,
  });

  // Stop acts on a child only while it has input that this session sent. The server says which.
  const activeHere = activeSessionCount(
    (sessions.data?.chats ?? []).filter(
      (session) => session.parentId === chatId && session.stoppedWithParent,
    ),
  );

  const health = useQuery(getHealthOptions());
  const memory = health.data ? `${health.data.dataDir}/memory` : undefined;
  const app = useApp();
  const [localDraft, setLocalDraft] = useState("");
  const draft = main ? app.composer.text : localDraft;
  const setDraft = main ? app.setComposerText : setLocalDraft;

  useEffect(() => {
    function receiveDraft() {
      const text = takeChatDraft(agentId, chatId);

      if (text) setDraft((prior) => (prior ? `${prior}\n\n${text}` : text));
    }

    receiveDraft();
    window.addEventListener(chatDraftEvent, receiveDraft);

    return () => window.removeEventListener(chatDraftEvent, receiveDraft);
  }, [agentId, chatId, setDraft]);

  const embedded = useContext(InPaneContext);
  const settingsOpen = useContext(ChatSettingsContext);
  const [jump, setJump] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const clear = useClearContext(chatId, state.running, () => setAnnouncement("Context cleared."));
  const wasRunning = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const failedMessage = useRef<{ body: MessageInput; attachments: Attachment[] } | null>(null);
  const send = useMutation(sendMessageMutation());
  const models = useQuery(listModelsOptions());
  const files = useAttachments(main);
  // A session with its own model sends with that model, not the bot's.
  const own = chat.data?.settings;

  const model = own?.modelId
    ? findModel(own.provider ?? "", own.modelId, models.data?.models)
    : agentModel(settings.agent, models.data?.models);

  // Withdrawing an answer opens its question again.
  const withdraw = useMutation({ ...withdrawMessageMutation(), onSuccess: () => void refetchChat() });

  // The native queue is the source of the list: only messages that the stream shows as queued can be withdrawn.
  const queuedActions: QueuedActions = {
    ids: new Set(state.items.flatMap((item) => (item.kind === "user" && item.queued ? [item.id] : []))),
    pending: withdraw.isPending,
    // Edit does not keep the queue position. The text returns to the box and sends again as a new message.
    edit: (id) =>
      withdraw.mutate(
        { path: { agentId, chatId, messageId: id } },
        {
          onSuccess: (message) => {
            setDraft((prior) => (prior.trim() ? `${message.text}\n\n${prior}` : message.text));
            files.restore(message.attachments);
          },
        },
      ),
    remove: (id) => withdraw.mutate({ path: { agentId, chatId, messageId: id } }),
  };

  const abort = useMutation({
    ...abortChatMutation(),
    onSuccess: async () => {
      setQueued([]);
      await Promise.all([refreshChats(agentId), refetchChat()]);
    },
  });

  // Announce the start and the end of a run once each, not each streamed token.
  useEffect(() => {
    if (state.running && !wasRunning.current) setAnnouncement("The bot is working.");

    if (!state.running && wasRunning.current) {
      setAnnouncement("The bot finished.");
      // A finished run may have asked a question, or consumed the answer to one.
      void refetchChat();
    }

    wasRunning.current = state.running;
  }, [state.running, refetchChat]);

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

  const blank = !items.length && state.historyBefore === undefined && connection === "ready";

  const agents = useQuery(listAgentsOptions());

  const channelsRunning =
    agents.data?.agents.find((item) => item.id === agentId)?.activity?.runningChannels ?? 0;

  const recent = (sessions.data?.chats ?? [])
    .filter((item) => !item.main && !item.archivedAt && item.id !== chatId)
    .sort((a, b) => b.updatedAt - a.updatedAt)
    .slice(0, 3);

  const reason = !model.ok
    ? "Choose an available model before you send."
    : connection !== "ready"
      ? "dex is reconnecting to the bot…"
      : undefined;

  const continueRun = useMutation({ ...continueChatMutation(), onSuccess: () => void refreshChats(agentId) });
  const compact = useMutation(compactChatMutation());
  const pending = send.isPending || abort.isPending || continueRun.isPending || compact.isPending;
  const navigate = useNavigate();
  const fork = useMutation(forkChatMutation());

  // A fork opens as its own Chat. Edit in new chat carries the message text, not its attachments.
  const messageActions: MessageActions = {
    pending: fork.isPending,
    fork: (entryId, before, text) =>
      fork.mutate(
        { path: { agentId, chatId }, body: { entryId, before } },
        {
          onSuccess: async (created) => {
            if (text) putChatDraft(agentId, created.chatId, text);

            await refreshChats(agentId);
            await navigate({
              to: "/a/$agentId/c/$chatId",
              params: { agentId, chatId: String(created.chatId) },
            });
          },
        },
      ),
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
        await compact.mutateAsync({
          path: { agentId, chatId },
          body: instructions ? { instructions } : {},
        });

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
      await send.mutateAsync({ path: { agentId, chatId }, body });
      failedMessage.current = null;
      setDraft((current) => (current.trim() === text ? "" : current));
      files.sent(ids);
      await refreshChats(agentId);

      // The question card shows the saved answer. A plain message leaves the question as it was.
      if (answersQuestionId) await refetchChat();
    } catch (cause) {
      setQueued((previous) => previous.filter((item) => item.id !== body.id));

      // A refused answer means the question closed meanwhile.
      if (answersQuestionId) void refetchChat();

      throw cause;
    }
  }

  function retryText(index: number) {
    const item = state.items[index];

    // The run already holds the message, so native continue answers it without a second copy.
    if (item?.kind === "error" && item.failure) {
      abort.reset();
      continueRun.mutate({ path: { agentId, chatId }, body: { id: newId() } });

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
      {chat.data?.question ? (
        <QuestionCard
          key={chat.data.question.id}
          question={chat.data.question}
          pending={send.isPending}
          answer={(text) => void sendText(text, [], chat.data?.question?.id).catch(() => {})}
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
              else if (abort.isError && chatId) abort.mutate({ path: { agentId, chatId } });
            }}
          />
        </div>
      ) : null}
      {clear.refused ? (
        <div role="alert" className="mb-3">
          <Note tone="warn">
            <strong className="font-medium">Context not cleared.</strong> {settings.agent.name} is working.
            Wait for the reply or stop it, then try again.
          </Note>
        </div>
      ) : null}
      {clear.error ? (
        <div className="mb-3">
          <Failure title="Could not clear context" error={clear.error} />
        </div>
      ) : null}
      <Composer
        commands={commands}
        fix={
          model.ok ? null : settings.providers.length ? (
            <ModelPicker
              trigger={
                <button type="button" className="font-medium text-primary underline">
                  Choose a model
                </button>
              }
            />
          ) : (
            <Link className="font-medium text-primary underline" to="/providers">
              Add a provider
            </Link>
          )
        }
        files={files}
        running={running}
        stoppable={
          running ||
          activeHere > 0 ||
          items.some((item) => item.kind === "user" && item.queued) ||
          (connection !== "ready" && chat.data?.busy === true)
        }
        pending={pending}
        disabledReason={reason}
        placeholder={`Message ${settings.agent.name}`}
        autoFocus
        onSend={(text, mode) => sendText(text, files.ready, undefined, mode)}
        onStop={() => {
          if (chatId) abort.mutate({ path: { agentId, chatId } });
        }}
        text={draft}
        onTextChange={setDraft}
      />
    </>
  );

  const transcript =
    chatId && chat.isPending ? (
      <div className="flex h-60 items-center justify-center">
        <Loading />
      </div>
    ) : chatId && chat.isError ? (
      <div className="flex flex-col items-start gap-4">
        <Failure title="Could not load this session" error={chat.error} retry={() => void chat.refetch()} />
        <Link
          className="text-[13px] font-medium text-primary underline"
          to="/a/$agentId"
          params={{ agentId }}
        >
          Open timeline
        </Link>
      </div>
    ) : (
      <>
        {readOnly ? (
          <div className="flex items-start gap-2.5 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] leading-relaxed text-muted-foreground shadow-card">
            <Lock className="mt-0.5 size-3.5 shrink-0" />
            <p>{readOnlyCopy(chat.data?.channel)}</p>
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
        {chat.data?.forkedFrom ? <ForkOrigin origin={chat.data.forkedFrom} /> : null}
        <LoadEarlierButton
          chatId={chatId}
          before={state.historyBefore}
          onPage={(page) => {
            pinned.current = false;
            const box = scroller.current;
            const height = box?.scrollHeight ?? 0;
            setState((previous) => mergeHistory(previous, page));
            requestAnimationFrame(() => {
              if (box) box.scrollTop += box.scrollHeight - height;
            });
          }}
        />
        <Transcript
          items={items}
          running={running}
          retry={retryText}
          canRetry={!readOnly && !reason && !pending}
          memory={memory}
          queuedActions={readOnly ? undefined : queuedActions}
          messageActions={readOnly ? undefined : messageActions}
        />
        <Artifacts
          chatId={chatId}
          onContinue={
            readOnly ? undefined : (text) => setDraft((prior) => (prior ? `${prior}\n\n${text}` : text))
          }
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
    <>
      <section
        className={cn(
          "flex min-w-0 flex-1 flex-col overflow-hidden bg-background",
          embedded ? "min-h-0" : "lg:border-l lg:border-border",
        )}
      >
        {chat.data && switchable ? (
          <>
            <header className="flex min-w-0 items-center gap-2 border-b border-border px-4 py-2">
              {!chat.data.main ? (
                <>
                  <Link
                    to="/a/$agentId"
                    params={{ agentId }}
                    className="shrink-0 text-sm text-muted-foreground hover:text-foreground hover:underline"
                  >
                    Main
                  </Link>
                  <span aria-hidden className="text-ring">
                    /
                  </span>
                </>
              ) : null}
              <h1
                id={visible ? "page-title" : undefined}
                tabIndex={-1}
                className="min-w-0 flex-1 truncate text-sm font-medium"
              >
                {chat.data.main ? "Main" : chat.data.title || "New chat"}
              </h1>
              {!chat.data.main && chat.data.parentId !== undefined ? (
                <ParentLink parentId={chat.data.parentId} />
              ) : null}
            </header>
            {settingsOpen ? (
              <div
                role="region"
                aria-label="Chat settings"
                className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-border bg-muted/40 px-4 py-1.5"
              >
                <p className="min-w-0 basis-56 flex-1 text-[12px] text-muted-foreground">
                  {chat.data.main
                    ? "Changes apply from the next message, including channel messages."
                    : "Changes apply from the next message in this chat."}
                </p>
                <div className="min-w-0 max-w-full">
                  <SessionSettings chat={chat.data} settings={settings} />
                </div>
              </div>
            ) : null}
          </>
        ) : (
          <PageHeader
            heading
            menu={!embedded}
            title={chat.data?.title || "New session"}
            crumb={
              chat.data ? (
                <Link
                  to="/a/$agentId/automations"
                  params={{ agentId }}
                  className="shrink-0 text-[13px] text-muted-foreground hover:text-foreground hover:underline"
                >
                  Routines
                </Link>
              ) : null
            }
            badge={chat.data ? <OriginPill chat={chat.data} /> : null}
          />
        )}
        <div className="flex min-h-0 flex-1">
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
                        {`What should ${settings.agent.name} do?`}
                      </p>
                      <p className="mt-1.5 text-[13px] leading-relaxed text-muted-foreground">
                        {chat.data?.main
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
                            {sessions.data?.chats.length ?? 0}
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
                              to="/a/$agentId/c/$chatId"
                              params={{ agentId, chatId: String(item.id) }}
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
        </div>
        {status}
      </section>
    </>
  );
}

function readOnlyCopy(channel?: ChatInfo["channel"]) {
  const names = { telegram: "Telegram", wechat: "WeChat", feishu: "Feishu" };

  if (channel === "telegram" || channel === "wechat" || channel === "feishu")
    return `You can read this conversation here. Reply in ${names[channel]}.`;

  return channel === "automation" ? "You can read this run here." : "You can read this conversation here.";
}
