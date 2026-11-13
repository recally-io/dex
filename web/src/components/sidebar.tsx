import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, CircleAlert, Info, List, MoreHorizontal, Plus, Settings } from "lucide-react";
import type { Agent } from "../api/generated/types.gen";
import { getSettingsOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { AgentContext } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { needsItems, workOptions, type BotStatus } from "../lib/work";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./agent-avatar";
import { AgentCreateDialog } from "./agent-create-dialog";
import { row, useNewChat } from "./chat-list";
import { buttonVariants } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";

export const settingsSegments = ["agent", "instructions", "channels", "extensions"];

const statusLabel = { needs: "Needs you", working: "Working" } as const;

const iconButton =
  "relative flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground aria-expanded:bg-accent aria-expanded:text-foreground";

const menuPopup = "rounded-card bg-card p-1 shadow-raised outline-none";
const menuItem = "h-8 gap-2.5 px-2 text-[13px]";

export function Sidebar({
  path,
  statuses,
  lead,
  className,
}: {
  path: string;
  statuses: Map<string, BotStatus>;
  lead: ReactNode;
  className?: string;
}) {
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);
  const [a, selected] = path.split("/").filter(Boolean);
  const openAgent = a === "a" ? selected : undefined;

  return (
    <aside
      aria-label="Sidebar"
      className={cn("flex h-full w-[260px] shrink-0 flex-col border-r border-border bg-sidebar", className)}
    >
      <div className="flex h-12 shrink-0 items-center px-2">{lead}</div>
      <nav aria-label="Bots" className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 pb-3">
        {(agents.data?.agents ?? []).map((agent) => (
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <BotGroup
              agent={agent}
              status={statuses.get(agent.id!)}
              path={path}
              open={agent.id === openAgent}
            />
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
      <Footer />
      {creating ? (
        <AgentContext.Provider value={openAgent ?? "default"}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
    </aside>
  );
}

function Footer() {
  const settings = useQuery(getSettingsOptions());
  const errors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const label = `${errors} ${errors === 1 ? "provider needs" : "providers need"} attention`;

  return (
    <div className="flex gap-1 border-t border-border p-2">
      <Link to="/providers" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row)}>
        <Settings size={15} aria-hidden />
        Settings
        {errors ? (
          <span role="img" aria-label={label} title={label} className="size-2 rounded-full bg-red" />
        ) : null}
      </Link>
      <Link to="/about" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), row)}>
        <Info size={15} aria-hidden />
        About
      </Link>
    </div>
  );
}

function BotGroup({
  agent,
  status,
  path,
  open,
}: {
  agent: Agent;
  status: BotStatus;
  path: string;
  open: boolean;
}) {
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, agent.instructions);

  const label = [agent.name, dirty ? "instructions draft" : "", status ? statusLabel[status] : ""]
    .filter(Boolean)
    .join(", ");

  return (
    <div className="group flex items-center">
      <Link
        to="/a/$agentId"
        params={{ agentId: agent.id! }}
        aria-current={open ? "page" : undefined}
        aria-label={label}
        title={label}
        className={cn(
          "flex h-9 min-w-0 flex-1 items-center gap-2.5 rounded-[9px] px-2 text-[13.5px] hover:bg-accent",
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
      <BotMenu agent={agent} path={path} dirty={dirty} open={open} />
    </div>
  );
}

function BotMenu({
  agent,
  path,
  dirty,
  open,
}: {
  agent: Agent;
  path: string;
  dirty: boolean;
  open: boolean;
}) {
  const agentId = agent.id!;
  const segment = path.split("/")[3] ?? "";

  const here =
    open && (settingsSegments.includes(segment) || segment === "sessions" || segment === "automations");

  const label = dirty ? `Bot menu, unsaved instructions draft: ${agent.name}` : `Bot menu: ${agent.name}`;
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);
  const newChat = useNewChat();
  const routines = agent.activity?.enabledAutomations ?? 0;
  const routineFailed = agent.activity?.routineFailed ?? false;

  const routinesLabel = [
    "Routines",
    routines ? `${routines} on` : "",
    routineFailed ? "latest run failed" : "",
  ]
    .filter(Boolean)
    .join(", ");

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={label}
        title={label}
        data-current={here || undefined}
        className={cn(iconButton, "data-current:bg-accent data-current:text-foreground")}
      >
        <MoreHorizontal size={16} />
        {dirty ? (
          <span
            role="img"
            aria-label="Unsaved instructions draft"
            className="absolute top-1.5 right-1.5 size-1.5 rounded-full bg-orange"
          />
        ) : null}
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        sideOffset={4}
        className={cn(menuPopup, "w-[240px] max-w-[calc(100vw-1rem)]")}
      >
        <DropdownMenuItem
          disabled={newChat.isPending}
          onClick={() => newChat.mutate({ path: { agentId } })}
          className={menuItem}
        >
          <Plus size={15} />
          New chat
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/a/$agentId/sessions" params={{ agentId }} />}
          className={menuItem}
        >
          <List size={15} />
          All sessions
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/a/$agentId/automations" params={{ agentId }} />}
          aria-label={routinesLabel}
          className={menuItem}
        >
          <CalendarClock size={15} />
          <span className="min-w-0 flex-1 truncate">Routines</span>
          {routineFailed ? (
            <span
              role="img"
              aria-label="Latest routine run failed"
              className="size-1.5 rounded-full bg-red"
            />
          ) : null}
          {routines ? <span className="text-[11.5px] tabular-nums">{routines}</span> : null}
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/a/$agentId/agent" params={{ agentId }} />}
          data-tab="Settings"
          className={menuItem}
        >
          <Settings size={15} />
          <span className="flex-1">Settings</span>
          {dirty ? <span className="text-orange">Draft</span> : null}
        </DropdownMenuItem>
        {needs.questions.map((chat) => (
          <DropdownMenuItem
            key={`question-${chat.id}`}
            render={
              chat.main ? (
                <Link to="/a/$agentId" params={{ agentId }} />
              ) : (
                <Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(chat.id) }} />
              )
            }
            className="h-auto gap-2 px-2 py-1.5"
          >
            <CircleAlert size={15} className="text-orange" />
            <span className="min-w-0 flex-1 overflow-hidden">
              <span className="block truncate">{chat.main ? "Main" : chat.title}</span>
              <span className="block text-xs text-muted-foreground">Asks you a question</span>
            </span>
          </DropdownMenuItem>
        ))}
        {needs.results.map((item) => (
          <DropdownMenuItem
            key={`result-${item.runId}`}
            render={<Link to="/inbox/$runId" params={{ runId: item.runId }} />}
            className="h-auto gap-2 px-2 py-1.5"
          >
            <CircleAlert size={15} className="text-orange" />
            <span className="min-w-0 flex-1 overflow-hidden">
              <span className="block truncate">{item.automationName}</span>
              <span className="block text-xs text-muted-foreground">Routine run failed</span>
            </span>
          </DropdownMenuItem>
        ))}
        {needs.setup.map((automation) => (
          <DropdownMenuItem
            key={`setup-${automation.id}`}
            render={
              <Link
                to="/a/$agentId/automations/$automationId"
                params={{ agentId, automationId: automation.id }}
              />
            }
            className="h-auto gap-2 px-2 py-1.5"
          >
            <CircleAlert size={15} className="text-orange" />
            <span className="min-w-0 flex-1 overflow-hidden">
              <span className="block truncate">{automation.name}</span>
              <span className="block text-xs text-muted-foreground">Routine needs an available model</span>
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
