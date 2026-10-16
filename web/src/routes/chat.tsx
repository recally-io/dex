import { useEffect, useState, useRef, type ReactNode } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { IconArrowDown } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconArrowDown";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconLock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconLock";
import { IconPaperPlane } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPaperPlane";
import { IconSidebarSimpleRightWide } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconSidebarSimpleRightWide";
import type {
  Attachment,
  Settings,
  ChatEvent,
  Item,
  ToolItem,
  UserItem,
  MessageInput,
  Chat as ChatInfo,
} from "../api/generated/types.gen";
import {
  getAutomationOptions,
  getHealthOptions,
  getSettingsOptions,
  getChatOptions,
  getMainChatOptions,
  sendMessageMutation,
  abortChatMutation,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { applyChatEvent, emptyChatState, mergeHistory, retryMessage } from "../lib/chat-state";
import { refreshChats } from "../lib/queries";
import { chatModel, thinkingNames } from "../lib/agent";
import { useAttachments } from "../lib/attachments";
import { useApp } from "../lib/app-state";
import { describeTool, touchesMemory } from "../lib/tools";
import { Composer } from "../components/composer";
import { ClearContextButton, LoadEarlierButton } from "../components/timeline-actions";
import { RetryNotice, Transcript, type ToolFocus } from "../components/agent/transcript";
import { ModelPicker } from "../components/model-picker";
import { Failure, Loading } from "../components/feedback";
import { FramedPage, MainPane, MenuButton, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import LoadingState from "../components/primitives/LoadingState";
import { checkSession } from "../lib/session";
import { newId } from "../lib/utils";

function Done() {
  return (
    <StatusPill tone="green" className="h-5 px-2 text-[11.5px]">
      Done
    </StatusPill>
  );
}

function Setup({ settings }: { settings: Settings }) {
  const navigate = useNavigate();
  const models = useQuery(listModelsOptions());
  const hasProvider = settings.providers.length > 0;
  const hasModels = !!models.data?.models.length;

  return (
    <FramedPage
      title="Set up dex"
      description="dex needs one model before it can answer. Everything else is optional."
    >
      <Section
        n={1}
        title="Add a provider"
        caption="A saved connection to a model service."
        action={hasProvider ? <Done /> : null}
      >
        <Panel className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
          <p className="min-w-0 flex-1 text-[13px] text-ink-2">
            Connect Anthropic, OpenAI, OpenRouter, or a local server such as LM Studio.
          </p>
          {hasProvider ? null : (
            <Button size="sm" variant="primary" onClick={() => void navigate({ to: "/providers" })}>
              Add provider
            </Button>
          )}
        </Panel>
      </Section>
      <Section
        n={2}
        title="Choose the agent model"
        caption="The default model of every chat."
        action={settings.agent.modelId ? <Done /> : null}
      >
        <Panel className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
          <p className="min-w-0 flex-1 text-[13px] text-ink-2">
            {hasModels ? "Pick one of the available models." : "Models appear after a provider lists them."}
          </p>
          <ModelPicker
            trigger={
              <Button size="sm" variant="primary" disabled={!hasModels}>
                Choose model
              </Button>
            }
          />
        </Panel>
        {models.isError ? (
          <div className="mt-3">
            <Failure
              title="Could not load the models"
              error={models.error}
              retry={() => void models.refetch()}
            />
          </div>
        ) : null}
      </Section>
      <Section
        n={3}
        title="Connect Telegram"
        caption="Optional. The web works now."
        action={settings.telegram.enabled ? <Done /> : null}
      >
        <Panel className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
          <p className="min-w-0 flex-1 text-[13px] text-ink-2">
            Add a Telegram bot to reach dex from your phone.
          </p>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void navigate({ to: "/channels/$channelId", params: { channelId: "telegram" } })}
          >
            Open Telegram
          </Button>
        </Panel>
      </Section>
      <Section n={4} title="Before you start" caption="Read this once.">
        <Note tone="warn">
          Tools run on this computer without a sandbox, and the server has no sign-in. Keep dex on 127.0.0.1.
        </Note>
      </Section>
    </FramedPage>
  );
}

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
  ) : !chatId && !settings.data.agent.modelId ? (
    <Setup settings={settings.data} />
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

function OriginPill({ chat }: { chat: ChatInfo }) {
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

// The Context panel is a UI choice of this browser, not a dex setting. It is open by default.
const contextKey = "dex-context";

function useContextOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(contextKey) !== "closed");

  return [
    open,
    (next: boolean) => {
      localStorage.setItem(contextKey, next ? "open" : "closed");
      setOpen(next);
    },
  ] as const;
}

