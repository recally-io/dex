import { AgentContext } from "../lib/agent-scope";
import { AgentAvatar } from "./agent-avatar";
import { AgentCreateDialog } from "./agent-create-dialog";
import { BotColumn } from "./bot-column";
import type { Agent } from "../api/generated/types.gen";
import { botStatus, workOptions, type BotStatus } from "../lib/work";
import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog, DialogClose, DialogContent } from "./ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Plus, Settings, X } from "lucide-react";

import {
  getHealthOptions,
  getSettingsOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { ShellContext } from "./page";
import { useSystemTheme } from "./theme";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { cn } from "../lib/utils";

const sidebarKey = "dex-sidebar";

/** The bot column starts open. The choice stays in this browser. */
function useSidebarOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(sidebarKey) !== "collapsed");

  // Stable, so the shell context changes only when the column opens or closes.
  const toggle = useCallback(
    () =>
      setOpen((was) => {
        localStorage.setItem(sidebarKey, was ? "collapsed" : "open");

        return !was;
      }),
    [],
  );

  return [open, toggle] as const;
}

/** The status of each bot. The Shell polls it, so the drawer button can show it while the drawer is closed. */
function useBotStatuses() {
  const agents = useQuery({ ...listAgentsOptions(), refetchInterval: 3000 });
  const work = useQuery(workOptions());

  return new Map(
    (agents.data?.agents ?? []).map((agent) => [agent.id!, botStatus(agent.id!, work.data, agent.activity)]),
  );
}

export function Shell() {
  const statuses = useBotStatuses();
  const [menu, setMenu] = useState(false);
  const [sidebarOpen, toggleSidebar] = useSidebarOpen();
  const location = useRouterState({ select: (s) => s.location });
  const path = location.pathname;
  const shown = useRef(path);
  // The drawer closes when the page changes.
  const [menuPath, setMenuPath] = useState(path);
  const [lastAgent, setLastAgent] = useState("default");
  const routeAgent = path.match(/^\/a\/([^/]+)/)?.[1];
  const current = routeAgent ?? lastAgent;
  // The drawer button shows when another bot needs you. The current bot shows its own in the bot column.
  const attention = [...statuses].some(([id, status]) => id !== current && status === "needs");

  const shell = useMemo(
    () => ({
      openDrawer: () => setMenu(true),
      attention,
      column: routeAgent ? { open: sidebarOpen, toggle: toggleSidebar } : undefined,
    }),
    [attention, routeAgent, sidebarOpen, toggleSidebar],
  );

  useSystemTheme();

  if (routeAgent && routeAgent !== lastAgent) setLastAgent(routeAgent);

  if (menuPath !== path) {
    setMenuPath(path);
    setMenu(false);
  }

  // After navigation, focus moves to the page title, so keyboard and screen reader users land on
  // the new page.
  useEffect(() => {
    // The first page load keeps the browser's focus.
    if (shown.current === path) return;
    shown.current = path;

    const frame = requestAnimationFrame(() =>
      document.querySelector<HTMLElement>('[id="page-title"]')?.focus(),
    );

    return () => cancelAnimationFrame(frame);
  }, [path]);

  return (
    <ShellContext.Provider value={shell}>
      <div className="flex h-dvh bg-sidebar text-foreground lg:gap-0 lg:p-0">
        <a
          href="#main"
          onClick={(e) => {
            e.preventDefault();
            document.getElementById("main")?.focus();
          }}
          className="sr-only z-50 rounded-md bg-foreground px-3 py-2 text-background focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
        >
          Skip to content
        </a>
        <Rail path={path} statuses={statuses} className="hidden lg:flex" />
        {routeAgent && sidebarOpen ? (
          <AgentContext.Provider value={routeAgent}>
            <BotColumn path={path} className="hidden lg:flex" />
          </AgentContext.Provider>
        ) : null}
        <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 outline-none">
          <AgentContext.Provider value={routeAgent ?? lastAgent}>
            <Outlet />
          </AgentContext.Provider>
        </main>
      </div>
      <Dialog open={menu} onOpenChange={setMenu}>
        <DialogContent
          showCloseButton={false}
          aria-label="Navigation"
          className="fixed inset-y-0 left-0 top-0 z-50 flex h-full w-auto max-w-[92vw] translate-x-0 translate-y-0 animate-[drawer-in_240ms_var(--ease-out-strong)_both] flex-row gap-0 rounded-none bg-sidebar p-0 shadow-raised outline-none lg:hidden"
        >
          <Rail
            path={path}
            statuses={statuses}
            top={
              <DialogClose
                aria-label="Close navigation"
                className="mt-1.5 flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <X size={18} />
              </DialogClose>
            }
          />
          {routeAgent ? (
            <AgentContext.Provider value={routeAgent}>
              <BotColumn path={path} className="max-w-[calc(92vw-3.5rem)]" />
            </AgentContext.Provider>
          ) : null}
        </DialogContent>
      </Dialog>
    </ShellContext.Provider>
  );
}

/**
 * The rail starts with the user's menu, which holds the global settings, and lists the bots as
 * avatars. Everything inside one bot sits in its bot column, so the rail stays one narrow strip. Each avatar shows one status, so a
 * bot that needs you stands out. One strip fits while there are few bots; a global inbox comes
 * back if bots outgrow it (~6).
 */
