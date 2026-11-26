import { useRef, useState } from "react";
import { formatDistanceToNow } from "date-fns";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  ChevronDown,
  ExternalLink,
  GitBranch,
  GitPullRequest,
  Link2,
  ListTree,
  MessageSquare,
  MoreHorizontal,
  Plus,
  Terminal,
  UserCheck,
} from "lucide-react";
import {
  getHealthOptions,
  getTaskOptions,
  listAgentsOptions,
  listTaskExecutionsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import {
  addTaskDependency,
  createTaskEntry,
  getMainChat,
  removeTaskDependency,
  reviewTask,
  sendMessage,
  splitTask,
  updateTask,
} from "../api/generated/sdk.gen";
import type {
  Agent,
  TaskDetail,
  TaskEntry,
  TaskExecution,
  TaskLink,
  TaskQuestion,
  TaskRef,
  TaskReview,
  TaskReviewerKind,
  TaskStatus,
  TaskSummary,
  TaskUpdate,
  TaskWait,
} from "../api/generated/types.gen";
import { AgentAvatar } from "../components/agent-avatar";
import { Modal } from "../components/dialog";
import { Failure, Loading } from "../components/feedback";
import { Field, Select } from "../components/form";
import { MarkdownBody } from "../components/markdown-body";
import { FramedPage } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../components/ui/dropdown-menu";
import { Input } from "../components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "../components/ui/popover";
import { Textarea } from "../components/ui/textarea";
import { GitHubPullForm } from "../plugins/github/pull-form";
import { invalidateTasks } from "../lib/task-stream";
import { isClosed, nextStatuses, statusLabel } from "../lib/tasks";
import { errorText, refreshChats } from "../lib/queries";
import { newId } from "../lib/utils";

class TaskConflict extends Error {}

type ActiveDialog = "rename" | "reviewer" | "subtask" | "dependency" | "github" | null;

export function TaskDetailPage({ taskKey }: { taskKey: string }) {
  const detail = useQuery(getTaskOptions({ path: { taskKey } }));
  const bots = useQuery(listAgentsOptions()).data?.agents ?? [];

  if (detail.isPending) {
    return (
      <FramedPage
        headerTitle="Board"
        title={taskKey}
        eyebrow={
          <Link className="hover:text-foreground" to="/board">
            ← Board
          </Link>
        }
      >
        <Loading />
      </FramedPage>
    );
  }

  if (detail.isError || !detail.data) {
    return (
      <FramedPage
        headerTitle="Board"
        title={taskKey}
        eyebrow={
          <Link className="hover:text-foreground" to="/board">
            ← Board
          </Link>
        }
      >
        <Failure title="Could not load task" error={detail.error} retry={() => void detail.refetch()} />
      </FramedPage>
    );
  }

  return <TaskWorkspace detail={detail.data} bots={bots} />;
}

function TaskWorkspace({ detail, bots }: { detail: TaskDetail; bots: Agent[] }) {
  const { task } = detail;
  const open = !isClosed(task.status);
  const client = useQueryClient();
  const health = useQuery(getHealthOptions());
  const [dialog, setDialog] = useState<ActiveDialog>(null);

  const botById = new Map<string | undefined, Agent>(bots.map((bot) => [bot.id, bot]));

  const botOf = (id: string) => {
    const found = botById.get(id);

    return { id, name: found?.name ?? id };
  };

  const assignee = botOf(task.assigneeBotId);

  const externalRunsQuery = useQuery({
    ...listTaskExecutionsOptions({ query: { taskKey: task.key, kind: "external_agent" } }),
    refetchInterval: ({ state }) =>
      state.data?.executions.some((run) => run.state === "running") ? 5000 : false,
  });

  const externalRuns = externalRunsQuery.data?.executions ?? [];

  const patchTask = useMutation({
    mutationFn: async (changes: TaskUpdate) => {
      const { error, response } = await updateTask({
        path: { taskKey: task.key },
        headers: { "If-Match": task.revision },
        body: changes,
      });

      if (response?.status === 412 || response?.status === 409) {
        await invalidateTasks(client);
        throw new TaskConflict("This task changed before your update. Please try again.");
      }

      if (error) throw new Error(errorText(error));
    },
    onSuccess: () => invalidateTasks(client),
  });

  const canLinkGitHub = open && Boolean(health.data?.plugins.includes("github"));

  return (
    <FramedPage
      headerTitle="Board"
      title={task.title}
      eyebrow={
        <Link className="hover:text-foreground" to="/board">
          ← Board
        </Link>
      }
      badge={
        <Badge variant="outline" className="font-mono text-[12px] font-normal">
          {task.key}
        </Badge>
      }
      actions={
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button variant="ghost" size="icon-sm" aria-label="Task actions">
                <MoreHorizontal className="size-4" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem onClick={() => setDialog("rename")}>Rename task</DropdownMenuItem>
            <DropdownMenuItem onClick={() => setDialog("reviewer")}>Set reviewer</DropdownMenuItem>
            {detail.canSplit ? (
              <DropdownMenuItem onClick={() => setDialog("subtask")}>Add subtask</DropdownMenuItem>
            ) : null}
            {detail.parentTask ? (
              <DropdownMenuItem onClick={() => setDialog("dependency")}>Add dependency</DropdownMenuItem>
            ) : null}
            {canLinkGitHub ? (
              <DropdownMenuItem onClick={() => setDialog("github")}>Link GitHub PR</DropdownMenuItem>
            ) : null}
            <DropdownMenuSeparator />
            {open ? (
              <DropdownMenuItem
                variant="destructive"
                disabled={patchTask.isPending}
                onClick={() => patchTask.mutate({ status: "cancelled" })}
              >
                Cancel task
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem
                disabled={patchTask.isPending}
                onClick={() => patchTask.mutate({ status: "in_progress" })}
              >
                Reopen task
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      }
    >
      <div className="space-y-6">
        <TaskPropertyStrip
          task={task}
          assignee={assignee}
          bots={bots}
          botOf={botOf}
          pending={patchTask.isPending}
          canSplit={detail.canSplit}
          hasParent={Boolean(detail.parentTask)}
          canLinkGitHub={canLinkGitHub}
          onPatch={(changes) => patchTask.mutate(changes)}
          onOpenDialog={setDialog}
        />

        {patchTask.error ? <Failure title="Could not update task" error={patchTask.error} /> : null}

        {task.status === "in_review" ? (
          <ReviewActionCard task={task} entries={detail.entries} assigneeName={assignee.name} />
        ) : null}

        <BlockerActionCard detail={detail} />

        {externalRunsQuery.isError ? (
          <Failure
            title="Could not load external runs"
            error={externalRunsQuery.error}
            retry={() => void externalRunsQuery.refetch()}
          />
        ) : null}

        <TaskContextCards
          detail={detail}
          externalRuns={externalRuns}
          open={open}
          canSplit={detail.canSplit}
          canLinkGitHub={canLinkGitHub}
          onOpenDialog={setDialog}
        />

        <TaskTimeline entries={detail.entries} botOf={botOf} />

        <ThreadComposer task={task} assigneeName={assignee.name} />
      </div>

      {dialog === "rename" ? (
        <RenameDialog
          task={task}
          pending={patchTask.isPending}
          onClose={() => setDialog(null)}
          onSave={async (nextTitle) => {
            await patchTask.mutateAsync({ title: nextTitle });
            setDialog(null);
          }}
        />
      ) : null}

      {dialog === "reviewer" ? (
        <ReviewerDialog
          task={task}
          bots={bots}
          pending={patchTask.isPending}
          onClose={() => setDialog(null)}
          onSave={async (reviewerKind, reviewerId) => {
            await patchTask.mutateAsync({ reviewerKind, reviewerId });
            setDialog(null);
          }}
        />
      ) : null}

      {dialog === "subtask" ? (
        <SubtaskDialog taskKey={task.key} onClose={() => setDialog(null)} onCreated={() => setDialog(null)} />
      ) : null}

      {dialog === "dependency" && detail.parentTask ? (
        <DependencyDialog
          task={task}
          parentKey={detail.parentTask.key}
          dependencies={detail.dependencies}
          onClose={() => setDialog(null)}
          onAdded={() => setDialog(null)}
        />
      ) : null}

      {dialog === "github" ? (
        <Modal
          title="Link GitHub PR"
          onClose={() => setDialog(null)}
          footer={
            <Button variant="ghost" onClick={() => setDialog(null)}>
              Close
            </Button>
          }
        >
          <GitHubPullForm
            path={{ agentId: task.assigneeBotId, taskId: task.id }}
            onLinked={() => setDialog(null)}
          />
        </Modal>
      ) : null}
    </FramedPage>
  );
}

function statusDotClass(status: TaskStatus) {
  if (status === "in_review") return "size-2 shrink-0 rounded-full bg-orange";

  if (status === "in_progress" || status === "done") return "size-2 shrink-0 rounded-full bg-green";

  if (status === "ready") return "size-2 shrink-0 rounded-full border-[1.5px] border-foreground/65";

  return "size-2 shrink-0 rounded-full bg-muted-foreground/40";
}

function TaskPropertyStrip({
  task,
  assignee,
  bots,
  botOf,
  pending,
  canSplit,
  hasParent,
  canLinkGitHub,
  onPatch,
  onOpenDialog,
}: {
  task: TaskSummary;
  assignee: Pick<Agent, "id" | "name">;
  bots: Agent[];
  botOf: (id: string) => Pick<Agent, "id" | "name">;
  pending: boolean;
  canSplit: boolean;
  hasParent: boolean;
  canLinkGitHub: boolean;
  onPatch: (changes: TaskUpdate) => void;
  onOpenDialog: (dialog: ActiveDialog) => void;
}) {
  const [assigneeOpen, setAssigneeOpen] = useState(false);
  const [statusOpen, setStatusOpen] = useState(false);
  const validNext = nextStatuses(task.status, task.reviewerKind !== "");
  const statusOptions: TaskStatus[] = [task.status, ...validNext];

  const reviewerLabel =
    task.reviewerKind === "owner"
      ? "Owner"
      : task.reviewerKind === "bot" && task.reviewerId
        ? botOf(task.reviewerId).name
        : "None";

  const pillClass =
    "inline-flex h-8 items-center gap-1.5 rounded-control border border-border bg-card px-2.5 text-[13px] text-foreground transition-colors hover:bg-accent disabled:opacity-50";

  return (
    <div className="space-y-2.5 rounded-card border border-border bg-muted/25 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Popover open={statusOpen} onOpenChange={setStatusOpen}>
          <PopoverTrigger disabled={pending} className={pillClass} aria-label="Status">
            <span aria-hidden className={statusDotClass(task.status)} />
            <span className="font-medium">{statusLabel[task.status]}</span>
            <ChevronDown className="size-3.5 text-muted-foreground" aria-hidden />
          </PopoverTrigger>
          <PopoverContent align="start" className="w-44 p-1">
            {statusOptions.map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => {
                  setStatusOpen(false);

                  if (option !== task.status) onPatch({ status: option });
                }}
                className="flex w-full items-center justify-between gap-2 rounded-control px-2 py-1.5 text-left text-[13px] hover:bg-accent"
              >
                <span className="inline-flex items-center gap-2">
                  <span aria-hidden className={statusDotClass(option)} />
                  <span>{statusLabel[option]}</span>
                </span>
                {option === task.status ? <Check className="size-3.5 text-foreground" /> : null}
              </button>
            ))}
          </PopoverContent>
        </Popover>

        <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
          <PopoverTrigger disabled={pending} className={pillClass} aria-label="Assignee Bot">
            <AgentAvatar agent={assignee} className="size-4 text-[9px]" />
            <span className="font-medium">{assignee.name}</span>
            <ChevronDown className="size-3.5 text-muted-foreground" aria-hidden />
          </PopoverTrigger>
          <PopoverContent align="start" className="w-48 p-1">
            {bots.map((bot) => (
              <button
                key={bot.id}
                type="button"
                onClick={() => {
                  setAssigneeOpen(false);

                  if (bot.id !== task.assigneeBotId) onPatch({ assigneeBotId: bot.id });
                }}
                className="flex w-full items-center gap-2 rounded-control px-2 py-1.5 text-left text-[13px] hover:bg-accent"
              >
                <AgentAvatar agent={bot} className="size-4 text-[9px]" />
                <span className="min-w-0 flex-1 truncate">{bot.name}</span>
                {bot.id === task.assigneeBotId ? <Check className="size-3.5 text-foreground" /> : null}
              </button>
            ))}
          </PopoverContent>
        </Popover>

        <button
          type="button"
          disabled={pending}
          onClick={() => onOpenDialog("reviewer")}
          className={pillClass}
        >
          <UserCheck className="size-3.5 text-muted-foreground" aria-hidden />
          <span className="text-muted-foreground">Reviewer:</span>
          <span className="font-medium">{reviewerLabel}</span>
        </button>

        {task.blocked ? (
          <span className="inline-flex h-8 items-center rounded-control bg-orange-tint px-2.5 text-[12.5px] font-medium text-orange">
            Blocked
          </span>
        ) : null}

        <span
          className="ml-auto text-[12px] text-muted-foreground"
          title={`Created ${new Date(task.createdAt).toLocaleString()} · Updated ${new Date(task.updatedAt).toLocaleString()}`}
        >
          Updated {formatDistanceToNow(task.updatedAt, { addSuffix: true })}
        </span>
      </div>

      {canSplit || hasParent || canLinkGitHub ? (
        <div className="flex flex-wrap items-center gap-1.5 border-t border-border/70 pt-2">
          {canSplit ? (
            <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("subtask")}>
              <Plus className="size-3.5" />
              Subtask
            </Button>
          ) : null}
          {hasParent ? (
            <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("dependency")}>
              <Plus className="size-3.5" />
              Dependency
            </Button>
          ) : null}
          {canLinkGitHub ? (
            <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("github")}>
              <GitPullRequest className="size-3.5" />
              Link PR
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ReviewActionCard({
  task,
  entries,
  assigneeName,
}: {
  task: TaskSummary;
  entries: TaskEntry[];
  assigneeName: string;
}) {
  const [requesting, setRequesting] = useState(false);
  const [feedback, setFeedback] = useState("");
  const client = useQueryClient();

  const reviewEntry = entries.find((entry) => entry.seq === task.reviewSeq);

  const review = useMutation({
    mutationFn: async (decision: TaskReview["decision"]) => {
      const { error, response } = await reviewTask({
        path: { taskKey: task.key },
        body: {
          decision,
          reviewSeq: decision === "approve" ? task.reviewSeq : undefined,
          note: feedback.trim() || undefined,
        },
      });

      if (response?.status === 409) {
        await invalidateTasks(client);
        throw new TaskConflict("The review changed. Check the latest update before trying again.");
      }

      if (error) throw new Error(errorText(error));
    },
    onSuccess: async () => {
      setFeedback("");
      setRequesting(false);
      await invalidateTasks(client);
    },
  });

  return (
    <section
      aria-label="In Review"
      className="space-y-3 rounded-card border border-orange/30 bg-orange-tint/40 p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[13.5px] font-medium text-foreground">
          {assigneeName} requested review
          {reviewEntry ? (
            <span className="font-normal text-muted-foreground">
              {" "}
              · {formatDistanceToNow(reviewEntry.createdAt, { addSuffix: true })}
            </span>
          ) : null}
        </p>
        {!requesting ? (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={review.isPending} onClick={() => review.mutate("approve")}>
              Approve
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={review.isPending}
              onClick={() => setRequesting(true)}
            >
              Request changes…
            </Button>
          </div>
        ) : null}
      </div>

      {requesting ? (
        <div className="space-y-2.5">
          <Textarea
            aria-label="Review feedback"
            placeholder="Describe what needs to change…"
            value={feedback}
            maxLength={32000}
            onChange={(event) => setFeedback(event.target.value)}
          />
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={!feedback.trim() || review.isPending}
              onClick={() => review.mutate("request_changes")}
            >
              Request changes
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={review.isPending}
              onClick={() => setRequesting(false)}
            >
              Cancel
            </Button>
          </div>
        </div>
      ) : null}

      {review.error ? <Failure title="Could not submit review" error={review.error} /> : null}
    </section>
  );
}

function BlockerActionCard({ detail }: { detail: TaskDetail }) {
  const client = useQueryClient();

  const answer = useMutation({
    mutationFn: ({ question, text }: { question: TaskQuestion; text: string }) =>
      sendMessage({
        path: { agentId: question.agentId, chatId: question.conversationId },
        body: { id: newId(), text, answersQuestionId: question.id },
        throwOnError: true,
      }),
    onSuccess: (_, { question }) => Promise.all([invalidateTasks(client), refreshChats(question.agentId)]),
  });

  const waits = detail.blockers.filter((blocker) => blocker.kind === "wait");

  if (!detail.questions.length && !waits.length) return null;

  return (
    <section
      aria-label={detail.task.blocked ? "Blocked" : "Questions"}
      className="space-y-3 rounded-card border border-orange/30 bg-orange-tint/40 p-4"
    >
      {waits.map((wait) => (
        <p key={wait.waitId} className="text-[13px] text-foreground">
          Waiting for <span className="font-medium">{wait.eventType}</span> on {wait.objectType}{" "}
          {wait.objectKey} · <span className="text-muted-foreground">{wait.namespace}</span>
        </p>
      ))}
      {detail.questions.map((question) => (
        <div key={question.id} className="space-y-2.5">
          <p className="whitespace-pre-wrap text-[14px] font-medium text-foreground">{question.text}</p>
          <div className="flex flex-wrap gap-2">
            {Array.from(new Set(question.options)).map((text) => (
              <Button
                key={text}
                variant="secondary"
                size="sm"
                disabled={answer.isPending}
                onClick={() => answer.mutate({ question, text })}
              >
                {text}
              </Button>
            ))}
          </div>
        </div>
      ))}
      {answer.error ? <Failure title="Could not answer question" error={answer.error} /> : null}
    </section>
  );
}

function waitState(wait: TaskWait, open: boolean) {
  if (wait.resolved) return "Received";

  return open ? "Waiting" : "Paused";
}

function TaskContextCards({
  detail,
  externalRuns,
  open,
  canSplit,
  canLinkGitHub,
  onOpenDialog,
}: {
  detail: TaskDetail;
  externalRuns: TaskExecution[];
  open: boolean;
  canSplit: boolean;
  canLinkGitHub: boolean;
  onOpenDialog: (dialog: ActiveDialog) => void;
}) {
  const client = useQueryClient();

  const removeDep = useMutation({
    mutationFn: (depKey: string) =>
      removeTaskDependency({ path: { taskKey: detail.task.key, depKey }, throwOnError: true }),
    onSuccess: () => invalidateTasks(client),
  });

  const hasHierarchy =
    Boolean(detail.parentTask) || detail.subtasks.length > 0 || detail.dependencies.length > 0;

  const hasRuns = detail.sessions.length > 0 || externalRuns.length > 0;
  const hasLinks = detail.links.length > 0;

  if (!hasHierarchy && !hasRuns && !hasLinks) return null;

  const doneSubtasks = detail.subtasks.filter((subtask) => subtask.status === "done").length;

  return (
    <div className="space-y-3">
      {hasRuns ? (
        <div className="rounded-card border border-border bg-card">
          {detail.sessions.length > 0 ? (
            <section aria-label="Sessions" className="p-3.5">
              <div className="mb-2 flex items-center gap-1.5 text-[12px] font-medium text-muted-foreground">
                <MessageSquare className="size-3.5" aria-hidden />
                <span>Sessions</span>
                <span className="font-mono">{detail.sessions.length}</span>
              </div>
              <ul className="divide-y divide-border/70">
                {detail.sessions.map((session) => (
                  <li
                    key={session.conversationId}
                    className="flex flex-wrap items-center justify-between gap-2 py-2 text-[13px] first:pt-0 last:pb-0"
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span
                        aria-hidden
                        className={
                          session.state === "running"
                            ? "size-2 shrink-0 rounded-full bg-green"
                            : "size-2 shrink-0 rounded-full bg-muted-foreground/35"
                        }
                      />
                      <Link
                        to="/a/$agentId/c/$chatId"
                        params={{ agentId: session.agentId, chatId: String(session.conversationId) }}
                        className="truncate font-medium text-foreground hover:underline"
                      >
                        {session.title || "Untitled session"}
                      </Link>
                      {session.isPrimary ? <Badge variant="secondary">Primary</Badge> : null}
                    </div>
                    <div className="flex shrink-0 items-center gap-2 text-[12px] text-muted-foreground">
                      <span>{session.agentName}</span>
                      <span aria-hidden>·</span>
                      <span className="capitalize">{session.state}</span>
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          {externalRuns.length > 0 ? (
            <section
              aria-label="External runs"
              className={detail.sessions.length > 0 ? "border-t border-border p-3.5" : "p-3.5"}
            >
              <div className="mb-2 flex items-center gap-1.5 text-[12px] font-medium text-muted-foreground">
                <Terminal className="size-3.5" aria-hidden />
                <span>External runs</span>
                <span className="font-mono">{externalRuns.length}</span>
              </div>
              <ul className="divide-y divide-border/70">
                {externalRuns.map((run) => (
                  <li key={run.id} className="space-y-1 py-2 text-[13px] first:pt-0 last:pb-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        aria-hidden
                        className={
                          run.state === "running"
                            ? "size-2 shrink-0 rounded-full bg-green"
                            : run.state === "failed" || run.state === "stale"
                              ? "size-2 shrink-0 rounded-full bg-red"
                              : "size-2 shrink-0 rounded-full bg-muted-foreground/35"
                        }
                      />
                      <span className="font-medium text-foreground">{run.harness ?? "External agent"}</span>
                      <Badge variant="outline" className="capitalize">
                        {run.state}
                      </Badge>
                      {run.gitBranch ? (
                        <span className="inline-flex items-center gap-1 font-mono text-[12px] text-muted-foreground">
                          <GitBranch className="size-3" aria-hidden />
                          {run.gitBranch}
                        </span>
                      ) : null}
                      {run.lastHeartbeatAt ? (
                        <span className="ml-auto text-[12px] text-muted-foreground">
                          Heartbeat {formatDistanceToNow(run.lastHeartbeatAt, { addSuffix: true })}
                        </span>
                      ) : null}
                    </div>
                    <p className="text-[12px] text-muted-foreground">
                      {run.runnerName ? `Registered Runner: ${run.runnerName}` : "No registered Runner"}
                    </p>
                    {run.hostname || run.workspacePath ? (
                      <p className="break-all font-mono text-[12px] text-muted-foreground">
                        Reported host: {run.hostname ?? "unknown"} · Workspace:{" "}
                        {run.workspacePath ?? "unknown"}
                      </p>
                    ) : null}
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </div>
      ) : null}

      {hasHierarchy ? (
        <div className="space-y-3 rounded-card border border-border bg-card p-3.5 text-[13px]">
          {detail.parentTask ? (
            <section aria-label="Parent Task" className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-[12px] font-medium text-muted-foreground">Parent</span>
              <TaskInlineLink task={detail.parentTask} />
            </section>
          ) : null}

          {detail.subtasks.length > 0 ? (
            <section
              aria-label="Subtasks"
              className={detail.parentTask ? "border-t border-border/70 pt-3" : undefined}
            >
              <div className="mb-2 flex items-center justify-between gap-2">
                <div className="flex items-center gap-1.5 text-[12px] font-medium text-muted-foreground">
                  <ListTree className="size-3.5" aria-hidden />
                  <span>Subtasks</span>
                  <span className="font-mono">
                    {doneSubtasks}/{detail.subtasks.length}
                  </span>
                </div>
                {canSplit ? (
                  <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("subtask")}>
                    <Plus className="size-3" />
                    Add
                  </Button>
                ) : null}
              </div>
              <ul className="divide-y divide-border/70">
                {detail.subtasks.map((subtask) => (
                  <li key={subtask.key} className="py-1.5 first:pt-0 last:pb-0">
                    <TaskInlineLink task={subtask} />
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          {detail.dependencies.length > 0 ? (
            <section
              aria-label="Dependencies"
              className={
                detail.parentTask || detail.subtasks.length > 0 ? "border-t border-border/70 pt-3" : undefined
              }
            >
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="text-[12px] font-medium text-muted-foreground">
                  Depends on ({detail.dependencies.length})
                </span>
                {detail.parentTask ? (
                  <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("dependency")}>
                    <Plus className="size-3" />
                    Add
                  </Button>
                ) : null}
              </div>
              <ul className="divide-y divide-border/70">
                {detail.dependencies.map((dep) => (
                  <li
                    key={dep.key}
                    className="flex items-center justify-between gap-2 py-1.5 first:pt-0 last:pb-0"
                  >
                    <TaskInlineLink task={dep} />
                    <Button
                      size="xs"
                      variant="ghost"
                      disabled={removeDep.isPending}
                      onClick={() => removeDep.mutate(dep.key)}
                    >
                      Remove
                    </Button>
                  </li>
                ))}
              </ul>
              {removeDep.error ? (
                <Failure title="Could not remove dependency" error={removeDep.error} />
              ) : null}
            </section>
          ) : null}
        </div>
      ) : null}

      {hasLinks ? (
        <section aria-label="Links" className="rounded-card border border-border bg-card p-3.5 text-[13px]">
          <div className="mb-2 flex items-center justify-between gap-2">
            <div className="flex items-center gap-1.5 text-[12px] font-medium text-muted-foreground">
              <Link2 className="size-3.5" aria-hidden />
              <span>Links</span>
              <span className="font-mono">{detail.links.length}</span>
            </div>
            {canLinkGitHub ? (
              <Button type="button" variant="ghost" size="xs" onClick={() => onOpenDialog("github")}>
                <Plus className="size-3" />
                Link PR
              </Button>
            ) : null}
          </div>
          <ul className="divide-y divide-border/70">
            {detail.links.map((link) => (
              <RelatedLinkItem key={`${link.namespace}:${link.type}:${link.key}`} link={link} open={open} />
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

function TaskInlineLink({ task }: { task: TaskRef }) {
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
      <Link
        className="inline-flex min-w-0 items-center gap-2 font-medium text-foreground hover:underline [overflow-wrap:anywhere]"
        to="/board/$taskKey"
        params={{ taskKey: task.key }}
      >
        <span aria-hidden className={statusDotClass(task.status)} />
        <span className="font-mono text-[12px] font-normal text-muted-foreground">{task.key}</span>
        <span className="truncate">{task.title}</span>
      </Link>
      <span className="rounded-chip bg-muted px-1.5 py-0.5 text-[11.5px] text-muted-foreground">
        {statusLabel[task.status]}
      </span>
    </div>
  );
}

function RelatedLinkItem({ link, open }: { link: TaskLink; open: boolean }) {
  return (
    <li className="space-y-1 py-2 first:pt-0 last:pb-0">
      <p className="break-words font-medium text-foreground">
        {link.type} {link.key} · <span className="font-normal text-muted-foreground">{link.namespace}</span>
      </p>
      {link.waits.length ? (
        <ul className="space-y-1 text-[12px] text-muted-foreground">
          {link.waits.map((wait) => {
            const label = waitState(wait, open);

            return (
              <li key={wait.id} className="flex flex-wrap items-center gap-1.5 break-words">
                <span
                  className={
                    wait.resolved
                      ? "rounded-chip bg-green-tint px-1.5 py-0.5 text-[11px] font-medium text-green"
                      : "rounded-chip bg-orange-tint px-1.5 py-0.5 text-[11px] font-medium text-orange"
                  }
                >
                  {label} {wait.event}
                </span>
                <span>{wait.revision ? <code>{wait.revision}</code> : "Any revision"}</span>
              </li>
            );
          })}
        </ul>
      ) : null}
    </li>
  );
}

const entryKindLabel: Record<TaskEntry["kind"], string> = {
  note: "Note",
  artifact: "Artifact",
  status: "Status",
  assign: "Assignment",
  review: "Review",
  execution: "Session",
  external: "External",
};

function TaskTimeline({
  entries,
  botOf,
}: {
  entries: TaskEntry[];
  botOf: (id: string) => Pick<Agent, "id" | "name">;
}) {
  return (
    <section aria-label="Timeline" className="space-y-3">
      <div className="flex items-center gap-2 border-b border-border pb-2">
        <h2 className="text-[12.5px] font-medium text-muted-foreground">Timeline</h2>
        <span className="font-mono text-[12px] text-muted-foreground">{entries.length}</span>
      </div>

      {entries.length === 0 ? (
        <p className="py-2 text-[13.5px] text-muted-foreground">No activity yet.</p>
      ) : (
        <ol className="relative space-y-3 pl-6 before:absolute before:top-2 before:bottom-2 before:left-[7px] before:w-px before:bg-border">
          {entries.map((entry) => {
            const at = formatDistanceToNow(entry.createdAt, { addSuffix: true });
            const exactTime = new Date(entry.createdAt).toLocaleString();
            const compact = isCompactSystemEntry(entry);

            if (compact) {
              return (
                <li
                  key={entry.id}
                  className="relative flex items-baseline justify-between gap-3 py-1 text-[12.5px] text-muted-foreground"
                >
                  <span
                    aria-hidden
                    className="absolute top-2.5 -left-[21px] size-2 rounded-full border border-muted-foreground/50 bg-background"
                  />
                  <span className="min-w-0 [overflow-wrap:anywhere]">
                    {compactEntrySummary(entry, botOf)}
                  </span>
                  <time
                    dateTime={new Date(entry.createdAt).toISOString()}
                    title={exactTime}
                    className="shrink-0 text-[11.5px]"
                  >
                    {at}
                  </time>
                </li>
              );
            }

            const isExternal = entry.trust === "external" || entry.actorKind === "external_agent";
            const bot = entry.trust === "agent" && entry.actorBotId ? botOf(entry.actorBotId) : null;

            const authorLabel = isExternal
              ? "External"
              : bot
                ? bot.name
                : entry.actorKind === "system"
                  ? "System"
                  : entry.actorKind === "unknown"
                    ? "Unknown"
                    : "You";

            return (
              <li key={entry.id} className="relative">
                <span
                  aria-hidden
                  className={
                    isExternal
                      ? "absolute top-3.5 -left-[21px] size-2 rounded-full bg-muted-foreground/70"
                      : "absolute top-3.5 -left-[21px] size-2 rounded-full bg-foreground/75"
                  }
                />
                <div className="overflow-hidden rounded-card border border-border bg-card">
                  <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border/60 bg-muted/25 px-3.5 py-2 text-[12.5px]">
                    <div className="flex flex-wrap items-center gap-1.5">
                      {bot ? <AgentAvatar agent={bot} className="size-4 text-[9px]" /> : null}
                      {isExternal ? (
                        <Badge variant="outline" className="text-[11px]">
                          External
                        </Badge>
                      ) : (
                        <span className="font-medium text-foreground">{authorLabel}</span>
                      )}
                      {entry.kind !== "external" ? (
                        <span className="rounded-chip bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                          {entryKindLabel[entry.kind]}
                        </span>
                      ) : null}
                      {entry.decision ? (
                        <span
                          className={
                            entry.decision === "approve"
                              ? "rounded-chip bg-green-tint px-1.5 py-0.5 text-[11px] font-medium text-green"
                              : "rounded-chip bg-orange-tint px-1.5 py-0.5 text-[11px] font-medium text-orange"
                          }
                        >
                          {entry.decision === "approve" ? "Approved" : "Requested changes"}
                        </span>
                      ) : null}
                      {entry.tag ? (
                        <Badge variant="secondary" className="text-[11px]">
                          {entry.tag}
                        </Badge>
                      ) : null}
                    </div>
                    <time
                      dateTime={new Date(entry.createdAt).toISOString()}
                      title={exactTime}
                      className="shrink-0 text-[11.5px] text-muted-foreground"
                    >
                      {at}
                    </time>
                  </div>
                  {entry.content ? (
                    <div className="px-3.5 py-3">
                      <MarkdownBody content={entry.content} />
                    </div>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}

function isCompactSystemEntry(entry: TaskEntry) {
  if (entry.kind === "external" || entry.kind === "note") return false;

  if (entry.kind === "review" && entry.content.trim()) return false;

  if (entry.kind === "artifact" && entry.content.trim()) return false;

  return true;
}

function compactEntrySummary(entry: TaskEntry, botOf: (id: string) => Pick<Agent, "id" | "name">) {
  const actor =
    entry.trust === "agent" && entry.actorBotId
      ? botOf(entry.actorBotId).name
      : entry.actorKind === "system"
        ? "System"
        : "You";

  if (entry.kind === "status" && entry.status) {
    return `${actor} moved to ${statusLabel[entry.status]}`;
  }

  if (entry.kind === "review") {
    return entry.decision === "approve" ? `${actor} approved` : `${actor} requested changes`;
  }

  if (entry.content.trim()) return `${actor}: ${entry.content.trim()}`;

  if (entry.kind === "assign") return `${actor} updated assignee`;

  if (entry.kind === "execution") return `${actor} updated session`;

  return `${actor} updated task`;
}

function ThreadComposer({ task, assigneeName }: { task: TaskSummary; assigneeName: string }) {
  const [text, setText] = useState("");
  const [sentNotice, setSentNotice] = useState<string | null>(null);
  const pending = useRef<{ id: string; text: string; agentId: string } | null>(null);
  const client = useQueryClient();
  const open = !isClosed(task.status);

  const appendNote = useMutation({
    mutationFn: () =>
      createTaskEntry({ path: { taskKey: task.key }, body: { content: text.trim() }, throwOnError: true }),
    onSuccess: async () => {
      setText("");
      setSentNotice(null);
      await invalidateTasks(client);
    },
  });

  const sendToBot = useMutation({
    mutationFn: async () => {
      const message = text.trim();
      const agentId = task.assigneeBotId;

      const id =
        pending.current?.text === message && pending.current.agentId === agentId
          ? pending.current.id
          : newId();

      pending.current = { id, text: message, agentId };
      const main = await getMainChat({ path: { agentId }, throwOnError: true });
      await sendMessage({
        path: { agentId, chatId: main.data.id },
        body: { id, text: message, taskId: task.id },
        throwOnError: true,
      });

      return agentId;
    },
    onSuccess: async (agentId) => {
      pending.current = null;
      setText("");
      setSentNotice(agentId);
      await invalidateTasks(client);
    },
  });

  const busy = appendNote.isPending || sendToBot.isPending;

  const submitDefault = () => {
    if (busy || !text.trim()) return;

    if (open) sendToBot.mutate();
    else appendNote.mutate();
  };

  return (
    <div className="space-y-2 border-t border-border pt-4">
      <form
        className="space-y-2.5 rounded-card border border-border bg-card p-3.5 transition-shadow focus-within:ring-1 focus-within:ring-ring"
        onSubmit={(event) => {
          event.preventDefault();
          submitDefault();
        }}
      >
        <Textarea
          aria-label="Task note"
          placeholder={open ? `Message ${assigneeName} or add a note…` : "Add a note…"}
          value={text}
          maxLength={32000}
          disabled={busy}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
              event.preventDefault();
              submitDefault();
            }
          }}
          className="min-h-20 border-0 bg-transparent p-0 text-[14px] shadow-none focus-visible:ring-0"
        />
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/60 pt-2.5">
          <span className="text-[11.5px] text-muted-foreground">⌘↵ to send</span>
          <div className="flex flex-wrap items-center gap-2">
            {open ? (
              <>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  disabled={!text.trim() || busy}
                  onClick={() => appendNote.mutate()}
                >
                  Note only
                </Button>
                <Button type="submit" size="sm" disabled={!text.trim() || busy}>
                  Send to {assigneeName}
                </Button>
              </>
            ) : (
              <Button type="submit" size="sm" disabled={!text.trim() || busy}>
                Add note
              </Button>
            )}
          </div>
        </div>
      </form>
      {sentNotice ? (
        <p className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
          <span>Sent to {assigneeName}.</span>
          <Link
            to="/a/$agentId"
            params={{ agentId: sentNotice }}
            className="inline-flex items-center gap-1 text-foreground hover:underline"
          >
            Open Main
            <ExternalLink className="size-3" />
          </Link>
        </p>
      ) : null}
      {appendNote.error ? <Failure title="Could not add note" error={appendNote.error} /> : null}
      {sendToBot.error ? <Failure title="Could not send message" error={sendToBot.error} /> : null}
    </div>
  );
}

function RenameDialog({
  task,
  pending,
  onClose,
  onSave,
}: {
  task: TaskSummary;
  pending: boolean;
  onClose: () => void;
  onSave: (title: string) => Promise<void>;
}) {
  const [title, setTitle] = useState(task.title);

  return (
    <Modal
      title="Rename task"
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" disabled={pending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!title.trim() || title.trim() === task.title || pending}
            onClick={() => void onSave(title.trim())}
          >
            Save
          </Button>
        </>
      }
    >
      <Field label="Title">
        {({ id }) => (
          <Input
            id={id}
            value={title}
            maxLength={160}
            autoFocus
            onChange={(event) => setTitle(event.target.value)}
          />
        )}
      </Field>
    </Modal>
  );
}

function ReviewerDialog({
  task,
  bots,
  pending,
  onClose,
  onSave,
}: {
  task: TaskSummary;
  bots: Agent[];
  pending: boolean;
  onClose: () => void;
  onSave: (kind: TaskReviewerKind, id: string) => Promise<void>;
}) {
  const [kind, setKind] = useState<TaskReviewerKind>(task.reviewerKind);
  const [botId, setBotId] = useState(task.reviewerId || bots[0]?.id || "");

  return (
    <Modal
      title="Set reviewer"
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" disabled={pending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={(kind === "bot" && !botId) || pending}
            onClick={() => void onSave(kind, kind === "bot" ? botId : "")}
          >
            Save
          </Button>
        </>
      }
    >
      <Field label="Reviewer">
        {({ id }) => (
          <Select
            id={id}
            value={kind}
            onChange={(event) => {
              const value = event.target.value;
              const nextKind: TaskReviewerKind = value === "owner" || value === "bot" ? value : "";
              setKind(nextKind);
            }}
          >
            <option value="">None</option>
            <option value="owner">Human Owner</option>
            <option value="bot">Bot</option>
          </Select>
        )}
      </Field>
      {kind === "bot" ? (
        <Field label="Reviewer Bot">
          {({ id }) => (
            <Select id={id} value={botId} onChange={(event) => setBotId(event.target.value)}>
              {bots.map((bot) => (
                <option key={bot.id} value={bot.id}>
                  {bot.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      ) : null}
    </Modal>
  );
}

function SubtaskDialog({
  taskKey,
  onClose,
  onCreated,
}: {
  taskKey: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [title, setTitle] = useState("");
  const client = useQueryClient();

  const split = useMutation({
    mutationFn: (childTitle: string) =>
      splitTask({ path: { taskKey }, body: { title: childTitle }, throwOnError: true }),
    onSuccess: async () => {
      await invalidateTasks(client);
      onCreated();
    },
  });

  return (
    <Modal
      title="Add subtask"
      pending={split.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" disabled={split.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!title.trim() || split.isPending} onClick={() => split.mutate(title.trim())}>
            Add subtask
          </Button>
        </>
      }
    >
      <Field label="Subtask title">
        {({ id }) => (
          <Input
            id={id}
            value={title}
            maxLength={160}
            autoFocus
            placeholder="Subtask title"
            onChange={(event) => setTitle(event.target.value)}
          />
        )}
      </Field>
      {split.error ? <Failure title="Could not add subtask" error={split.error} /> : null}
    </Modal>
  );
}

function DependencyDialog({
  task,
  parentKey,
  dependencies,
  onClose,
  onAdded,
}: {
  task: TaskSummary;
  parentKey: string;
  dependencies: TaskRef[];
  onClose: () => void;
  onAdded: () => void;
}) {
  const client = useQueryClient();
  const parent = useQuery(getTaskOptions({ path: { taskKey: parentKey } }));
  const [selected, setSelected] = useState("");

  const siblings = (parent.data?.subtasks ?? []).filter(
    (sibling) =>
      sibling.key !== task.key && !dependencies.some((dependency) => dependency.key === sibling.key),
  );

  const add = useMutation({
    mutationFn: (depKey: string) =>
      addTaskDependency({
        path: { taskKey: task.key },
        body: { dependsOn: depKey },
        throwOnError: true,
      }),
    onSuccess: async () => {
      await invalidateTasks(client);
      onAdded();
    },
  });

  return (
    <Modal
      title="Add dependency"
      description="This task waits for incomplete tasks in its dependency list."
      pending={add.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" disabled={add.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!selected || add.isPending} onClick={() => add.mutate(selected)}>
            Add dependency
          </Button>
        </>
      }
    >
      {siblings.length ? (
        <Field label="Sibling task">
          {({ id }) => (
            <Select id={id} value={selected} onChange={(event) => setSelected(event.target.value)}>
              <option value="">Select a sibling task…</option>
              {siblings.map((sibling) => (
                <option key={sibling.key} value={sibling.key}>
                  {sibling.key} · {sibling.title}
                </option>
              ))}
            </Select>
          )}
        </Field>
      ) : (
        <p className="text-[13px] text-muted-foreground">No available sibling tasks to depend on.</p>
      )}
      {add.error ? <Failure title="Could not add dependency" error={add.error} /> : null}
    </Modal>
  );
}
