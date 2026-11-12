import { type ReactElement, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CircleAlert, ListTodo, PanelRightClose, X } from "lucide-react";
import type { Chat } from "../../api/generated/types.gen";
import { useAgentId } from "../../lib/agent-scope";
import { activeSessionCount } from "../../lib/sessions";
import { needsItems, workOptions } from "../../lib/work";
import { cn } from "../../lib/utils";
import { sidePanel } from "../page";
import { ChatSection } from "../chat-list";
import { Button } from "../ui/button";

/** The counts that the Sessions button shows: what needs the user, and what runs now. */
function useSessionCounts(chats: Chat[]) {
  const agentId = useAgentId();
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);

  return {
    needs: needs.questions.length + needs.results.length + needs.setup.length,
    running: activeSessionCount(chats.filter((chat) => !chat.archivedAt)),
  };
}

/** Opens the Sessions panel. Its counts tell what needs the user and what the bot runs now. */
export function SessionsButton({
  chats,
  open,
  onClick,
}: {
  chats: Chat[];
  open: boolean;
  onClick: () => void;
}) {
  const counts = useSessionCounts(chats);

  const label = [
    "Sessions",
    counts.needs ? `${counts.needs} need you` : "",
    counts.running ? `${counts.running} running` : "",
  ]
    .filter(Boolean)
    .join(", ");

  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={open}
      title={label}
      onClick={onClick}
      className="flex h-8 items-center gap-2 rounded-full border border-border px-2.5 text-[12.5px] font-medium hover:border-ring aria-pressed:bg-accent"
    >
      <ListTodo className="size-4 sm:hidden" aria-hidden />
      <span className="max-sm:hidden">Sessions</span>
      {counts.needs ? (
        <span className="flex items-center gap-1 tabular-nums">
          <span aria-hidden className="size-1.5 rounded-full bg-orange" />
          {counts.needs}
        </span>
      ) : null}
      {counts.running ? (
        <span className="flex items-center gap-1 tabular-nums">
          <span aria-hidden className="size-1.5 rounded-full bg-primary" />
          {counts.running}
        </span>
      ) : null}
    </button>
  );
}

function Heading({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex h-7 items-center px-3 pt-2">
      <h2 className="min-w-0 flex-1 text-[11.5px] font-medium text-muted-foreground">{children}</h2>
      {action}
    </div>
  );
}

/**
 * Everything the bot manages besides Main: what needs the user, then its Chats and the child
 * sessions that it started. Desktop shows it as a right sidebar, a phone as a full-screen sheet.
 */
export function SessionsPanel(props: { chatId: number; open: boolean; onClose: () => void }) {
  const { chatId, open, onClose } = props;
  const agentId = useAgentId();
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);
  const otherNeeds = needs.questions.length + needs.results.length + needs.setup.length;

  if (!open) return null;

  return (
    <aside aria-label="Sessions" className={cn(sidePanel, "md:w-[320px]")}>
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-3 md:h-10">
        <span className="flex-1 text-[14px] font-medium md:text-[12.5px]">Sessions</span>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label="Close sessions"
          onClick={onClose}
          className="max-md:size-8"
        >
          {/* A phone shows the panel as a full-screen sheet, which closes with an X. */}
          <X className="md:hidden" />
          <PanelRightClose className="max-md:hidden" />
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {otherNeeds ? (
          <section aria-label="Needs you" className="pb-1">
            <Heading>Needs you</Heading>
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
        <ChatSection chatId={chatId} />
      </div>
      <Link
        to="/a/$agentId/sessions"
        params={{ agentId }}
        className="shrink-0 border-t border-border px-3 py-2.5 text-[12px] font-medium text-primary hover:underline"
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
      className="h-auto w-full justify-start gap-2 rounded-none px-3 py-2 text-left"
    >
      <CircleAlert className="size-3.5 shrink-0 text-orange" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[12.5px] font-medium">{title}</span>
        <span className="block text-[11.5px] font-normal text-muted-foreground">{detail}</span>
      </span>
    </Button>
  );
}
