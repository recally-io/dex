import { Menu } from "@base-ui/react/menu";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { IconChevronDownSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronDownSmall";
import { IconPlusSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPlusSmall";
import {
  createChatMutation,
  getMainChatOptions,
  listChatsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { errorText, refreshChats } from "../lib/queries";
import { sessionKind, taskTime } from "../lib/tasks";
import { cn } from "../lib/utils";

const item =
  "flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left text-[12.5px] text-ink outline-none data-[highlighted]:bg-hover";

/**
 * Names the open chat and switches between the bot's writable chats. Main is the bot's one
 * timeline and always comes first. Other chats have their own context and sit in this menu, not
 * in a list column, so the pane keeps one navigation level.
 */
export function ChatSwitcher({ chatId, title }: { chatId: number; title: string }) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();
  const main = useQuery(getMainChatOptions({ path: { agentId } }));
  const sessions = useQuery({ ...listChatsOptions({ path: { agentId } }), refetchInterval: 3000 });

  const chats = (sessions.data?.chats ?? [])
    .filter((chat) => sessionKind(chat) === "Chat")
    .toSorted((a, b) => b.updatedAt - a.updatedAt);

  const create = useMutation({
    ...createChatMutation(),
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
    },
    onError: (cause) => notify(`Could not open a new chat. ${errorText(cause)}`),
  });

  return (
    <Menu.Root>
      <Menu.Trigger
        className="-ml-2 flex h-7 min-w-0 items-center gap-1 rounded-control px-2 text-[13px] font-semibold text-ink hover:bg-hover-2 aria-expanded:bg-hover-2"
        aria-label={`${title}. Switch chat`}
      >
        <span className="min-w-0 truncate">{title}</span>
        <IconChevronDownSmall size={14} className="shrink-0 text-ink-3" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner align="start" sideOffset={6} collisionPadding={8} className="z-40">
          <Menu.Popup className="flex max-h-[min(420px,var(--available-height))] w-[300px] max-w-[calc(100vw-1rem)] origin-(--transform-origin) animate-[pop-in_180ms_var(--ease-out-strong)_both] flex-col overflow-y-auto rounded-[12px] bg-surface p-1 shadow-raised outline-none">
            <Menu.LinkItem
              render={<Link to="/a/$agentId" params={{ agentId }} />}
              aria-current={main.data?.id === chatId ? "page" : undefined}
              className={cn(item, "aria-[current=page]:bg-hover-2")}
            >
              <span className="min-w-0 flex-1 truncate font-medium">Main</span>
              <span className="shrink-0 text-[11.5px] text-ink-3">Every channel</span>
            </Menu.LinkItem>
            {chats.map((chat) => (
              <Menu.LinkItem
                key={chat.id}
                render={<Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(chat.id) }} />}
                aria-current={chat.id === chatId ? "page" : undefined}
                className={cn(item, "aria-[current=page]:bg-hover-2")}
              >
                <span className="min-w-0 flex-1 truncate">{chat.title || "New chat"}</span>
                <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">
                  {taskTime(chat.updatedAt)}
                </span>
              </Menu.LinkItem>
            ))}
            <Menu.Separator className="mx-1 my-1 h-px bg-line-soft" />
            <Menu.Item
              disabled={create.isPending}
              onClick={() => create.mutate({ path: { agentId } })}
              className={cn(item, "text-ink-2 data-[disabled]:opacity-50")}
            >
              <IconPlusSmall size={15} className="shrink-0" />
              <span className="min-w-0 flex-1 truncate">{create.isPending ? "Opening…" : "New chat"}</span>
              <span className="shrink-0 text-[11.5px] text-ink-3">Own context</span>
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