function Rail({
  path,
  statuses,
  top,
  className,
}: {
  path: string;
  statuses: Map<string, BotStatus>;
  top?: ReactNode;
  className?: string;
}) {
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);

  const [a, selected] = path.split("/").filter(Boolean);
  const agentId = a === "a" ? selected : "default";
  const list = agents.data?.agents ?? [];

  const iconButton =
    "relative flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground";

  return (
    <aside
      aria-label="Bots"
      className={cn(
        "flex h-full w-14 shrink-0 flex-col items-center border-r border-border bg-sidebar",
        className,
      )}
    >
      {top}
      <UserMenu />
      <div className="flex min-h-0 flex-col items-center gap-1 overflow-y-auto px-1 pt-1">
        {list.map((agent) => (
          // The row reads its own bot's draft from app state, so it renders inside that bot's scope.
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <AgentRow
              agent={agent}
              status={statuses.get(agent.id!)}
              current={a === "a" && agent.id === selected}
            />
          </AgentContext.Provider>
        ))}
        <button
          type="button"
          aria-label="New bot"
          title="New bot"
          onClick={() => setCreating(true)}
          className={iconButton}
        >
          <Plus size={15} />
        </button>
        {agents.isError ? (
          <p role="alert" className="px-1 text-center text-xs text-red">
            Error
          </p>
        ) : null}
      </div>
      {creating ? (
        <AgentContext.Provider value={agentId}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
    </aside>
  );
}

/**
 * The user's menu names this Dex and opens the global settings. Dex has one owner and no profile,
 * so the avatar is a plain figure. A red dot tells that a provider needs attention.
 */
function UserMenu() {
  const settings = useQuery(getSettingsOptions());
  const health = useQuery(getHealthOptions());
  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;

  const errorLabel = `${providerErrors} ${providerErrors === 1 ? "provider needs" : "providers need"} attention`;
  const label = providerErrors ? `User menu, ${errorLabel}` : "User menu";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={label}
        title={label}
        className="relative mt-2.5 mb-1 flex size-10 shrink-0 items-center justify-center rounded-full transition-transform hover:scale-105 aria-expanded:scale-105"
      >
        <UserAvatar className="size-8" />
        {providerErrors ? (
          <span
            role="img"
            aria-label={errorLabel}
            className="absolute top-1 right-1 size-2.5 rounded-full bg-red ring-2 ring-sidebar"
          />
        ) : null}
      </DropdownMenuTrigger>
      <DropdownMenuContent
        side="right"
        align="start"
        sideOffset={6}
        className="w-[220px] rounded-card bg-card p-1 shadow-raised outline-none"
      >
        <div className="flex items-center gap-2.5 px-2 pt-1.5 pb-2.5">
          <UserAvatar className="size-8" />
          <div className="min-w-0">
            <div className="text-[13px] font-semibold">Dex</div>
            <div className="truncate font-mono text-[11px] text-muted-foreground">
              {health.data?.version ?? " "}
            </div>
          </div>
        </div>
        <DropdownMenuSeparator className="mb-1" />
        <DropdownMenuItem render={<Link to="/providers" />} className="h-8 gap-2.5 px-2 text-[13px]">
          <Settings size={15} aria-hidden className="text-muted-foreground" />
          <span className="flex-1">Settings</span>
          {providerErrors ? <span className="text-[11.5px] text-red">{errorLabel}</span> : null}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A head and shoulders on a warm gradient. The colours follow the theme through styles.css. */
function UserAvatar({ className }: { className?: string }) {
  const gradient = useId();

  return (
    <svg
      aria-hidden
      viewBox="0 0 32 32"
      className={cn("user-avatar shrink-0 overflow-hidden rounded-full", className)}
    >
      <defs>
        <linearGradient id={gradient} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="var(--user-avatar-from)" />
          <stop offset="1" stopColor="var(--user-avatar-to)" />
        </linearGradient>
      </defs>
      <circle cx="16" cy="16" r="16" fill={`url(#${gradient})`} />
      <circle cx="16" cy="12.5" r="5.25" fill="var(--user-avatar-figure)" />
      <path d="M5 32c0-6.4 4.9-10.5 11-10.5S27 25.6 27 32z" fill="var(--user-avatar-figure)" />
    </svg>
  );
}

const statusLabel = { needs: "Needs you", working: "Working" } as const;

function StatusDot({ status }: { status: BotStatus }) {
  if (!status) return null;

  return (
    <span
      role="img"
      aria-label={statusLabel[status]}
      title={statusLabel[status]}
      className={cn(
        "absolute right-1 bottom-1 size-2 shrink-0 rounded-full ring-2 ring-sidebar",
        status === "needs" ? "bg-orange" : "bg-primary",
      )}
    />
  );
}

function AgentRow({ agent, status, current }: { agent: Agent; status: BotStatus; current: boolean }) {
  const { draft } = useApp();
  // An unsaved instructions draft survives a bot switch, so the row keeps a mark until it is saved.
  const dirty = instructionsDirty(draft, agent.instructions);

  const label = [agent.name, dirty ? "instructions draft" : "", status ? statusLabel[status] : ""]
    .filter(Boolean)
    .join(", ");

  return (
    <Link
      to="/a/$agentId"
      params={{ agentId: agent.id! }}
      aria-current={current ? "page" : undefined}
      aria-label={label}
      title={label}
      className="relative flex size-10 shrink-0 items-center justify-center rounded-md hover:bg-accent aria-[current=page]:bg-accent"
    >
      <AgentAvatar agent={agent} className="size-7 text-[13px]" />
      <StatusDot status={status} />
    </Link>
  );
}
