import { useState } from "react";
import { formatDistanceToNow } from "date-fns";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronRight, Plus, X } from "lucide-react";
import { listAgentsOptions, listTasksOptions } from "../api/generated/@tanstack/react-query.gen";
import { createTask, dismissInboxItem } from "../api/generated/sdk.gen";
import type { Agent, InboxItem, TaskSummary } from "../api/generated/types.gen";
import { AgentAvatar } from "../components/agent-avatar";
import { QuestionRow, ResultGroupRow, TaskReviewRow, groupResults } from "../components/board-results";
import { Failure, Loading } from "../components/feedback";
import { HeldEventRows } from "../components/held-events";
import { FramedPage } from "../components/page";
import { Button, buttonVariants } from "../components/ui/button";
import { heldEventsOptions } from "../lib/inbox";
import { attentionItems, inboxSummaryOptions } from "../lib/inbox-summary";
import { refreshInbox } from "../lib/queries";
import { invalidateTasks, useTaskStream } from "../lib/task-stream";
import { isClosed } from "../lib/tasks";

// Add a Bot filter control here when active tasks routinely exceed 30.
export function BoardPage({ bot, onBotChange }: { bot?: string; onBotChange: (bot?: string) => void }) {
  useTaskStream();
  const [showCompleted, setShowCompleted] = useState(false);
  const agents = useQuery(listAgentsOptions());
  const summary = useQuery(inboxSummaryOptions());
  const held = useQuery(heldEventsOptions());
  const tasks = useQuery(listTasksOptions({ query: { botId: bot, includeClosed: true } }));

  const bots = agents.data?.agents ?? [];
  const botById = new Map<string | undefined, Agent>(bots.map((agent) => [agent.id, agent]));

  const botOf = (id: string, fallbackName?: string) => {
    const found = botById.get(id);

    return { id, name: found?.name ?? fallbackName ?? id };
  };

  const rawAttention = attentionItems(summary.data);

  const questions = bot
    ? rawAttention.questions.filter((item) => item.agentId === bot)
    : rawAttention.questions;

  const results = bot ? rawAttention.results.filter((item) => item.agentId === bot) : rawAttention.results;
  const setup = bot ? rawAttention.setup.filter((item) => item.agentId === bot) : rawAttention.setup;
  const heldEvents = bot ? [] : (held.data?.events ?? []);
  const resultGroups = groupResults(results);

  const allTasks = tasks.data?.tasks ?? [];
  const reviewTasks: TaskSummary[] = [];
  const blockedTasks: TaskSummary[] = [];
  const activeTasks: TaskSummary[] = [];
  const laterTasks: TaskSummary[] = [];
  const completedTasks: TaskSummary[] = [];

  for (const task of allTasks) {
    if (isClosed(task.status)) {
      completedTasks.push(task);
    } else if (task.status === "in_review" && task.reviewerKind === "owner") {
      reviewTasks.push(task);
    } else if (task.blocked) {
      blockedTasks.push(task);
    } else if (task.status === "backlog") {
      laterTasks.push(task);
    } else {
      activeTasks.push(task);
    }
  }

  const needsYouCount =
    questions.length + reviewTasks.length + blockedTasks.length + setup.length + heldEvents.length;

  const hasAnyContent =
    needsYouCount > 0 ||
    activeTasks.length > 0 ||
    resultGroups.length > 0 ||
    laterTasks.length > 0 ||
    completedTasks.length > 0;

  const dismissAll = useMutation({
    mutationFn: async (items: InboxItem[]) => {
      await Promise.all(
        items.map((item) => dismissInboxItem({ path: { runId: item.runId }, throwOnError: true })),
      );
      await refreshInbox();
    },
  });

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
                {questions.map(({ agentId, chat, question }) => (
                  <QuestionRow
                    key={`${agentId}:${chat.id}:${question.id}`}
                    agent={botOf(agentId)}
                    chat={chat}
                    questionId={question.id}
                    text={question.text}
                    options={question.options}
                  />
                ))}
                {reviewTasks.map((task) => (
                  <TaskReviewRow key={task.key} task={task} agent={botOf(task.assigneeBotId)} />
                ))}
                {blockedTasks.map((task) => (
                  <TaskRow key={task.id} task={task} agent={botOf(task.assigneeBotId)} />
                ))}
                {setup.map(({ agentId, automation }) => {
                  const agent = botOf(agentId);

                  return (
                    <li
                      key={`${agentId}:${automation.id}`}
                      className="flex flex-wrap items-center justify-between gap-3 py-3.5"
                    >
                      <div className="flex min-w-0 items-center gap-2.5">
                        <AlertTriangle className="size-4 shrink-0 text-orange" aria-hidden />
                        <AgentAvatar agent={agent} className="size-5 text-[10px]" />
                        <div className="min-w-0">
                          <p className="truncate text-[14px] font-medium text-foreground">
                            {automation.name}
                          </p>
                          <p className="text-[12.5px] text-muted-foreground">
                            {agent.name} · No available model
                          </p>
                        </div>
                      </div>
                      <Link
                        className={buttonVariants({ variant: "secondary", size: "sm" })}
                        to="/a/$agentId/agent"
                        params={{ agentId }}
                      >
                        Configure model
                      </Link>
                    </li>
                  );
                })}
                <HeldEventRows events={heldEvents} />
              </ul>
            </BoardSection>
          ) : null}

          {activeTasks.length > 0 ? (
            <BoardSection title="Active" count={activeTasks.length}>
              <ul className="divide-y divide-border">
                {activeTasks.map((task) => (
                  <TaskRow key={task.id} task={task} agent={botOf(task.assigneeBotId)} />
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
                    onClick={() => dismissAll.mutate(results)}
                  >
                    Mark all done
                  </Button>
                ) : null
              }
            >
              {dismissAll.error ? <Failure title="Could not clear updates" error={dismissAll.error} /> : null}
              <ul className="divide-y divide-border">
                {resultGroups.map((group) => (
                  <ResultGroupRow
                    key={group.automationId}
                    group={group}
                    agent={botOf(group.agentId, group.agentName)}
                  />
                ))}
              </ul>
            </BoardSection>
          ) : null}

          {laterTasks.length > 0 ? (
            <BoardSection title="Later" count={laterTasks.length}>
              <ul className="divide-y divide-border">
                {laterTasks.map((task) => (
                  <TaskRow key={task.id} task={task} agent={botOf(task.assigneeBotId)} />
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
                    <TaskRow key={task.id} task={task} agent={botOf(task.assigneeBotId)} muted />
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

function QuickAddTask({ bot, bots }: { bot?: string; bots: Agent[] }) {
  const [title, setTitle] = useState("");
  const [selected, setSelected] = useState("");
  const client = useQueryClient();
  const assigneeBotId = selected || bots.find((agent) => agent.id === bot)?.id || bots[0]?.id || "";

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
            {bots.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}
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
  agent,
  muted = false,
}: {
  task: TaskSummary;
  agent: Pick<Agent, "id" | "name">;
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
          <AgentAvatar agent={agent} className="size-5 text-[10px]" />
          <span className="hidden max-w-28 truncate sm:inline">{agent.name}</span>
          <span aria-hidden className="hidden sm:inline">
            ·
          </span>
          <span className={hint.urgent ? "font-medium text-orange" : undefined}>{hint.text}</span>
        </div>
      </Link>
    </li>
  );
}

type TaskHint = { text: string; urgent?: boolean };

function taskStatusHint(task: TaskSummary): TaskHint {
  if (task.status === "in_review" && task.reviewerKind === "owner") {
    return { text: "Waiting for review", urgent: true };
  }

  if (task.blocked) {
    return { text: "Blocked", urgent: true };
  }

  if (task.status === "in_review") {
    return { text: "Bot review" };
  }

  if (task.status === "done") {
    return { text: "Done" };
  }

  if (task.status === "cancelled") {
    return { text: "Cancelled" };
  }

  return { text: formatDistanceToNow(task.updatedAt, { addSuffix: true }) };
}
