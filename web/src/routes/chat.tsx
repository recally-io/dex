import { useCallback, useEffect, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { IconArrowDown } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconArrowDown";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconLock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconLock";
import { IconPaperPlane } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPaperPlane";
import { IconBubble2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBubble2";
import { IconChevronDownSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronDownSmall";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconAlignmentLeft } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconAlignmentLeft";
import type {
  Attachment,
  Settings,
  UserItem,
  MessageInput,
  Chat as ChatInfo,
} from "../api/generated/types.gen";
import {
  getHealthOptions,
  getSettingsOptions,
  getMainChatOptions,
  sendMessageMutation,
  abortChatMutation,
  listModelsOptions,
  listChatsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { mergeHistory, retryMessage } from "../lib/chat-state";
import { useChatStream } from "../lib/chat-stream";
import { refreshChats } from "../lib/queries";
import { agentModel, saveAgent, thinkingNames, type AgentModel } from "../lib/agent";
import { instructionsDirty } from "../lib/instructions";
import { useApp } from "../lib/app-state";
import { useAttachments } from "../lib/attachments";
import { activeSessionCount } from "../lib/tasks";
import { Composer } from "../components/composer";
import { hasContext, useClearContext, LoadEarlierButton } from "../components/timeline-actions";
import { SessionsLink } from "../components/session-link";
import { RetryNotice, Transcript } from "../components/agent/transcript";
import { ModelPicker } from "../components/model-picker";
import { ThinkingPicker } from "../components/thinking-picker";
import { InstructionsEditor } from "../components/instructions-editor";
import { AgentDashboard, AgentDashboardSheet, useDashboardOpen } from "../components/agent-dashboard";
import { Failure, Loading } from "../components/feedback";
import { MainPane, MenuButton, Note } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import LoadingState from "../components/primitives/LoadingState";
import { cn, newId } from "../lib/utils";

export function ChatPage({ chatId }: { chatId?: number }) {
  const settings = useQuery(getSettingsOptions());

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

function MainTimeline({ settings }: { settings: Settings }) {
  const main = useQuery(getMainChatOptions());

  return main.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : main.isError ? (
    <MainPane>
      <Failure title="Could not open the timeline" error={main.error} retry={() => void main.refetch()} />
    </MainPane>
  ) : (
    <Chat key={main.data.id} chatId={main.data.id} settings={settings} />
  );
}

/** The width of the pane, measured from a callback ref so it is known on the first commit. */
function usePaneWidth() {
  const [width, setWidth] = useState(0);

  const ref = useCallback((pane: HTMLElement | null) => {
    if (!pane) return;

    setWidth(pane.getBoundingClientRect().width);

    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));

    observer.observe(pane);

    return () => observer.disconnect();
  }, []);

  return [ref, width] as const;
}

function OriginPill({ chat }: { chat: ChatInfo }) {
  if (chat.channel === "web" && !chat.main)
    return (
      <StatusPill dot={false} className="h-5 shrink-0 gap-1 px-2 text-[11.5px]">
        <IconBubble2 size={11} /> Chat
      </StatusPill>
    );

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
        to="/automations/$automationId"
        params={{ automationId: chat.automationId }}
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

function Chat({ chatId, settings }: { chatId: number; settings: Settings }) {
  const { chat, state, setState, queued, setQueued, connection, streamError, reconnect } =
    useChatStream(chatId);

  const readOnly = chat.data?.readOnly ?? true;

  const sessions = useQuery({
    ...listChatsOptions(),
    enabled: !readOnly,
    refetchInterval: 3000,
  });

  const active = activeSessionCount(sessions.data?.chats ?? []);

  const activeHere = activeSessionCount(
    (sessions.data?.chats ?? []).filter((session) => session.task?.parentConversationId === chatId),
  );

  const health = useQuery(getHealthOptions());
  const memory = health.data ? `${health.data.dataDir}/memory` : undefined;
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState(false);
  const [pane, width] = usePaneWidth();
  // The side panel needs room for the transcript beside it; the tiles need room for five columns.
  const wide = width >= 960;
  const roomy = width >= 720;
  const [dashboard, toggleDashboard] = useDashboardOpen();
  const [sheet, setSheet] = useState(false);
  const [jump, setJump] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const clear = useClearContext(chatId, state.running, () => setAnnouncement("Context cleared."));
  const wasRunning = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const failedMessage = useRef<{ body: MessageInput; attachments: Attachment[] } | null>(null);
  const send = useMutation(sendMessageMutation());
  const models = useQuery(listModelsOptions());
  const files = useAttachments();
  const model = agentModel(settings.agent, models.data?.models);
  const { draft: instructions } = useApp();
  const dirty = instructionsDirty(instructions, settings.agent.instructions);

  const abort = useMutation({
    ...abortChatMutation(),
    onSuccess: async () => {
      setQueued([]);
      await refreshChats();
    },
  });

  // Announce the start and the end of a run once each, not each streamed token.
  useEffect(() => {
    if (state.running && !wasRunning.current) setAnnouncement("The agent is working.");

    if (!state.running && wasRunning.current) setAnnouncement("The agent finished.");

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

  const reason = !model.ok
    ? "Choose an available model before you send."
    : connection !== "ready"
      ? "dex is reconnecting to the agent…"
      : undefined;

  const pending = send.isPending || abort.isPending;
  const error = send.error ?? abort.error;

  async function sendText(text: string, attachments: Attachment[]) {
    abort.reset();
    const ids = attachments.map((a) => a.id);
    const failed = failedMessage.current?.body;

    // A transport retry keeps its ID. A retry of a failed agent turn gets a new ID.
    const body: MessageInput =
      failed?.text === text && failed.attachments?.join() === ids.join()
        ? failed
        : { id: newId(), text, attachments: ids };

    failedMessage.current = { body, attachments };
    const local: UserItem = { kind: "user", id: body.id, text, attachments, queued: true };

    setQueued((previous) => [...previous, local]);

    try {
      await send.mutateAsync({ path: { chatId }, body });
      failedMessage.current = null;
      setDraft((current) => (current.trim() === text ? "" : current));
      files.sent(ids);
      await refreshChats();
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
              else if (abort.isError && chatId) abort.mutate({ path: { chatId } });
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
                <button type="button" className="font-medium text-accent-ink underline">
                  Choose a model
                </button>
              }
            />
          ) : (
            <Link className="font-medium text-accent-ink underline" to="/providers">
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
          if (chatId) abort.mutate({ path: { chatId } });
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
        <Link className="text-[13px] font-medium text-accent-ink underline" to="/">
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
              {connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the agent…"}
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
        {!items.length && state.historyBefore === undefined && connection === "ready" ? (
          <div className="my-auto py-12 text-center">
            <h1 className="text-[22px] font-semibold text-ink sm:text-[26px]">
              {readOnly ? "No messages yet." : `What should ${settings.agent.name} do?`}
            </h1>
            {!chat.data?.main && !readOnly ? (
              <p className="mt-2 text-[13px] text-ink-3">
                This session has its own context. It does not see the main timeline.
              </p>
            ) : null}
          </div>
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
        ref={pane}
        className="flex min-w-0 flex-1 flex-col overflow-hidden bg-page lg:rounded-window lg:border lg:border-line"
      >
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4 lg:h-11">
          <MenuButton />
          {chat.data && !chat.data.main ? (
            <>
              <Link to="/sessions" className="shrink-0 text-[13px] text-ink-3 hover:text-ink hover:underline">
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
            {chat.data?.main ? settings.agent.name : chat.data?.title || "New session"}
          </h1>
          {chat.data && !chat.data.main ? <OriginPill chat={chat.data} /> : null}
          {chat.data?.main ? (
            <AgentStrip
              settings={settings}
              model={model}
              dirty={dirty}
              editing={editing}
              onEdit={() => setEditing(!editing)}
              wide={wide}
            />
          ) : null}
          <span className="flex-1" />
          {chat.data?.main ? <SessionsLink active={active} /> : null}
          {chat.data?.main ? (
            <button
              type="button"
              aria-expanded={roomy ? dashboard : sheet}
              aria-controls={(roomy ? dashboard : sheet) ? "agent-dashboard" : undefined}
              aria-label={`${roomy && dashboard ? "Hide" : "Show"} ${settings.agent.name} dashboard`}
              title="Dashboard"
              onClick={() => (roomy ? toggleDashboard() : setSheet(true))}
              className="flex size-7 shrink-0 items-center justify-center rounded-control text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink"
            >
              <IconChevronDownSmall
                size={18}
                className={cn("transition-transform duration-200", roomy && dashboard && "rotate-180")}
              />
            </button>
          ) : null}
        </header>
        {chat.data?.main && roomy && dashboard ? (
          <AgentDashboard
            settings={settings}
            model={model}
            dirty={dirty}
            onEditInstructions={wide ? () => setEditing(true) : undefined}
          />
        ) : null}
        {chat.data?.main && !roomy && sheet ? (
          <AgentDashboardSheet
            settings={settings}
            model={model}
            dirty={dirty}
            onClose={() => setSheet(false)}
          />
        ) : null}
        <div className="flex min-h-0 flex-1">
          <div className="relative flex min-w-0 flex-1 flex-col">
            <div
              ref={scroller}
              className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
              onScroll={() => {
                const el = scroller.current;

                if (el) {
                  pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
                  setJump(!pinned.current);
                }
              }}
            >
              <div className="mx-auto flex min-h-full w-full max-w-[720px] flex-col gap-6 px-4 py-8 sm:px-8">
                {transcript}
              </div>
            </div>
            {readOnly ? null : (
              <div className="shrink-0 px-3 pb-3 sm:px-8 sm:pb-5">
                <div className="relative mx-auto max-w-[720px]">
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
                </div>
              </div>
            )}
          </div>
          {/* The editor sits beside the transcript, so the answer being fixed stays readable. */}
          {chat.data?.main && editing && wide ? (
            <aside
              aria-label="Agent instructions"
              className="flex w-[400px] shrink-0 flex-col border-l border-line"
            >
              <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3">
                <h2 className="text-[13px] font-semibold text-ink">Instructions</h2>
                {dirty ? (
                  <StatusPill tone="orange" className="h-5 px-2 text-[11.5px]">
                    Draft
                  </StatusPill>
                ) : null}
                <span className="flex-1" />
                <Link to="/agent" className="text-[12px] text-ink-3 hover:text-ink hover:underline">
                  All settings
                </Link>
                <button
                  type="button"
                  aria-label="Close instructions"
                  onClick={() => setEditing(false)}
                  className="flex size-7 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2 hover:text-ink"
                >
                  <IconCrossSmall size={16} />
                </button>
              </div>
              <div className="flex min-h-0 flex-1 flex-col p-2">
                <InstructionsEditor saved={settings.agent.instructions} grow />
              </div>
            </aside>
          ) : null}
        </div>
        {status}
      </section>
    </>
  );
}

/**
 * The model, the thinking level and the instructions of the bot, in the header of its own
 * timeline. A gear in the sidebar was the only way to these, and an icon without text is not
 * findable.
 */
function AgentStrip({
  settings,
  model,
  dirty,
  editing,
  onEdit,
  wide,
}: {
  settings: Settings;
  model: AgentModel;
  dirty: boolean;
  editing: boolean;
  onEdit: () => void;
  wide: boolean;
}) {
  const { notify } = useApp();

  const caret = (
    <svg
      width="10"
      height="10"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="text-ink-3"
    >
      <path d="M6 9l6 6 6-6" />
    </svg>
  );

  const chip =
    "flex h-7 min-w-0 items-center gap-1 rounded-control px-2 text-[12.5px] text-ink-2 transition-colors hover:bg-hover-2 hover:text-ink aria-expanded:bg-hover-2";

  return (
    <div className="flex min-w-0 items-center gap-0.5">
      <ModelPicker
        trigger={
          <button type="button" aria-label="Change model" className={chip}>
            <span className={cn("min-w-0 truncate font-medium", model.ok ? "text-ink" : "text-orange")}>
              {settings.agent.modelId ? model.label : "No model"}
            </span>
            {caret}
          </button>
        }
      />
      {model.model?.reasoning ? (
        <ThinkingPicker
          value={settings.agent.thinkingLevel}
          levels={model.model.thinkingLevels}
          save={async (level) => {
            if (!level) return;
            await saveAgent((agent) => ({ ...agent, thinkingLevel: level }));
            notify(`Thinking is now ${thinkingNames[level]}. It applies from the next message.`);
          }}
          trigger={
            <button type="button" aria-label="Change thinking level" className={cn(chip, "hidden sm:flex")}>
              {thinkingNames[settings.agent.thinkingLevel]}
              {caret}
            </button>
          }
        />
      ) : null}
      {/* Below the width that fits the side panel, the settings page takes over. */}
      {wide ? (
        <button
          type="button"
          aria-expanded={editing}
          aria-label="Edit instructions"
          onClick={onEdit}
          className={cn(chip, editing && "bg-hover-2 text-ink")}
        >
          <IconAlignmentLeft size={14} className="shrink-0" />
          <span className="hidden md:inline">Instructions</span>
          {dirty ? (
            <StatusPill tone="orange" className="h-[18px] px-1.5 text-[11px]">
              Draft
            </StatusPill>
          ) : null}
        </button>
      ) : (
        <Link to="/agent" aria-label="Edit instructions" className={chip}>
          <IconAlignmentLeft size={14} className="shrink-0" />
          <span className="hidden md:inline">Instructions</span>
          {dirty ? (
            <StatusPill tone="orange" className="h-[18px] px-1.5 text-[11px]">
              Draft
            </StatusPill>
          ) : null}
        </Link>
      )}
    </div>
  );
}

function readOnlyCopy(channel?: ChatInfo["channel"]) {
  const names = { telegram: "Telegram", wechat: "WeChat", feishu: "Feishu" };

  if (channel === "telegram" || channel === "wechat" || channel === "feishu")
    return `You can read this conversation here. Reply in ${names[channel]}.`;

  return channel === "automation" ? "You can read this run here." : "You can read this conversation here.";
}
