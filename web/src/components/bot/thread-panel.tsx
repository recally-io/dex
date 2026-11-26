import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, X } from "lucide-react";
import type { Session, HistoryPage, Item, ChildEvent } from "../../api/generated/types.gen";
import { listBotsOptions } from "../../api/generated/@tanstack/react-query.gen";
import { BotContext, useBotId } from "../../lib/bot-scope";
import { mergeHistory } from "../../lib/session-state";
import { useSessionStream } from "../../lib/session-stream";
import { threadState, type ThreadRef } from "../../lib/threads";
import { Failure, Loading } from "../feedback";
import { IconButton } from "../icon-button";
import { LoadEarlierButton } from "../timeline-actions";
import { StateBadge } from "./thread";
import { Transcript } from "./transcript";

/**
 * The transcript of a child session beside its parent, read-only and live while the child runs.
 * It covers the parent on a narrow screen.
 */
export function ThreadPanel({ thread, onClose }: { thread: ThreadRef; onClose: () => void }) {
  const current = useBotId();
  const botId = thread.botId ?? current;

  // A child can belong to another bot, so the stream and the transcript read in that bot.
  return (
    <BotContext.Provider value={botId}>
      <PanelBody
        key={`${botId}/${thread.sessionId}`}
        sessionId={thread.sessionId}
        botId={botId}
        onClose={onClose}
      />
    </BotContext.Provider>
  );
}

function PanelBody({ sessionId, botId, onClose }: { sessionId: number; botId: string; onClose: () => void }) {
  const { session, state, setState, connection, reconnect } = useSessionStream(sessionId);

  return (
    <ThreadView
      botId={botId}
      sessionId={sessionId}
      session={session}
      items={state.items}
      running={state.running}
      status={state.children?.[sessionId]}
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
  botId,
  sessionId,
  session,
  items,
  running,
  status,
  historyBefore,
  connection,
  reconnect,
  onPage,
  onClose,
}: {
  botId: string;
  sessionId: number;
  session: { data?: Session; error?: unknown; isError: boolean; isPending: boolean; refetch: () => void };
  items: Item[];
  running: boolean;
  status?: ChildEvent["state"];
  historyBefore?: number;
  connection: "connecting" | "ready" | "error";
  reconnect: () => void;
  onPage: (page: HistoryPage) => void;
  onClose: () => void;
}) {
  const bots = useQuery(listBotsOptions());
  const bot = bots.data?.bots.find((item) => item.id === botId)?.name ?? botId;
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

  const statusState = threadState(status, connection === "ready");

  return (
    <aside
      aria-label="Child session"
      onKeyDown={(event) => {
        if (event.key === "Escape") onClose();
      }}
      className="absolute inset-0 z-20 flex min-w-0 flex-col bg-background lg:static lg:z-auto lg:w-[420px] lg:shrink-0 lg:border-l lg:border-border xl:w-[480px]"
    >
      <header className="flex min-h-12 shrink-0 items-center gap-2 border-b border-border py-2 pr-2 pl-4">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[14px] font-semibold">{session.data?.title || "Session"}</h2>
          <p className="truncate text-[12px] text-muted-foreground">{bot}</p>
        </div>
        <StateBadge state={statusState} />
        <Link
          to="/b/$botId/s/$sessionId"
          params={{ botId, sessionId: String(sessionId) }}
          aria-label="Open as a session page"
          title="Open as a session page"
          className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ExternalLink aria-hidden className="size-4" />
        </Link>
        <IconButton label="Close child session" onClick={onClose}>
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
          {session.isError ? (
            <Failure
              title="Could not load this child session"
              error={session.error}
              retry={() => void session.refetch()}
            />
          ) : session.isPending ? (
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
              <LoadEarlierButton sessionId={sessionId} before={historyBefore} onPage={onPage} />
              <Transcript
                sessionId={sessionId}
                items={items}
                running={running}
                retry={() => {}}
                canRetry={false}
              />
            </>
          )}
        </div>
      </div>
    </aside>
  );
}
