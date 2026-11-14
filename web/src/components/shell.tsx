import { AgentContext } from "../lib/agent-scope";
import { botStatus, workOptions } from "../lib/work";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog, DialogClose, DialogContent } from "./ui/dialog";
import { PanelLeft, X } from "lucide-react";

import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { ShellContext } from "./page";
import { Sidebar } from "./sidebar";
import { SearchDialog } from "./search-dialog";
import { useSystemTheme } from "./theme";

const sidebarKey = "dex-sidebar";

/**
 * The sidebar starts open. The choice stays in this browser. The button that hides the sidebar
 * goes with it, so focus moves to the button that shows it again, and back.
 */
function useSidebarOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(sidebarKey) !== "collapsed");

  // Stable, so the shell context changes only when the sidebar opens or closes.
  const toggle = useCallback(
    () =>
      setOpen((was) => {
        localStorage.setItem(sidebarKey, was ? "collapsed" : "open");
        requestAnimationFrame(() =>
          document
            .querySelector<HTMLElement>(
              was ? 'button[aria-label^="Show sidebar"]' : 'button[aria-label="Hide sidebar"]',
            )
            ?.focus(),
        );

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
  const [searchOpen, setSearchOpen] = useState(false);

  const changeSearch = useCallback((open: boolean) => {
    if (open) setMenu(false);
    setSearchOpen(open);
  }, []);

  const [sidebarOpen, toggleSidebar] = useSidebarOpen();
  const location = useRouterState({ select: (s) => s.location });
  const path = location.pathname;
  const shown = useRef(path);
  // The drawer closes when the page changes.
  const [menuPath, setMenuPath] = useState(path);
  const [lastAgent, setLastAgent] = useState("default");
  const routeAgent = path.match(/^\/a\/([^/]+)/)?.[1];
  const current = routeAgent ?? lastAgent;
  // The drawer button shows when another bot needs you. The current bot shows its own on its page.
  const attention = [...statuses].some(([id, status]) => id !== current && status === "needs");
  // A hidden sidebar hides every bot, so its button shows when any bot needs you.
  const anyNeeds = [...statuses.values()].includes("needs");

  const shell = useMemo(
    () => ({
      openDrawer: () => setMenu(true),
      attention,
      sidebar: { open: sidebarOpen, toggle: toggleSidebar, attention: anyNeeds },
    }),
    [attention, anyNeeds, sidebarOpen, toggleSidebar],
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

    // A redirected page can load its heading after the first frame. Stop waiting if the user
    // starts navigating, so a late heading cannot take focus away from their next action.
    const observer = new MutationObserver(() => focusTitle());

    const stop = () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      document.removeEventListener("focusin", stop);
      document.removeEventListener("pointerdown", stop);
      document.removeEventListener("keydown", stop);
    };

    const focusTitle = () => {
      const title = document.querySelector<HTMLElement>('#main [id="page-title"]:not([inert] *)');

      if (!title) return;
      stop();
      title.focus();
    };

    document.addEventListener("focusin", stop);
    document.addEventListener("pointerdown", stop);
    document.addEventListener("keydown", stop);

    const frame = requestAnimationFrame(() => {
      observer.observe(document.getElementById("main")!, { childList: true, subtree: true });
      focusTitle();
    });

    return stop;
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
        {sidebarOpen ? (
          <Sidebar
            path={path}
            statuses={statuses}
            className="hidden lg:flex"
            onSearch={() => changeSearch(true)}
            lead={
              <button
                type="button"
                aria-label="Hide sidebar"
                title="Hide sidebar"
                onClick={toggleSidebar}
                className="flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <PanelLeft size={16} />
              </button>
            }
          />
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
          className="fixed inset-y-0 left-0 top-0 z-50 flex h-full w-auto max-w-[85vw] translate-x-0 translate-y-0 animate-[drawer-in_240ms_var(--ease-out-strong)_both] flex-row gap-0 rounded-none bg-sidebar p-0 shadow-raised outline-none lg:hidden"
        >
          <Sidebar
            path={path}
            statuses={statuses}
            className="max-w-[85vw]"
            onSearch={() => changeSearch(true)}
            lead={
              <DialogClose
                aria-label="Close navigation"
                className="flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <X size={17} />
              </DialogClose>
            }
          />
        </DialogContent>
      </Dialog>
      <SearchDialog open={searchOpen} onOpenChange={changeSearch} />
    </ShellContext.Provider>
  );
}
