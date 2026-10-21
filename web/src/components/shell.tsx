import { AgentContext, useAgentId } from "../lib/agent-scope";
import { AgentAvatar } from "./agent-avatar";
import { DexMark } from "./dex-mark";
import { AgentCreateDialog } from "./agent-create-dialog";
import type { Agent } from "../api/generated/types.gen";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog } from "@base-ui/react/dialog";
import { IconSettingsGear2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconSettingsGear2";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconMagnifyingGlass } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconMagnifyingGlass";
import { IconPlusSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPlusSmall";
import { IconSidebar } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconSidebar";

import { getSettingsOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { StatusPill } from "./atoms/StatusPill";
import { ShellContext } from "./page";
import { globalPages } from "./settings-switcher";
import { useSystemTheme } from "./theme";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { cn } from "../lib/utils";

const sidebarKey = "dex-sidebar";

/** The sidebar starts collapsed to the bot icons. The choice stays in this browser. */
function useSidebarOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(sidebarKey) === "open");

  function toggle() {
    localStorage.setItem(sidebarKey, open ? "collapsed" : "open");
    setOpen(!open);
  }

  return [open, toggle] as const;
}

export function Shell() {
  useQuery({ ...listAgentsOptions(), refetchInterval: 3000 });
  const [menu, setMenu] = useState(false);
  const [sidebarOpen, toggleSidebar] = useSidebarOpen();
  const location = useRouterState({ select: (s) => s.location });
  const path = location.pathname;
  const shown = useRef(path);
  // The drawer closes when the page changes.
  const [menuPath, setMenuPath] = useState(path);
  const [lastAgent, setLastAgent] = useState("default");
  const routeAgent = path.match(/^\/a\/([^/]+)/)?.[1];
  const shell = useMemo(() => ({ openDrawer: () => setMenu(true) }), []);
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
      <div className="flex h-dvh bg-canvas text-ink lg:gap-0 lg:p-0">
        <a
          href="#main"
          onClick={(e) => {
            e.preventDefault();
            document.getElementById("main")?.focus();
          }}
          className="sr-only z-50 rounded-control bg-ink px-3 py-2 text-canvas focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
        >
          Skip to content
        </a>
        <Sidebar
          path={path}
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
      <Dialog.Root open={menu} onOpenChange={setMenu}>
        <Dialog.Portal>
          <Dialog.Backdrop className="fixed inset-0 z-50 animate-[fade-in_160ms_ease-out_both] bg-[oklch(0.2_0.01_264/0.32)] lg:hidden" />
          <Dialog.Popup
            aria-label="Navigation"
            className="fixed inset-y-0 left-0 z-50 flex w-[272px] max-w-[85vw] animate-[drawer-in_240ms_var(--ease-out-strong)_both] flex-col bg-canvas py-2 shadow-overlay outline-none lg:hidden"
          >
            <Dialog.Close
              aria-label="Close navigation"
              className="absolute top-3 -right-10 z-20 flex size-8 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2 hover:text-ink"
            >
              <IconCrossSmall size={18} />
            </Dialog.Close>
            <Sidebar path={path} className="w-full" />
          </Dialog.Popup>
        </Dialog.Portal>
      </Dialog.Root>
    </ShellContext.Provider>
  );
}

/**
 * The list of bots. Each one is a long-lived role the user talks to, so the list holds the
 * bots themselves, not their sessions or settings. The global settings page sits in the footer.
 * Collapsed, the sidebar keeps one icon per bot; the drawer below lg is always expanded.
 */
