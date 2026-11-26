import { useEffect } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Archive, MessageCircle } from "lucide-react";
import type { Session } from "../api/generated/types.gen";
import { listBotsOptions, listSessionsOptions } from "../api/generated/@tanstack/react-query.gen";
import { sessionBotId } from "../lib/sessions";
import { BotAvatar } from "./bot-avatar";
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
          <DialogTitle className="sr-only">Search bots and sessions</DialogTitle>
          <DialogDescription className="sr-only">Search bot names and conversation titles.</DialogDescription>
          <SearchResults close={() => onOpenChange(false)} />
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

function SearchResults({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const botList = useQuery(listBotsOptions());
  const bots = botList.data?.bots ?? [];

  // Lists include archived sessions and cross-bot children. Load only while search is open.
  const lists = useQueries({
    queries: bots.map((bot) => ({
      ...listSessionsOptions({ path: { botId: bot.id! } }),
      staleTime: 30_000,
    })),
  });

  const sessions = new Map<string, { session: Session; botId: string; name: string }>();

  lists.forEach((list, index) => {
    const bot = bots[index]!;

    for (const session of list.data?.sessions ?? []) {
      const botId = sessionBotId(session, bot.id!);
      const name = bots.find((item) => item.id === botId)?.name ?? session.botName ?? bot.name;
      sessions.set(`${botId}/${session.id}`, { session, botId, name });
    }
  });

  const sorted = [...sessions.values()].sort(
    (a, b) => b.session.updatedAt - a.session.updatedAt || b.session.id - a.session.id,
  );

  const loading = botList.isPending || lists.some((list) => list.isPending);

  return (
    <Command>
      <CommandInput autoFocus aria-label="Search bots and sessions" placeholder="Search bots and sessions…" />
      <CommandList className="max-h-[min(360px,60dvh)] p-1">
        {loading ? (
          <p role="status" className="p-2 text-xs text-muted-foreground">
            Loading sessions…
          </p>
        ) : (
          <CommandEmpty>No matches.</CommandEmpty>
        )}
        <CommandGroup heading="Bots">
          {bots.map((bot) => (
            <CommandItem
              key={bot.id}
              value={`bot/${bot.id}`}
              keywords={[bot.name, "Main"]}
              onSelect={() => {
                close();
                void navigate({ to: "/b/$botId", params: { botId: bot.id! } });
              }}
            >
              <BotAvatar bot={bot} className="size-5 text-[10px]" />
              <span className="min-w-0 flex-1 truncate">{bot.name}</span>
              <span className="text-muted-foreground">Main</span>
            </CommandItem>
          ))}
        </CommandGroup>
        <CommandGroup heading="Sessions">
          {sorted.map(({ session, botId, name }) => (
            <CommandItem
              key={`${botId}/${session.id}`}
              value={`session/${botId}/${session.id}`}
              keywords={[session.title || "New session", name]}
              onSelect={() => {
                close();
                void navigate({
                  to: "/b/$botId/s/$sessionId",
                  params: { botId, sessionId: String(session.id) },
                });
              }}
            >
              <MessageCircle />
              <span className="min-w-0 flex-1 truncate" title={session.title}>
                {session.title || "New session"}
              </span>
              <span className="max-w-24 truncate text-muted-foreground">{name}</span>
              {session.archivedAt ? (
                <Archive aria-label="Archived conversation" className="text-muted-foreground" />
              ) : null}
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
      {botList.isError ? (
        <Failure title="Could not load bots" error={botList.error} retry={() => void botList.refetch()} />
      ) : null}
      {lists.map((list, index) =>
        list.isError ? (
          <Failure
            key={bots[index]!.id}
            title={`Could not load sessions of ${bots[index]!.name}`}
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
