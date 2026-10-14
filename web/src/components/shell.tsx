import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, Outlet, useRouterState, type LinkProps } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import {
  Bot,
  Boxes,
  ChevronDown,
  Clock,
  Info,
  KeyRound,
  Menu,
  MessageSquare,
  Monitor,
  Moon,
  Plus,
  Radio,
  Send,
  Server,
  Sun,
} from "lucide-react";
import { getSettingsOptions, listChatsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Chat } from "../api/generated/types.gen";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "./ui/sheet";
import GlideMenu from "./primitives/GlideMenu";
import { Failure } from "./feedback";
import { Skeleton } from "./ui/skeleton";
import { cn } from "../lib/utils";
import { unreadAutomationChats } from "../lib/automations";
import { useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";

type Theme = "system" | "light" | "dark";

function readTheme(): Theme {
  const saved = localStorage.getItem("dex-theme");

  return saved === "dark" || saved === "light" ? saved : "system";
}

function groupChats(chats: Chat[], now: Date) {
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1).getTime();
  const week = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 7).getTime();

  return Object.groupBy(
    chats.toSorted((a, b) => b.updatedAt - a.updatedAt),
    (chat) =>
      chat.updatedAt >= start
        ? "Today"
        : chat.updatedAt >= yesterday
          ? "Yesterday"
          : chat.updatedAt >= week
            ? "Previous 7 days"
            : "Older",
  );
}

export function Shell() {
  const [menu, setMenu] = useState(false);
  const [theme, setTheme] = useState<Theme>(readTheme);
  const location = useRouterState({ select: (s) => s.location });
  const path = location.pathname;
  const shown = useRef(path);
  // The drawer closes when the page changes.
  const [menuPath, setMenuPath] = useState(path);

  if (menuPath !== path) {
    setMenuPath(path);
    setMenu(false);
  }

  useEffect(() => {
    const preference = matchMedia("(prefers-color-scheme: dark)");

    function apply() {
      document.documentElement.classList.toggle(
        "dark",
        theme === "dark" || (theme === "system" && preference.matches),
      );
    }

    apply();
    preference.addEventListener("change", apply);
    localStorage.setItem("dex-theme", theme);

    return () => preference.removeEventListener("change", apply);
  }, [theme]);

  // After navigation, focus moves to the page title, so keyboard and screen reader users land on
  // the new page. A new chat keeps the focus in its composer.
  const firstMessage = !!location.state.firstMessage;
  useEffect(() => {
    // The first page load keeps the browser's focus.
    if (shown.current === path) return;
    shown.current = path;

    if (firstMessage) return;
    const frame = requestAnimationFrame(() => document.getElementById("page-title")?.focus());

    return () => cancelAnimationFrame(frame);
  }, [path, firstMessage]);

  return (
    <div className="flex h-dvh bg-page text-ink">
      <a
        href="#main"
        onClick={(e) => {
          e.preventDefault();
          document.getElementById("main")?.focus();
        }}
        className="sr-only z-50 rounded bg-ink px-3 py-2 text-canvas focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <aside className="hidden w-[272px] shrink-0 border-r border-line bg-canvas lg:block">
        <Rail path={path} theme={theme} onTheme={setTheme} />
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-14 shrink-0 items-center gap-2 border-b border-line bg-canvas/95 px-3 lg:hidden">
          <button
            type="button"
            aria-label="Open navigation"
            onClick={() => setMenu(true)}
            className="rounded-lg p-2 hover:bg-hover focus-visible:outline-2 focus-visible:outline-ink"
          >
            <Menu className="size-5" />
          </button>
          <span className="flex-1 text-sm font-semibold">dex</span>
          <Link
            to="/"
            className="inline-flex h-7 items-center gap-1 rounded-full bg-ink px-2.5 text-[12px] text-canvas focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
          >
            <Plus aria-hidden className="size-3.5" /> New chat
          </Link>
        </div>
        <main id="main" tabIndex={-1} className="min-h-0 flex-1 overflow-auto outline-none">
          <Outlet />
        </main>
      </div>
      <Sheet open={menu} onOpenChange={setMenu}>
        <SheetContent side="left" className="w-[300px] gap-0 bg-canvas p-0">
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <SheetDescription className="sr-only">Chats, automations and configuration</SheetDescription>
          <Rail path={path} theme={theme} onTheme={setTheme} />
        </SheetContent>
      </Sheet>
    </div>
  );
}

