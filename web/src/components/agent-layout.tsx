import type { AgentSettings as Settings } from "../lib/agent-scope";
import { AgentContext, useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import {
  getChatOptions,
  getMainChatOptions,
  listAgentsOptions,
  listChatsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { instructionsDirty } from "../lib/instructions";
import { sessionKind } from "../lib/sessions";
import { useApp } from "../lib/app-state";
import { MainTimeline } from "../routes/chat";
import { SlidersHorizontal } from "lucide-react";
import { SessionsPanel, SessionsButton } from "./agent/sessions-panel";
import { IconButton } from "./icon-button";
import { AgentAvatar } from "./agent-avatar";
import { Failure, Loading } from "./feedback";
import { Badge } from "./ui/badge";
import { ChatSettingsContext, InPaneContext, MainPane, PageHeader } from "./page";
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
  { to: "/a/$agentId/automations", label: "Routines", segments: ["automations"] },
  {
    to: "/a/$agentId/agent",
    label: "Settings",
    segments: ["agent", "instructions", "channels", "extensions"],
  },
];

const sessionsKey = "dex-sessions-panel";

/** Below md the side panel covers the page, so it starts closed and closes on navigation. */
const narrow = () => window.matchMedia("(max-width: 767px)").matches;

/** The Sessions sidebar starts closed. The choice stays in this browser. */
function useSessionsOpen() {
  const [open, setOpen] = useState(() => !narrow() && localStorage.getItem(sessionsKey) === "open");

  function set(next: boolean) {
    localStorage.setItem(sessionsKey, next ? "open" : "closed");
    setOpen(next);
  }

  return [open, set] as const;
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
    routineFailed: activity?.routineFailed ?? false,
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
        <p className="mt-2 text-muted-foreground">Dex has no bot with this ID.</p>
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
  // Chat tab, next to Main. Routine transcripts open under Routines.
  const chat = useQuery({
    ...getChatOptions({ path: { agentId, chatId: openChat } }),
    enabled: openChat > 0,
  });

  const chats = useQuery({ ...listChatsOptions({ path: { agentId } }) });
  const main = useQuery({ ...getMainChatOptions({ path: { agentId } }), enabled: openChat === 0 });
  const shown = openChat > 0 ? chat.data : main.data;
  // A dot on the button tells that the open chat does not simply follow the bot.
  const ownSettings = !!shown && !!(shown.cwd || shown.settings.modelId || shown.settings.thinkingLevel);

  const webChat = chat.data ? sessionKind(chat.data) === "Chat" : false;

  const current =
    webChat || segment === "tasks"
      ? tabs[0]
      : segment === "c"
        ? tabs[1]
        : tabs.find((tab) => tab.segments.includes(segment));

  const onChatTab = current === tabs[0];
  const [sessionsOpen, setSessionsOpen] = useSessionsOpen();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [panelPath, setPanelPath] = useState(path);

  if (panelPath !== path) {
    setPanelPath(path);

    if (narrow() && sessionsOpen) setSessionsOpen(false);
  }

  const { draft } = useApp();
  const dirty = instructionsDirty(draft, settings.agent.instructions);
  const activity = useAgentActivity();

  const counts = new Map([["Routines", activity.automations]]);

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
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background lg:border-l lg:border-border">
        <PageHeader
          title={settings.agent.name}
          // The other tabs open with their own page heading.
          heading={false}
          badge={
            dirty ? (
              <Badge
                variant="outline"
                className="hidden h-5 shrink-0 border-orange bg-orange-tint px-2 text-[11px] text-foreground sm:inline-flex"
              >
                Instructions draft
              </Badge>
            ) : null
          }
          avatar={
            <AgentAvatar
              agent={settings.agent}
              className="hidden size-5 rounded-control text-[11px] sm:flex"
            />
          }
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
                    "relative flex shrink-0 items-center gap-1 px-1.5 text-[12.5px] sm:gap-1.5 sm:px-2.5 whitespace-nowrap text-muted-foreground transition-colors hover:text-foreground",
                    "after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full",
                    "aria-[current=page]:font-medium aria-[current=page]:text-foreground aria-[current=page]:after:bg-foreground",
                  )}
                >
                  {tab.label}
                  {counts.get(tab.label) ? (
                    <span className="text-[11px] text-muted-foreground tabular-nums">
                      {counts.get(tab.label)}
                    </span>
                  ) : null}
                  {tab.label === "Settings" && dirty ? (
                    <span
                      role="img"
                      aria-label="Unsaved instructions draft"
                      className="size-1.5 rounded-full bg-orange"
                    />
                  ) : null}
                  {tab.label === "Routines" && activity.routineFailed ? (
                    <span
                      role="img"
                      aria-label="Latest routine run failed"
                      className="size-1.5 rounded-full bg-red"
                    />
                  ) : null}
                </Link>
              ))}
            </nav>
          }
          actions={
            onChatTab ? (
              <>
                {segment === "tasks" ? null : (
                  <IconButton
                    label={ownSettings ? "Chat settings, changed for this chat" : "Chat settings"}
                    aria-pressed={settingsOpen}
                    onClick={() => setSettingsOpen(!settingsOpen)}
                    className="relative"
                  >
                    <SlidersHorizontal size={16} />
                    {ownSettings ? (
                      <span
                        aria-hidden
                        className="absolute top-1.5 right-1.5 size-1.5 rounded-full bg-primary"
                      />
                    ) : null}
                  </IconButton>
                )}
                <SessionsButton
                  chats={chats.data?.chats ?? []}
                  open={sessionsOpen}
                  onClick={() => setSessionsOpen(!sessionsOpen)}
                />
              </>
            ) : null
          }
        />
        <div className="flex min-h-0 flex-1">
          <div className="flex min-w-0 flex-1 flex-col">
            <ChatSettingsContext.Provider value={settingsOpen}>
              <div inert={!onMain} className={cn("min-h-0 flex-1 flex-col", onMain ? "flex" : "hidden")}>
                <MainTimeline settings={settings} visible={onMain} />
              </div>
              {!onMain ? <Outlet /> : null}
            </ChatSettingsContext.Provider>
          </div>
          {onChatTab ? (
            <SessionsPanel chatId={openChat} open={sessionsOpen} onClose={() => setSessionsOpen(false)} />
          ) : null}
        </div>
      </section>
    </InPaneContext.Provider>
  );
}