function Heading({ n, title, detail }: { n: number; title: string; detail?: string }) {
  return (
    <div className="mb-2.5 flex items-baseline gap-2">
      <span className="font-mono text-[10.5px] text-ink-3 tabular-nums">{String(n).padStart(2, "0")}</span>
      <h3 className="text-[13px] font-semibold text-ink">{title}</h3>
      {detail ? <span className="truncate text-[12px] text-ink-3">{detail}</span> : null}
    </div>
  );
}

// Card style from upstream primitives/ContextCards.tsx.
function CardBar({ children, meta }: { children: ReactNode; meta?: ReactNode }) {
  return (
    <div className="flex h-9 items-center gap-2.5 border-b border-line px-3">
      <span className="flex min-w-0 items-center gap-1.5 text-[13px] font-medium text-ink">
        <svg
          width="11"
          height="11"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          aria-hidden
          className="shrink-0"
        >
          <path d="M4 6h16M4 12h16M4 18h10" />
        </svg>
        <span className="truncate">{children}</span>
      </span>
      {meta ? <span className="ml-auto shrink-0 text-[12px] text-ink-3 tabular-nums">{meta}</span> : null}
    </div>
  );
}

/** What dex has for this chat: the agent, the channel, the tool calls and the memory use. */
function ContextPane({
  chat,
  items,
  settings,
  memory,
  onClose,
  onFocusTool,
}: {
  chat: ChatInfo;
  items: Item[];
  settings: Settings;
  memory?: string;
  onClose: () => void;
  onFocusTool: (id: string) => void;
}) {
  const models = useQuery(listModelsOptions());
  const { model, overrides } = chatModel(settings.agent, chat.settings, models.data?.models);
  const { draft } = useApp();

  const automation = useQuery({
    ...getAutomationOptions({ path: { automationId: chat.automationId ?? "" } }),
    enabled: !!chat.automationId,
  });

  const tools = items.filter((item): item is ToolItem => item.kind === "tool");
  const memoryTools = tools.filter((tool) => touchesMemory(tool, memory));
  let answers = 0;
  const tokens = { input: 0, output: 0 };

  for (const item of items) {
    if (item.kind !== "assistant") continue;
    answers++;
    tokens.input += item.usage?.input ?? 0;
    tokens.output += item.usage?.output ?? 0;
  }

  const lines = settings.agent.instructions.split("\n").filter(Boolean).length;
  const dirty = draft !== null && draft !== settings.agent.instructions;

  return (
    <aside
      aria-label="Context"
      className="hidden w-[344px] shrink-0 animate-[fade-in_300ms_ease_both] flex-col overflow-hidden rounded-window border border-line bg-page xl:flex"
    >
      <div className="flex h-11 shrink-0 items-center justify-between border-b border-line pr-2 pl-4">
        <span className="text-[13px] font-semibold text-ink">Context</span>
        <button
          type="button"
          aria-label="Close context"
          onClick={onClose}
          className="flex size-7 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <IconCrossSmall size={16} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <div className="flex flex-col gap-7">
          <div>
            <Heading
              n={1}
              title="Agent"
              detail={overrides ? "this chat overrides the default" : "the default for every chat"}
            />
            <div className="overflow-hidden rounded-card bg-surface shadow-card">
              <CardBar
                meta={
                  <span className={model.ok ? "" : "text-orange"}>
                    {model.ok ? "Available" : "Unavailable"}
                  </span>
                }
              >
                {model.label}
              </CardBar>
              <dl className="flex flex-col gap-1.5 px-3 py-2.5 text-[12.5px]">
                <div className="flex justify-between gap-3">
                  <dt className="text-ink-3">Model ID</dt>
                  <dd className="min-w-0 truncate font-mono text-[11.5px] text-ink-2">
                    {chat.settings.modelId
                      ? `${chat.settings.provider}/${chat.settings.modelId}`
                      : `${settings.agent.provider}/${settings.agent.modelId}`}
                  </dd>
                </div>
                <div className="flex justify-between gap-3">
                  <dt className="text-ink-3">Thinking</dt>
                  <dd className="text-ink-2">
                    {model.model && !model.model.reasoning
                      ? "Off"
                      : chat.settings.thinkingLevel
                        ? thinkingNames[chat.settings.thinkingLevel]
                        : "Default"}
                  </dd>
                </div>
                <div className="flex justify-between gap-3">
                  <dt className="text-ink-3">Instructions</dt>
                  <dd className="text-ink-2">
                    <Link to="/agent" className="hover:underline">
                      {lines} {lines === 1 ? "line" : "lines"}
                      {dirty ? " · draft open" : ""}
                    </Link>
                  </dd>
                </div>
                {answers ? (
                  <div className="flex justify-between gap-3">
                    <dt className="text-ink-3">Tokens in this chat</dt>
                    <dd className="font-mono text-[11.5px] text-ink-2 tabular-nums">
                      {tokens.input.toLocaleString()} in · {tokens.output.toLocaleString()} out
                    </dd>
                  </div>
                ) : null}
              </dl>
            </div>
          </div>

          <div>
            <Heading n={2} title="Channel" />
            <div className="rounded-card bg-surface px-3 py-2.5 text-[12.5px] leading-relaxed text-ink-2 shadow-card">
              {!["web", "automation"].includes(chat.channel) ? (
                <>
                  <span className="mb-1 flex items-center gap-1.5 font-medium text-ink">
                    <IconPaperPlane size={13} /> {chat.channel}
                  </span>
                  dex replies in the original channel. The web shows the same transcript and memory,
                  read-only.
                </>
              ) : chat.channel === "automation" ? (
                <>
                  <span className="mb-1 flex items-center gap-1.5 font-medium text-ink">
                    <IconClock size={13} /> Automation
                  </span>
                  Started by{" "}
                  {chat.automationId && automation.data ? (
                    <Link
                      to="/automations/$automationId"
                      params={{ automationId: chat.automationId }}
                      className="font-medium text-accent-ink hover:underline"
                    >
                      {automation.data.name}
                    </Link>
                  ) : chat.automationId && !automation.isError ? (
                    "an automation"
                  ) : (
                    "a deleted automation"
                  )}
                  . You can reply here.
                </>
              ) : (
                <>
                  <span className="mb-1 block font-medium text-ink">Web</span>
                  This page, on this computer. You can read and send.
                </>
              )}
            </div>
          </div>

          <div>
            <Heading
              n={3}
              title="Tool calls"
              detail={tools.length ? `${tools.length} in this chat` : undefined}
            />
            {tools.length ? (
              <div className="overflow-hidden rounded-card bg-surface p-1 shadow-card">
                {tools.map((tool) => {
                  const { verb, target } = describeTool(tool, memory);

                  return (
                    <button
                      key={tool.id}
                      type="button"
                      onClick={() => onFocusTool(tool.id)}
                      className="flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left transition-colors hover:bg-hover"
                    >
                      <span
                        role="img"
                        aria-label={
                          tool.status === "error" ? "Failed" : tool.status === "running" ? "Running" : "Done"
                        }
                        className={`size-1.5 shrink-0 rounded-full ${tool.status === "error" ? "bg-red" : tool.status === "running" ? "bg-orange" : "bg-green"}`}
                      />
                      <span className="shrink-0 text-[12.5px] font-medium text-ink">{verb}</span>
                      <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-3">
                        {target}
                      </span>
                    </button>
                  );
                })}
              </div>
            ) : (
              <p className="px-0.5 text-[12.5px] text-ink-3">No tool calls yet.</p>
            )}
          </div>

          <div>
            <Heading n={4} title="Memory" detail="shared by every chat" />
            <div className="overflow-hidden rounded-card bg-surface shadow-card">
              <CardBar meta="in every prompt">MEMORY.md</CardBar>
              <div className="px-3 py-2.5 text-[12.5px] leading-relaxed text-ink-2">
                {memoryTools.length ? (
                  <ul className="flex flex-col gap-1.5">
                    {memoryTools.map((tool) => {
                      const { verb, target } = describeTool(tool, memory);

                      return (
                        <li key={tool.id} className="flex min-w-0 items-center gap-2">
                          <span className="inline-flex h-6 min-w-0 items-center gap-1.5 rounded-full bg-inset px-2 text-[12px] font-medium text-ink-2 shadow-btn">
                            <span className="truncate font-mono text-[11px]">{target}</span>
                          </span>
                          <span className="shrink-0 text-[11.5px] text-ink-3">
                            {verb.replace(" memory", "")}
                          </span>
                        </li>
                      );
                    })}
                  </ul>
                ) : (
                  "This chat did not search or change other memory files."
                )}
                <p className="mt-2 text-[11.5px] text-ink-3">
                  dex has no memory page yet. The agent changes memory with its tools.
                </p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </aside>
  );
}

