import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { CalendarClock, ChevronDown, List, Plus, Settings } from "lucide-react";
import type { Agent, Session } from "../api/generated/types.gen";
import { createAgentSession, setSessionProject } from "../api/generated/sdk.gen";
import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { instructionsDirty } from "../lib/instructions";
import { errorText, refreshSessions } from "../lib/queries";
import { cn } from "../lib/utils";
import { SessionProject } from "./session-project";
import { Button, buttonVariants } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";

const settingsSegments = ["agent", "skills", "integrations", "secrets", "channels"];

/** Opens a session with the current project. The server reuses the newest empty session. */
function useNewSession(cwd: string, created: (session: Session) => void) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();

  return useMutation({
    mutationFn: async () => {
      const { data: session } = await createAgentSession({ path: { agentId }, throwOnError: true });
      await setSessionProject({
        path: { agentId, sessionId: session.id },
        body: { cwd },
        throwOnError: true,
      });

      return { ...session, cwd };
    },
    onSuccess: async (session) => {
      await refreshSessions(agentId);
      await navigate({ to: "/a/$agentId/s/$sessionId", params: { agentId, sessionId: String(session.id) } });
      created(session);
    },
    onError: (cause) => notify(`Could not open a new session. ${errorText(cause)}`),
  });
}

export function BotNavigation({ agent, segment, cwd }: { agent: Agent; segment: string; cwd: string }) {
  const agentId = agent.id!;
  const { draft } = useApp();
  const dirty = instructionsDirty(draft, agent.instructions);
  const [created, setCreated] = useState<Session>();
  const newSession = useNewSession(cwd, setCreated);
  const agents = useQuery(listAgentsOptions());
  const activity = agents.data?.agents.find((item) => item.id === agentId)?.activity;
  const automations = activity?.enabledAutomations ?? 0;
  const failed = activity?.automationFailed ?? false;

  const automationLabel = [
    "Automations",
    automations ? `${automations} on` : "",
    failed ? "latest run failed" : "",
  ]
    .filter(Boolean)
    .join(", ");

  const linkClass = cn(
    buttonVariants({ variant: "ghost", size: "icon-sm" }),
    "text-muted-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground",
  );

  const settingsLabel = dirty ? "Bot settings, unsaved instructions draft" : "Bot settings";

  const items = (
    <>
      <DropdownMenuItem disabled={newSession.isPending} onClick={() => newSession.mutate()}>
        <Plus size={15} /> New session
      </DropdownMenuItem>
      <DropdownMenuItem render={<Link to="/a/$agentId/sessions" params={{ agentId }} />}>
        <List size={15} /> Sessions
      </DropdownMenuItem>
      <DropdownMenuItem
        render={<Link to="/a/$agentId/automations" params={{ agentId }} />}
        aria-label={automationLabel}
      >
        <CalendarClock size={15} /> Automations
        {automations ? <span className="text-xs tabular-nums">{automations}</span> : null}
        {failed ? (
          <span
            role="img"
            aria-label="Latest automation run failed"
            className="size-1.5 rounded-full bg-red"
          />
        ) : null}
      </DropdownMenuItem>
      <DropdownMenuItem
        render={<Link to="/a/$agentId/agent" params={{ agentId }} />}
        aria-label={settingsLabel}
      >
        <Settings size={15} /> Settings
        {dirty ? (
          <span aria-label="Unsaved instructions draft" className="text-orange">
            Draft
          </span>
        ) : null}
      </DropdownMenuItem>
    </>
  );

  return (
    <>
      <nav aria-label="Bot navigation" className="hidden shrink-0 items-center gap-0.5 lg:flex">
        <Button
          variant="ghost"
          size="icon-sm"
          className="text-muted-foreground"
          aria-label="New session"
          title="Open a session without changing Main"
          disabled={newSession.isPending}
          onClick={() => newSession.mutate()}
        >
          <Plus aria-hidden="true" />
        </Button>
        <Link
          to="/a/$agentId/sessions"
          params={{ agentId }}
          aria-current={segment === "sessions" ? "page" : undefined}
          aria-label="Sessions"
          title="Sessions"
          className={linkClass}
        >
          <List aria-hidden="true" />
        </Link>
        <Link
          to="/a/$agentId/automations"
          params={{ agentId }}
          aria-current={segment === "automations" ? "page" : undefined}
          aria-label={automationLabel}
          title={automationLabel}
          className={linkClass}
        >
          <CalendarClock aria-hidden="true" />
          {failed ? (
            <span
              role="img"
              aria-label="Latest automation run failed"
              className="size-1.5 rounded-full bg-red"
            />
          ) : null}
        </Link>
        <Link
          to="/a/$agentId/agent"
          params={{ agentId }}
          aria-current={settingsSegments.includes(segment) ? "page" : undefined}
          aria-label={settingsLabel}
          title={settingsLabel}
          className={linkClass}
        >
          <Settings aria-hidden="true" />
          {dirty ? (
            <span
              role="img"
              aria-label="Unsaved instructions draft"
              className="size-1.5 rounded-full bg-orange"
            />
          ) : null}
        </Link>
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
          {dirty ? (
            <span
              aria-label="Unsaved instructions draft"
              className="absolute top-1 right-1 size-1.5 rounded-full bg-orange"
            />
          ) : null}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60 max-w-[calc(100vw-1rem)]">
          {items}
        </DropdownMenuContent>
      </DropdownMenu>
      {created ? (
        <SessionProject
          key={created.id}
          sessionId={created.id}
          cwd={created.cwd}
          runnerId={created.runnerId}
          initialOpen
          onClose={() => setCreated(undefined)}
        />
      ) : null}
    </>
  );
}
