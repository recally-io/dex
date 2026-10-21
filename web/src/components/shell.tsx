import { AgentContext, useAgentId } from "../lib/agent-scope";
import { AgentAvatar } from "./agent-avatar";
import { AgentCreateDialog } from "./agent-create-dialog";
import type { Agent } from "../api/generated/types.gen";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog } from "@base-ui/react/dialog";
import { IconChevronRightSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronRightSmall";
import { IconSettingsGear2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconSettingsGear2";
import { IconCircleInfo } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCircleInfo";
import { IconCloudApi } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCloudApi";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconKey2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconKey2";
import { IconMagnifyingGlass } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconMagnifyingGlass";

import { getSettingsOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { StatusPill } from "./atoms/StatusPill";
import { Modal } from "./dialog";
import { ShellContext } from "./page";
import { ThemeToggle, useSystemTheme } from "./theme";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { cn } from "../lib/utils";

export function Shell() {
  useQuery({ ...listAgentsOptions(), refetchInterval: 3000 });
  const [menu, setMenu] = useState(false);
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
        <Sidebar path={path} className="hidden pt-0.5 lg:flex" />
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

function Dot({ tone, label }: { tone: "orange" | "red"; label: string }) {
  return (
    <span
      role="img"
      aria-label={label}
      className={cn("size-1.5 rounded-full", tone === "red" ? "bg-red" : "bg-orange")}
    />
  );
}

const globalPages = [
  { to: "/providers", label: "Providers", icon: <IconCloudApi size={16} /> },
  { to: "/secrets", label: "Secrets", icon: <IconKey2 size={16} /> },
  { to: "/about", label: "About", icon: <IconCircleInfo size={16} /> },
] as const;

/**
 * The list of bots. Each one is a long-lived role the user talks to, so the list holds the
 * bots themselves, not their sessions or settings. Global pages sit behind one footer entry.
 */
function Sidebar({ path, className }: { path: string; className?: string }) {
  const settings = useQuery(getSettingsOptions());
  const agents = useQuery(listAgentsOptions());
  const [creating, setCreating] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [a, selected] = path.split("/").filter(Boolean);
  const agentId = a === "a" ? selected : "default";
  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;

  const list = agents.data?.agents ?? [];

  const shown = query
    ? list.filter((agent) => agent.name.toLowerCase().includes(query.trim().toLowerCase()))
    : list;

  return (
    <aside aria-label="Bots" className={cn("flex h-full w-[240px] shrink-0 flex-col", className)}>
      <div className="mx-2 mt-1 flex h-8 items-center justify-between px-2 text-[12.5px] font-medium text-ink-3">
        <span>Bots</span>
        <button
          type="button"
          aria-label="Create bot"
          onClick={() => setCreating(true)}
          className="flex size-7 items-center justify-center rounded-control text-lg hover:bg-hover-2 hover:text-ink"
        >
          +
        </button>
      </div>
      {list.length > 4 ? (
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
      <div className="min-h-0 overflow-y-auto">
        {shown.map((agent) => (
          <AgentContext.Provider key={agent.id} value={agent.id!}>
            <AgentRow agent={agent} current={a === "a" && agent.id === selected} />
          </AgentContext.Provider>
        ))}
        {list.length && !shown.length ? (
          <p className="px-4 py-2 text-[12.5px] text-ink-3">No bot matches.</p>
        ) : null}
        {agents.isError ? (
          <p role="alert" className="px-4 text-xs text-red">
            Could not load bots.
          </p>
        ) : null}
      </div>
      {creating ? (
        <AgentContext.Provider value={agentId}>
          <AgentCreateDialog onClose={() => setCreating(false)} />
        </AgentContext.Provider>
      ) : null}
      <div className="flex-1" />

      <div className="mt-2 shrink-0 border-t border-line px-2 py-2">
        <button
          type="button"
          aria-haspopup="dialog"
          onClick={() => setSettingsOpen(true)}
          className="flex h-8 w-full items-center gap-1.5 rounded-control px-2 text-[13px] text-ink-2 hover:bg-hover-2 hover:text-ink"
        >
          <span className="flex size-5 shrink-0 items-center justify-center text-ink-3">
            <IconSettingsGear2 size={15} />
          </span>
          <span className="min-w-0 flex-1 truncate text-left">Global settings</span>
          {providerErrors ? (
            <Dot
              tone="red"
              label={`${providerErrors} ${providerErrors === 1 ? "provider needs" : "providers need"} attention`}
            />
          ) : null}
          <IconChevronRightSmall size={13} className="text-ink-3" />
        </button>
        <div className="mt-1 flex items-center px-1 pt-1.5">
          <ThemeToggle />
        </div>
      </div>
      {settingsOpen ? <GlobalSettingsDialog path={path} onClose={() => setSettingsOpen(false)} /> : null}
    </aside>
  );
}

/** The pages that belong to the whole installation rather than to one agent. */
function GlobalSettingsDialog({ path, onClose }: { path: string; onClose: () => void }) {
  return (
    <Modal
      title="Global settings"
      description="These apply to dex on this computer, not to one bot."
      onClose={onClose}
      footer={
        <button
          type="button"
          onClick={onClose}
          className="h-8 rounded-control bg-hover-2 px-3 text-[13px] font-medium text-ink"
        >
          Close
        </button>
      }
    >
      <div className="flex flex-col">
        {globalPages.map((page) => (
          <Link
            key={page.to}
            to={page.to}
            onClick={onClose}
            aria-current={path === page.to ? "page" : undefined}
            className="flex h-10 items-center gap-2.5 rounded-control px-2 text-[13.5px] text-ink-2 hover:bg-hover-2 hover:text-ink aria-[current=page]:text-ink"
          >
            <span className="flex size-5 shrink-0 items-center justify-center text-ink-3">{page.icon}</span>
            {page.label}
          </Link>
        ))}
      </div>
    </Modal>
  );
}

function AgentRow({ agent, current }: { agent: Agent; current: boolean }) {
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
