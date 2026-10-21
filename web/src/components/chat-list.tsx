import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  createChatMutation,
  getMainChatOptions,
  listChatsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { errorText, refreshChats } from "../lib/queries";
import { sessionKind, taskTime } from "../lib/tasks";

/** Opens a Chat with its own context. The server reuses the newest empty Chat. */
export function useNewChat() {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();

  return useMutation({
    ...createChatMutation(),
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
    },
    onError: (cause) => notify(`Could not open a new chat. ${errorText(cause)}`),
  });
}

const row =
  "flex h-9 min-w-0 items-center gap-2 rounded-control px-2 text-[13px] text-ink-2 hover:bg-hover-2 hover:text-ink aria-[current=page]:bg-hover-2 aria-[current=page]:text-ink";

/**
 * The right sidebar of the Chat tab: Main, the bot's one timeline, then its Chat sessions,
 * newest first. Each Chat has its own context.
 */
export function ChatList({ chatId }: { chatId: number }) {
  const agentId = useAgentId();
  const main = useQuery(getMainChatOptions({ path: { agentId } }));
  const sessions = useQuery({ ...listChatsOptions({ path: { agentId } }), refetchInterval: 3000 });
  const onMain = !chatId || chatId === main.data?.id;

  const chats = (sessions.data?.chats ?? [])
    .filter((chat) => sessionKind(chat) === "Chat")
    .toSorted((a, b) => b.updatedAt - a.updatedAt);

  return (
    <aside aria-label="Chats" className="hidden w-[248px] shrink-0 flex-col border-l border-line md:flex">
      <h2 className="px-4 pt-3 pb-1.5 text-[12px] font-medium text-ink-3">Chats</h2>
      <nav aria-label="Chats" className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 pb-3">
        <Link
          to="/a/$agentId"
          params={{ agentId }}
          activeOptions={{ exact: true }}
          aria-current={onMain ? "page" : undefined}
          className={row}
        >
          <span className="min-w-0 flex-1 truncate font-medium">Main</span>
          <span className="shrink-0 text-[11.5px] text-ink-3">Every channel</span>
        </Link>
        {/* The list skips empty chats, so a Chat opened just now shows here until its first message. */}
        {!onMain && !chats.some((chat) => chat.id === chatId) ? (
          <span aria-current="page" className={row}>
            <span className="min-w-0 flex-1 truncate">New chat</span>
          </span>
        ) : null}
        {chats.map((chat) => (
          <Link
            key={chat.id}
            to="/a/$agentId/c/$chatId"
            params={{ agentId, chatId: String(chat.id) }}
            aria-current={chat.id === chatId ? "page" : undefined}
            className={row}
          >
            <span className="min-w-0 flex-1 truncate">{chat.title || "New chat"}</span>
            <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{taskTime(chat.updatedAt)}</span>
          </Link>
        ))}
        {sessions.isSuccess && onMain && !chats.length ? (
          <p className="px-2 py-2 text-[12px] leading-relaxed text-ink-3">
            New chat starts a conversation with its own context.
          </p>
        ) : null}
      </nav>
    </aside>
  );
}
