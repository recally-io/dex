import { createContext, useContext, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CornerDownRight, MessagesSquare } from "lucide-react";
import type { SessionExecution, TaskItem, ToolItem } from "../../api/generated/types.gen";
import { getTurnChatOptions } from "../../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../../lib/agent-scope";
import { sessionAgentId } from "../../lib/sessions";
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
  sessions?: SessionExecution[];
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
  const agentId = useAgentId();

  if (!thread) return <span className={className}>{children}</span>;

  if (context?.open)
    return (
      <button type="button" onClick={() => context.open?.(thread)} className={className}>
        {children}
      </button>
    );

  return (
    <Link
      to="/a/$agentId/c/$chatId"
      params={{ agentId: thread.agentId ?? agentId, chatId: String(thread.chatId) }}
      className={className}
    >
      {children}
    </Link>
  );
}

/** Cards consume only the parent's content-free execution stream. */
export function useThreadData(call: ThreadCall) {
  const current = useAgentId();
  const context = useContext(ThreadContext);

  const found = useQuery({
    ...getTurnChatOptions({ path: { agentId: current, turnId: call.turn ?? "" } }),
    enabled: !!call.turn,
  });

  const thread =
    call.thread ??
    (found.data ? { chatId: found.data.id, agentId: sessionAgentId(found.data, current) } : undefined);

  const session = context?.sessions?.find(
    (item) => item.chatId === thread?.chatId && item.agentId === (thread?.agentId ?? current),
  );

  const request = call.requestId ?? call.turn;

  return {
    thread,
    session,
    state: request
      ? threadState(session, request, context?.connection === "ready")
      : ("unavailable" as const),
  };
}

const stateStyle: Record<ThreadState, "secondary" | "outline" | "destructive"> = {
  running: "secondary",
  waiting: "outline",
  queued: "outline",
  unavailable: "outline",
  model: "secondary",
  tool: "secondary",
  retry: "secondary",
  deferred: "secondary",
  compaction: "secondary",
  finishing: "secondary",
  idle: "outline",
  done: "outline",
  failed: "destructive",
  interrupted: "destructive",
  stopped: "outline",
};

const stateLabels: Record<ThreadState, string> = {
  idle: "idle",
  waiting: "Waiting to run",
  queued: "Queued",
  unavailable: "Status unavailable",
  running: "Running",
  model: "Waiting for model",
  tool: "Running tool",
  retry: "Retrying",
  deferred: "Provider pending",
  compaction: "Compacting",
  finishing: "Finishing",
  done: "done",
  failed: "failed",
  interrupted: "interrupted",
  stopped: "stopped",
};

export function StateBadge({ state }: { state: ThreadState }) {
  return (
    <Badge variant={stateStyle[state]} data-state={state}>
      {["running", "model", "tool", "retry", "deferred", "compaction", "finishing"].includes(state) ? (
        <Spinner />
      ) : null}
      {stateLabels[state]}
    </Badge>
  );
}

export function ExecutionNotice({ state, queued }: { state: ThreadState; queued: number }) {
  return (
    <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
      {stateLabels[state]}
      {queued > 0 ? <span> · {queued} queued</span> : null}
    </p>
  );
}

/** One child session that a call created, at the place of the call. */
function ThreadCard({ call }: { call: ThreadCall }) {
  const agentId = useAgentId();
  const data = useThreadData(call);
  const other = call.bot && call.bot.id !== agentId ? call.bot.name : undefined;
  const title = call.title;

  return (
    <section aria-label={`Session thread: ${title}`} className="my-2 min-w-0 rounded-md border text-sm">
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
        {data.session && data.session.queued > 0 ? (
          <span className="mt-1 block text-xs text-muted-foreground">{data.session.queued} queued</span>
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
 * says why on the line. A click finds the session of the turn.
 */
export function ResultLine({ item }: { item: TaskItem }) {
  const agentId = useAgentId();
  const context = useContext(ThreadContext);
  const client = useQueryClient();
  const [failed, setFailed] = useState(false);
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
        {label} ·{" "}
        {context?.open ? (
          <button
            type="button"
            className={link}
            onClick={() => {
              setFailed(false);
              client
                .fetchQuery(getTurnChatOptions({ path: { agentId, turnId: item.taskId } }))
                .then((chat) => context.open?.({ chatId: chat.id, agentId: sessionAgentId(chat, agentId) }))
                .catch(() => setFailed(true));
            }}
          >
            View thread
          </button>
        ) : (
          <Link to="/a/$agentId/tasks/$turnId" params={{ agentId, turnId: item.taskId }} className={link}>
            View thread
          </Link>
        )}
        {failed ? <span role="alert"> · Could not open the thread.</span> : null}
      </h3>
    </header>
  );
}
