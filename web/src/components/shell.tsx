import { AgentContext } from "../lib/agent-scope";
import { AgentAvatar } from "./agent-avatar";
import { DexMark } from "./dex-mark";
import { AgentCreateDialog } from "./agent-create-dialog";
import type { Agent } from "../api/generated/types.gen";
import { botStatus, type BotStatus } from "../lib/work";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog, DialogClose, DialogContent } from "./ui/dialog";
import { Settings, X, Plus, PanelLeft } from "lucide-react";

import {
  getSettingsOptions,
  getWorkOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { ShellContext } from "./page";
import { globalPages } from "./settings-switcher";
import { useSystemTheme } from "./theme";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { cn } from "../lib/utils";

const sidebarKey = "dex-sidebar";

/** The sidebar starts expanded. The choice stays in this browser. */
function useSidebarOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(sidebarKey) !== "collapsed");

  function toggle() {
    localStorage.setItem(sidebarKey, open ? "collapsed" : "open");
    setOpen(!open);
  }

  return [open, toggle] as const;
}

/** The status of each bot. The Shell polls it, so the drawer button can show it while the drawer is closed. */
function useBotStatuses() {
  const agents = useQuery({ ...listAgentsOptions(), refetchInterval: 3000 });
  const [now] = useState(() => Date.now());

  const work = useQuery({
    ...getWorkOptions({ query: { from: now, to: now + 7 * 86400000 } }),
    refetchInterval: 10000,
  });

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
  // The drawer button shows when another bot needs you. The current bot shows its own in the task panel.
  const attention = [...statuses].some(([id, status]) => id !== current && status === "needs");
  const shell = useMemo(() => ({ openDrawer: () => setMenu(true), attention }), [attention]);
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
        <Sidebar
          path={path}
          statuses={statuses}
          collapsed={!sidebarOpen}
          onToggle={toggleSidebar}
          className="hidden pt-0.5 lg:flex"
        />
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
          className="fixed inset-y-0 left-0 top-0 z-50 flex h-full w-[272px] max-w-[85vw] translate-x-0 translate-y-0 animate-[drawer-in_240ms_var(--ease-out-strong)_both] flex-col rounded-none bg-sidebar p-0 py-2 shadow-raised outline-none lg:hidden"
        >
          <DialogClose
            aria-label="Close navigation"
            className="absolute top-2.5 right-2 z-20 flex size-9 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <X size={18} />
          </DialogClose>
          <Sidebar path={path} statuses={statuses} className="w-full" />
        </DialogContent>
      </Dialog>
    </ShellContext.Provider>
  );
}

/**
 * The sidebar lists the bots and the global settings. Work happens inside a bot: its Main, its
 * task panel and its Routines. Each row shows one status, so a bot that needs you stands out.
 * One list of bots fits while there are few; a global inbox comes back if bots outgrow it (~6).
 */
