import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleDot,
  Clock3,
  ListTodo,
  PanelRightClose,
  Timer,
  X,
  XCircle,
} from "lucide-react";
import type { Chat, Item, TaskSummary } from "../../api/generated/types.gen";
import {
  acknowledgeTaskMutation,
  cancelTaskMutation,
  listChatsQueryKey,
} from "../../api/generated/@tanstack/react-query.gen";
import { useChatStream } from "../../lib/chat-stream";
import { useAgentId } from "../../lib/agent-scope";
import {
  groupTasksByChat,
  isStalled,
  markTaskSeen,
  orderTasks,
  readSeen,
  taskBucket,
  taskDuration,
} from "../../lib/tasks";
import { useHandled } from "../../lib/handled";
import { needsItems, needsTask, workOptions } from "../../lib/work";
import { cn } from "../../lib/utils";
import { sidePanel } from "../page";
import { ChatSection } from "../chat-list";
import { Button } from "../ui/button";

function statusIcon(task: TaskSummary, className: string) {
  const bucket = taskBucket(task);

  if (bucket === "needs-you")
    return isStalled(task) ? <Timer className={className} /> : <CircleAlert className={className} />;

  if (bucket === "running") return <CircleDot className={className} />;

  if (bucket === "queued") return <Clock3 className={className} />;

  if (task.status === "failed" || task.status === "interrupted" || task.status === "cancelled")
    return <XCircle className={className} />;

  return <Check className={className} />;
}

function elapsed(task: TaskSummary, now: number) {
  if (task.status === "queued") return "";

  if (task.status === "done" || task.endedAt !== undefined)
    return taskDuration(task.endedAt! - (task.startedAt ?? task.createdAt));

  return taskDuration(Math.max(0, now - (task.startedAt ?? task.createdAt)));
}

function taskStep(item: Item) {
  if (item.kind === "tool") return `${item.name}${item.status === "running" ? " · running" : ""}`;

  if (item.kind === "assistant") return item.text;

  if (item.kind === "thinking") return item.text;

  if (item.kind === "error") return item.reason;

  return undefined;
}

function ExpandedTask({ task, onSeen }: { task: TaskSummary; onSeen: (id: string) => void }) {
  const agentId = useAgentId();
  const queryClient = useQueryClient();

  const stop = useMutation({
    ...cancelTaskMutation(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: listChatsQueryKey({ path: { agentId } }) }),
  });

  const acknowledge = useMutation({
    ...acknowledgeTaskMutation(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: listChatsQueryKey({ path: { agentId } }) }),
  });

  const stoppable = ["queued", "admitting", "running"].includes(task.status);
  const { state, connection, streamError, reconnect } = useChatStream(task.conversationId);

  const steps = state.items
    .map(taskStep)
    .filter((step): step is string => Boolean(step))
    .slice(-5);

  useEffect(() => onSeen(task.id), [onSeen, task.id]);

  return (
    <div className="mt-2 ml-6 flex flex-col gap-2 text-[11.5px] text-muted-foreground">
      <div>
        Chat <span className="text-foreground">{task.parentConversationId}</span>
      </div>
      {task.lastAction ? <div className="truncate">{task.lastAction}</div> : null}
      <div className="flex flex-col gap-1 border-l border-border pl-2">
        {steps.length ? steps.map((step) => <div key={step}>{step}</div>) : <div>No steps yet.</div>}
      </div>
      {connection === "error" ? (
        <div className="flex items-center gap-2 text-destructive">
          <span>{streamError || "Connection lost."}</span>
          <Button size="xs" variant="secondary" onClick={() => reconnect.current?.()}>
            Reconnect
          </Button>
        </div>
      ) : null}
      <div className="flex items-center gap-3">
        {!stoppable && task.handledAt === undefined ? (
          <Button
            size="xs"
            variant="outline"
            disabled={acknowledge.isPending}
            onClick={() => acknowledge.mutate({ path: { agentId, taskId: task.id } })}
          >
            Mark handled
          </Button>
        ) : null}
        {stoppable ? (
          <Button
            size="xs"
            variant="outline"
            className="text-destructive"
            disabled={stop.isPending}
            onClick={() => stop.mutate({ path: { agentId, taskId: task.id } })}
          >
            Stop
          </Button>
        ) : null}
        <Link
          to="/a/$agentId/tasks/$taskId"
          params={{ agentId, taskId: task.id }}
          className="font-medium text-primary hover:underline"
        >
          Open full record →
        </Link>
      </div>
      {stop.error ? <div className="text-destructive">Could not stop the task.</div> : null}
      {acknowledge.error ? <div className="text-destructive">Could not mark the task handled.</div> : null}
    </div>
  );
}

