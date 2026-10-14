import { useEffect, useState, useRef } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { ArrowDown, ArrowUpRight, Check, Plus, Sparkles, Send } from "lucide-react";
import type { Settings, ChatEvent, UserItem, MessageInput } from "../api/generated/types.gen";
import {
  getSettingsOptions,
  getChatOptions,
  getChatQueryKey,
  createChatMutation,
  sendMessageMutation,
  abortChatMutation,
  setAgentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { applyChatEvent, emptyChatState } from "../lib/chat-state";
import { queryClient, refreshChats, refreshSettings } from "../lib/queries";
import { Composer } from "../components/composer";
import { TranscriptItem, RetryNotice } from "../components/agent/transcript";
import { ProviderDialog } from "../components/provider-dialog";
import { ModelPicker } from "../components/model-picker";
import { Failure, Loading } from "../components/feedback";
import { Alert, AlertTitle, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";

declare module "@tanstack/history" {
  interface HistoryState {
    firstMessage?: UserItem;
  }
}

function Setup({ settings }: { settings: Settings }) {
  const [add, setAdd] = useState(false);
  const model = useMutation({ ...setAgentMutation(), onSuccess: refreshSettings });

  return (
    <div className="mx-auto w-full max-w-[600px] px-5 py-14 md:py-24">
      <div className="mb-7 flex size-12 items-center justify-center rounded-2xl bg-ink text-2xl font-semibold text-page">
        d
      </div>
      <p className="mb-3 text-xs font-medium tracking-widest text-ink-2 uppercase">Welcome to dex</p>
      <h1 className="text-3xl font-semibold tracking-tight">A little setup. A lot of possibility.</h1>
      <p className="mt-4 max-w-md leading-relaxed text-ink-2">
        Your personal agent runs here, on your computer. Connect a provider and choose a model to start.
      </p>
      <div className="surface mt-9 divide-y divide-line">
        <div className="flex items-center gap-4 p-5">
          <Step done={!!settings.providers.length} number={1} />
          <div className="flex-1">
            <h2 className="font-medium">Add a provider</h2>
            <p className="mt-1 text-xs text-ink-2">Bring your own API key, or use a local server.</p>
          </div>
          <Button size="sm" variant="outline" onClick={() => setAdd(true)}>
            <Plus />
            Add
          </Button>
        </div>
        <div className="flex items-center gap-4 p-5">
          <Step done={!!settings.agent.modelId} number={2} />
          <div className="min-w-0 flex-1">
            <h2 className="font-medium">Choose a model</h2>
            <p className="mt-1 text-xs text-ink-2">The model powers all of your chats.</p>
          </div>
          <ModelPicker
            provider={settings.agent.provider}
            modelId={settings.agent.modelId}
            onSelect={(provider, modelId) => model.mutate({ body: { ...settings.agent, provider, modelId } })}
            disabled={!settings.providers.length || model.isPending}
          />
        </div>
        <div className="flex items-center gap-4 p-5">
          <Step done={settings.telegram.enabled} number={3} />
          <div className="flex-1">
            <h2 className="font-medium">
              Connect Telegram <span className="font-normal text-ink-3">· optional</span>
            </h2>
            <p className="mt-1 text-xs text-ink-2">Take your agent with you.</p>
          </div>
          <Link
            to="/settings/$section"
            params={{ section: "telegram" }}
            className="rounded-md p-2 text-ink-2 hover:bg-hover"
            aria-label="Set up Telegram"
          >
            <ArrowUpRight className="size-4" />
          </Link>
        </div>
      </div>
      {!settings.providers.length ? (
        <p className="mt-3 text-xs text-ink-2">Add a provider before you choose a model.</p>
      ) : null}
      {model.isError ? (
        <div className="mt-5">
          <Failure
            title="Could not choose model"
            error={model.error}
            retry={() => {
              if (model.variables) model.mutate(model.variables);
            }}
          />
        </div>
      ) : null}
      {add ? <ProviderDialog settings={settings} onClose={() => setAdd(false)} /> : null}
    </div>
  );
}

function Step({ done, number }: { done: boolean; number: number }) {
  return (
    <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-inset text-xs text-ink-2">
      {done ? <Check className="size-3.5 text-green" /> : number}
    </span>
  );
}

export function ChatPage({ chatId }: { chatId?: number }) {
  const settings = useQuery(getSettingsOptions());

  return settings.isPending ? (
    <div className="mx-auto w-full max-w-[720px] p-8">
      <Loading />
    </div>
  ) : settings.isError ? (
    <div className="m-8">
      <Failure error={settings.error} retry={() => void settings.refetch()} />
    </div>
  ) : settings.data ? (
    !chatId && !settings.data.agent.modelId ? (
      <Setup settings={settings.data} />
    ) : (
      <Chat key={chatId ?? "new"} chatId={chatId} settings={settings.data} />
    )
  ) : null;
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
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const snapshotSeen = useRef(false);
  const failedMessage = useRef<MessageInput | null>(null);
  const create = useMutation(createChatMutation());
  const send = useMutation(sendMessageMutation());

  const abort = useMutation({
    ...abortChatMutation(),
    onSuccess: async () => {
      setQueued([]);
      await refreshChats();
    },
  });

  const model = useMutation({ ...setAgentMutation(), onSuccess: refreshSettings });
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

  const reason =
    chat.data?.channel === "telegram"
      ? "Reply in Telegram"
      : !settings.agent.modelId
        ? "Choose a model to start"
        : chatId && connection !== "ready"
          ? "dex is reconnecting to the agent…"
          : undefined;

  const pending = create.isPending || send.isPending || abort.isPending;
  const error = create.error ?? send.error ?? abort.error ?? model.error;

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

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-y-auto px-5"
        onScroll={() => {
          const el = scroller.current;

          if (el) {
            pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
            setJump(!pinned.current);
          }
        }}
      >
        <div className="mx-auto max-w-[720px] pb-8 pt-6 md:pt-10">
          {chatId && chat.isPending ? (
            <Loading />
          ) : chatId && chat.isError ? (
            <Failure title="Could not load this chat" error={chat.error} retry={() => void chat.refetch()} />
          ) : chatId ? (
            <>
              <div className="mb-8 flex items-center gap-2 text-xs text-ink-2">
                {chat.data?.channel === "telegram" ? <Send className="size-3.5" /> : null}
                <h1 className="truncate">{chat.data?.title}</h1>
              </div>
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
                    live={state.running && index === state.items.length - 1}
                    retry={() => retryText(index)}
                    canRetry={
                      !reason && !pending && state.items.slice(0, index).some((i) => i.kind === "user")
                    }
                  />
                ))}
                {state.retry ? <RetryNotice retry={state.retry} /> : null}
                {!items.length && connection === "ready" ? (
                  <p className="py-12 text-center text-sm text-ink-2">No messages yet.</p>
                ) : null}
              </div>
            </>
          ) : (
            <div className="flex min-h-[45dvh] flex-col justify-center">
              <div className="mb-5 flex size-10 items-center justify-center rounded-xl bg-ink text-xl font-semibold text-page">
                d
              </div>
              <p className="mb-2 text-xs font-medium tracking-widest text-ink-2 uppercase">
                Your local agent
              </p>
              <h1 className="text-3xl font-semibold tracking-tight md:text-4xl">What can we work on?</h1>
              <p className="mt-4 text-ink-2">A thought, a task, a question. Start here.</p>
              <div className="mt-8 grid gap-3 sm:grid-cols-2">
                {[
                  "Explore this project and explain how it works",
                  "Help me plan my next week",
                  "Find ways to simplify my workflow",
                  "Review a document and suggest changes",
                ].map((text) => (
                  <button
                    key={text}
                    className="surface flex items-start gap-3 p-4 text-left text-sm leading-relaxed text-ink-2 transition-shadow hover:shadow-raised"
                    onClick={() => setDraft(text)}
                  >
                    <Sparkles className="mt-0.5 size-4 shrink-0 text-ink-3" />
                    {text}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
      <div className="relative mx-auto w-full max-w-[760px] px-5 pb-5 pt-3">
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
                else if (model.isError && model.variables) model.mutate(model.variables);
              }}
            />
          </div>
        ) : null}
        <Composer
          agent={settings.agent}
          running={state.running}
          pending={pending}
          disabledReason={reason}
          onSend={sendText}
          onStop={() => {
            if (chatId) abort.mutate({ path: { chatId } });
          }}
          onModel={(provider, modelId) => model.mutate({ body: { ...settings.agent, provider, modelId } })}
          text={draft}
          onTextChange={setDraft}
        />
      </div>
    </div>
  );
}