function Sidebar({
  path,
  statuses,
  className,
  collapsed = false,
  onToggle,
}: {
  path: string;
  statuses: Map<string, BotStatus>;
  className?: string;
  collapsed?: boolean;
  onToggle?: () => void;
}) {
  const settings = useQuery(getSettingsOptions());
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);

  const [a, selected] = path.split("/").filter(Boolean);
  const agentId = a === "a" ? selected : "default";
  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const onSettings = globalPages.some((page) => page.to === path);
  const list = agents.data?.agents ?? [];

  const iconButton =
    "relative flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground";

  const footerRow =
    "relative flex h-9 w-full items-center gap-2 rounded-md px-2 text-[13px] text-muted-foreground hover:bg-accent hover:text-foreground";

  const errorLabel = `${providerErrors} ${providerErrors === 1 ? "provider needs" : "providers need"} attention`;

  return (
    <aside
      aria-label="Bots"
      className={cn(
        "flex h-full shrink-0 flex-col transition-[width] duration-150 ease-out motion-reduce:transition-none",
        collapsed ? "w-14 items-center" : "w-[240px]",
        className,
      )}
    >
      <div
        className={cn("mt-1 mb-2 flex h-10 items-center gap-2", collapsed ? "justify-center" : "mx-2 px-2")}
      >
        <DexMark />
        {collapsed ? null : <span className="text-[14px] font-semibold text-foreground">dex</span>}
      </div>
      <div
        className={cn(
          "flex min-h-0 flex-col gap-0.5 overflow-y-auto",
          collapsed ? "items-center px-1" : "px-2",
        )}
      >
        {list.map((agent) => (
          // The row reads its own bot's draft from app state, so it renders inside that bot's scope.
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <AgentRow
              agent={agent}
              status={statuses.get(agent.id!)}
              current={a === "a" && agent.id === selected}
              collapsed={collapsed}
            />
          </AgentContext.Provider>
        ))}
        <button
          type="button"
          aria-label="New bot"
          title={collapsed ? "New bot" : undefined}
          onClick={() => setCreating(true)}
          className={collapsed ? iconButton : footerRow}
        >
          <span className="flex size-6 shrink-0 items-center justify-center">
            <Plus size={15} />
          </span>
          {collapsed ? null : <span className="min-w-0 flex-1 truncate text-left">New bot</span>}
        </button>
        {agents.isError ? (
          <p role="alert" className={cn("text-xs text-red", collapsed ? "px-1 text-center" : "px-2")}>
            {collapsed ? "Error" : "Could not load bots."}
          </p>
        ) : null}
      </div>
      {creating ? (
        <AgentContext.Provider value={agentId}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
      <div className="flex-1" />

      <div
        className={cn(
          "mt-2 flex w-full shrink-0 flex-col gap-0.5 border-t border-border py-2",
          collapsed ? "items-center px-2" : "px-2",
        )}
      >
        <Link
          to="/providers"
          aria-current={onSettings ? "page" : undefined}
          aria-label={collapsed ? "Settings" : undefined}
          title={collapsed ? (providerErrors ? `Settings · ${errorLabel}` : "Settings") : undefined}
          className={cn(
            collapsed ? iconButton : footerRow,
            "aria-[current=page]:bg-accent aria-[current=page]:text-foreground",
          )}
        >
          <span className="flex size-6 shrink-0 items-center justify-center">
            <Settings size={15} />
          </span>
          {collapsed ? null : <span className="min-w-0 flex-1 truncate text-left">Settings</span>}
          {providerErrors ? (
            <span
              role="img"
              aria-label={errorLabel}
              className={cn("size-1.5 rounded-full bg-red", collapsed && "absolute top-1.5 right-1.5")}
            />
          ) : null}
        </Link>
        {onToggle ? (
          <button
            type="button"
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            aria-expanded={!collapsed}
            title={collapsed ? "Expand sidebar" : undefined}
            onClick={onToggle}
            className={collapsed ? iconButton : footerRow}
          >
            <span className="flex size-6 shrink-0 items-center justify-center">
              <PanelLeft size={15} />
            </span>
            {collapsed ? null : <span className="min-w-0 flex-1 truncate text-left">Collapse</span>}
          </button>
        ) : null}
      </div>
    </aside>
  );
}

const statusLabel = { needs: "Needs you", working: "Working" } as const;

function StatusDot({ status, floating = false }: { status: BotStatus; floating?: boolean }) {
  if (!status) return null;

  return (
    <span
      role="img"
      aria-label={statusLabel[status]}
      title={statusLabel[status]}
      className={cn(
        "size-2 shrink-0 rounded-full",
        status === "needs" ? "bg-orange" : "bg-primary",
        floating && "absolute right-1 bottom-1 ring-2 ring-sidebar",
      )}
    />
  );
}

function AgentRow({
  agent,
  status,
  current,
  collapsed,
}: {
  agent: Agent;
  status: BotStatus;
  current: boolean;
  collapsed: boolean;
}) {
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
      aria-label={collapsed ? label : undefined}
      title={collapsed ? label : undefined}
      className={cn(
        "relative flex shrink-0 items-center rounded-md hover:bg-accent aria-[current=page]:bg-accent",
        collapsed ? "size-9 justify-center" : "h-10 gap-2.5 px-2",
      )}
    >
      <AgentAvatar agent={agent} />
      {collapsed ? null : (
        <>
          <span className="min-w-0 flex-1 truncate text-[14px] font-medium text-foreground">
            {agent.name}
          </span>
          {dirty ? <span className="shrink-0 text-[11.5px] text-orange">Draft</span> : null}
        </>
      )}
      <StatusDot status={status} floating={collapsed} />
    </Link>
  );
}
