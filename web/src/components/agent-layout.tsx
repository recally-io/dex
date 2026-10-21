import type { AgentSettings as Settings } from "../lib/agent-scope";
import { AgentContext, useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import { getChatOptions, listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { instructionsDirty } from "../lib/instructions";
import { sessionKind } from "../lib/tasks";
import { useApp } from "../lib/app-state";
import { MainTimeline } from "../routes/chat";
import { MessageCirclePlus, PanelRight } from "lucide-react";
import { ChatList, useNewChat } from "./chat-list";
import { IconButton } from "./icon-button";
import { AgentAvatar } from "./agent-avatar";
import { Failure, Loading } from "./feedback";
import { InPaneContext, MainPane, PageHeader } from "./page";
import { cn } from "../lib/utils";

/** The first path segment of each agent tab, with the segments of the pages that open from it. */
type AgentTab = {
  to:
    | "/a/$agentId"
    | "/a/$agentId/instructions"
    | "/a/$agentId/channels"
    | "/a/$agentId/automations"
    | "/a/$agentId/sessions"
    | "/a/$agentId/extensions"
    | "/a/$agentId/agent";
  label: string;
  segments: (string | undefined)[];
};

const tabs: AgentTab[] = [
  { to: "/a/$agentId", label: "Chat", segments: [undefined] },
  { to: "/a/$agentId/sessions", label: "Sessions", segments: ["sessions", "c", "tasks"] },
  { to: "/a/$agentId/automations", label: "Automations", segments: ["automations"] },
  { to: "/a/$agentId/extensions", label: "Extensions", segments: ["extensions"] },
  { to: "/a/$agentId/agent", label: "Settings", segments: ["agent", "instructions", "channels"] },
];

const chatsKey = "dex-chats";

/** The right Chats sidebar starts closed. The choice stays in this browser. */
function useChatsOpen() {
  const [open, setOpen] = useState(() => localStorage.getItem(chatsKey) === "open");

  function toggle() {
    localStorage.setItem(chatsKey, open ? "closed" : "open");
    setOpen(!open);
  }

  return [open, toggle] as const;
}

/** Activity reads the bot list cache. Shell owns its polling interval. */
export function useAgentActivity() {
  const agentId = useAgentId();
  const agents = useQuery(listAgentsOptions());
  const activity = agents.data?.agents.find((agent) => agent.id === agentId)?.activity;

  return {
    channels: activity?.runningChannels ?? 0,
    automations: activity?.enabledAutomations ?? 0,
    active: activity?.activeSessions ?? 0,
  };
}

/**
 * The page of one bot: its name, a tab for each part of it, and its model. Everything that
 * belongs to the bot is a tab here, so the sidebar only holds the list of bots and the global
 * settings. The main timeline stays mounted under the other tabs, so its stream and the
 * unsent message survive a visit to them.
 */
export function AgentLayout() {
  const { agentId = "default" } = useParams({ strict: false });

  return (
    <AgentContext.Provider key={agentId} value={agentId}>
      <AgentView />
    </AgentContext.Provider>
  );
}

function AgentView() {
  const agentId = useAgentId();
  const agents = useQuery(listAgentsOptions());
  const missing = agents.data && !agents.data.agents.some((agent) => agent.id === agentId);
  const settings = useAgentSettings(agentId);

  return missing ? (
    <MainPane>
      <div className="p-8">
        <h1 id="page-title" className="text-xl font-semibold">
          Bot not found
        </h1>
        <p className="mt-2 text-ink-2">Dex has no bot with this ID.</p>
        <Link to="/" className="mt-4 block underline">
          Open Dex
        </Link>
      </div>
    </MainPane>
  ) : settings.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : settings.isError ? (
    <MainPane>
      <div className="mx-auto max-w-[720px] px-4 py-10 sm:px-8">
        <Failure
          title="Could not load settings"
          error={settings.error}
          retry={() => void settings.refetch()}
        />
      </div>
    </MainPane>
  ) : (
    <AgentPane settings={settings.data} />
  );
}

function AgentPane({ settings }: { settings: Settings }) {
  const agentId = useAgentId();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [, , segment, chatSegment] = path.split("/").filter(Boolean);
  const onMain = segment === undefined;
  const openChat = segment === "c" ? Number(chatSegment) : 0;

  // The chat page reads the same query, so this adds no request. A writable Chat belongs to the
  // Chat tab, next to Main; task, automation and channel records stay under Sessions.
  const chat = useQuery({
    ...getChatOptions({ path: { agentId, chatId: openChat } }),
    enabled: openChat > 0,
  });

  const webChat = chat.data ? sessionKind(chat.data) === "Chat" : false;
  const current = webChat ? tabs[0] : tabs.find((tab) => tab.segments.includes(segment));
  const onChatTab = current === tabs[0];
  const [chatsOpen, toggleChats] = useChatsOpen();
  const newChat = useNewChat();
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, settings.agent.instructions);
  const activity = useAgentActivity();

  const counts = new Map([["Automations", activity.automations]]);

  const nav = useRef<HTMLElement>(null);

  const label = current?.label;

  // Counts and viewport changes can move the active tab after the first render.
  useEffect(() => {
    const element = nav.current;

    if (!element) return;

    const center = () =>
      element.querySelector(`[data-tab="${label}"]`)?.scrollIntoView({ block: "nearest", inline: "center" });

    const observer = new ResizeObserver(center);
    observer.observe(element);

    for (const tab of element.children) observer.observe(tab);
    center();

    return () => observer.disconnect();
  }, [label]);

  return (
    <InPaneContext.Provider value>
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-page lg:border-l lg:border-line">
        <PageHeader
          title={settings.agent.name}
          avatar={<AgentAvatar agent={settings.agent} className="size-5 rounded-[6px] text-[11px]" />}
          tabs={
            <nav
              ref={nav}
              aria-label={`${settings.agent.name} sections`}
              className="-mb-px flex min-w-0 flex-1 gap-0.5 self-stretch overflow-x-auto [scrollbar-width:none]"
            >
              {tabs.map((tab) => (
                <Link
                  key={tab.label}
                  activeOptions={{ exact: true }}
                  to={tab.to}
                  params={{ agentId }}
                  data-tab={tab.label}
                  aria-current={tab === current ? "page" : undefined}
                  className={cn(
                    "relative flex shrink-0 items-center gap-1.5 px-2.5 text-[12.5px] whitespace-nowrap text-ink-2 transition-colors hover:text-ink",
                    "after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full",
                    "aria-[current=page]:font-medium aria-[current=page]:text-ink aria-[current=page]:after:bg-ink",
                  )}
                >
                  {tab.label}
                  {counts.get(tab.label) ? (
                    <span className="text-[11px] text-ink-3 tabular-nums">{counts.get(tab.label)}</span>
                  ) : null}
                  {tab.label === "Settings" && dirty ? (
                    <span
                      role="img"
                      aria-label="Unsaved instructions draft"
                      className="size-1.5 rounded-full bg-orange"
                    />
                  ) : null}
                  {tab.label === "Sessions" && activity.active > 0 ? (
                    <span
                      role="img"
                      aria-label={`${activity.active} active`}
                      className="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-dex-accent-tint px-1.5 text-[11px] font-medium text-dex-accent-ink tabular-nums"
                    >
                      {activity.active}
                    </span>
                  ) : null}
                </Link>
              ))}
            </nav>
          }
          actions={
            onChatTab ? (
              <>
                <IconButton
                  label="New chat"
                  disabled={newChat.isPending}
                  onClick={() => newChat.mutate({ path: { agentId } })}
                >
                  <MessageCirclePlus size={17} />
                </IconButton>
                <IconButton
                  label="Chats"
                  aria-pressed={chatsOpen}
                  onClick={toggleChats}
                  className="hidden md:flex"
                >
                  <PanelRight size={17} />
                </IconButton>
              </>
            ) : null
          }
        />
        <div className="flex min-h-0 flex-1">
          <div className="flex min-w-0 flex-1 flex-col">
            <div inert={!onMain} className={cn("min-h-0 flex-1 flex-col", onMain ? "flex" : "hidden")}>
              <MainTimeline settings={settings} />
            </div>
            {!onMain ? <Outlet /> : null}
          </div>
          {onChatTab && chatsOpen ? <ChatList chatId={openChat} /> : null}
        </div>
      </section>
    </InPaneContext.Provider>
  );
}
