import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link, Navigate, useNavigate } from "@tanstack/react-router";
import { useQuery, useMutation } from "@tanstack/react-query";
import { ChevronRight, MessageCircle, Clock3, Plus } from "lucide-react";
import type { Chat } from "../api/generated/types.gen";
import {
  getTurnChatOptions,
  listChatsOptions,
  createChatMutation,
} from "../api/generated/@tanstack/react-query.gen";
import {
  activeSessionCount,
  isStalled,
  sessionAgentId,
  sessionKind,
  sessionTime,
  startedBy,
} from "../lib/sessions";
import { refreshChats } from "../lib/queries";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { Select } from "../components/form";

export function SessionsPage() {
  const agentId = useAgentId();
  const settings = useAgentSettings(agentId);

  const sessions = useQuery({
    ...listChatsOptions({ path: { agentId } }),
    placeholderData: (previous) => previous,
    refetchInterval: 3000,
  });

  const navigate = useNavigate();

  const create = useMutation({
    ...createChatMutation(),
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
    },
  });

  const [kind, setKind] = useState("All kinds");
  const name = settings.data?.agent.name ?? "dex";
  // Archived Chats live in the Chats sidebar of the Chat tab, not here.
  const all = (sessions.data?.chats ?? []).filter((chat) => !chat.archivedAt);
  const list = all.filter((chat) => kind === "All kinds" || sessionKind(chat) === kind);
  const titles = new Map(all.map((chat) => [chat.id, chat.title || "New session"]));

  return (
    <FramedPage
      title="Sessions"
      description={`Conversations of ${name} apart from the main timeline. Each session has its own context. Chat sessions and the sessions that ${name} started take messages. Automation runs are read-only.`}
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={create.isPending}
          onClick={() => create.mutate({ path: { agentId } })}
        >
          <Plus size={16} /> {create.isPending ? "Opening…" : "New session"}
        </Button>
      }
    >
      <Section
        n={1}
        title="All sessions"
        caption={all.length ? `${activeSessionCount(list)} active · newest first` : undefined}
        action={
          all.length ? (
            <Select
              aria-label="Kind"
              className="h-8 w-auto text-[12.5px]"
              value={kind}
              onChange={(event) => setKind(event.target.value)}
            >
              {["All kinds", "Chat", "Automation"].map((value) => (
                <option key={value}>{value}</option>
              ))}
            </Select>
          ) : null
        }
      >
        {create.isError ? <Failure title="Could not open a new session" error={create.error} /> : null}
        {sessions.isPending ? (
          <Loading />
        ) : sessions.isError ? (
          <Failure
            title="Could not load sessions"
            error={sessions.error}
            retry={() => void sessions.refetch()}
          />
        ) : list.length ? (
          <Panel>
            <ul>
              {list.map((chat) => (
                <SessionRow
                  key={chat.id}
                  chat={chat}
                  parent={startedBy(
                    chat,
                    chat.parentId === undefined ? undefined : (titles.get(chat.parentId) ?? "Main"),
                  )}
                />
              ))}
            </ul>
          </Panel>
        ) : all.length ? (
          <Empty title="No sessions of this kind">Choose All kinds to see every session.</Empty>
        ) : (
          <Empty title="No sessions yet">
            Automation runs, sessions that {name} started and your Chat sessions show here. Use New session
            for work that needs a clean context.
          </Empty>
        )}
      </Section>
    </FramedPage>
  );
}

/** A busy row says Running, or Stalled when the session shows no sign of work. */
function SessionRow({ chat, parent }: { chat: Chat; parent?: string }) {
  const agentId = useAgentId();
  const kind = sessionKind(chat);
  const Icon = kind === "Chat" ? MessageCircle : Clock3;
  const stalled = isStalled(chat);

  return (
    <li className="flex items-center gap-3 border-t border-border px-4 py-3.5 first:border-t-0">
      <Icon size={16} className="mt-0.5 shrink-0 self-start text-muted-foreground" />
      <Link
        to="/a/$agentId/c/$chatId"
        params={{ agentId: sessionAgentId(chat, agentId), chatId: String(chat.id) }}
        className="min-w-0 flex-1 hover:underline"
      >
        <span className="block text-[13.5px] font-medium text-foreground [overflow-wrap:anywhere]">
          {chat.title || "New session"}
        </span>
        <span className="mt-0.5 block text-[12px] text-muted-foreground">
          {parent === undefined ? kind : `Started by ${parent}`} · {sessionTime(chat.updatedAt)}
        </span>
      </Link>
      {chat.busy ? (
        <Badge
          variant="secondary"
          className={`h-5 shrink-0 px-2 text-[11.5px] ${stalled ? "bg-orange-tint text-orange" : "bg-primary/10 text-primary"}`}
        >
          {stalled ? "Stalled" : "Running"}
        </Badge>
      ) : null}
      <ChevronRight size={16} className="shrink-0 text-muted-foreground" />
    </li>
  );
}

/** An old task link and a result link name a turn. They open the session that holds it. */
export function TurnPage({ turnId }: { turnId: string }) {
  const agentId = useAgentId();
  const turn = useQuery(getTurnChatOptions({ path: { agentId, turnId } }));

  if (turn.data)
    // A result of a child in another bot opens the session in that bot.
    return (
      <Navigate
        to="/a/$agentId/c/$chatId"
        params={{ agentId: sessionAgentId(turn.data, agentId), chatId: String(turn.data.id) }}
        replace
      />
    );

  return (
    <FramedPage title="Session">
      {turn.isError ? (
        <Failure title="Could not find the session" error={turn.error} retry={() => void turn.refetch()} />
      ) : (
        <Loading />
      )}
    </FramedPage>
  );
}
