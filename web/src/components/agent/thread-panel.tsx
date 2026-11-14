import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, X } from "lucide-react";
import type { Chat, HistoryPage, Item, SessionExecution } from "../../api/generated/types.gen";
import { listAgentsOptions } from "../../api/generated/@tanstack/react-query.gen";
import { AgentContext, useAgentId } from "../../lib/agent-scope";
import { mergeHistory } from "../../lib/chat-state";
import { useChatStream } from "../../lib/chat-stream";
import { threadState, type ThreadRef } from "../../lib/threads";
import { Failure, Loading } from "../feedback";
import { IconButton } from "../icon-button";
import { LoadEarlierButton } from "../timeline-actions";
import { ExecutionNotice, StateBadge } from "./thread";
import { Transcript } from "./transcript";

/**
 * The transcript of a child session beside its parent, read-only and live while the child runs.
 * It covers the parent on a narrow screen.
 */
export function ThreadPanel({ thread, onClose }: { thread: ThreadRef; onClose: () => void }) {
  const current = useAgentId();
  const agentId = thread.agentId ?? current;

  // A child can belong to another bot, so the stream and the transcript read in that bot.
  return (
    <AgentContext.Provider value={agentId}>
      <PanelBody
        key={`${agentId}/${thread.chatId}`}
        chatId={thread.chatId}
        agentId={agentId}
        onClose={onClose}
      />
    </AgentContext.Provider>
  );
}

function PanelBody({ chatId, agentId, onClose }: { chatId: number; agentId: string; onClose: () => void }) {
  const { chat, state, setState, connection, reconnect } = useChatStream(chatId);

  return (
    <ThreadView
      agentId={agentId}
      chatId={chatId}
      chat={chat}
      items={state.items}
      running={state.running}
      execution={state.sessions?.find((item) => item.chatId === chatId && item.agentId === agentId)}
      historyBefore={state.historyBefore}
      connection={connection}
      reconnect={() => reconnect.current?.()}
      onPage={(page) => setState((previous) => mergeHistory(previous, page))}
      onClose={onClose}
    />
  );
}

/** The panel without its stream. The page passes the items that the stream holds. */
export function ThreadView({
  agentId,
  chatId,
  chat,
  items,
  running,
  execution,
  historyBefore,
  connection,
  reconnect,
  onPage,
  onClose,
}: {
  agentId: string;
  chatId: number;
  chat: { data?: Chat; error?: unknown; isError: boolean; isPending: boolean; refetch: () => void };
  items: Item[];
  running: boolean;
  execution?: SessionExecution;
  historyBefore?: number;
  connection: "connecting" | "ready" | "error";
  reconnect: () => void;
  onPage: (page: HistoryPage) => void;
  onClose: () => void;
}) {
  const agents = useQuery(listAgentsOptions());
  const bot = agents.data?.agents.find((agent) => agent.id === agentId)?.name ?? agentId;
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);

  useEffect(() => {
    const container = scroller.current;
    const content = container?.firstElementChild;

    if (!container || !content) return;

    const observer = new ResizeObserver(() => {
      if (pinned.current) container.scrollTop = container.scrollHeight;
    });

    observer.observe(content);

    return () => observer.disconnect();
  }, []);

  const phase = threadState(execution, undefined, connection === "ready");

  return (
    <aside
      aria-label="Session thread"
      onKeyDown={(event) => {
        if (event.key === "Escape") onClose();
      }}
      className="absolute inset-0 z-20 flex min-w-0 flex-col bg-background lg:static lg:z-auto lg:w-[420px] lg:shrink-0 lg:border-l lg:border-border xl:w-[480px]"
    >
      <header className="flex min-h-12 shrink-0 items-center gap-2 border-b border-border py-2 pr-2 pl-4">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[14px] font-semibold">{chat.data?.title || "Session"}</h2>
          <p className="truncate text-[12px] text-muted-foreground">{bot}</p>
        </div>
        {phase ? <StateBadge state={phase} /> : null}
        <Link
          to="/a/$agentId/c/$chatId"
          params={{ agentId, chatId: String(chatId) }}
          aria-label="Open as a session page"
          title="Open as a session page"
          className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ExternalLink aria-hidden className="size-4" />
        </Link>
        <IconButton label="Close thread" onClick={onClose}>
          <X aria-hidden className="size-4" />
        </IconButton>
      </header>
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
        onScroll={() => {
          const el = scroller.current;

          if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
        }}
      >
        <div className="flex flex-col gap-3 px-4 py-5">
          {chat.isError ? (
            <Failure
              title="Could not load this thread"
              error={chat.error}
              retry={() => void chat.refetch()}
            />
          ) : chat.isPending ? (
            <Loading />
          ) : (
            <>
              {connection === "error" ? (
                <Failure title="Connection lost">
                  <button type="button" className="underline" onClick={reconnect}>
                    Reconnect
                  </button>
                </Failure>
              ) : null}
              <ExecutionNotice state={phase} queued={execution?.queued ?? 0} />
              <LoadEarlierButton chatId={chatId} before={historyBefore} onPage={onPage} />
              <Transcript chatId={chatId} items={items} running={running} retry={() => {}} canRetry={false} />
            </>
          )}
        </div>
      </div>
    </aside>
  );
}
