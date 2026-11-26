import { useState } from "react";
import { formatDistanceToNow } from "date-fns";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronRight, ExternalLink } from "lucide-react";
import type {
  Bot,
  AutomationResult,
  AutomationResultSummary,
  BoardAttentionSession,
  TaskSummary,
} from "../api/generated/types.gen";
import { getTask, reviewTask, sendMessage } from "../api/generated/sdk.gen";
import { errorText, refreshBoardAttention, refreshSessions } from "../lib/queries";
import {
  automationResultsOptions,
  useContinueResult,
  useDismissResults,
  useReadResults,
  useReadResultsOnOpen,
} from "../lib/automation-results";
import { invalidateTasks } from "../lib/task-stream";
import { newId } from "../lib/utils";
import { BotAvatar } from "./bot-avatar";
import { MarkdownBody } from "./markdown-body";
import { Empty, FramedPage } from "./page";
import { Failure, Loading } from "./feedback";
import { Badge } from "./ui/badge";
import { Button, buttonVariants } from "./ui/button";

export type ResultGroup = {
  automationId: string;
  automationName: string;
  botId: string;
  runs: AutomationResultSummary[];
  hasFailure: boolean;
  hasUnread: boolean;
};

export function groupResults(items: AutomationResultSummary[]): ResultGroup[] {
  const byAutomation = new Map<string, ResultGroup>();

  for (const item of items) {
    const existing = byAutomation.get(item.automationId);

    if (existing) {
      existing.runs.push(item);

      if (item.outcome === "failed") existing.hasFailure = true;

      if (!item.read) existing.hasUnread = true;
    } else {
      byAutomation.set(item.automationId, {
        automationId: item.automationId,
        automationName: item.automationName,
        botId: item.botId,
        runs: [item],
        hasFailure: item.outcome === "failed",
        hasUnread: !item.read,
      });
    }
  }

  return Array.from(byAutomation.values());
}

