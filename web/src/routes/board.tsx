import { useState } from "react";
import { formatDistanceToNow } from "date-fns";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronRight, Plus, X } from "lucide-react";
import { listBotsOptions, listTasksOptions } from "../api/generated/@tanstack/react-query.gen";
import { createTask } from "../api/generated/sdk.gen";
import type { Bot, TaskSummary } from "../api/generated/types.gen";
import { BotAvatar } from "../components/bot-avatar";
import { QuestionRow, ResultGroupRow, TaskReviewRow, groupResults } from "../components/board-results";
import { Failure, Loading } from "../components/feedback";
import { HeldEventRows } from "../components/held-events";
import { FramedPage } from "../components/page";
import { Button, buttonVariants } from "../components/ui/button";
import { useDismissResults } from "../lib/automation-results";
import { attentionItems, boardAttentionOptions, heldEventsOptions } from "../lib/board-attention";
import { invalidateTasks } from "../lib/task-stream";
import { isClosed, statusLabel } from "../lib/tasks";

// Add a Bot filter control here when active tasks routinely exceed 30.
export function BoardPage({ bot, onBotChange }: { bot?: string; onBotChange: (bot?: string) => void }) {
  const [showCompleted, setShowCompleted] = useState(false);
  const botList = useQuery(listBotsOptions());
  const summary = useQuery(boardAttentionOptions());
  const held = useQuery(heldEventsOptions());
  const tasks = useQuery(listTasksOptions({ query: { botId: bot, includeClosed: true } }));

  const bots = botList.data?.bots ?? [];
  const botById = new Map<string | undefined, Bot>(bots.map((item) => [item.id, item]));

  const botOf = (id: string) => ({ id, name: botById.get(id)?.name ?? id });

  const {
    questions,
    results,
    setup,
    taskReviews,
    held: heldEvents,
    needsYouCount,
    count,
  } = attentionItems(summary.data, held.data, bot);

  const resultGroups = groupResults(results);

  const allTasks = tasks.data?.tasks ?? [];
  const blockedTasks: TaskSummary[] = [];
  const activeTasks: TaskSummary[] = [];
  const laterTasks: TaskSummary[] = [];
  const completedTasks: TaskSummary[] = [];

  for (const task of allTasks) {
    if (isClosed(task.status)) {
      completedTasks.push(task);
    } else if (task.status === "in_review" && task.reviewerKind === "owner") {
      // The attention query owns the Owner review rows.
      continue;
    } else if (task.blocked) {
      blockedTasks.push(task);
    } else if (task.status === "backlog") {
      laterTasks.push(task);
    } else {
      activeTasks.push(task);
    }
  }

  const hasAnyContent =
    count > 0 ||
    blockedTasks.length > 0 ||
    activeTasks.length > 0 ||
    laterTasks.length > 0 ||
    completedTasks.length > 0;

  const dismissAll = useDismissResults();

  const loading = tasks.isPending && summary.isPending;

  return (
    <FramedPage
      headerTitle="Board"
      title="Board"
      actions={
        bot ? (
          <Button variant="secondary" size="sm" onClick={() => onBotChange(undefined)}>
            <span>{botOf(bot).name}</span>
            <X className="size-3.5" aria-hidden />
          </Button>
        ) : null
      }
    >
      <QuickAddTask key={bot ?? "all"} bot={bot} bots={bots} />

      {loading ? <Loading /> : null}
      {tasks.isError ? (
        <Failure title="Could not load tasks" error={tasks.error} retry={() => void tasks.refetch()} />
      ) : null}
      {summary.isError ? <Failure title="Could not load attention items" error={summary.error} /> : null}
      {held.isError ? <Failure title="Could not load held events" error={held.error} /> : null}

      {!loading ? (
        <div className="mt-6 space-y-7">
          {needsYouCount > 0 ? (
            <BoardSection title="Needs you" count={needsYouCount}>
              <ul className="divide-y divide-border">
                {questions.map(({ session, question }) => (
                  <QuestionRow
                    key={`${session.botId}:${session.id}:${question.id}`}
                    bot={botOf(session.botId)}
                    session={session}
                    questionId={question.id}
                    text={question.text}
                    options={question.options}
                  />
                ))}
                {taskReviews.map((task) => (
                  <TaskReviewRow key={task.key} task={task} bot={botOf(task.assigneeBotId)} />
                ))}
                {setup.map(({ botId, id, name, targetSessionId }) => {
                  const setupBot = botOf(botId);

                  return (
                    <li key={id} className="flex flex-wrap items-center justify-between gap-3 py-3.5">
                      <div className="flex min-w-0 items-center gap-2.5">
                        <AlertTriangle className="size-4 shrink-0 text-orange" aria-hidden />
                        <BotAvatar bot={setupBot} className="size-5 text-[10px]" />
                        <div className="min-w-0">
                          <p className="truncate text-[14px] font-medium text-foreground">{name}</p>
                          <p className="text-[12.5px] text-muted-foreground">
                            {setupBot.name} · No available model
                          </p>
                        </div>
                      </div>
                      {targetSessionId ? (
                        <Link
                          className={buttonVariants({ variant: "secondary", size: "sm" })}
                          to="/b/$botId/s/$sessionId"
                          params={{ botId, sessionId: String(targetSessionId) }}
                        >
                          Configure session model
                        </Link>
                      ) : (
                        <Link
                          className={buttonVariants({ variant: "secondary", size: "sm" })}
                          to="/b/$botId/bot"
                          params={{ botId }}
                        >
                          Configure model
                        </Link>
                      )}
                    </li>
                  );
                })}
                {heldEvents ? <HeldEventRows {...heldEvents} /> : null}
              </ul>
            </BoardSection>
          ) : null}

          {blockedTasks.length > 0 ? (
            <BoardSection title="Blocked" count={blockedTasks.length}>
              <ul className="divide-y divide-border">
                {blockedTasks.map((task) => (
                  <TaskRow key={task.id} task={task} bot={botOf(task.assigneeBotId)} />
                ))}
              </ul>
            </BoardSection>
          ) : null}

          {activeTasks.length > 0 ? (
            <BoardSection title="Active" count={activeTasks.length}>
              <ul className="divide-y divide-border">
                {activeTasks.map((task) => (
                  <TaskRow key={task.id} task={task} bot={botOf(task.assigneeBotId)} />
                ))}
              </ul>
            </BoardSection>
          ) : null}

          {resultGroups.length > 0 ? (
            <BoardSection
              title="Updates"
              count={results.length}
              action={
                results.length > 1 ? (
                  <Button
                    variant="ghost"
                    size="xs"
                    disabled={dismissAll.isPending}
                    onClick={() => dismissAll.mutate(results.map((result) => result.runId))}
                  >
                    Mark all done
                  </Button>
                ) : null
              }
            >
              {dismissAll.error ? <Failure title="Could not clear updates" error={dismissAll.error} /> : null}
              <ul className="divide-y divide-border">
                {resultGroups.map((group) => (
                  <ResultGroupRow key={group.automationId} group={group} bot={botOf(group.botId)} />
                ))}
              </ul>
            </BoardSection>
          ) : null}

          {laterTasks.length > 0 ? (
            <BoardSection title="Later" count={laterTasks.length}>
              <ul className="divide-y divide-border">
                {laterTasks.map((task) => (
                  <TaskRow key={task.id} task={task} bot={botOf(task.assigneeBotId)} />
                ))}
              </ul>
            </BoardSection>
          ) : null}

          {tasks.isSuccess && summary.isSuccess && !held.isPending && !hasAnyContent ? (
            <p className="py-4 text-[14px] text-muted-foreground">All caught up.</p>
          ) : null}

          {completedTasks.length > 0 ? (
            <section aria-label="Completed">
              <button
                type="button"
                aria-expanded={showCompleted}
                onClick={() => setShowCompleted(!showCompleted)}
                className="flex items-center gap-1.5 py-1 text-[13px] font-medium text-muted-foreground transition-colors hover:text-foreground"
              >
                {showCompleted ? (
                  <ChevronDown className="size-4" aria-hidden />
                ) : (
                  <ChevronRight className="size-4" aria-hidden />
                )}
                <span>Completed</span>
                <span className="font-mono text-[12px]">{completedTasks.length}</span>
              </button>
              {showCompleted ? (
                <ul className="mt-1 divide-y divide-border">
                  {completedTasks.map((task) => (
                    <TaskRow key={task.id} task={task} bot={botOf(task.assigneeBotId)} muted />
                  ))}
                </ul>
              ) : null}
            </section>
          ) : null}
        </div>
      ) : null}
    </FramedPage>
  );
}

