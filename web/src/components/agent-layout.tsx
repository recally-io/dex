import type { AgentSettings as Settings } from "../lib/agent-scope";
import { AgentContext, useAgentSettings, useAgentId } from "../lib/agent-scope";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import {
  getChatOptions,
  getMainChatOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { ChatTitle, MainTimeline } from "../routes/chat";
import { BotNavigation } from "./bot-navigation";
import { AgentAvatar } from "./agent-avatar";
import { Failure, Loading } from "./feedback";
import { InPaneContext, MainPane, MenuButton } from "./page";
import { cn } from "../lib/utils";

/** The name of each page under a bot that is not a chat. */
const pageNames = new Map([
  ["automations", "Routines"],
  ["agent", "Settings"],
  ["instructions", "Settings"],
  ["channels", "Settings"],
  ["extensions", "Settings"],
  ["sessions", "Tasks"],
]);

/**
 * The bot header holds navigation and task controls. The main timeline stays mounted under the
 * other pages, so its stream and the
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

  return (
    <InPaneContext.Provider value>
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border pr-2 pl-3 sm:pl-4">
          <MenuButton />
          <AgentAvatar agent={settings.agent} className="size-5 shrink-0 rounded-control text-[11px]" />
          <Link
            to="/a/$agentId"
            params={{ agentId }}
            className="max-w-24 shrink-0 truncate text-[13px] text-muted-foreground lg:max-w-40"
          >
            {settings.agent.name}
          </Link>
          <span aria-hidden className="text-ring">
            /
          </span>
          {shown ? (
            <div className="flex min-w-0 flex-1 items-center gap-2">
              <ChatTitle chat={shown} heading />
            </div>
          ) : (
            <span className="min-w-0 flex-1 truncate text-[14px] font-semibold tracking-[-0.01em]">
              {pageNames.get(segment ?? "") ?? ""}
            </span>
          )}
          <BotNavigation
            agent={settings.agent}
            segment={segment ?? ""}
            cwd={shown?.cwd ?? main.data?.cwd ?? ""}
          />
        </header>
        <div inert={!onMain} className={cn("min-h-0 flex-1 flex-col", onMain ? "flex" : "hidden")}>
          <MainTimeline settings={settings} />
        </div>
        {!onMain ? <Outlet /> : null}
      </section>
    </InPaneContext.Provider>
  );
}
