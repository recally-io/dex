import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { format, isToday, startOfDay, subDays } from "date-fns";
import { Archive, ChevronRight, MoreHorizontal } from "lucide-react";
import type { Chat } from "../api/generated/types.gen";
import {
  archiveChatMutation,
  createChatMutation,
  getMainChatOptions,
  listChatsOptions,
  unarchiveChatMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { errorText, refreshChats } from "../lib/queries";
import { sessionKind } from "../lib/tasks";
import { cn } from "../lib/utils";
import { Button, buttonVariants } from "./ui/button";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuTrigger } from "./ui/context-menu";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";

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

/** Archives or restores a Chat. Archiving the open Chat returns to Main. */
function useArchive(openChat: number) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();

  const done = async (chat: Chat) => {
    await refreshChats(agentId);

    if (chat.archivedAt && chat.id === openChat) await navigate({ to: "/a/$agentId", params: { agentId } });
  };

  const fail = (cause: unknown) => notify(`Could not change the chat. ${errorText(cause)}`);
  const archive = useMutation({ ...archiveChatMutation(), onSuccess: done, onError: fail });
  const restore = useMutation({ ...unarchiveChatMutation(), onSuccess: done, onError: fail });

  return (chat: Chat) => (chat.archivedAt ? restore : archive).mutate({ path: { agentId, chatId: chat.id } });
}

/** ChatGPT-style buckets by latest activity: recent days first, then one per month. */
export function dateGroup(at: number, now: number) {
  const today = startOfDay(now).getTime();

  if (at >= today) return "Today";

  if (at >= subDays(today, 1).getTime()) return "Yesterday";

  if (at >= subDays(today, 7).getTime()) return "Previous 7 days";

  if (at >= subDays(today, 30).getTime()) return "Previous 30 days";

  return format(at, "MMMM yyyy");
}

const pageSize = 5;

const row =
  "h-9 min-w-0 justify-start gap-2 rounded-md px-2 text-[13px] text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground";

const menuItem =
  "h-8 w-full justify-start gap-2 rounded-[7px] px-2 text-[12.5px] text-foreground data-highlighted:bg-accent";

const menuPopup =
  "w-[180px] origin-(--transform-origin) animate-[pop-in_160ms_var(--ease-out-strong)_both] rounded-[10px] bg-card p-1 shadow-raised outline-none";

/**
 * The right sidebar of the Chat tab. Main is the bot's one timeline: it comes first and has no
 * menu. Chat sessions follow in date groups, newest first, and archived ones go last. Today
 * starts open; each group shows five chats until the user asks for more.
 */
export function ChatList({ chatId }: { chatId: number }) {
  const agentId = useAgentId();
  const main = useQuery(getMainChatOptions({ path: { agentId } }));
  const sessions = useQuery({ ...listChatsOptions({ path: { agentId } }), refetchInterval: 3000 });
  const toggleArchive = useArchive(chatId);
  const onMain = !chatId || chatId === main.data?.id;
  const [now] = useState(() => Date.now());

  const chats = (sessions.data?.chats ?? [])
    .filter((chat) => sessionKind(chat) === "Chat")
    .toSorted((a, b) => b.updatedAt - a.updatedAt);

  const active = chats.filter((chat) => !chat.archivedAt);
  const archived = chats.filter((chat) => chat.archivedAt);
  const groups = Map.groupBy(active, (chat) => dateGroup(chat.updatedAt, now));

  // The list skips empty chats, so a Chat opened just now shows on its own until its first message.
  const pending = !onMain && sessions.isSuccess && !chats.some((chat) => chat.id === chatId);

  const item = (chat: Chat) => (
    <ChatRow key={chat.id} chat={chat} current={chat.id === chatId} onToggleArchive={toggleArchive} />
  );

  return (
    <aside aria-label="Chats" className="hidden w-[256px] shrink-0 flex-col border-l border-border md:flex">
      <nav aria-label="Chats" className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2 py-2">
        <Link
          to="/a/$agentId"
          params={{ agentId }}
          activeOptions={{ exact: true }}
          aria-current={onMain ? "page" : undefined}
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row)}
        >
          <span className="min-w-0 flex-1 truncate font-medium">Main</span>
          <span className="shrink-0 text-[11.5px] text-muted-foreground">Every channel</span>
        </Link>
        {pending ? (
          <span aria-current="page" className={cn(row, "mt-0.5")}>
            <span className="min-w-0 flex-1 truncate">New chat</span>
          </span>
        ) : null}
        {[...groups].map(([label, list]) => (
          <Group key={label} label={label} count={list.length} defaultOpen={label === "Today"}>
            {(limit) => list.slice(0, limit).map(item)}
          </Group>
        ))}
        {archived.length ? (
          <Group label="Archived" count={archived.length} defaultOpen={false}>
            {(limit) => archived.slice(0, limit).map(item)}
          </Group>
        ) : null}
        {sessions.isSuccess && onMain && !chats.length ? (
          <p className="px-2 py-2 text-[12px] leading-relaxed text-muted-foreground">
            New chat starts a conversation with its own context.
          </p>
        ) : null}
      </nav>
    </aside>
  );
}