function NavRow({
  to,
  params,
  current,
  icon,
  children,
  badge,
  title,
}: {
  to: LinkProps["to"];
  params?: LinkProps["params"];
  current: boolean;
  icon: ReactNode;
  children: ReactNode;
  badge?: ReactNode;
  title?: string;
}) {
  return (
    <Link
      to={to}
      params={params}
      title={title}
      data-menu-row
      aria-current={current ? "page" : undefined}
      className={cn(
        "relative z-10 flex h-8 min-w-0 items-center gap-2.5 rounded-[8px] px-2.5 text-[13px] focus-visible:outline-2 focus-visible:outline-ink",
        current ? "bg-inset font-medium text-ink" : "text-ink-2 hover:text-ink",
      )}
    >
      <span className="flex size-4 shrink-0 items-center justify-center text-ink-3">{icon}</span>
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {badge}
    </Link>
  );
}

function Badge({
  tone,
  children,
  label,
}: {
  tone: "orange" | "red" | "brand";
  children: ReactNode;
  label: string;
}) {
  return (
    <span
      role="img"
      aria-label={label}
      className={cn(
        "rounded-full px-1.5 text-[10.5px] leading-4 font-semibold text-white",
        tone === "red" ? "bg-red" : tone === "orange" ? "bg-orange" : "bg-brand",
      )}
    >
      {children}
    </span>
  );
}

const origins = {
  web: { icon: <MessageSquare aria-hidden className="size-3.5" />, name: "Web" },
  telegram: { icon: <Send aria-hidden className="size-3.5" />, name: "Telegram" },
  automation: { icon: <Clock aria-hidden className="size-3.5" />, name: "Automation" },
};