function TaskRow({
  task,
  now,
  expanded,
  onToggle,
  onSeen,
}: {
  task: TaskSummary;
  now: number;
  expanded: boolean;
  onToggle: () => void;
  onSeen: (id: string) => void;
}) {
  const bucket = taskBucket(task, now);

  const iconClass = cn(
    "size-3.5 shrink-0",
    bucket === "needs-you" && "text-orange",
    bucket === "running" && "text-primary",
    bucket === "queued" && "text-muted-foreground",
    bucket === "done" && "text-green",
  );

  return (
    <div className={cn("px-3 py-2 hover:bg-muted", expanded && "bg-muted")}>
      <button
        type="button"
        className="flex w-full min-w-0 items-center gap-2 text-left"
        onClick={onToggle}
        title={task.status === "queued" || task.status === "admitting" ? "Starting" : undefined}
      >
        {statusIcon(task, iconClass)}
        <span className="min-w-0 flex-1 truncate text-[12.5px]">{task.title}</span>
        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">{elapsed(task, now)}</span>
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0" />
        )}
      </button>
      {expanded ? <ExpandedTask task={task} onSeen={onSeen} /> : null}
    </div>
  );
}

/** The counts that the Sessions button shows: what needs the user, and what runs now. */
function useSessionCounts(tasks: TaskSummary[]) {
  const agentId = useAgentId();
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);
  const open = tasks.filter((task) => task.handledAt === undefined);

  return {
    needs:
      open.filter((task) => taskBucket(task) === "needs-you").length +
      needs.questions.length +
      needs.results.length +
      needs.setup.length,
    running: open.filter((task) => ["running", "queued"].includes(taskBucket(task))).length,
  };
}

/** Opens the Sessions panel. Its counts tell what needs the user and what the bot runs now. */
export function SessionsButton({
  tasks,
  open,
  onClick,
}: {
  tasks: TaskSummary[];
  open: boolean;
  onClick: () => void;
}) {
  const counts = useSessionCounts(tasks);

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
 * Everything the bot manages besides Main: what needs the user, the tasks that run, the Chats and
 * the finished tasks. Desktop shows it as a right sidebar, a phone as a full-screen sheet.
 */
export function SessionsPanel(props: { chats: Chat[]; chatId: number; open: boolean; onClose: () => void }) {
  const { chats, chatId, open, onClose } = props;
  const agentId = useAgentId();
  const [expanded, setExpanded] = useState<string | undefined>();
  const [doneOpen, setDoneOpen] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [, setSeenVersion] = useState(0);

  const tasks = useMemo(
    () => chats.flatMap((chat) => (chat.task && chat.task.handledAt === undefined ? [chat.task] : [])),
    [chats],
  );

  const handle = useHandled();
  const work = useQuery(workOptions());
  const needs = needsItems(agentId, work.data);
  const seen = readSeen(agentId);
  const ordered = orderTasks(tasks, now);
  const handleable = tasks.filter(needsTask);
  const needsTasks = ordered.filter((task) => taskBucket(task, now) === "needs-you");
  const running = ordered.filter((task) => ["running", "queued"].includes(taskBucket(task, now)));
  const done = ordered.filter((task) => taskBucket(task, now) === "done" && !seen[task.id]);
  const grouped = running.length > 6 ? groupTasksByChat(running, chats) : undefined;
  const otherNeeds = needs.questions.length + needs.results.length + needs.setup.length;

  useEffect(() => {
    if (!open) return;
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);

    return () => window.clearInterval(timer);
  }, [open]);

  if (!open) return null;

  const markSeen = (id: string) => {
    markTaskSeen(agentId, id);
    setSeenVersion((version) => version + 1);
  };

  const row = (task: TaskSummary) => (
    <TaskRow
      key={task.id}
      task={task}
      now={now}
      expanded={expanded === task.id}
      onToggle={() => {
        const next = expanded === task.id ? undefined : task.id;
        setExpanded(next);

        if (taskBucket(task, now) === "done") markSeen(task.id);
      }}
      onSeen={markSeen}
    />
  );

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
        {otherNeeds || needsTasks.length ? (
          <section aria-label="Needs you" className="pb-1">
            <Heading
              action={
                handleable.length > 1 ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    disabled={handle.isPending}
                    className="-mr-1 text-[12px]"
                    onClick={() =>
                      handle.mutate(
                        handleable.map((task) => ({ kind: "task", agentId, id: task.id, undo: false })),
                      )
                    }
                  >
                    Mark all handled
                  </Button>
                ) : null
              }
            >
              Needs you
            </Heading>
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
            {needsTasks.map(row)}
            {handle.isError ? (
              <p className="px-3 py-2 text-[12px] text-destructive">Could not mark the tasks handled.</p>
            ) : null}
          </section>
        ) : null}
        {running.length ? (
          <section aria-label="Running">
            <Heading>Running</Heading>
            {grouped
              ? [...grouped].map(([title, group]) => (
                  <section key={title}>
                    <div className="px-3 pt-2 pb-1 text-[11px] font-medium">{title}</div>
                    {group.map(row)}
                  </section>
                ))
              : running.map(row)}
          </section>
        ) : null}
        <ChatSection chatId={chatId} />
        {done.length ? (
          <section aria-label="Completed">
            <button
              type="button"
              aria-expanded={doneOpen}
              className="flex h-9 w-full items-center gap-1 px-3 text-left text-[11.5px] font-medium text-muted-foreground hover:text-foreground"
              onClick={() => setDoneOpen(!doneOpen)}
            >
              {doneOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
              Completed · {done.length}
            </button>
            {doneOpen ? done.map(row) : null}
          </section>
        ) : null}
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

/** One item that needs the user and is not a task. The row opens the place to act on it. */
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
