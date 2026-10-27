import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleDot,
  Clock3,
  PanelRightClose,
  Timer,
  XCircle,
} from "lucide-react";
import type { Chat, Item, TaskSummary } from "../../api/generated/types.gen";
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
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../ui/tooltip";

function statusIcon(task: TaskSummary, className: string) {
  const bucket = taskBucket(task);

  if (bucket === "needs-you")
    return isStalled(task) ? <Timer className={className} /> : <CircleAlert className={className} />;

  if (bucket === "running") return <CircleDot className={className} />;

  if (bucket === "queued") return <Clock3 className={className} />;

  if (task.status === "failed" || task.status === "interrupted") return <XCircle className={className} />;

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
      <Link
        to="/a/$agentId/tasks/$taskId"
        params={{ agentId, taskId: task.id }}
        className="font-medium text-primary hover:underline"
      >
        Open full record →
      </Link>
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
      <button type="button" className="flex w-full min-w-0 items-center gap-2 text-left" onClick={onToggle}>
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

export function TaskSummaryButton({
  tasks,
  open,
  onClick,
}: {
  tasks: TaskSummary[];
  open: boolean;
  onClick: () => void;
}) {
  const agentId = useAgentId();
  const seen = readSeen(agentId);
  const visibleTasks = tasks.filter((task) => taskBucket(task) !== "done" || !seen[task.id]);

  const counts = visibleTasks.reduce(
    (result, task) => {
      result[taskBucket(task)] += 1;

      return result;
    },
    { "needs-you": 0, running: 0, queued: 0, done: 0 },
  );

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          render={
            <button
              type="button"
              aria-label="Background tasks"
              aria-pressed={open}
              onClick={onClick}
              className="flex h-7 items-center gap-2 rounded-full border border-border px-2.5 text-[11.5px] hover:border-ring"
            />
          }
        >
          {/* Each count sits next to its own icon. */}
          {(
            [
              ["running", CircleDot, "text-primary"],
              ["queued", Clock3, "text-muted-foreground"],
              ["needs-you", CircleAlert, "text-orange"],
              ["done", Check, "text-green"],
            ] as const
          ).map(([bucket, Icon, color]) =>
            counts[bucket] ? (
              <span key={bucket} className="flex items-center gap-1">
                <Icon className={`size-3.5 ${color}`} />
                {counts[bucket]}
              </span>
            ) : null,
          )}
          {!visibleTasks.length ? <PanelRightClose className="size-3.5 text-muted-foreground" /> : null}
        </TooltipTrigger>
        <TooltipContent>Background tasks</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

export function TaskPanel(props: {
  chats: Chat[];
  open: boolean;
  onClose: () => void;
  onStop?: (task: TaskSummary) => void;
}) {
  const { chats, open, onClose, onStop } = props;
  const agentId = useAgentId();
  void onStop; // PR 2 adds the cancel request; the row keeps this seam without a control yet.
  const [expanded, setExpanded] = useState<string | undefined>();
  const [doneOpen, setDoneOpen] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [, setSeenVersion] = useState(0);
  const tasks = useMemo(() => chats.flatMap((chat) => (chat.task ? [chat.task] : [])), [chats]);
  const seen = readSeen(agentId);
  const ordered = orderTasks(tasks, now);
  const active = ordered.filter((task) => taskBucket(task, now) !== "done");
  const done = ordered.filter((task) => taskBucket(task, now) === "done" && !seen[task.id]);

  const grouped =
    active.length > 6
      ? groupTasksByChat(
          active.filter((task) => taskBucket(task, now) !== "needs-you"),
          chats,
        )
      : undefined;

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
    <aside
      aria-label="Background tasks"
      className="hidden w-[340px] shrink-0 flex-col border-l border-border md:flex"
    >
      <header className="flex h-10 shrink-0 items-center gap-2 border-b border-border px-3">
        <span className="flex-1 text-[12.5px] font-medium">Background tasks</span>
        <Button type="button" variant="ghost" size="icon-xs" aria-label="Close tasks" onClick={onClose}>
          <PanelRightClose />
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto py-1">
        {ordered.filter((task) => taskBucket(task, now) === "needs-you").map(row)}
        {ordered.some((task) => taskBucket(task, now) === "needs-you") && active.length ? (
          <div className="mx-3 mt-2 border-t border-border" />
        ) : null}
        {grouped
          ? [...grouped].map(([title, group]) => (
              <section key={title}>
                <div className="px-3 pt-2 pb-1 text-[11px] font-medium">{title}</div>
                {group.map(row)}
              </section>
            ))
          : active.filter((task) => taskBucket(task, now) !== "needs-you").map(row)}
        {done.length ? (
          <>
            <button
              type="button"
              className="flex w-full items-center gap-1 border-t border-border px-3 py-2 text-left text-[11px] text-muted-foreground"
              onClick={() => setDoneOpen(!doneOpen)}
            >
              {doneOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}{" "}
              Completed · {done.length}
            </button>
            {doneOpen ? done.map(row) : null}
          </>
        ) : null}
        {!active.length && !done.length ? (
          <p className="px-3 py-4 text-[12px] text-muted-foreground">No background tasks.</p>
        ) : null}
      </div>
    </aside>
  );
}
