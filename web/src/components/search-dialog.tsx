import { useEffect } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Archive, MessageCircle } from "lucide-react";
import type { Chat } from "../api/generated/types.gen";
import { listAgentsOptions, listChatsOptions } from "../api/generated/@tanstack/react-query.gen";
import { sessionAgentId } from "../lib/sessions";
import { AgentAvatar } from "./agent-avatar";
import { Failure } from "./feedback";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "./ui/command";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";

export function SearchDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  useEffect(() => {
    function shortcut(event: KeyboardEvent) {
      if (event.defaultPrevented || !(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "k")
        return;
      event.preventDefault();
      onOpenChange(!open);
    }

    window.addEventListener("keydown", shortcut);

    return () => window.removeEventListener("keydown", shortcut);
  }, [open, onOpenChange]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open ? (
        <DialogContent showCloseButton={false} className="gap-0 overflow-hidden p-0 sm:max-w-lg">
          <DialogTitle className="sr-only">Search bots and conversations</DialogTitle>
          <DialogDescription className="sr-only">Search bot names and conversation titles.</DialogDescription>
          <SearchResults close={() => onOpenChange(false)} />
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

function SearchResults({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const agents = useQuery(listAgentsOptions());
  const bots = agents.data?.agents ?? [];

  // Lists include archived chats and cross-bot children. Load only while search is open.
  const lists = useQueries({
    queries: bots.map((agent) => ({
      ...listChatsOptions({ path: { agentId: agent.id! } }),
      staleTime: 30_000,
    })),
  });

  const conversations = new Map<string, { chat: Chat; agentId: string; name: string }>();

  lists.forEach((list, index) => {
    const bot = bots[index]!;

    for (const chat of list.data?.chats ?? []) {
      const agentId = sessionAgentId(chat, bot.id!);
      const name = bots.find((agent) => agent.id === agentId)?.name ?? chat.agentName ?? bot.name;
      conversations.set(`${agentId}/${chat.id}`, { chat, agentId, name });
    }
  });

  const sorted = [...conversations.values()].sort(
    (a, b) => b.chat.updatedAt - a.chat.updatedAt || b.chat.id - a.chat.id,
  );

  const loading = agents.isPending || lists.some((list) => list.isPending);

  return (
    <Command>
      <CommandInput
        autoFocus
        aria-label="Search bots and conversations"
        placeholder="Search bots and conversations…"
      />
      <CommandList className="max-h-[min(360px,60dvh)] p-1">
        {loading ? (
          <p role="status" className="p-2 text-xs text-muted-foreground">
            Loading conversations…
          </p>
        ) : (
          <CommandEmpty>No matches.</CommandEmpty>
        )}
        <CommandGroup heading="Bots">
          {bots.map((agent) => (
            <CommandItem
              key={agent.id}
              value={`bot/${agent.id}`}
              keywords={[agent.name, "Main"]}
              onSelect={() => {
                close();
                void navigate({ to: "/a/$agentId", params: { agentId: agent.id! } });
              }}
            >
              <AgentAvatar agent={agent} className="size-5 text-[10px]" />
              <span className="min-w-0 flex-1 truncate">{agent.name}</span>
              <span className="text-muted-foreground">Main</span>
            </CommandItem>
          ))}
        </CommandGroup>
        <CommandGroup heading="Conversations">
          {sorted.map(({ chat, agentId, name }) => (
            <CommandItem
              key={`${agentId}/${chat.id}`}
              value={`chat/${agentId}/${chat.id}`}
              keywords={[chat.title || "New session", name]}
              onSelect={() => {
                close();
                void navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
              }}
            >
              <MessageCircle />
              <span className="min-w-0 flex-1 truncate" title={chat.title}>
                {chat.title || "New session"}
              </span>
              <span className="max-w-24 truncate text-muted-foreground">{name}</span>
              {chat.archivedAt ? (
                <Archive aria-label="Archived conversation" className="text-muted-foreground" />
              ) : null}
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
      {agents.isError ? (
        <Failure title="Could not load bots" error={agents.error} retry={() => void agents.refetch()} />
      ) : null}
      {lists.map((list, index) =>
        list.isError ? (
          <Failure
            key={bots[index]!.id}
            title={`Could not load conversations of ${bots[index]!.name}`}
            error={list.error}
            retry={() => void list.refetch()}
          />
        ) : null,
      )}
      <p className="border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
        Searches titles, not message contents.
      </p>
    </Command>
  );
}