function Rail({ path, theme, onTheme }: { path: string; theme: Theme; onTheme: (theme: Theme) => void }) {
  const chats = useQuery({
    ...listChatsOptions(),
    refetchInterval: 3000,
    refetchIntervalInBackground: false,
  });

  const settings = useQuery(getSettingsOptions());
  const model = useAgentModel(settings.data);
  const { draft } = useApp();
  const [runsOpen, setRunsOpen] = useState(false);
  const [a] = path.split("/").filter(Boolean);
  const allChats = chats.data?.chats ?? [];

  const groups = groupChats(
    allChats.filter((chat) => chat.channel !== "automation"),
    new Date(chats.dataUpdatedAt),
  );

  const runs = allChats
    .filter((chat) => chat.channel === "automation")
    .toSorted((x, y) => y.updatedAt - x.updatedAt);

  const seen = Number(localStorage.getItem("dex-automations-seen")) || 0;
  const activeRun = runs.some((chat) => path === `/c/${chat.id}`);
  const open = runsOpen || activeRun;
  const unread = unreadAutomationChats(runs, seen);
  // The runs that start while the group is open are seen too.
  useEffect(() => {
    if (!open) return;
    const markSeen = () => localStorage.setItem("dex-automations-seen", String(Date.now()));
    markSeen();

    return markSeen;
  }, [open]);

  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const dirty = draft !== null && draft !== settings.data?.agent.instructions;

  const chatRow = (chat: Chat) => (
    <li key={chat.id}>
      <NavRow
        to="/c/$chatId"
        params={{ chatId: String(chat.id) }}
        current={path === `/c/${chat.id}`}
        icon={origins[chat.channel].icon}
        title={chat.title}
        badge={
          chat.busy ? (
            <span className="size-2 shrink-0 animate-pulse rounded-full bg-orange" aria-label="Running" />
          ) : null
        }
      >
        <span className="sr-only">{origins[chat.channel].name}: </span>
        {chat.title}
      </NavRow>
    </li>
  );

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between px-4 pt-4 pb-3">
        <Link
          to="/"
          className="flex items-center gap-2 rounded-md text-[15px] font-semibold focus-visible:outline-2 focus-visible:outline-ink"
        >
          <span className="flex size-7 items-center justify-center rounded-lg bg-ink text-sm text-page">
            d
          </span>
          dex
        </Link>
      </div>
      <div className="px-3 pb-2">
        <Link
          to="/"
          className="inline-flex h-[27px] w-full items-center justify-center gap-1.5 rounded-full bg-ink px-3 text-[13px] font-medium text-canvas hover:opacity-90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
        >
          <Plus aria-hidden className="size-3.5" /> New chat
        </Link>
      </div>
      <nav aria-label="Main" className="flex min-h-0 flex-1 flex-col">
        <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
          <GlideMenu>
            <NavRow
              to="/automations"
              current={a === "automations"}
              icon={<Clock className="size-4" />}
              badge={
                unread && !open ? (
                  <Badge tone="brand" label={`${unread} unread runs`}>
                    {unread}
                  </Badge>
                ) : null
              }
            >
              Automations
            </NavRow>
          </GlideMenu>
          {chats.isPending ? (
            <div className="mt-4 flex flex-col gap-2 px-2.5" role="status" aria-label="Loading chats">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-6 w-full" />
              ))}
            </div>
          ) : chats.isError ? (
            <div className="mt-4">
              <Failure title="Could not load chats" error={chats.error} retry={() => void chats.refetch()} />
            </div>
          ) : !allChats.length ? (
            <p className="px-2.5 py-3 text-xs text-ink-3">No chats yet.</p>
          ) : (
            <>
              {(["Today", "Yesterday", "Previous 7 days", "Older"] as const).map((group) =>
                groups[group]?.length ? (
                  <div key={group} className="mt-4">
                    <h2 className="px-2.5 pb-1 text-[11px] font-medium text-ink-3">{group}</h2>
                    <GlideMenu>
                      <ul>{groups[group]?.map(chatRow)}</ul>
                    </GlideMenu>
                  </div>
                ) : null,
              )}
              {runs.length ? (
                <div className="mt-4">
                  <button
                    type="button"
                    aria-expanded={open}
                    aria-controls="rail-runs"
                    onClick={() => setRunsOpen(!open)}
                    className="flex w-full items-center gap-1.5 rounded-md px-2.5 pb-1 text-[11px] font-medium text-ink-3 hover:text-ink focus-visible:outline-2 focus-visible:outline-ink"
                  >
                    <ChevronDown
                      aria-hidden
                      className={cn("size-3 transition-transform", open ? "" : "-rotate-90")}
                    />
                    Automation runs ({runs.length})
                    {unread && !open ? (
                      <Badge tone="brand" label={`${unread} unread`}>
                        {unread}
                      </Badge>
                    ) : null}
                  </button>
                  <div id="rail-runs" hidden={!open}>
                    <GlideMenu>
                      <ul>{runs.map(chatRow)}</ul>
                    </GlideMenu>
                  </div>
                </div>
              ) : null}
            </>
          )}
        </div>
        <div className="border-t border-line px-3 py-3">
          <h2 className="px-2.5 pb-1 text-[11px] font-medium text-ink-3">Configure</h2>
          <GlideMenu>
            <NavRow
              to="/agent"
              current={a === "agent"}
              icon={<Bot className="size-4" />}
              badge={
                dirty ? (
                  <span className="text-[10.5px] text-ink-3">Draft</span>
                ) : model && !model.ok ? (
                  <Badge tone="orange" label="No available model">
                    !
                  </Badge>
                ) : null
              }
            >
              Agent{" "}
              {model ? (
                <span className={cn("text-xs", model.ok ? "text-ink-3" : "text-orange")}>
                  · {model.label}
                </span>
              ) : null}
            </NavRow>
            <NavRow
              to="/providers"
              current={a === "providers"}
              icon={<Server className="size-4" />}
              badge={
                providerErrors ? (
                  <Badge tone="red" label={`${providerErrors} providers need attention`}>
                    {providerErrors}
                  </Badge>
                ) : null
              }
            >
              Providers
            </NavRow>
            <NavRow to="/channels" current={a === "channels"} icon={<Radio className="size-4" />}>
              Channels
            </NavRow>
            <NavRow to="/extensions" current={a === "extensions"} icon={<Boxes className="size-4" />}>
              Extensions
            </NavRow>
            <NavRow to="/secrets" current={a === "secrets"} icon={<KeyRound className="size-4" />}>
              Secrets
            </NavRow>
          </GlideMenu>
        </div>
        <div className="flex items-center gap-1 border-t border-line px-3 py-2">
          <Link
            to="/about"
            aria-current={a === "about" ? "page" : undefined}
            className={cn(
              "flex h-8 flex-1 items-center gap-2 rounded-lg px-2.5 text-[13px] text-ink-2 hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-ink",
              a === "about" && "bg-inset font-medium text-ink",
            )}
          >
            <Info aria-hidden className="size-4 text-ink-3" /> About
          </Link>
          <div className="flex items-center rounded-lg bg-inset p-0.5" role="group" aria-label="Theme">
            {(
              [
                { id: "system", icon: Monitor },
                { id: "light", icon: Sun },
                { id: "dark", icon: Moon },
              ] as const
            ).map(({ id, icon: Icon }) => (
              <button
                key={id}
                type="button"
                aria-label={`${id[0].toUpperCase()}${id.slice(1)} theme`}
                aria-pressed={theme === id}
                title={`${id[0].toUpperCase()}${id.slice(1)} theme`}
                className={cn(
                  "flex rounded-md p-1.5 text-ink-2 hover:text-ink focus-visible:outline-2 focus-visible:outline-ink",
                  theme === id && "bg-surface text-ink shadow-xs",
                )}
                onClick={() => onTheme(id)}
              >
                <Icon className="size-3.5" />
              </button>
            ))}
          </div>
        </div>
      </nav>
    </div>
  );
}
