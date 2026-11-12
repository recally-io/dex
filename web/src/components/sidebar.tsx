import { useState, type ReactElement, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  CalendarClock,
  CircleAlert,
  Info,
  List,
  MessagesSquare,
  MoreHorizontal,
  Plus,
  Settings,
} from "lucide-react";
import type { Agent } from "../api/generated/types.gen";
import { getSettingsOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { AgentContext } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { needsItems, workOptions, type BotStatus } from "../lib/work";
import { cn } from "../lib/utils";
import { AgentAvatar } from "./agent-avatar";
import { AgentCreateDialog } from "./agent-create-dialog";
import { ChatList, row } from "./chat-list";
import { Button, buttonVariants } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";

/** The path segments that open under a bot's Settings. */
export const settingsSegments = ["agent", "instructions", "channels", "extensions"];

const statusLabel = { needs: "Needs you", working: "Working" } as const;

const iconButton =
  "relative flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground aria-expanded:bg-accent aria-expanded:text-foreground";

const menuPopup = "rounded-card bg-card p-1 shadow-raised outline-none";
const menuItem = "h-8 gap-2.5 px-2 text-[13px]";

/**
 * The one sidebar: the bots as groups, with the open bot expanded to what it holds. Other bots
 * show one row with their status, so the list stays short. One column fits while there are few
 * bots; a global inbox comes back if bots outgrow it (~6).
 */
export function Sidebar({
  path,
  statuses,
  lead,
  className,
}: {
  path: string;
  statuses: Map<string, BotStatus>;
  /** The control at the top left: hide the sidebar, or close the drawer. */
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
      <div className="flex h-12 shrink-0 items-center justify-between px-2">
        {lead}
        <MoreMenu />
      </div>
      <nav aria-label="Bots" className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 pb-3">
        {(agents.data?.agents ?? []).map((agent) => (
          // The group reads its own bot's draft, chats and needs, so it renders inside that bot's scope.
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
      {creating ? (
        <AgentContext.Provider value={openAgent ?? "default"}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
    </aside>
  );
}

/** The global entries. A red dot tells that a provider needs attention. */
function MoreMenu() {
  const settings = useQuery(getSettingsOptions());
  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;

  const errorLabel = `${providerErrors} ${providerErrors === 1 ? "provider needs" : "providers need"} attention`;
  const label = providerErrors ? `More, ${errorLabel}` : "More";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={label} title={label} className={iconButton}>
        <MoreHorizontal size={16} />
        {providerErrors ? (
          <span
            role="img"
            aria-label={errorLabel}
            className="absolute top-1 right-1 size-2 rounded-full bg-red ring-2 ring-sidebar"
          />
        ) : null}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={4} className={cn(menuPopup, "w-[220px]")}>
        <DropdownMenuItem render={<Link to="/providers" />} className={menuItem}>
          <Settings size={15} aria-hidden className="text-muted-foreground" />
          <span className="flex-1">Settings</span>
          {providerErrors ? <span className="text-[11.5px] text-red">{errorLabel}</span> : null}
        </DropdownMenuItem>
        <DropdownMenuItem render={<Link to="/about" />} className={menuItem}>
          <Info size={15} aria-hidden className="text-muted-foreground" />
          About
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * One bot: a heading row, and for the open bot what needs the user, Main, Routines and its chats.
 * Its Settings and All sessions sit in the heading's ⋯ menu.
 */
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
  // An unsaved instructions draft survives a bot switch, so the heading keeps a mark until it is saved.
  const dirty = instructionsDirty(draft, agent.instructions);

  const label = [agent.name, dirty ? "instructions draft" : "", status ? statusLabel[status] : ""]
    .filter(Boolean)
    .join(", ");

  return (
    <div className="flex flex-col">
      <div className="group relative flex items-center">
        <Link
          to="/a/$agentId"
          params={{ agentId: agent.id! }}
          aria-current={open ? "page" : undefined}
          aria-label={label}
          title={label}
          className={cn(
            "flex h-9 min-w-0 flex-1 items-center gap-2.5 rounded-[9px] px-2 text-[13.5px] hover:bg-accent",
            open ? "pr-10 font-semibold text-foreground" : "font-medium text-muted-foreground",
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
        {open ? <BotMenu path={path} dirty={dirty} /> : null}
      </div>
      {open ? <BotPages path={path} agent={agent} /> : null}
    </div>
  );
}

/** The ⋯ of the open bot. It stays lit on a page that it opens, since no row names that page. */
function BotMenu({ path, dirty }: { path: string; dirty: boolean }) {
  const agentId = path.split("/")[2]!;
  const segment = path.split("/")[3] ?? "";
  const here = settingsSegments.includes(segment) || segment === "sessions";
  const label = dirty ? "Bot menu, unsaved instructions draft" : "Bot menu";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={label}
        title={label}
        data-current={here || undefined}
        className={cn(iconButton, "absolute right-1 data-current:bg-accent data-current:text-foreground")}
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
      <DropdownMenuContent align="start" sideOffset={4} className={cn(menuPopup, "w-[200px]")}>
        <DropdownMenuItem
          render={<Link to="/a/$agentId/agent" params={{ agentId }} />}
          data-tab="Settings"
          className={menuItem}
        >
          <Settings size={15} aria-hidden className="text-muted-foreground" />
          <span className="flex-1">Settings</span>
          {dirty ? <span className="text-[11.5px] text-orange">Draft</span> : null}
        </DropdownMenuItem>
        <DropdownMenuItem
          render={<Link to="/a/$agentId/sessions" params={{ agentId }} />}
          className={menuItem}
        >
          <List size={15} aria-hidden className="text-muted-foreground" />
          All sessions
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** What the open bot holds, indented under its heading. */
function BotPages({ path, agent }: { path: string; agent: Agent }) {
  const agentId = agent.id!;
  const work = useQuery(workOptions());
  const [, , segment, chatSegment] = path.split("/").filter(Boolean);
  const chatId = segment === "c" ? Number(chatSegment) : 0;
  const routines = agent.activity?.enabledAutomations ?? 0;
  const routineFailed = agent.activity?.routineFailed ?? false;
  const needs = needsItems(agentId, work.data);
  const needCount = needs.questions.length + needs.results.length + needs.setup.length;

  const routinesLabel = [
    "Routines",
    routines ? `${routines} on` : "",
    routineFailed ? "latest run failed" : "",
  ]
    .filter(Boolean)
    .join(", ");

  const link = cn(buttonVariants({ variant: "ghost", size: "sm" }), row);

  return (
    <div className="flex flex-col gap-px pt-0.5 pb-2 pl-5">
      {needCount ? (
        <section aria-label="Needs you" className="flex flex-col gap-px pb-1">
          {needs.questions.map((chat) => (
            <NeedRow
              key={`question-${chat.id}`}
              link={
                chat.main ? (
                  <Link to="/a/$agentId" params={{ agentId }} />
                ) : (
                  <Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(chat.id) }} />
                )
              }
              title={chat.main ? "Main" : chat.title}
              detail="Asks you a question"
            />
          ))}
          {needs.results.map((item) => (
            <NeedRow
              key={`result-${item.runId}`}
              link={<Link to="/inbox/$runId" params={{ runId: item.runId }} />}
              title={item.automationName}
              detail="Routine run failed"
            />
          ))}
          {needs.setup.map((automation) => (
            <NeedRow
              key={`setup-${automation.id}`}
              link={
                <Link
                  to="/a/$agentId/automations/$automationId"
                  params={{ agentId, automationId: automation.id }}
                />
              }
              title={automation.name}
              detail="Routine needs an available model"
            />
          ))}
        </section>
      ) : null}
      <Link
        to="/a/$agentId"
        params={{ agentId }}
        activeOptions={{ exact: true }}
        data-tab="Main"
        title="Main hears every channel"
        className={link}
      >
        <MessagesSquare size={14} aria-hidden className="shrink-0" />
        Main
      </Link>
      <Link
        to="/a/$agentId/automations"
        params={{ agentId }}
        data-tab="Routines"
        aria-label={routinesLabel}
        aria-current={segment === "automations" ? "page" : undefined}
        className={link}
      >
        <CalendarClock size={14} aria-hidden className="shrink-0" />
        <span className="min-w-0 flex-1 truncate">Routines</span>
        {routineFailed ? (
          <span role="img" aria-label="Latest routine run failed" className="size-1.5 rounded-full bg-red" />
        ) : null}
        {routines ? <span className="text-[11.5px] tabular-nums">{routines}</span> : null}
      </Link>
      <ChatList chatId={chatId} />
    </div>
  );
}

/** One item that needs the user. The row opens the place to act on it. */
function NeedRow({ link, title, detail }: { link: ReactElement; title: string; detail: string }) {
  return (
    <Button
      variant="ghost"
      render={link}
      className="h-auto w-full justify-start gap-2 rounded-[9px] px-2 py-1.5 text-left"
    >
      <CircleAlert className="size-3.5 shrink-0 text-orange" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[12.5px] font-medium">{title}</span>
        <span className="block text-[11.5px] font-normal text-muted-foreground">{detail}</span>
      </span>
    </Button>
  );
}
