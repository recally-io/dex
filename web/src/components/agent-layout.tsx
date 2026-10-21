import type { AgentSettings as Settings } from "../lib/agent-scope";
import { AgentContext, useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useEffect, useRef } from "react";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import { listAgentsOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { agentModel, saveAgent, thinkingNames, type AgentModel } from "../lib/agent";
import { instructionsDirty } from "../lib/instructions";
import { useApp } from "../lib/app-state";
import { MainTimeline } from "../routes/chat";
import { ModelPicker } from "./model-picker";
import { ThinkingPicker } from "./thinking-picker";
import { AgentAvatar } from "./agent-avatar";
import { StatusPill } from "./atoms/StatusPill";
import { Failure, Loading } from "./feedback";
import { InPaneContext, MainPane, MenuButton } from "./page";
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
  { to: "/a/$agentId/instructions", label: "Instructions", segments: ["instructions"] },
  { to: "/a/$agentId/channels", label: "Channels", segments: ["channels"] },
  { to: "/a/$agentId/automations", label: "Automations", segments: ["automations"] },
  { to: "/a/$agentId/sessions", label: "Sessions", segments: ["sessions", "c", "tasks"] },
  { to: "/a/$agentId/extensions", label: "Extensions", segments: ["extensions"] },
  { to: "/a/$agentId/agent", label: "Settings", segments: ["agent"] },
];

/** Activity reads the agent list cache. Shell owns its polling interval. */
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
 * The page of one agent: its name, a tab for each part of it, and its model. Everything that
 * belongs to the agent is a tab here, so the sidebar only holds the list of agents and the
 * global settings. The main timeline stays mounted under the other tabs, so its stream and the
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
          Agent not found
        </h1>
        <p className="mt-2 text-ink-2">Dex has no agent with this ID.</p>
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
  const segment = path.split("/").filter(Boolean)[2];
  const current = tabs.find((tab) => tab.segments.includes(segment));
  const onChat = current?.label === "Chat";
  const models = useQuery(listModelsOptions());
  const model = agentModel(settings.agent, models.data?.models);
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, settings.agent.instructions);
  const activity = useAgentActivity();

  const counts = new Map([
    ["Channels", activity.channels],
    ["Automations", activity.automations],
  ]);

  // The other tabs have their own page title.
  const Name = onChat ? "h1" : "span";
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
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line pr-2 pl-3 sm:pl-4 lg:h-11">
          <MenuButton />
          <AgentAvatar agent={settings.agent} className="size-5 rounded-[6px] text-[11px]" />
          <Name
            id={onChat ? "page-title" : undefined}
            tabIndex={onChat ? -1 : undefined}
            className="max-w-[40%] shrink-0 truncate text-[13px] font-semibold text-ink outline-none sm:max-w-[200px]"
          >
            {settings.agent.name}
          </Name>
          {dirty ? (
            <StatusPill tone="orange" className="hidden h-5 shrink-0 px-2 text-[11px] sm:inline-flex">
              Instructions draft
            </StatusPill>
          ) : null}
          <span aria-hidden className="h-4 w-px shrink-0 bg-line-strong" />
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
                {tab.label === "Instructions" && dirty ? (
                  <span role="img" aria-label="Unsaved draft" className="size-1.5 rounded-full bg-orange" />
                ) : null}
                {tab.label === "Sessions" && activity.active > 0 ? (
                  <span
                    role="img"
                    aria-label={`${activity.active} active`}
                    className="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-orange-tint px-1.5 text-[11px] font-medium text-orange tabular-nums"
                  >
                    {activity.active}
                  </span>
                ) : null}
              </Link>
            ))}
          </nav>
          <ModelChips settings={settings} model={model} />
        </header>
        <div inert={!onChat} className={cn("min-h-0 flex-1 flex-col", onChat ? "flex" : "hidden")}>
          <MainTimeline settings={settings} />
        </div>
        {!onChat ? <Outlet /> : null}
      </section>
    </InPaneContext.Provider>
  );
}

/** The model and the thinking level: one-step changes that fit in the header of every tab. */
function ModelChips({ settings, model }: { settings: Settings; model: AgentModel }) {
  const agentId = useAgentId();
  const { notify } = useApp();

  const caret = (
    <svg
      width="10"
      height="10"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="text-ink-3"
    >
      <path d="M6 9l6 6 6-6" />
    </svg>
  );

  const chip =
    "flex h-7 min-w-0 items-center gap-1 rounded-control px-2 text-[12.5px] text-ink-2 transition-colors hover:bg-hover-2 hover:text-ink aria-expanded:bg-hover-2";

  return (
    <div className="hidden max-w-[240px] min-w-0 shrink items-center gap-0.5 md:flex">
      <ModelPicker
        trigger={
          <button type="button" aria-label="Change model" className={chip}>
            <span className={cn("min-w-0 truncate font-medium", model.ok ? "text-ink" : "text-orange")}>
              {settings.agent.modelId ? model.label : "No model"}
            </span>
            {caret}
          </button>
        }
      />
      {model.model?.reasoning ? (
        <ThinkingPicker
          value={settings.agent.thinkingLevel}
          levels={model.model.thinkingLevels}
          save={async (level) => {
            if (!level) return;
            await saveAgent(agentId, (agent) => ({ ...agent, thinkingLevel: level }));
            notify(`Thinking is now ${thinkingNames[level]}. It applies from the next message.`);
          }}
          trigger={
            <button type="button" aria-label="Change thinking level" className={cn(chip, "shrink-0")}>
              {thinkingNames[settings.agent.thinkingLevel]}
              {caret}
            </button>
          }
        />
      ) : null}
    </div>
  );
}
