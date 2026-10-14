import { useEffect, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { ArrowDown, Check, Clock, Lock, Send } from "lucide-react";
import type {
  Settings,
  ChatEvent,
  UserItem,
  MessageInput,
  Chat as ChatInfo,
} from "../api/generated/types.gen";
import {
  getSettingsOptions,
  getChatOptions,
  getChatQueryKey,
  createChatMutation,
  sendMessageMutation,
  abortChatMutation,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { applyChatEvent, emptyChatState } from "../lib/chat-state";
import { queryClient, refreshChats } from "../lib/queries";
import { useAgentModel } from "../lib/agent";
import { Composer } from "../components/composer";
import { TranscriptItem, RetryNotice } from "../components/agent/transcript";
import { ModelDialog } from "../components/model-dialog";
import { Failure, Loading } from "../components/feedback";
import { Card, Note, Page, PageHeader } from "../components/page";
import { Alert, AlertTitle, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Button as Pill } from "../components/atoms/Button";
import { ValuePill } from "../components/atoms/ValuePill";
import { Shimmer } from "../components/atoms/Shimmer";
import LoadingState from "../components/primitives/LoadingState";
import { cn } from "../lib/utils";

declare module "@tanstack/history" {
  interface HistoryState {
    firstMessage?: UserItem;
  }
}

function Setup({ settings }: { settings: Settings }) {
  const navigate = useNavigate();
  const [picker, setPicker] = useState(false);
  const models = useQuery(listModelsOptions());
  const hasProvider = settings.providers.length > 0;
  const hasModels = !!models.data?.models.length;

  const steps = [
    {
      done: hasProvider,
      title: "Add a provider",
      body: "Connect Anthropic, OpenAI, OpenRouter, or a local server such as LM Studio.",
      label: "Add provider",
      action: () => void navigate({ to: "/providers" }),
    },
    {
      done: !!settings.agent.modelId,
      title: "Choose the agent model",
      body: hasModels ? "Pick one of the available models." : "Models appear after a provider lists them.",
      label: "Choose model",
      action: () => setPicker(true),
      disabled: !hasModels,
    },
    {
      done: settings.telegram.enabled,
      title: "Connect Telegram (optional)",
      body: "The web works now. Add a Telegram bot to reach dex from your phone.",
      label: "Open Telegram",
      action: () => void navigate({ to: "/channels/$channelId", params: { channelId: "telegram" } }),
      optional: true,
    },
  ];

  return (
    <Page className="max-w-2xl">
      <PageHeader
        title="Set up dex"
        description="dex needs one model before it can answer. Everything else is optional."
      />
      <ol className="flex flex-col gap-3">
        {steps.map((step, i) => (
          <li key={step.title}>
            <Card className="flex items-start gap-4">
              <span
                aria-hidden
                className={cn(
                  "flex size-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold",
                  step.done ? "bg-green text-white" : "bg-inset text-ink-2",
                )}
              >
                {step.done ? <Check className="size-4" /> : i + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h2 className="text-sm font-semibold">
                  {step.title}
                  {step.done ? <span className="sr-only"> (done)</span> : null}
                </h2>
                <p className="mt-1 text-[13px] text-ink-2">{step.body}</p>
              </div>
              {!step.done ? (
                <Pill
                  size="sm"
                  variant={step.optional ? "secondary" : "primary"}
                  onClick={step.action}
                  disabled={step.disabled}
                >
                  {step.label}
                </Pill>
              ) : null}
            </Card>
          </li>
        ))}
      </ol>
      {models.isError ? (
        <div className="mt-4">
          <Failure
            title="Could not load the models"
            error={models.error}
            retry={() => void models.refetch()}
          />
        </div>
      ) : null}
      <div className="mt-6">
        <Note>
          Tools run on this computer without a sandbox, and the server has no sign-in. Keep dex on 127.0.0.1.
        </Note>
      </div>
      <ModelDialog open={picker} onOpenChange={setPicker} />
    </Page>
  );
}

export function ChatPage({ chatId }: { chatId?: number }) {
  const settings = useQuery(getSettingsOptions());

  return settings.isPending ? (
    <Page>
      <Loading />
    </Page>
  ) : settings.isError ? (
    <Page>
      <Failure title="Could not load settings" error={settings.error} retry={() => void settings.refetch()} />
    </Page>
  ) : !chatId && !settings.data.agent.modelId ? (
    <Setup settings={settings.data} />
  ) : (
    <Chat key={chatId ?? "new"} chatId={chatId} settings={settings.data} />
  );
}

function OriginBadge({ chat }: { chat: ChatInfo }) {
  if (chat.channel === "telegram")
    return (
      <ValuePill>
        <Send aria-hidden className="mr-1 inline size-3" />
        Telegram · read-only here
      </ValuePill>
    );

  if (chat.channel === "automation")
    return chat.automationId ? (
      <Link
        to="/automations/$automationId"
        params={{ automationId: chat.automationId }}
        className="rounded-full hover:underline focus-visible:outline-2 focus-visible:outline-ink"
      >
        <ValuePill>
          <Clock aria-hidden className="mr-1 inline size-3" />
          Automation
        </ValuePill>
      </Link>
    ) : (
      <ValuePill>
        <Clock aria-hidden className="mr-1 inline size-3" />
        Automation
      </ValuePill>
    );

  return <ValuePill>Web</ValuePill>;
}

function Chat({ chatId, settings }: { chatId?: number; settings: Settings }) {
  const navigate = useNavigate();

  const chat = useQuery({
    ...getChatOptions({ path: { chatId: chatId ?? 0 } }),
    enabled: chatId !== undefined,
  });

  const [state, setState] = useState(emptyChatState);

  const [connection, setConnection] = useState<"connecting" | "ready" | "error">(
    chatId ? "connecting" : "ready",
  );

  const [streamError, setStreamError] = useState("");
  const reconnect = useRef<(() => void) | null>(null);
  const firstMessage = useRouterState({ select: (router) => router.location.state.firstMessage });
  const [queued, setQueued] = useState<UserItem[]>(() => (firstMessage ? [firstMessage] : []));
  const [draft, setDraft] = useState("");
  const [jump, setJump] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const wasRunning = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const snapshotSeen = useRef(false);
  const failedMessage = useRef<MessageInput | null>(null);
  const create = useMutation(createChatMutation());
  const send = useMutation(sendMessageMutation());
  const model = useAgentModel(settings) ?? { label: "", ok: false };

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
      source.onerror = () => setConnection("error");
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

  const telegram = chat.data?.channel === "telegram";

  const reason = telegram
    ? "Reply in Telegram. Telegram chats are read-only on the web."
    : !model.ok
      ? "Choose an available model before you send."
      : chatId && connection !== "ready"
        ? "dex is reconnecting to the agent…"
        : undefined;

  const pending = create.isPending || send.isPending || abort.isPending;
  const error = create.error ?? send.error ?? abort.error;

  async function sendText(text: string) {
    // A transport retry keeps its ID. A retry of a failed agent turn gets a new ID.
    const body =
      failedMessage.current?.text === text ? failedMessage.current : { id: crypto.randomUUID(), text };

    failedMessage.current = body;

    if (chatId) {
      setQueued((previous) => [...previous, { kind: "user", ...body, queued: true }]);

      try {
        await send.mutateAsync({ path: { chatId }, body });
        failedMessage.current = null;
        setDraft((current) => (current.trim() === text ? "" : current));
        await refreshChats();
      } catch (cause) {
        setQueued((previous) => previous.filter((item) => item.id !== body.id));
        throw cause;
      }
    } else {
      const result = await create.mutateAsync({ body });
      failedMessage.current = null;
      await refreshChats();
      queryClient.setQueryData(getChatQueryKey({ path: { chatId: result.chat.id } }), result.chat);
      await navigate({
        to: "/c/$chatId",
        params: { chatId: String(result.chat.id) },
        state: { firstMessage: { kind: "user", ...body, queued: true } },
      });
    }
  }

  function retryText(index: number) {
    const previous = state.items.slice(0, index).findLast((item) => item.kind === "user");

    if (previous?.kind === "user") {
      failedMessage.current = null;
      void sendText(previous.text).catch(() => {});
    }
  }

  const running = state.running;

  return (
    <div className="flex h-full min-h-0 flex-col bg-page">
      {chatId ? (
        <header className="flex min-h-14 shrink-0 flex-wrap items-center gap-2 border-b border-line bg-page/90 px-4 py-3 backdrop-blur sm:px-8">
          <h1
            id="page-title"
            tabIndex={-1}
            className="min-w-0 flex-1 truncate text-base font-semibold outline-none"
          >
            {chat.data?.title || "Chat"}
          </h1>
          {chat.data ? <OriginBadge chat={chat.data} /> : null}
          {running ? (
            <ValuePill tone="orange">
              <Shimmer>Running</Shimmer>
            </ValuePill>
          ) : null}
        </header>
      ) : null}
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-8"
        onScroll={() => {
          const el = scroller.current;

          if (el) {
            pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
            setJump(!pinned.current);
          }
        }}
      >
        <div className="mx-auto max-w-3xl pb-10 pt-6">
          {chatId && chat.isPending ? (
            <div className="flex h-60 items-center justify-center">
              <LoadingState label="Loading chat" />
            </div>
          ) : chatId && chat.isError ? (
            <div className="flex flex-col items-start gap-4">
              <Failure
                title="Could not load this chat"
                error={chat.error}
                retry={() => void chat.refetch()}
              />
              <Link className="text-sm text-brand underline" to="/">
                Start a new chat
              </Link>
            </div>
          ) : chatId ? (
            <>
              {telegram ? (
                <div className="mb-6 flex items-start gap-2 rounded-xl bg-inset px-4 py-3 text-[13px] text-ink-2">
                  <Lock aria-hidden className="mt-0.5 size-4 shrink-0" />
                  <p>
                    This chat came from Telegram. dex replies in Telegram. The web shows the same transcript
                    and the same memory, but you cannot post here.
                  </p>
                </div>
              ) : null}
              {connection !== "ready" ? (
                <Alert className="mb-6">
                  <AlertTitle>
                    {connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the agent…"}
                  </AlertTitle>
                  <AlertDescription>
                    <p>
                      {streamError ||
                        "Messages are paused until the stream returns. Your transcript stays here."}
                    </p>
                    <Button variant="outline" size="sm" onClick={() => reconnect.current?.()}>
                      Reconnect
                    </Button>
                  </AlertDescription>
                </Alert>
              ) : null}
              <div className="flex flex-col gap-6">
                {items.map((item, index) => (
                  <TranscriptItem
                    key={item.id}
                    item={item}
                    live={running && index === state.items.length - 1}
                    retry={() => retryText(index)}
                    canRetry={
                      !reason && !pending && state.items.slice(0, index).some((i) => i.kind === "user")
                    }
                  />
                ))}
                {state.retry ? <RetryNotice retry={state.retry} /> : null}
                {running &&
                !state.retry &&
                !items.some((item) => item.kind === "tool" && item.status === "running") &&
                items.at(-1)?.kind !== "assistant" &&
                items.at(-1)?.kind !== "thinking" ? (
                  <div className="rounded-xl border border-line bg-surface p-4 shadow-card">
                    <LoadingState label="dex is working" />
                  </div>
                ) : null}
                {!items.length && connection === "ready" ? (
                  <p className="py-12 text-center text-sm text-ink-2">No messages yet.</p>
                ) : null}
              </div>
            </>
          ) : (
            <div className="flex min-h-[50dvh] flex-col justify-end pt-10">
              <h1
                id="page-title"
                tabIndex={-1}
                className="mb-3 text-center text-2xl font-semibold tracking-tight outline-none sm:text-3xl"
              >
                What should dex do?
              </h1>
              <p className="text-center text-sm text-ink-2">
                One agent with its own workspace and memory, on this computer.
              </p>
            </div>
          )}
        </div>
      </div>
      <div className="sr-only" role="status" aria-live="polite">
        {announcement}
      </div>
      <div className="relative mx-auto w-full max-w-[52rem] px-4 pb-4 pt-3 sm:px-8">
        {jump ? (
          <Button
            variant="outline"
            size="sm"
            className="absolute -top-10 left-1/2 -translate-x-1/2 shadow-sm"
            onClick={() => {
              pinned.current = true;
              setJump(false);
              scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: "smooth" });
            }}
          >
            <ArrowDown className="size-3.5" />
            Jump to latest
          </Button>
        ) : null}
        {error ? (
          <div className="mb-3">
            <Failure
              title="Could not complete this action"
              error={error}
              retry={() => {
                if ((create.isError || send.isError) && failedMessage.current)
                  void sendText(failedMessage.current.text).catch(() => {});
                else if (abort.isError && chatId) abort.mutate({ path: { chatId } });
              }}
            />
          </div>
        ) : null}
        <Composer
          model={model}
          hasProvider={settings.providers.length > 0}
          running={running}
          pending={pending}
          disabledReason={reason}
          readOnly={telegram}
          autoFocus={!chatId || !!firstMessage}
          onSend={sendText}
          onStop={() => {
            if (chatId) abort.mutate({ path: { chatId } });
          }}
          text={draft}
          onTextChange={setDraft}
        />
      </div>
    </div>
  );
}