function Sidebar({
  path,
  className,
  collapsed = false,
  onToggle,
}: {
  path: string;
  className?: string;
  collapsed?: boolean;
  onToggle?: () => void;
}) {
  const settings = useQuery(getSettingsOptions());
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState("");
  const [a, selected] = path.split("/").filter(Boolean);
  const agentId = a === "a" ? selected : "default";
  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const onSettings = globalPages.some((page) => page.to === path);

  const list = agents.data?.agents ?? [];

  const shown =
    query && !collapsed
      ? list.filter((agent) => agent.name.toLowerCase().includes(query.trim().toLowerCase()))
      : list;

  const iconButton =
    "relative flex size-9 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2 hover:text-ink";

  const footerRow =
    "relative flex h-8 w-full items-center gap-1.5 rounded-control px-2 text-[13px] text-ink-2 hover:bg-hover-2 hover:text-ink";

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
      <div className={cn("mt-1 flex h-10 items-center gap-2", collapsed ? "justify-center" : "mx-2 px-2")}>
        <DexMark />
        {collapsed ? null : (
          <span className="text-[13px] font-semibold tracking-[-0.01em] text-ink">dex</span>
        )}
      </div>
      {collapsed ? (
        <span aria-hidden className="my-2 h-px w-6 shrink-0 bg-line" />
      ) : (
        <div className="mx-2 mt-2 mb-1 flex h-7 items-center justify-between px-2">
          <span className="text-[12px] font-medium text-ink-3">Bots</span>
          <button
            type="button"
            aria-label="Create bot"
            onClick={() => setCreating(true)}
            className="flex size-6 items-center justify-center rounded-control text-[15px] leading-none text-ink-3 hover:bg-hover-2 hover:text-ink"
          >
            +
          </button>
        </div>
      )}
      {list.length > 4 && !collapsed ? (
        <div className="mx-2 mb-1.5 flex h-8 shrink-0 items-center gap-1.5 rounded-control bg-field px-2 text-ink-3">
          <IconMagnifyingGlass size={13} />
          <input
            type="search"
            value={query}
            aria-label="Search bots"
            placeholder="Search bots"
            onChange={(event) => setQuery(event.target.value)}
            className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink placeholder:text-ink-3"
          />
        </div>
      ) : null}
      <div className={cn("min-h-0 overflow-y-auto", collapsed && "flex flex-col items-center gap-1")}>
        {shown.map((agent) => (
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <AgentRow agent={agent} current={a === "a" && agent.id === selected} collapsed={collapsed} />
          </AgentContext.Provider>
        ))}
        {collapsed ? (
          <button
            type="button"
            aria-label="Create bot"
            title="Create bot"
            onClick={() => setCreating(true)}
            className={iconButton}
          >
            <IconPlusSmall size={16} />
          </button>
        ) : null}
        {list.length && !shown.length ? (
          <p className="px-4 py-2 text-[12.5px] text-ink-3">No bot matches.</p>
        ) : null}
        {agents.isError ? (
          <p role="alert" className={cn("text-xs text-red", collapsed ? "px-1 text-center" : "px-4")}>
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
          "mt-2 flex w-full shrink-0 flex-col gap-0.5 border-t border-line py-2",
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
            "aria-[current=page]:bg-hover-2 aria-[current=page]:text-ink",
          )}
        >
          <span className="flex size-5 shrink-0 items-center justify-center">
            <IconSettingsGear2 size={15} />
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
            <span className="flex size-5 shrink-0 items-center justify-center">
              <IconSidebar size={15} />
            </span>
            {collapsed ? null : <span className="min-w-0 flex-1 truncate text-left">Collapse</span>}
          </button>
        ) : null}
      </div>
    </aside>
  );
}

function AgentRow({ agent, current, collapsed }: { agent: Agent; current: boolean; collapsed: boolean }) {
  const agentId = useAgentId();
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, agent.instructions);
  const active = agent.activity?.activeSessions ?? 0;
  const channels = agent.activity?.runningChannels ?? 0;

  const status =
    active > 0
      ? `${active} active`
      : channels > 0
        ? `${channels} ${channels === 1 ? "channel" : "channels"} on`
        : "Idle";

  if (collapsed)
    return (
      <Link
        to="/a/$agentId"
        params={{ agentId }}
        aria-current={current ? "page" : undefined}
        aria-label={`${agent.name}, ${dirty ? "instructions draft, " : ""}${status}`}
        title={`${agent.name} · ${status}`}
        className="relative flex size-9 shrink-0 items-center justify-center rounded-control hover:bg-hover-2 aria-[current=page]:bg-hover-2"
      >
        <AgentAvatar agent={agent} />
        {active > 0 || dirty ? (
          <span
            aria-hidden
            className={cn(
              "absolute right-1 bottom-1 size-2 rounded-full ring-2 ring-canvas",
              active > 0 ? "bg-accent" : "bg-orange",
            )}
          />
        ) : null}
      </Link>
    );

  return (
    <div className="mx-2 mb-1 flex shrink-0 items-center">
      <Link
        to="/a/$agentId"
        params={{ agentId }}
        aria-current={current ? "page" : undefined}
        className={cn(
          "flex h-11 min-w-0 flex-1 items-center gap-2.5 rounded-control px-2 hover:bg-hover-2",
          current && "bg-hover-2",
        )}
      >
        <AgentAvatar agent={agent} />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-[14px] leading-tight font-semibold text-ink">{agent.name}</span>
          <span className="truncate text-[11.5px] leading-tight text-ink-3">{status}</span>
        </span>
        {dirty ? (
          <StatusPill tone="orange" className="h-5 shrink-0 px-2 text-[11px]">
            Draft
          </StatusPill>
        ) : null}
      </Link>
    </div>
  );
}