function Group({
  label,
  count,
  defaultOpen,
  children,
}: {
  label: string;
  count: number;
  defaultOpen: boolean;
  children: (limit: number) => ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [limit, setLimit] = useState(pageSize);

  return (
    <section className="mt-3">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        // The stock ghost button fills aria-expanded; a group header only rotates its chevron.
        className="flex h-7 w-full items-center justify-start gap-1 rounded-md px-2 text-[12px] font-medium text-muted-foreground hover:text-foreground aria-expanded:bg-transparent aria-expanded:text-muted-foreground aria-expanded:hover:text-foreground"
      >
        <ChevronRight
          className={cn("size-3.5 shrink-0 transition-transform duration-150", open && "rotate-90")}
        />
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
        <span className="shrink-0 tabular-nums">{count}</span>
      </Button>
      {open ? (
        <div className="flex flex-col gap-0.5">
          {children(limit)}
          {count > limit ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setLimit(count)}
              className="h-8 justify-start rounded-md px-2 text-left text-[12.5px] text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              Show {count - limit} more
            </Button>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

/** One Chat. The ⋯ button and a right-click open the same menu. */
function ChatRow({
  chat,
  current,
  onToggleArchive,
}: {
  chat: Chat;
  current: boolean;
  onToggleArchive: (chat: Chat) => void;
}) {
  const agentId = useAgentId();
  const action = chat.archivedAt ? "Restore" : "Archive";
  const title = chat.title || "New chat";

  const itemContent = (
    <>
      <Archive className="size-[15px] shrink-0 text-muted-foreground" />
      {action}
    </>
  );

  return (
    <ContextMenu>
      <ContextMenuTrigger className="group relative flex min-w-0 items-center">
        <Link
          to="/a/$agentId/c/$chatId"
          params={{ agentId, chatId: String(chat.id) }}
          aria-current={current ? "page" : undefined}
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row, "flex-1 pr-9")}
        >
          <span className="min-w-0 flex-1 truncate">{title}</span>
          <span className="shrink-0 text-[11.5px] text-muted-foreground tabular-nums group-hover:invisible group-focus-within:invisible">
            {format(chat.updatedAt, isToday(chat.updatedAt) ? "HH:mm" : "d MMM")}
          </span>
        </Link>
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={`${title}: more actions`}
            className="absolute right-1 flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100 aria-expanded:opacity-100"
          >
            <MoreHorizontal className="size-[15px]" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" sideOffset={4} className={menuPopup}>
            <DropdownMenuItem onClick={() => onToggleArchive(chat)} className={menuItem}>
              {itemContent}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </ContextMenuTrigger>
      <ContextMenuContent className={menuPopup}>
        <ContextMenuItem onClick={() => onToggleArchive(chat)} className={menuItem}>
          {itemContent}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
}