function Chat({ chatId, settings }: { chatId: number; settings: Settings }) {
  const chat = useQuery({
    ...getChatOptions({ path: { chatId: chatId ?? 0 } }),
    enabled: chatId !== undefined,
  });

  const health = useQuery(getHealthOptions());
  const memory = health.data ? `${health.data.dataDir}/memory` : undefined;
  const [state, setState] = useState(emptyChatState);

  const [connection, setConnection] = useState<"connecting" | "ready" | "error">(
    chatId ? "connecting" : "ready",
  );

  const [streamError, setStreamError] = useState("");
  const reconnect = useRef<(() => void) | null>(null);
  const [queued, setQueued] = useState<UserItem[]>([]);
  const [draft, setDraft] = useState("");
  const [jump, setJump] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [contextOpen, setContextOpen] = useContextOpen();
  const [focus, setFocus] = useState<ToolFocus>();
  const [composerHeight, setComposerHeight] = useState(150);
  const wasRunning = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);
  const composerBox = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const snapshotSeen = useRef(false);
  const failedMessage = useRef<{ body: MessageInput; attachments: Attachment[] } | null>(null);
  const send = useMutation(sendMessageMutation());
  const models = useQuery(listModelsOptions());
  const files = useAttachments();
  const { model } = chatModel(settings.agent, {}, models.data?.models);

  const abort = useMutation({
    ...abortChatMutation(),
    onSuccess: async () => {
      setQueued([]);
      await refreshChats();
    },
  });

  // A reconnect starts with a complete snapshot; local deltas need no replay.
  useEffect(() => {
    if (!chatId || !chat.isSuccess) return;
    let source: EventSource | undefined;

    function connect() {
      source?.close();
      snapshotSeen.current = false;
      setConnection("connecting");
      source = new EventSource(`/api/chats/${chatId}/events`);
      source.onopen = () => setConnection("connecting");

      const receive = (message: MessageEvent<string>) => {
        try {
          const event: ChatEvent = JSON.parse(message.data);

          if (!["snapshot", "item", "append", "run"].includes(event.type))
            throw new Error("Unknown chat event.");

          if (!snapshotSeen.current && event.type !== "snapshot")
            throw new Error("The stream did not start with a snapshot.");

          if (event.type === "snapshot") snapshotSeen.current = true;
          setState((previous) => applyChatEvent(previous, event));
          setConnection("ready");
          setStreamError("");

          if (event.type === "snapshot")
            setQueued((previous) =>
              previous.filter((item) => !event.items.some((remote) => remote.id === item.id)),
            );

          if (event.type === "item")
            setQueued((previous) => previous.filter((item) => item.id !== event.item.id));

          if (event.type === "run" && !event.running) void refreshChats();
        } catch {
          source?.close();
          setConnection("error");
          setStreamError("The chat stream returned an invalid event. Reconnect to load a new snapshot.");
        }
      };

      for (const type of ["snapshot", "item", "append", "run"]) source.addEventListener(type, receive);
      source.onerror = () => {
        setConnection("error");
        checkSession();
      };
    }

    reconnect.current = connect;
    connect();

    return () => {
      reconnect.current = null;
      source?.close();
    };
  }, [chatId, chat.isSuccess]);

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

    return () => observer.disconnect();
  }, []);

  // The composer floats over the thread. The thread keeps space for it at the bottom.
  useEffect(() => {
    const box = composerBox.current;

    if (!box) return;
    const observer = new ResizeObserver(() => setComposerHeight(box.offsetHeight));
    observer.observe(box);

    return () => observer.disconnect();
  }, []);

  const readOnly = chat.data?.readOnly ?? true;

  const reason = readOnly
    ? "This chat is read-only. Send messages in the main timeline."
    : !model.ok
      ? "Choose an available model before you send."
      : chatId && connection !== "ready"
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

  const composer = (
    <>
      {abort.isSuccess ? (
        <p role="status" className="mb-3 text-[12.5px] text-ink-2">
          Stop requested. Earlier actions are not undone. Processes and remote work may continue.
        </p>
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
      <Composer
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
        files={readOnly ? undefined : files}
        running={running}
        pending={pending}
        disabledReason={reason}
        readOnly={readOnly}
        placeholder={`Message ${settings.agent.name}`}
        autoFocus={!readOnly}
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
        <Failure title="Could not load this chat" error={chat.error} retry={() => void chat.refetch()} />
        <Link className="text-[13px] font-medium text-accent-ink underline" to="/">
          Open timeline
        </Link>
      </div>
    ) : (
      <>
        {readOnly ? (
          <div className="flex items-start gap-2.5 rounded-card bg-inset px-3.5 py-2.5 text-[12.5px] leading-relaxed text-ink-2 shadow-hairline">
            <IconLock size={14} className="mt-0.5 shrink-0" />
            <p>This chat is read-only. Open the timeline to send a message.</p>
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
          canRetry={!reason && !pending}
          memory={memory}
          focus={focus}
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
        {!items.length && connection === "ready" ? (
          <p className="py-12 text-center text-[13px] text-ink-3">No messages yet.</p>
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
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-page lg:rounded-window lg:border lg:border-line">
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4 lg:h-11">
          <MenuButton />
          <h1
            id="page-title"
            tabIndex={-1}
            className="min-w-0 truncate text-[13px] font-semibold text-ink outline-none"
          >
            {chat.data?.main ? settings.agent.name : chat.data?.title || "Chat"}
          </h1>
          {chat.data ? <OriginPill chat={chat.data} /> : null}
          {running ? (
            <StatusPill tone="orange" className="h-5 shrink-0 px-2 text-[11.5px]">
              Running
            </StatusPill>
          ) : null}
          <span className="flex-1" />
          {chat.data?.main ? (
            <ClearContextButton
              chatId={chatId}
              busy={
                state.running ||
                pending ||
                queued.length > 0 ||
                items.some((item) => item.kind === "user" && item.queued)
              }
            />
          ) : null}
          {chat.data && !readOnly ? (
            <Button
              size="xs"
              variant="secondary"
              disabled={abort.isPending}
              onClick={() => abort.mutate({ path: { chatId } })}
            >
              {abort.isPending ? "Requesting stop…" : "Stop"}
            </Button>
          ) : null}
          {chat.data && !contextOpen ? (
            <button
              type="button"
              aria-label="Open context"
              onClick={() => setContextOpen(true)}
              className="hidden h-7 items-center gap-1.5 rounded-[7px] px-2 text-[12.5px] font-medium text-ink-2 transition-colors hover:bg-hover hover:text-ink xl:flex"
            >
              <IconSidebarSimpleRightWide size={16} /> Context
            </button>
          ) : null}
        </header>
        <div className="relative flex min-h-0 flex-1 flex-col">
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
            <div
              className="mx-auto flex w-full max-w-[720px] flex-col gap-6 px-4 pt-8 sm:px-8"
              style={{ paddingBottom: composerHeight + 16 }}
            >
              {transcript}
            </div>
          </div>
          <div
            aria-hidden
            className="pointer-events-none absolute inset-x-0 bottom-0 bg-[linear-gradient(to_top,var(--page)_64%,transparent)]"
            style={{ height: composerHeight + 32 }}
          />
          <div ref={composerBox} className="absolute inset-x-0 bottom-0 px-3 pb-3 sm:px-8 sm:pb-5">
            <div className="relative mx-auto max-w-[720px]">
              {jump ? (
                <Button
                  size="xs"
                  variant="secondary"
                  className="absolute -top-10 left-1/2 -translate-x-1/2 gap-1"
                  onClick={() => {
                    pinned.current = true;
                    setJump(false);
                    scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: "smooth" });
                  }}
                >
                  <IconArrowDown size={14} />
                  Jump to latest
                </Button>
              ) : null}
              {composer}
            </div>
          </div>
        </div>
        {status}
      </section>
      {chat.data && contextOpen ? (
        <ContextPane
          chat={chat.data}
          items={items}
          settings={settings}
          memory={memory}
          onClose={() => setContextOpen(false)}
          onFocusTool={(id) => setFocus({ id, at: Date.now() })}
        />
      ) : null}
    </>
  );
}
