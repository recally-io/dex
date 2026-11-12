import type { AgentSettings as Settings } from "../lib/agent-scope";
import { AgentContext, useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import type { Chat } from "../api/generated/types.gen";
import { useQuery } from "@tanstack/react-query";

import {
  getChatOptions,
  getMainChatOptions,
  listAgentsOptions,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { findModel, thinkingNames } from "../lib/agent";
import { sessionKind } from "../lib/sessions";
import { ChatTitle, MainTimeline } from "../routes/chat";
import { ChevronDown, SlidersHorizontal } from "lucide-react";
import { AgentAvatar } from "./agent-avatar";
import { Failure, Loading } from "./feedback";
import { ChatSettingsContext, InPaneContext, MainPane, MenuButton } from "./page";
import { cn } from "../lib/utils";

/** The name of each page under a bot that is not a chat. */
const pageNames = new Map([
  ["automations", "Routines"],
  ["agent", "Settings"],
  ["instructions", "Settings"],
  ["channels", "Settings"],
  ["extensions", "Settings"],
  ["sessions", "All sessions"],
]);

/**
 * The page of one bot: a header that names the open page, and the page. The bot column holds the
 * way to each page. The main timeline stays mounted under the other pages, so its stream and the
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

  // The chat page reads the same query, so this adds no request.
  const chat = useQuery({
    ...getChatOptions({ path: { agentId, chatId: openChat } }),
    enabled: openChat > 0,
  });

  const main = useQuery({ ...getMainChatOptions({ path: { agentId } }), enabled: openChat === 0 });
  const shown = onMain ? main.data : openChat > 0 ? chat.data : undefined;
  // Main and a writable Chat have settings of their own. A routine transcript does not.
  const writable = shown ? ["Main", "Chat"].includes(sessionKind(shown)) : false;
  const [settingsOpen, setSettingsOpen] = useState(false);

  return (
    <InPaneContext.Provider value>
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
        {/* The bot column names the bot and holds the way to its pages, so the header names only
         * the open page. Below lg the column sits in the drawer, so the avatar still names the bot. */}
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border pr-2 pl-3 sm:pl-4">
          <MenuButton />
          <AgentAvatar agent={settings.agent} className="size-5 rounded-control text-[11px] lg:hidden" />
          {shown ? (
            <div className="flex min-w-0 flex-1 items-center gap-2">
              <ChatTitle chat={shown} heading />
            </div>
          ) : (
            <span className="min-w-0 flex-1 truncate text-[14px] font-semibold tracking-[-0.01em]">
              {pageNames.get(segment ?? "") ?? ""}
            </span>
          )}
          {shown && writable ? (
            <ChatSettingsButton
              chat={shown}
              settings={settings}
              open={settingsOpen}
              onClick={() => setSettingsOpen(!settingsOpen)}
            />
          ) : null}
        </header>
        <ChatSettingsContext.Provider value={settingsOpen}>
          <div inert={!onMain} className={cn("min-h-0 flex-1 flex-col", onMain ? "flex" : "hidden")}>
            <MainTimeline settings={settings} />
          </div>
          {!onMain ? <Outlet /> : null}
        </ChatSettingsContext.Provider>
      </section>
    </InPaneContext.Provider>
  );
}

/**
 * Names the model and thinking level that answer the next message in the open chat, and opens
 * the row that changes them. A dot tells that the chat has its own values.
 */
function ChatSettingsButton({
  chat,
  settings,
  open,
  onClick,
}: {
  chat: Chat;
  settings: Settings;
  open: boolean;
  onClick: () => void;
}) {
  const models = useQuery(listModelsOptions());
  const own = chat.settings;
  const bot = findModel(settings.agent.provider, settings.agent.modelId, models.data?.models);
  const effective = own.modelId ? findModel(own.provider ?? "", own.modelId, models.data?.models) : bot;
  const thinking = own.thinkingLevel ?? settings.agent.thinkingLevel;

  const text = effective.model?.reasoning
    ? `${effective.label} · ${thinkingNames[thinking]}`
    : effective.label;

  const changed = !!(chat.cwd || own.modelId || own.thinkingLevel);
  const label = `Chat settings${changed ? ", changed for this chat" : ""}: ${text}`;

  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={open}
      title={label}
      onClick={onClick}
      className="relative flex h-8 max-w-[45%] shrink-0 items-center gap-1.5 rounded-md px-2 text-[12.5px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground aria-pressed:bg-accent aria-pressed:text-foreground"
    >
      <SlidersHorizontal size={15} aria-hidden className="shrink-0 sm:hidden" />
      <span className="truncate max-sm:hidden">{text}</span>
      <ChevronDown size={13} aria-hidden className="shrink-0 max-sm:hidden" />
      {changed ? (
        <span aria-hidden className="absolute top-1 right-1 size-1.5 rounded-full bg-primary" />
      ) : null}
    </button>
  );
}
