import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, ChevronDown, CircleAlert, List, Plus, Settings } from "lucide-react";
import type { Agent, Chat } from "../api/generated/types.gen";
import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { needsItems, workOptions } from "../lib/work";
import { cn } from "../lib/utils";
import { useNewChat } from "./chat-list";
import { ChatProject } from "./chat-project";
import { Button, buttonVariants } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";

const settingsSegments = ["agent", "instructions", "channels", "extensions"];

export function BotNavigation({ agent, segment, cwd }: { agent: Agent; segment: string; cwd: string }) {
  const agentId = agent.id!;
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, agent.instructions);
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);
  const attention = needs.questions.length + needs.results.length + needs.setup.length;
  const [created, setCreated] = useState<Chat>();
  const newTask = useNewChat(cwd, setCreated);
  const agents = useQuery(listAgentsOptions());
  const activity = agents.data?.agents.find((item) => item.id === agentId)?.activity;
  const routines = activity?.enabledAutomations ?? 0;
  const failed = activity?.routineFailed ?? false;

  const routineLabel = ["Routines", routines ? `${routines} on` : "", failed ? "latest run failed" : ""]
    .filter(Boolean)
    .join(", ");

  const linkClass = cn(
    buttonVariants({ variant: "ghost", size: "sm" }),
    "text-muted-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground",
  );

  const settingsLabel = dirty ? "Bot settings, unsaved instructions draft" : "Bot settings";

  const items = (
    <>
      <DropdownMenuItem disabled={newTask.isPending} onClick={() => newTask.mutate()}>
        <Plus size={15} /> New task
      </DropdownMenuItem>
      <DropdownMenuItem render={<Link to="/a/$agentId/sessions" params={{ agentId }} />}>
        <List size={15} /> Tasks
      </DropdownMenuItem>
      <DropdownMenuItem
        render={<Link to="/a/$agentId/automations" params={{ agentId }} />}
        aria-label={routineLabel}
      >
        <CalendarClock size={15} /> Routines
        {routines ? <span className="text-xs tabular-nums">{routines}</span> : null}
        {failed ? (
          <span role="img" aria-label="Latest routine run failed" className="size-1.5 rounded-full bg-red" />
        ) : null}
      </DropdownMenuItem>
      <DropdownMenuItem
        render={<Link to="/a/$agentId/agent" params={{ agentId }} />}
        aria-label={settingsLabel}
      >
        <Settings size={15} /> Bot settings
        {dirty ? (
          <span aria-label="Unsaved instructions draft" className="text-orange">
            Draft
          </span>
        ) : null}
      </DropdownMenuItem>
    </>
  );

  const attentionItems = (
    <>
      {needs.questions.map((chat) => (
        <DropdownMenuItem
          key={`question-${chat.id}`}
          render={
            chat.main ? (
              <Link to="/a/$agentId" params={{ agentId }} />
            ) : (
              <Link to="/a/$agentId/c/$chatId" params={{ agentId, chatId: String(chat.id) }} />
            )
          }
          className="h-auto"
        >
          <CircleAlert size={15} className="shrink-0 text-orange" />
          <span className="min-w-0 flex-1 overflow-hidden">
            <span className="block truncate">{chat.main ? "Main" : chat.title}</span>
            <span className="block text-xs text-muted-foreground">Asks you a question</span>
          </span>
        </DropdownMenuItem>
      ))}
      {needs.results.map((item) => (
        <DropdownMenuItem
          key={`result-${item.runId}`}
          render={<Link to="/inbox/$runId" params={{ runId: item.runId }} />}
          className="h-auto"
        >
          <CircleAlert size={15} className="shrink-0 text-orange" />
          <span className="min-w-0 flex-1 overflow-hidden">
            <span className="block truncate">{item.automationName}</span>
            <span className="block text-xs text-muted-foreground">Routine run failed</span>
          </span>
        </DropdownMenuItem>
      ))}
      {needs.setup.map((automation) => (
        <DropdownMenuItem
          key={`setup-${automation.id}`}
          render={
            <Link
              to="/a/$agentId/automations/$automationId"
              params={{ agentId, automationId: automation.id }}
            />
          }
          className="h-auto"
        >
          <CircleAlert size={15} className="shrink-0 text-orange" />
          <span className="min-w-0 flex-1 overflow-hidden">
            <span className="block truncate">{automation.name}</span>
            <span className="block text-xs text-muted-foreground">Routine needs an available model</span>
          </span>
        </DropdownMenuItem>
      ))}
    </>
  );

  return (
    <>
      <nav aria-label="Bot navigation" className="hidden shrink-0 items-center gap-0.5 lg:flex">
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          aria-label="New task"
          title="Start an independent task without changing Main"
          disabled={newTask.isPending}
          onClick={() => newTask.mutate()}
        >
          New task
        </Button>
        <Link
          to="/a/$agentId/sessions"
          params={{ agentId }}
          aria-current={segment === "sessions" ? "page" : undefined}
          className={linkClass}
        >
          Tasks
        </Link>
        <Link
          to="/a/$agentId/automations"
          params={{ agentId }}
          aria-current={segment === "automations" ? "page" : undefined}
          aria-label={routineLabel}
          className={linkClass}
        >
          Routines {routines ? <span className="text-xs tabular-nums">{routines}</span> : null}
          {failed ? (
            <span
              role="img"
              aria-label="Latest routine run failed"
              className="size-1.5 rounded-full bg-red"
            />
          ) : null}
        </Link>
        <Link
          to="/a/$agentId/agent"
          params={{ agentId }}
          aria-current={settingsSegments.includes(segment) ? "page" : undefined}
          aria-label={settingsLabel}
          className={linkClass}
        >
          Bot settings{" "}
          {dirty ? (
            <span
              role="img"
              aria-label="Unsaved instructions draft"
              className="size-1.5 rounded-full bg-orange"
            />
          ) : null}
        </Link>
        {attention ? (
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label="Needs you" />}>
              <CircleAlert className="text-orange" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-60 max-w-[calc(100vw-1rem)]">
              {attentionItems}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
      </nav>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="sm"
              className="relative shrink-0 text-muted-foreground lg:hidden"
              aria-label={`Bot menu: ${agent.name}`}
            />
          }
        >
          Menu <ChevronDown />
          {dirty || attention ? (
            <span
              aria-label={dirty ? "Unsaved instructions draft" : "Needs you"}
              className="absolute top-1 right-1 size-1.5 rounded-full bg-orange"
            />
          ) : null}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60 max-w-[calc(100vw-1rem)]">
          {items}
          {attentionItems}
        </DropdownMenuContent>
      </DropdownMenu>
      {created ? (
        <ChatProject
          key={created.id}
          chatId={created.id}
          cwd={created.cwd}
          initialOpen
          onClose={() => setCreated(undefined)}
        />
      ) : null}
    </>
  );
}