function BoardSection({
  title,
  count,
  action,
  children,
}: {
  title: string;
  count: number;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={title}>
      <div className="mb-1 flex items-center justify-between gap-2 border-b border-border pb-2">
        <div className="flex items-center gap-2">
          <h2 className="text-[12.5px] font-medium text-muted-foreground">{title}</h2>
          <span className="font-mono text-[12px] text-muted-foreground">{count}</span>
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}

function QuickAddTask({ bot, bots }: { bot?: string; bots: Bot[] }) {
  const [title, setTitle] = useState("");
  const [selected, setSelected] = useState("");
  const client = useQueryClient();
  const assigneeBotId = selected || bots.find((item) => item.id === bot)?.id || bots[0]?.id || "";

  const create = useMutation({
    mutationFn: () =>
      createTask({
        body: { title: title.trim(), assigneeBotId, status: "ready" },
        throwOnError: true,
      }),
    onSuccess: async () => {
      setTitle("");
      await invalidateTasks(client);
    },
  });

  const canSubmit = Boolean(assigneeBotId && title.trim() && !create.isPending);

  return (
    <div className="space-y-2">
      <form
        className="flex items-center gap-2 rounded-card border border-border bg-card px-3 py-1.5 transition-shadow focus-within:ring-1 focus-within:ring-ring"
        onSubmit={(event) => {
          event.preventDefault();

          if (canSubmit) create.mutate();
        }}
      >
        <Plus className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <input
          type="text"
          aria-label="Task title"
          placeholder="New task, press Enter to create…"
          value={title}
          maxLength={160}
          disabled={create.isPending}
          onChange={(event) => setTitle(event.target.value)}
          className="h-8 min-w-0 flex-1 bg-transparent text-[14px] text-foreground placeholder:text-muted-foreground focus:outline-none"
        />
        {bots.length > 0 ? (
          <select
            aria-label="Assignee Bot"
            value={assigneeBotId}
            disabled={create.isPending}
            onChange={(event) => setSelected(event.target.value)}
            className="h-7 max-w-36 shrink-0 truncate rounded-control bg-muted/60 px-2 text-[12.5px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none"
          >
            {bots.map((choice) => (
              <option key={choice.id} value={choice.id}>
                {choice.name}
              </option>
            ))}
          </select>
        ) : null}
        {title.trim() ? (
          <Button type="submit" size="xs" disabled={!canSubmit}>
            Add
          </Button>
        ) : null}
      </form>
      {create.error ? <Failure title="Could not create task" error={create.error} /> : null}
    </div>
  );
}

function TaskRow({
  task,
  bot,
  muted = false,
}: {
  task: TaskSummary;
  bot: Pick<Bot, "id" | "name">;
  muted?: boolean;
}) {
  const hint = taskStatusHint(task);

  return (
    <li>
      <Link
        to="/board/$taskKey"
        params={{ taskKey: task.key }}
        className="flex items-center gap-3 rounded-control px-2 py-2.5 transition-colors hover:bg-accent/50"
      >
        <span aria-hidden className={taskStatusDotClass(task)} />
        <span className="w-10 shrink-0 font-mono text-[12px] text-muted-foreground">{task.key}</span>
        <span
          className={
            muted
              ? "min-w-0 flex-1 truncate text-[14px] text-muted-foreground"
              : "min-w-0 flex-1 truncate text-[14px] font-medium text-foreground"
          }
        >
          {task.title}
        </span>
        <div className="flex shrink-0 items-center gap-2 text-[12.5px] text-muted-foreground">
          <span
            className={
              hint.urgent
                ? "rounded-chip bg-orange-tint px-1.5 py-0.5 text-[11.5px] font-medium text-orange"
                : "rounded-chip bg-muted px-1.5 py-0.5 text-[11.5px] text-muted-foreground"
            }
          >
            {hint.label}
          </span>
          <BotAvatar bot={bot} className="size-5 text-[10px]" />
          <span className="hidden max-w-24 truncate sm:inline">{bot.name}</span>
          <span aria-hidden className="hidden md:inline">
            ·
          </span>
          <span className="hidden text-[12px] md:inline">
            {formatDistanceToNow(task.updatedAt, { addSuffix: true })}
          </span>
        </div>
      </Link>
    </li>
  );
}

type TaskHint = { label: string; urgent?: boolean };

function taskStatusHint(task: TaskSummary): TaskHint {
  if (task.status === "in_review" && task.reviewerKind === "owner") {
    return { label: "Review", urgent: true };
  }

  if (task.blocked) {
    return { label: "Blocked", urgent: true };
  }

  if (task.status === "in_review") {
    return { label: "Bot review" };
  }

  return { label: statusLabel[task.status] };
}

function taskStatusDotClass(task: TaskSummary) {
  if (task.blocked || (task.status === "in_review" && task.reviewerKind === "owner")) {
    return "size-2 shrink-0 rounded-full bg-orange";
  }

  if (task.status === "in_progress" || task.status === "done") {
    return "size-2 shrink-0 rounded-full bg-green";
  }

  if (task.status === "ready") {
    return "size-2 shrink-0 rounded-full border-[1.5px] border-foreground/60";
  }

  return "size-2 shrink-0 rounded-full bg-muted-foreground/35";
}
