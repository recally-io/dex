import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Inbox, ListTodo, Plus, Search, Settings } from "lucide-react";
import type { Agent } from "../api/generated/types.gen";
import { getSettingsOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { AgentContext } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import type { BotStatus } from "../lib/inbox-summary";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./agent-avatar";
import { AgentCreateDialog } from "./agent-create-dialog";
import { Button, buttonVariants } from "./ui/button";

const row =
  "h-8 min-w-0 justify-start gap-2 rounded-[9px] border-0 px-2 text-[13px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-card aria-[current=page]:text-foreground aria-[current=page]:shadow-[0_0_0_1px_var(--border),0_1px_2px_rgb(0_0_0/0.06)]";

const statusLabel = { needs: "Inbox attention", working: "Working" } as const;

export function Sidebar({
  path,
  statuses,
  attentionCount,
  lead,
  onSearch,
  className,
}: {
  path: string;
  statuses: Map<string, BotStatus>;
  attentionCount: number;
  lead: ReactNode;
  onSearch: () => void;
  className?: string;
}) {
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);
  const [a, selected, section] = path.split("/").filter(Boolean);
  const openAgent = a === "a" ? selected : undefined;

  return (
    <aside
      aria-label="Sidebar"
      className={cn("flex h-full w-[260px] shrink-0 flex-col border-r border-border bg-sidebar", className)}
    >
      <div className="flex h-12 shrink-0 items-center gap-1 px-2">
        {lead}
        <Button
          variant="ghost"
          size="sm"
          aria-label="Search bots and conversations"
          aria-haspopup="dialog"
          title="Search bots and conversations (⌘K / Ctrl+K)"
          onClick={onSearch}
          className="min-w-0 flex-1 justify-start gap-2 bg-muted/60 text-muted-foreground"
        >
          <Search />
          <span>Search</span>
          <kbd className="ml-auto text-[10px]">⌘K</kbd>
        </Button>
      </div>
      <nav aria-label="Workspace" className="space-y-1 px-2 pb-2">
        <Link
          to="/board"
          aria-current={a === "board" || (a === "a" && section === "inbox") ? "page" : undefined}
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row, "w-full")}
        >
          <ListTodo size={15} aria-hidden />
          Board
        </Link>
        <Link
          to="/inbox"
          aria-current={a === "inbox" ? "page" : undefined}
          aria-label={
            attentionCount
              ? `Inbox, ${attentionCount} ${attentionCount === 1 ? "item needs" : "items need"} attention`
              : undefined
          }
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row, "w-full")}
        >
          <Inbox size={15} aria-hidden />
          Inbox
          {attentionCount ? (
            <span
              aria-hidden
              className="ml-auto rounded-full bg-muted px-1.5 text-xs tabular-nums text-orange"
            >
              {attentionCount}
            </span>
          ) : null}
        </Link>
      </nav>
      <nav aria-label="Bots" className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 pb-3">
        {(agents.data?.agents ?? []).map((agent) => (
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <BotGroup agent={agent} status={statuses.get(agent.id!)} open={agent.id === openAgent} />
          </AgentContext.Provider>
        ))}
        <button
          type="button"
          aria-label="New bot"
          onClick={() => setCreating(true)}
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row, "mt-1")}
        >
          <Plus className="size-3.5 shrink-0" />
          New bot
        </button>
        {agents.isError ? (
          <p role="alert" className="px-2 text-xs text-red">
            Could not load the bots.
          </p>
        ) : null}
      </nav>
      <Footer active={a === "settings"} />
      {creating ? (
        <AgentContext.Provider value={openAgent ?? "default"}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
    </aside>
  );
}

function Footer({ active }: { active: boolean }) {
  const settings = useQuery(getSettingsOptions());
  const errors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const label = `${errors} ${errors === 1 ? "provider needs" : "providers need"} attention`;

  return (
    <div className="flex gap-1 border-t border-border p-2">
      <Link
        to="/settings/general"
        aria-current={active ? "page" : undefined}
        className={cn(
          buttonVariants({ variant: "ghost", size: "sm" }),
          row,
          active && "bg-accent font-medium text-foreground",
        )}
      >
        <Settings size={15} aria-hidden />
        Settings
        {errors ? (
          <span role="img" aria-label={label} title={label} className="size-2 rounded-full bg-red" />
        ) : null}
      </Link>
    </div>
  );
}

function BotGroup({ agent, status, open }: { agent: Agent; status: BotStatus; open: boolean }) {
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, agent.instructions);

  const label = [agent.name, dirty ? "instructions draft" : "", status ? statusLabel[status] : ""]
    .filter(Boolean)
    .join(", ");

  return (
    <Link
      to="/a/$agentId"
      params={{ agentId: agent.id! }}
      aria-current={open ? "page" : undefined}
      aria-label={label}
      title={label}
      className={cn(
        "flex h-9 min-w-0 items-center gap-2.5 rounded-[9px] px-2 text-[13.5px] hover:bg-accent",
        open ? "font-semibold text-foreground" : "font-medium text-muted-foreground",
      )}
    >
      <AgentAvatar agent={agent} className="size-5 text-[11px]" />
      <span className="min-w-0 flex-1 truncate">{agent.name}</span>
      {status ? (
        <span
          role="img"
          aria-label={statusLabel[status]}
          title={statusLabel[status]}
          className={cn("size-2 shrink-0 rounded-full", status === "needs" ? "bg-orange" : "bg-primary")}
        />
      ) : null}
    </Link>
  );
}