function firstLine(text: string) {
  const trimmed = text.trim();

  if (!trimmed) return "";

  const newline = trimmed.indexOf("\n");
  const line = newline === -1 ? trimmed : trimmed.slice(0, newline).trim();

  return line
    .replace(/^#{1,6}\s+/, "")
    .replace(/\*\*(.+?)\*\*/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .trim();
}

function shortTime(timestamp: number) {
  return formatDistanceToNow(timestamp, { addSuffix: true });
}

export function QuestionRow({
  bot,
  session,
  questionId,
  text,
  options,
}: {
  bot: { id: string; name: string };
  session: BoardAttentionSession;
  questionId: string;
  text: string;
  options: string[];
}) {
  const answer = useMutation({
    mutationFn: (choice: string) =>
      sendMessage({
        path: { botId: bot.id, sessionId: session.id },
        body: { id: newId(), text: choice, answersQuestionId: questionId },
        throwOnError: true,
      }),
    onSuccess: () => Promise.all([refreshBoardAttention(), refreshSessions(bot.id)]),
  });

  const uniqueOptions = Array.from(new Set(options));

  return (
    <li className="space-y-2.5 py-3.5">
      <div className="flex items-start gap-2.5">
        <span aria-hidden className="mt-2 size-1.5 shrink-0 rounded-full bg-orange" />
        <BotAvatar bot={bot} className="mt-0.5 size-5 text-[10px]" />
        <div className="min-w-0 flex-1">
          <p className="whitespace-pre-wrap break-words text-[14px] font-medium text-foreground">{text}</p>
          <p className="mt-0.5 text-[12.5px] text-muted-foreground">
            {bot.name} · {session.main ? "Main" : session.title || "Session"}
          </p>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 pl-9">
        {uniqueOptions.map((choice) => (
          <Button
            key={choice}
            size="sm"
            variant="secondary"
            disabled={answer.isPending}
            onClick={() => answer.mutate(choice)}
          >
            {choice}
          </Button>
        ))}
        {session.main ? (
          <Link
            className={buttonVariants({ variant: "ghost", size: "sm" })}
            to="/b/$botId"
            params={{ botId: bot.id }}
          >
            Open session
          </Link>
        ) : (
          <Link
            className={buttonVariants({ variant: "ghost", size: "sm" })}
            to="/b/$botId/s/$sessionId"
            params={{ botId: bot.id, sessionId: String(session.id) }}
          >
            Open session
          </Link>
        )}
      </div>
      {answer.error ? (
        <div className="pl-9">
          <Failure title="Could not send answer" error={answer.error} />
        </div>
      ) : null}
    </li>
  );
}

export function TaskReviewRow({
  task,
  bot,
}: {
  task: Pick<TaskSummary, "key" | "title">;
  bot: Pick<Bot, "id" | "name">;
}) {
  const client = useQueryClient();

  const approve = useMutation({
    mutationFn: async () => {
      const detail = await getTask({ path: { taskKey: task.key }, throwOnError: true });

      const { error, response } = await reviewTask({
        path: { taskKey: task.key },
        body: { decision: "approve", reviewSeq: detail.data.task.reviewSeq },
      });

      if (response?.status === 409) {
        throw new Error("This review changed. Open the task to inspect the latest update.");
      }

      if (error) throw new Error(errorText(error));
    },
    onSuccess: () => invalidateTasks(client),
  });

  return (
    <li className="space-y-2.5 py-3.5">
      <div className="flex items-start gap-2.5">
        <span aria-hidden className="mt-2 size-1.5 shrink-0 rounded-full bg-orange" />
        <BotAvatar bot={bot} className="mt-0.5 size-5 text-[10px]" />
        <div className="min-w-0 flex-1">
          <Link
            to="/board/$taskKey"
            params={{ taskKey: task.key }}
            className="block text-[14px] font-medium text-foreground hover:underline [overflow-wrap:anywhere]"
          >
            <span className="mr-1.5 font-mono text-[12.5px] font-normal text-muted-foreground">
              {task.key}
            </span>
            {task.title}
          </Link>
          <p className="mt-0.5 text-[12.5px] text-muted-foreground">{bot.name} · Waiting for your review</p>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 pl-9">
        <Button size="sm" disabled={approve.isPending} onClick={() => approve.mutate()}>
          Approve
        </Button>
        <Link
          className={buttonVariants({ variant: "ghost", size: "sm" })}
          to="/board/$taskKey"
          params={{ taskKey: task.key }}
        >
          View
        </Link>
      </div>
      {approve.error ? (
        <div className="pl-9">
          <Failure title="Could not approve task" error={approve.error} />
        </div>
      ) : null}
    </li>
  );
}

export function ResultGroupRow({ group, bot }: { group: ResultGroup; bot: Pick<Bot, "id" | "name"> }) {
  const [expanded, setExpanded] = useState(false);
  const latest = group.runs[0];
  const earlier = group.runs.slice(1);
  const read = useReadResults();
  const dismiss = useDismissResults();
  const next = useContinueResult();
  const dismissGroup = () => dismiss.mutate(group.runs.map((run) => run.runId));

  const toggle = () => {
    const opening = !expanded;
    setExpanded(opening);

    if (opening && latest && !latest.read && !read.isPending) {
      read.mutate(latest.runId);
    }
  };

  if (!latest) return null;

  const preview = firstLine(latest.summary);
  const busy = dismiss.isPending || next.isPending;

  return (
    <li className="py-1">
      <div className="group flex items-start gap-2 rounded-control px-1.5 py-2.5 transition-colors hover:bg-accent/50">
        <button
          type="button"
          aria-expanded={expanded}
          onClick={toggle}
          className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
        >
          <span
            aria-hidden
            className={
              group.hasFailure
                ? "mt-2 size-1.5 shrink-0 rounded-full bg-red"
                : group.hasUnread
                  ? "mt-2 size-1.5 shrink-0 rounded-full bg-orange"
                  : "mt-2 size-1.5 shrink-0 rounded-full border border-muted-foreground/40"
            }
          />
          <BotAvatar bot={bot} className="mt-0.5 size-5 text-[10px]" />
          <div className="min-w-0 flex-1">
            <div className="flex items-baseline justify-between gap-3">
              <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                <span className="truncate text-[14px] font-medium text-foreground">
                  {group.automationName}
                </span>
                {group.runs.length > 1 ? (
                  <span className="rounded-chip bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                    ×{group.runs.length}
                  </span>
                ) : null}
                {group.hasFailure ? (
                  <span className="rounded-chip bg-red-tint px-1.5 py-0.5 text-[11px] font-medium text-red">
                    Failed
                  </span>
                ) : null}
              </div>
              <time
                dateTime={new Date(latest.createdAt).toISOString()}
                className="shrink-0 text-[12px] text-muted-foreground"
              >
                {shortTime(latest.createdAt)}
              </time>
            </div>
            {!expanded && preview ? (
              <p className="mt-0.5 truncate text-[13px] text-muted-foreground">{preview}</p>
            ) : null}
          </div>
          {expanded ? (
            <ChevronDown className="mt-1 size-4 shrink-0 text-muted-foreground" aria-hidden />
          ) : (
            <ChevronRight className="mt-1 size-4 shrink-0 text-muted-foreground" aria-hidden />
          )}
        </button>
        {!expanded ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={`Mark ${group.automationName} done`}
            title="Done"
            disabled={busy}
            className="mt-0.5 shrink-0 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
            onClick={dismissGroup}
          >
            <Check className="size-3.5" />
          </Button>
        ) : null}
      </div>

      {expanded ? (
        <div className="space-y-3 pb-3 pl-10 pr-2">
          {latest.summary ? (
            <MarkdownBody content={latest.summary} />
          ) : (
            <p className="text-[13.5px] text-muted-foreground">No summary.</p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={busy} onClick={dismissGroup}>
              {group.runs.length > 1 ? `Done (${group.runs.length})` : "Done"}
            </Button>
            {latest.sessionId ? (
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                onClick={() => next.mutate({ result: latest })}
              >
                Continue in session
              </Button>
            ) : null}
            {latest.sessionId ? (
              <Link
                to="/b/$botId/s/$sessionId"
                params={{ botId: latest.botId, sessionId: String(latest.sessionId) }}
                className={buttonVariants({ variant: "ghost", size: "sm" })}
              >
                Run transcript
                <ExternalLink className="size-3" />
              </Link>
            ) : null}
            <Link
              to="/b/$botId/automations/$automationId"
              params={{ botId: latest.botId, automationId: latest.automationId }}
              className={buttonVariants({ variant: "ghost", size: "sm" })}
            >
              Automation
            </Link>
          </div>
          {read.isError ? (
            <Failure
              title="Could not mark result read"
              error={read.error}
              retry={() => read.mutate(latest.runId)}
            />
          ) : null}
          {next.error ? <Failure title="Could not continue in session" error={next.error} /> : null}

          {earlier.length > 0 ? (
            <div className="border-t border-border pt-2.5">
              <p className="mb-1.5 text-[11.5px] font-medium text-muted-foreground">
                Earlier ({earlier.length})
              </p>
              <ul className="space-y-1.5">
                {earlier.map((run) => (
                  <li
                    key={run.runId}
                    className="flex items-start justify-between gap-2 text-[12.5px] text-muted-foreground"
                  >
                    <div className="min-w-0 flex-1">
                      <span className="mr-2 font-mono text-[11.5px]">{shortTime(run.createdAt)}</span>
                      {run.outcome === "failed" ? (
                        <span className="mr-1.5 font-medium text-red">Failed</span>
                      ) : null}
                      <Link
                        to="/board/results/$runId"
                        params={{ runId: run.runId }}
                        className="text-foreground/85 hover:underline [overflow-wrap:anywhere]"
                      >
                        {firstLine(run.summary) || "View result"}
                      </Link>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label="Dismiss earlier run"
                      disabled={busy}
                      onClick={() => dismiss.mutate(run.runId)}
                    >
                      <Check className="size-3" />
                    </Button>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
      {dismiss.error ? <Failure title="Could not mark done" error={dismiss.error} /> : null}
    </li>
  );
}

export function BoardResultPage({ runId }: { runId: string }) {
  const results = useQuery(automationResultsOptions());

  return (
    <BoardResultDetail
      key={runId}
      item={results.data?.results.find((item) => item.runId === runId)}
      pending={results.isPending}
      error={results.error}
    />
  );
}

function BoardResultDetail({
  item,
  pending,
  error,
}: {
  item?: AutomationResult;
  pending: boolean;
  error: unknown;
}) {
  const navigate = useNavigate();
  const read = useReadResultsOnOpen(item ? [item] : []);
  const done = useDismissResults();
  const next = useContinueResult();

  return (
    <FramedPage
      headerTitle="Board"
      eyebrow={
        <Link className="hover:text-foreground" to="/board">
          ← Board
        </Link>
      }
      title={item?.automationName ?? "Update"}
      description={item ? `${item.botName} · ${new Date(item.createdAt).toLocaleString()}` : undefined}
    >
      {pending ? (
        <Loading />
      ) : error ? (
        <Failure title="Could not load result" error={error} />
      ) : !item ? (
        <Empty title="Result not found">This run is no longer listed.</Empty>
      ) : (
        <div className="space-y-5">
          {item.outcome === "failed" || item.dismissed ? (
            <div className="flex items-center gap-2">
              {item.outcome === "failed" ? (
                <span className="rounded-chip bg-red-tint px-2 py-0.5 text-[12px] font-medium text-red">
                  Failed
                </span>
              ) : null}
              {item.dismissed ? <Badge variant="outline">Done</Badge> : null}
            </div>
          ) : null}
          <MarkdownBody content={item.summary} />
          <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
            <Button
              onClick={() => done.mutate(item.runId, { onSuccess: () => void navigate({ to: "/board" }) })}
              disabled={item.dismissed || done.isPending || next.isPending}
            >
              Done
            </Button>
            {item.sessionId ? (
              <Button
                variant="secondary"
                onClick={() => next.mutate({ result: item })}
                disabled={next.isPending || done.isPending}
              >
                Continue in session
              </Button>
            ) : null}
            {item.sessionId ? (
              <Link
                to="/b/$botId/s/$sessionId"
                params={{ botId: item.botId, sessionId: String(item.sessionId) }}
                className={buttonVariants({ variant: "ghost" })}
              >
                Run transcript
              </Link>
            ) : null}
            <Link
              to="/b/$botId/automations/$automationId"
              params={{ botId: item.botId, automationId: item.automationId }}
              className={buttonVariants({ variant: "ghost" })}
            >
              Open automation
            </Link>
          </div>
          {read.isError ? (
            <Failure
              title="Could not mark result read"
              error={read.error}
              retry={() => read.mutate(item.runId)}
            />
          ) : null}
          {done.isError ? <Failure title="Could not dismiss result" error={done.error} /> : null}
          {next.isError ? <Failure title="Could not continue in session" error={next.error} /> : null}
        </div>
      )}
    </FramedPage>
  );
}
