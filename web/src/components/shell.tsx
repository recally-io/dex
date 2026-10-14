import { useEffect, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Plus, MessageSquare, Send, Settings, Menu, Monitor, Sun, Moon } from "lucide-react";
import { listChatsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Chat } from "../api/generated/types.gen";
import { Button } from "./ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "./ui/sheet";
import { Failure } from "./feedback";
import { Skeleton } from "./ui/skeleton";
import { cn } from "../lib/utils";

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
  const [mobile, setMobile] = useState(false);
  const [theme, setTheme] = useState<Theme>(readTheme);
  const path = useRouterState({ select: (s) => s.location.pathname });
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

  const sidebar = (
    <Sidebar path={path} theme={theme} onTheme={setTheme} onNavigate={() => setMobile(false)} />
  );

  return (
    <div className="flex h-dvh bg-page">
      <aside className="hidden w-[252px] shrink-0 border-r border-line bg-canvas md:block">{sidebar}</aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-between border-b border-line px-4 md:hidden">
          <Button variant="ghost" size="icon" aria-label="Open sidebar" onClick={() => setMobile(true)}>
            <Menu className="size-5" />
          </Button>
          <span className="font-semibold tracking-tight">dex</span>
          <Link onClick={() => setMobile(false)} to="/" aria-label="New chat" className="p-2">
            <Plus className="size-5" />
          </Link>
        </header>
        <main className="min-h-0 flex-1 overflow-auto">
          <Outlet />
        </main>
      </div>
      <Sheet open={mobile} onOpenChange={setMobile}>
        <SheetContent side="left" className="w-[280px] bg-canvas p-0">
          <SheetHeader className="sr-only">
            <SheetTitle>Navigation</SheetTitle>
            <SheetDescription>Chats and settings</SheetDescription>
          </SheetHeader>
          {sidebar}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function Sidebar({
  path,
  theme,
  onTheme,
  onNavigate,
}: {
  path: string;
  theme: Theme;
  onTheme: (theme: Theme) => void;
  onNavigate: () => void;
}) {
  const chats = useQuery({
    ...listChatsOptions(),
    refetchInterval: 3000,
    refetchIntervalInBackground: false,
  });

  const groups = groupChats(chats.data?.chats ?? [], new Date(chats.dataUpdatedAt));

  return (
    <div className="flex h-full flex-col px-3">
      <div className="flex items-center gap-3 px-3 pb-6 pt-7">
        <span className="flex size-8 items-center justify-center rounded-lg bg-ink text-lg font-semibold text-page">
          d
        </span>
        <span className="text-lg font-semibold tracking-tight">dex</span>
        <span className="ml-auto rounded-md bg-surface px-1.5 py-0.5 text-[10px] font-medium text-ink-2">
          LOCAL
        </span>
      </div>
      <Link
        onClick={onNavigate}
        to="/"
        className="surface mx-1 mb-7 flex items-center gap-2.5 px-3 py-2.5 text-sm font-medium hover:bg-hover"
      >
        <Plus className="size-4" />
        New chat<span className="ml-auto text-xs text-ink-3">↵</span>
      </Link>
      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        {chats.isPending ? (
          <div className="flex flex-col gap-3 px-2">
            {[1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : chats.isError ? (
          <Failure title="Could not load chats" error={chats.error} retry={() => void chats.refetch()} />
        ) : !chats.data?.chats.length ? (
          <div className="px-4 py-5">
            <p className="text-sm text-ink-2">No chats yet</p>
            <p className="mt-2 text-xs leading-relaxed text-ink-3">Your conversations will appear here.</p>
          </div>
        ) : (
          (["Today", "Yesterday", "Previous 7 days", "Older"] as const).map((group) =>
            groups[group]?.length ? (
              <section key={group} className="mb-5">
                <h2 className="mb-1.5 px-3 text-[11px] font-medium text-ink-2">{group}</h2>
                {groups[group]?.map((chat) => (
                  <Link
                    onClick={onNavigate}
                    key={chat.id}
                    to="/c/$chatId"
                    params={{ chatId: String(chat.id) }}
                    title={chat.title}
                    className={cn(
                      "flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-[13px] text-ink-2 hover:bg-hover",
                      path === `/c/${chat.id}` && "bg-surface font-medium text-ink shadow-xs",
                    )}
                  >
                    {chat.channel === "telegram" ? (
                      <Send className="size-3.5 shrink-0" />
                    ) : (
                      <MessageSquare className="size-3.5 shrink-0" />
                    )}
                    <span className="truncate">{chat.title}</span>
                    {chat.busy ? <span className="busy-dot ml-auto shrink-0" aria-label="Running" /> : null}
                  </Link>
                ))}
              </section>
            ) : null,
          )
        )}
      </div>
      <div className="flex flex-col gap-2 border-t border-line py-4">
        <Link
          onClick={onNavigate}
          to="/settings/$section"
          params={{ section: "providers" }}
          className={cn(
            "flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-sm text-ink-2 hover:bg-hover",
            path.startsWith("/settings") && "bg-hover text-ink",
          )}
        >
          <Settings className="size-4" />
          Settings
        </Link>
        <div className="mx-2 flex items-center rounded-lg bg-inset p-1" role="group" aria-label="Theme">
          {(
            [
              { id: "system", icon: Monitor },
              { id: "light", icon: Sun },
              { id: "dark", icon: Moon },
            ] as const
          ).map(({ id, icon: Icon }) => (
            <button
              key={id}
              aria-label={`${id} theme`}
              aria-pressed={theme === id}
              title={`${id} theme`}
              className={cn(
                "flex flex-1 justify-center rounded-md py-2 text-ink-2 hover:text-ink",
                theme === id && "bg-surface text-ink shadow-xs",
              )}
              onClick={() => onTheme(id)}
            >
              <Icon className="size-3.5" />
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
