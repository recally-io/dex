import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useContext, useEffect, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { IconArrowDown } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconArrowDown";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconLock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconLock";
import { IconPaperPlane } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPaperPlane";
import type { Attachment, UserItem, MessageInput, Chat as ChatInfo } from "../api/generated/types.gen";
import {
  getHealthOptions,
  getMainChatOptions,
  sendMessageMutation,
  abortChatMutation,
  listModelsOptions,
  listChatsOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { mergeHistory, retryMessage } from "../lib/chat-state";
import { useChatStream } from "../lib/chat-stream";
import { refreshChats } from "../lib/queries";
import { agentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { useAttachments } from "../lib/attachments";
import { activeSessionCount, sessionKind, taskTime } from "../lib/tasks";
import { Composer } from "../components/composer";
import { hasContext, useClearContext, LoadEarlierButton } from "../components/timeline-actions";
import { RetryNotice, Transcript } from "../components/agent/transcript";
import { ModelPicker } from "../components/model-picker";
import { Failure, Loading } from "../components/feedback";
import { InPaneContext, MainPane, MenuButton, Note } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import LoadingState from "../components/primitives/LoadingState";
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

export function MainTimeline({ settings }: { settings: Settings }) {
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
    <Chat key={main.data.id} chatId={main.data.id} settings={settings} main />
  );
}

function OriginPill({ chat }: { chat: ChatInfo }) {
  const agentId = useAgentId();

  if (!["web", "automation"].includes(chat.channel))
    return (
      <StatusPill dot={false} className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <IconPaperPlane size={11} /> {chat.channel} · read-only
      </StatusPill>
    );

  if (chat.channel === "automation") {
    const pill = (
      <StatusPill dot={false} className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <IconClock size={11} /> Automation
      </StatusPill>
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

function Chat({ chatId, settings, main = false }: { chatId: number; settings: Settings; main?: boolean }) {
  const agentId = useAgentId();

  const { chat, state, setState, queued, setQueued, connection, streamError, reconnect } =
    useChatStream(chatId);

  const readOnly = chat.data?.readOnly ?? true;
  const kind = chat.data ? sessionKind(chat.data) : undefined;
  const switchable = kind === "Main" || kind === "Chat";

  const sessions = useQuery({
    ...listChatsOptions({ path: { agentId } }),
    enabled: !readOnly,
    refetchInterval: 3000,
  });

  const activeHere = activeSessionCount(
    (sessions.data?.chats ?? []).filter((session) => session.task?.parentConversationId === chatId),
  );

  const health = useQuery(getHealthOptions());
  const memory = health.data ? `${health.data.dataDir}/memory` : undefined;
  const app = useApp();
  const [localDraft, setLocalDraft] = useState("");
  const draft = main ? app.composer.text : localDraft;
  const setDraft = main ? app.setComposerText : setLocalDraft;
  const embedded = useContext(InPaneContext);
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
  const model = agentModel(settings.agent, models.data?.models);

  const abort = useMutation({
    ...abortChatMutation(),
    onSuccess: async () => {
      setQueued([]);
      await refreshChats(agentId);
    },
  });

  // Announce the start and the end of a run once each, not each streamed token.
  useEffect(() => {
    if (state.running && !wasRunning.current) setAnnouncement("The bot is working.");

    if (!state.running && wasRunning.current) setAnnouncement("The bot finished.");

    wasRunning.current = state.running;
  }, [state.running]);

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

  const pending = send.isPending || abort.isPending;
  const error = send.error ?? abort.error;

  async function sendText(text: string, attachments: Attachment[]) {
    abort.reset();
    const ids = attachments.map((a) => a.id);
    const failed = failedMessage.current?.body;

    // A transport retry keeps its ID. A retry of a failed bot turn gets a new ID.
    const body: MessageInput =
      failed?.text === text && failed.attachments?.join() === ids.join()
        ? failed
        : { id: newId(), text, attachments: ids };

    failedMessage.current = { body, attachments };
    const local: UserItem = { kind: "user", id: body.id, text, attachments, queued: true };

    setQueued((previous) => [...previous, local]);

    try {
      await send.mutateAsync({ path: { agentId, chatId }, body });
      failedMessage.current = null;
      setDraft((current) => (current.trim() === text ? "" : current));
      files.sent(ids);
      await refreshChats(agentId);
    } catch (cause) {
      setQueued((previous) => previous.filter((item) => item.id !== body.id));
      throw cause;
    }
  }

  function retryText(index: number) {
    const previous = retryMessage(state.items, index);

    if (previous?.kind === "user") {
      failedMessage.current = null;
      void sendText(previous.text, previous.attachments ?? []).catch(() => {});
    }
  }

  const running = state.running;

  const composer = readOnly ? null : (
    <>
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
        controls={
          hasContext(items) ? (
            <button
              type="button"
              title="Start a new context. Earlier messages stay."
              disabled={clear.pending}
              onClick={clear.submit}
              className="flex h-7 items-center rounded-control px-1.5 text-[12px] font-medium text-ink-2 hover:bg-hover hover:text-ink disabled:opacity-40"
            >
              Clear context
            </button>
          ) : null
        }
        fix={
          model.ok ? null : settings.providers.length ? (
            <ModelPicker
              trigger={
                <button type="button" className="font-medium text-dex-accent-ink underline">
                  Choose a model
                </button>
              }
            />
          ) : (
            <Link className="font-medium text-dex-accent-ink underline" to="/providers">
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
        onSend={(text) => sendText(text, files.ready)}
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
        <LoadingState label="Loading chat" />
      </div>
    ) : chatId && chat.isError ? (
      <div className="flex flex-col items-start gap-4">
        <Failure title="Could not load this session" error={chat.error} retry={() => void chat.refetch()} />
        <Link
          className="text-[13px] font-medium text-dex-accent-ink underline"
          to="/a/$agentId"
          params={{ agentId }}
        >
          Open timeline
        </Link>
      </div>
    ) : (
      <>
        {readOnly ? (
          <div className="flex items-start gap-2.5 rounded-card bg-inset px-3.5 py-2.5 text-[12.5px] leading-relaxed text-ink-2 shadow-hairline">
            <IconLock size={14} className="mt-0.5 shrink-0" />
            <p>{readOnlyCopy(chat.data?.channel)}</p>
          </div>
        ) : null}
        {connection !== "ready" ? (
          <div
            role="alert"
            className="flex flex-col gap-2 rounded-card bg-orange-tint px-3.5 py-3 text-[12.5px]"
          >
            <div className="flex items-center gap-2 font-medium text-ink">
              <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-orange" />
              {connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the bot…"}
            </div>
            <p className="pl-3.5 text-ink-2 [overflow-wrap:anywhere]">
              {streamError || "Messages are paused until the stream returns. Your transcript stays here."}
            </p>
            <div className="pl-3.5">
              <Button size="xs" variant="secondary" onClick={() => reconnect.current?.()}>
                Reconnect
              </Button>
            </div>
          </div>
        ) : null}
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
        />
        {state.retry ? <RetryNotice retry={state.retry} /> : null}
        {running &&
        !state.retry &&
        !items.some((item) => item.kind === "tool" && item.status === "running") &&
        items.at(-1)?.kind !== "assistant" &&
        items.at(-1)?.kind !== "thinking" ? (
          <div className="flex min-h-6 items-center">
            <LoadingState label="dex is working" variant="Dots" />
          </div>
        ) : null}
        {blank && readOnly ? (
          <p className="my-auto py-12 text-center text-[14px] text-ink-2">{`No messages yet.`}</p>
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
          "flex min-w-0 flex-1 flex-col overflow-hidden bg-page",
          embedded ? "min-h-0" : "lg:border-l lg:border-line",
        )}
      >
        {/* Writable chats switch in the Chats sidebar. The bot pane names Main; a Chat keeps a
            title for focus and screen readers. */}
        {chat.data && switchable ? (
          chat.data.main ? null : (
            <h1 id="page-title" tabIndex={-1} className="sr-only">
              {chat.data.title || "New chat"}
            </h1>
          )
        ) : (
          <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4 lg:h-11">
            {embedded ? null : <MenuButton />}
            {chat.data ? (
              <>
                <Link
                  to="/a/$agentId/sessions"
                  params={{ agentId }}
                  className="shrink-0 text-[13px] text-ink-3 hover:text-ink hover:underline"
                >
                  Sessions
                </Link>
                <span aria-hidden className="text-line-strong">
                  /
                </span>
              </>
            ) : null}
            <h1
              id="page-title"
              tabIndex={-1}
              className="min-w-0 truncate text-[13px] font-semibold text-ink outline-none"
            >
              {chat.data?.title || "New session"}
            </h1>
            {chat.data ? <OriginPill chat={chat.data} /> : null}
          </header>
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
              <div className="mx-auto flex min-h-full w-full max-w-[720px] flex-col justify-end gap-6 px-4 py-8 sm:px-8">
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
                <div className="relative mx-auto w-full max-w-[720px]">
                  {blank ? (
                    <div className="mb-4 px-2">
                      <p className="text-[20px] leading-tight font-semibold tracking-[-0.01em] text-ink">
                        {`What should ${settings.agent.name} do?`}
                      </p>
                      {!chat.data?.main ? (
                        <p className="mt-1.5 text-[13px] leading-relaxed text-ink-2">
                          This session has its own context. It does not see the main timeline.
                        </p>
                      ) : null}
                    </div>
                  ) : null}
                  {jump ? (
                    <Button
                      size="xs"
                      variant="secondary"
                      className="absolute -top-10 left-1/2 -translate-x-1/2 gap-1"
                      onClick={() => {
                        pinned.current = true;
                        setJump(false);
                        scroller.current?.scrollTo({
                          top: scroller.current.scrollHeight,
                          behavior: "smooth",
                        });
                      }}
                    >
                      <IconArrowDown size={14} />
                      Jump to latest
                    </Button>
                  ) : null}
                  {composer}
                  {blank ? (
                    <div className="mt-6">
                      <p className="mb-1 px-2 text-[12px] font-medium text-ink-3">This bot so far</p>
                      <ul className="text-[13px]">
                        <li className="flex items-baseline gap-3 border-t border-line-soft px-2 py-2 first:border-t-0">
                          <span className="min-w-0 flex-1 text-ink-2">Model</span>
                          <span className={model.ok ? "text-ink" : "text-ink-2"}>
                            {model.ok ? model.label : "None chosen"}
                          </span>
                        </li>
                        <li className="flex items-baseline gap-3 border-t border-line-soft px-2 py-2">
                          <span className="min-w-0 flex-1 text-ink-2">Channels</span>
                          <span className="text-ink">
                            {channelsRunning ? `${channelsRunning} connected` : "None connected"}
                          </span>
                        </li>
                        <li className="flex items-baseline gap-3 border-t border-line-soft px-2 py-2">
                          <span className="min-w-0 flex-1 text-ink-2">Sessions</span>
                          <span className="text-ink tabular-nums">{sessions.data?.chats.length ?? 0}</span>
                        </li>
                      </ul>
                    </div>
                  ) : null}
                  {blank && recent.length ? (
                    <div className="mt-6">
                      <p className="mb-1 px-2 text-[12px] font-medium text-ink-3">Recent in this bot</p>
                      <ul>
                        {recent.map((item) => (
                          <li key={item.id}>
                            <Link
                              to="/a/$agentId/c/$chatId"
                              params={{ agentId, chatId: String(item.id) }}
                              className="flex items-baseline gap-3 rounded-control px-2 py-2 hover:bg-hover"
                            >
                              <span className="min-w-0 flex-1 truncate text-[13px] text-ink">
                                {item.title || "New session"}
                              </span>
                              <span className="shrink-0 text-[12px] text-ink-3 tabular-nums">
                                {taskTime(item.updatedAt)}
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
