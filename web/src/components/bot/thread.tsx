import { createContext, useContext, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CornerDownRight, MessagesSquare } from "lucide-react";
import type { SessionResultItem, ToolItem } from "../../api/generated/types.gen";
import { listSessionsOptions } from "../../api/generated/@tanstack/react-query.gen";
import { useBotId } from "../../lib/bot-scope";
import { sessionBotId } from "../../lib/sessions";
import {
  threadCall,
  threadState,
  type ThreadCall,
  type ThreadRef,
  type ThreadState,
} from "../../lib/threads";
import { cn } from "../../lib/utils";
import { Spinner } from "../feedback";
import { Badge } from "../ui/badge";

/** The page that shows the thread panel provides this. Without it, a thread opens as a session page. */
export const ThreadContext = createContext<{
  open?: (thread: ThreadRef) => void;
  children?: Record<number, ThreadState>;
  connection?: "connecting" | "ready" | "error";
} | null>(null);

/** A card, a line or a button that opens one child session. */
function ThreadOpen({
  thread,
  className,
  children,
}: {
  thread?: ThreadRef;
  className?: string;
  children: ReactNode;
}) {
  const context = useContext(ThreadContext);
  const botId = useBotId();

  if (!thread) return <span className={className}>{children}</span>;

  if (context?.open)
    return (
      <button type="button" onClick={() => context.open?.(thread)} className={className}>
        {children}
      </button>
    );

  return (
    <Link
      to="/b/$botId/s/$sessionId"
      params={{ botId: thread.botId ?? botId, sessionId: String(thread.sessionId) }}
      className={className}
    >
      {children}
    </Link>
  );
}

/** Cards consume only the parent's content-free execution stream. */
function useThreadData(call: ThreadCall) {
  const context = useContext(ThreadContext);
  const thread = call.thread;

  return {
    thread,
    state: threadState(
      thread ? context?.children?.[thread.sessionId] : undefined,
      context?.connection === "ready",
    ),
  };
}

const stateStyle: Record<ThreadState, "secondary" | "outline" | "destructive"> = {
  running: "secondary",
  waiting: "outline",
  unavailable: "outline",
  idle: "outline",
};

const stateLabels: Record<ThreadState, string> = {
  idle: "Idle",
  waiting: "Waiting to run",
  unavailable: "Status unavailable",
  running: "Running",
};

export function StateBadge({ state }: { state: ThreadState }) {
  return (
    <Badge variant={stateStyle[state]} data-state={state}>
      {state === "running" ? <Spinner /> : null}
      {stateLabels[state]}
    </Badge>
  );
}

/** One child session that a call created, at the place of the call. */
function ThreadCard({ call }: { call: ThreadCall }) {
  const botId = useBotId();
  const data = useThreadData(call);
  const other = call.bot && call.bot.id !== botId ? call.bot.name : undefined;
  const title = call.title;

  return (
    <section aria-label={`Child session: ${title}`} className="my-2 min-w-0 rounded-md border text-sm">
      <ThreadOpen
        thread={data.thread}
        className={cn("block w-full rounded-md p-3 text-left", data.thread && "hover:bg-accent")}
      >
        <span className="flex min-w-0 items-center gap-2">
          <MessagesSquare aria-hidden className="size-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0 flex-1 truncate font-semibold">{title}</span>
          {other ? <span className="shrink-0 text-xs text-muted-foreground">{other}</span> : null}
          {call.status === "running" ? (
            <Badge variant="secondary">
              <Spinner />
              Preparing delegation
            </Badge>
          ) : call.status === "error" ? (
            <Badge variant="destructive">not started</Badge>
          ) : data.state ? (
            <StateBadge state={data.state} />
          ) : null}
        </span>
        {call.error ? (
          <span className="mt-1 block text-red [overflow-wrap:anywhere] whitespace-pre-wrap">
            {call.error}
          </span>
        ) : null}
      </ThreadOpen>
    </section>
  );
}

/** A message to a child session that exists. It opens the same thread as the card. */
function ThreadLine({ call }: { call: ThreadCall }) {
  const data = useThreadData(call);
  const failed = call.status === "error";

  return (
    <p className="my-1 flex min-w-0 items-center gap-1.5 text-[13px] text-muted-foreground">
      <CornerDownRight aria-hidden className="size-3.5 shrink-0" />
      <ThreadOpen
        thread={call.thread}
        className={cn("min-w-0 truncate", call.thread && "hover:text-foreground hover:underline")}
      >
        {failed
          ? `Could not send to ${call.title}`
          : call.status === "running"
            ? "Sending a message"
            : `Sent to ${call.title}`}
      </ThreadOpen>
      {call.status === "done" ? <StateBadge state={data.state} /> : null}
      {failed && call.error ? <span className="min-w-0 truncate text-red">{call.error}</span> : null}
    </p>
  );
}

/** The thread entry of a session tool call, or null for any other step. */
export function ThreadEntry({ tool }: { tool: ToolItem }) {
  const call = threadCall(tool);

  if (!call) return null;

  return call.kind === "send" ? <ThreadLine call={call} /> : <ThreadCard call={call} />;
}

/**
 * The result of a child turn, as one line. The answer is in the thread. A turn that did not finish
 * says why on the line. The session list gives the child its owning Bot.
 */
export function ResultLine({ item }: { item: SessionResultItem }) {
  const botId = useBotId();

  const { data } = useQuery({
    ...listSessionsOptions({ path: { botId } }),
    enabled: !!item.childSessionId,
  });

  const child = data?.sessions.find((session) => session.id === item.childSessionId);
  const done = !item.status || item.status === "done";

  const label = (
    <>
      <span className="font-semibold text-foreground">{item.title}</span>{" "}
      {done ? "replied" : `${item.status}${item.reason ? `: ${item.reason}` : ""}`}
    </>
  );

  const link = "font-medium text-foreground decoration-ring underline-offset-3 hover:underline";

  return (
    <header className="flex min-w-0 items-start gap-2 text-[13px] leading-5 text-muted-foreground">
      <CornerDownRight size={16} aria-hidden className="mt-0.5 shrink-0" />
      <h3 className="min-w-0 font-normal [overflow-wrap:anywhere]">
        {label}
        {child ? (
          <>
            {" · "}
            <ThreadOpen thread={{ sessionId: child.id, botId: sessionBotId(child, botId) }} className={link}>
              View session
            </ThreadOpen>
          </>
        ) : null}
      </h3>
    </header>
  );
}
