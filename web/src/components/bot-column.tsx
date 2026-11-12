import { type ReactElement } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, CircleAlert, Settings } from "lucide-react";
import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { needsItems, workOptions } from "../lib/work";
import { cn } from "../lib/utils";
import { ChatSection } from "./chat-list";
import { Button } from "./ui/button";

/** The path segments that open under the bot's Settings. */
export const settingsSegments = ["agent", "instructions", "channels", "extensions"];

/**
 * Everything of one bot besides the open page: what needs the user, its Routines, its sessions
 * with Main first, and its Settings. It sits next to the bot rail, so one place answers "where to
 * go" and the page header only names what is open.
 */
export function BotColumn({ path, className }: { path: string; className?: string }) {
  const agentId = useAgentId();
  const agents = useQuery(listAgentsOptions());
  const work = useQuery(workOptions());
  const { draft } = useApp();
  const agent = agents.data?.agents.find((item) => item.id === agentId);
  const [, , segment, chatSegment] = path.split("/").filter(Boolean);
  const chatId = segment === "c" ? Number(chatSegment) : 0;
  const onSettings = settingsSegments.includes(segment ?? "");
  const dirty = agent ? instructionsDirty(draft, agent.instructions) : false;
  const routines = agent?.activity?.enabledAutomations ?? 0;
  const routineFailed = agent?.activity?.routineFailed ?? false;
  const needs = needsItems(agentId, work.data);
  const needCount = needs.questions.length + needs.results.length + needs.setup.length;

  const settingsLabel = dirty ? "Bot settings, unsaved instructions draft" : "Bot settings";

  const routinesLabel = [
    "Routines",
    routines ? `${routines} on` : "",
    routineFailed ? "latest run failed" : "",
  ]
    .filter(Boolean)
    .join(", ");

  return (
    <aside
      aria-label="Bot navigation"
      className={cn("flex h-full w-[248px] shrink-0 flex-col border-r border-border bg-sidebar", className)}
    >
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border pr-2 pl-4">
        <span className="min-w-0 flex-1 truncate text-[14px] font-semibold tracking-[-0.01em]">
          {agent?.name ?? ""}
        </span>
        {dirty ? <span className="shrink-0 text-[11.5px] text-orange">Draft</span> : null}
        <Link
          to="/a/$agentId/agent"
          params={{ agentId }}
          data-tab="Settings"
          aria-label={settingsLabel}
          title={settingsLabel}
          aria-current={onSettings ? "page" : undefined}
          className="relative flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground"
        >
          <Settings size={15} aria-hidden />
          {dirty ? (
            <span
              role="img"
              aria-label="Unsaved instructions draft"
              className="absolute top-1.5 right-1.5 size-1.5 rounded-full bg-orange"
            />
          ) : null}
        </Link>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {needCount ? (
          <section aria-label="Needs you" className="px-2 pt-3">
            <h2 className="flex h-7 items-center px-2 text-[11.5px] font-medium text-muted-foreground">
              Needs you
            </h2>
            {needs.questions.map((chat) => (
              <NeedRow
                key={`question-${chat.id}`}
                link={
                  chat.main ? (
                    <Link to="/a/$agentId" params={{ agentId }} />
                  ) : (
                    <Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(chat.id) }} />
                  )
                }
                title={chat.main ? "Main" : chat.title}
                detail="Asks you a question"
              />
            ))}
            {needs.results.map((item) => (
              <NeedRow
                key={`result-${item.runId}`}
                link={<Link to="/inbox/$runId" params={{ runId: item.runId }} />}
                title={item.automationName}
                detail="Routine run failed"
              />
            ))}
            {needs.setup.map((automation) => (
              <NeedRow
                key={`setup-${automation.id}`}
                link={
                  <Link
                    to="/a/$agentId/automations/$automationId"
                    params={{ agentId, automationId: automation.id }}
                  />
                }
                title={automation.name}
                detail="Routine needs an available model"
              />
            ))}
          </section>
        ) : null}
        <nav aria-label="Bot pages" className="px-2 pt-3">
          <Link
            to="/a/$agentId/automations"
            params={{ agentId }}
            data-tab="Routines"
            aria-label={routinesLabel}
            aria-current={segment === "automations" ? "page" : undefined}
            className="flex h-9 items-center gap-2 rounded-md px-2 text-[13px] text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground"
          >
            <CalendarClock size={15} aria-hidden className="shrink-0" />
            <span className="min-w-0 flex-1 truncate">Routines</span>
            {routineFailed ? (
              <span
                role="img"
                aria-label="Latest routine run failed"
                className="size-1.5 rounded-full bg-red"
              />
            ) : null}
            {routines ? <span className="text-[11.5px] tabular-nums">{routines}</span> : null}
          </Link>
        </nav>
        <ChatSection chatId={chatId} />
      </div>
      <Link
        to="/a/$agentId/sessions"
        params={{ agentId }}
        className="shrink-0 border-t border-border px-4 py-2.5 text-[12px] font-medium text-primary hover:underline"
      >
        All sessions →
      </Link>
    </aside>
  );
}

/** One item that needs the user. The row opens the place to act on it. */
function NeedRow({ link, title, detail }: { link: ReactElement; title: string; detail: string }) {
  return (
    <Button
      variant="ghost"
      render={link}
      className="h-auto w-full justify-start gap-2 rounded-md px-2 py-1.5 text-left"
    >
      <CircleAlert className="size-3.5 shrink-0 text-orange" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[12.5px] font-medium">{title}</span>
        <span className="block text-[11.5px] font-normal text-muted-foreground">{detail}</span>
      </span>
    </Button>
  );
}
