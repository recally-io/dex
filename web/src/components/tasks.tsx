import { useEffect, useState, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Task, TaskList, TaskStage, TaskStageStatus, TaskStatus } from "../api/generated/types.gen";
import {
  cancelTaskMutation,
  getTaskOptions,
  listTasksOptions,
  listTasksQueryKey,
} from "../api/generated/@tanstack/react-query.gen";
import { Button } from "./atoms/Button";
import { StatusPill } from "./atoms/StatusPill";
import { Failure, Loading } from "./feedback";
import { TaskTranscript } from "./task-transcript";

const uuid = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
const stageLink = new RegExp(`^#task-(${uuid})-stage-(${uuid})$`);

function readHash() {
  return window.location.hash.toLowerCase();
}

function subscribeHash(onChange: () => void) {
  window.addEventListener("hashchange", onChange);

  return () => window.removeEventListener("hashchange", onChange);
}

const labels = {
  active: "Active",
  queued: "Queued",
  running: "Running",
  needs_input: "Needs input",
  done: "Done",
  failed: "Failed",
  cancelling: "Cancel requested",
  unknown: "Execution unknown",
  cancelled: "Cancelled",
} satisfies Record<TaskStatus | TaskStageStatus, string>;

const tones = {
  active: "accent",
  queued: "neutral",
  running: "accent",
  needs_input: "orange",
  done: "green",
  failed: "red",
  cancelling: "orange",
  unknown: "orange",
  cancelled: "neutral",
} as const;

function isClosed(status: TaskStatus) {
  return status === "done" || status === "failed" || status === "cancelled";
}

function TaskPill({ status }: { status: TaskStatus | TaskStageStatus }) {
  return (
    <StatusPill tone={tones[status]} className="h-5 shrink-0 px-2 text-[11.5px]">
      {labels[status]}
    </StatusPill>
  );
}

function EventTime({ at }: { at: number }) {
  const date = new Date(at);

  return <time dateTime={date.toISOString()}>{date.toLocaleString()}</time>;
}

function Report({ text }: { text: string }) {
  return (
    <div className="whitespace-pre-wrap text-[13px] leading-relaxed text-ink-2 [overflow-wrap:anywhere]">
      {text}
    </div>
  );
}

function Stage({ stage, task }: { stage: TaskStage; task: Task }) {
  const fragment = `task-${task.id}-stage-${stage.id}`;
  const [open, setOpen] = useState(() => readHash() === `#${fragment}`);
  const [linkError, setLinkError] = useState("");
  useEffect(() => {
    const reveal = () => {
      if (readHash() !== `#${fragment}`) return;
      setOpen(true);
      const element = document.getElementById(fragment);
      element?.scrollIntoView({ block: "nearest" });
    };

    reveal();
    window.addEventListener("hashchange", reveal);

    return () => window.removeEventListener("hashchange", reveal);
  }, [fragment]);

  const copyLink = async () => {
    try {
      const url = new URL(window.location.href);
      url.hash = fragment;
      await navigator.clipboard.writeText(url.href);
      setLinkError("");
    } catch {
      setLinkError("Could not copy the link. Use the stage link below.");
    }
  };

  return (
    <div id={fragment} className="space-y-2 rounded-card bg-inset px-3 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="min-w-0 text-[13px] font-medium text-ink [overflow-wrap:anywhere]">{stage.title}</h4>
        <TaskPill status={stage.status} />
      </div>
      <dl className="space-y-1 text-[12px] text-ink-3 [overflow-wrap:anywhere]">
        <div>
          <dt className="inline">Model: </dt>
          <dd className="inline">
            {stage.provider}/{stage.modelId} · {stage.thinkingLevel}
          </dd>
        </div>
        <div>
          <dt className="inline">Agent: </dt>
          <dd className="inline font-mono">{stage.agentId}</dd>
        </div>
        <div>
          <dt className="inline">Current tool: </dt>
          <dd className="inline">{stage.currentTool || "None"}</dd>
        </div>
        <div>
          <dt className="inline">Last event: </dt>
          <dd className="inline">
            <EventTime at={stage.lastEventAt} />
          </dd>
        </div>
      </dl>
      {stage.report ? <Report text={stage.report} /> : null}
      <div className="flex items-center gap-3 text-[12px]">
        <a href={`#${fragment}`} className="text-ink-2 underline">
          Stage link
        </a>
        <Button type="button" size="xs" variant="secondary" onClick={() => void copyLink()}>
          Copy stage link
        </Button>
      </div>
      {linkError ? (
        <p role="alert" className="text-[12px] text-ink-2">
          {linkError}
        </p>
      ) : null}
      <details open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary className="cursor-pointer text-[12.5px] font-medium text-ink">
          Execution transcript · read only
        </summary>
        {open ? (
          <TaskTranscript key={stage.id} chatId={task.chatId} taskId={task.id} stageId={stage.id} />
        ) : null}
      </details>
    </div>
  );
}

function TaskCard({ task }: { task: Task }) {
  const [stagesOpen, setStagesOpen] = useState(() => readHash().startsWith(`#task-${task.id}-stage-`));

  useEffect(() => {
    const reveal = () => {
      if (readHash().startsWith(`#task-${task.id}-stage-`)) setStagesOpen(true);
    };

    window.addEventListener("hashchange", reveal);

    return () => window.removeEventListener("hashchange", reveal);
  }, [task.id]);

  const client = useQueryClient();
  const key = listTasksQueryKey({ path: { chatId: task.chatId } });

  const cancel = useMutation({
    ...cancelTaskMutation(),
    onSuccess: (updated) => {
      client.setQueryData<TaskList>(key, (previous) =>
        previous
          ? {
              ...previous,
              tasks: previous.tasks.map((item) => (item.id === updated.id ? updated : item)),
            }
          : previous,
      );
    },
    onSettled: () => client.invalidateQueries({ queryKey: key }),
  });

  const stage = task.stages.at(-1);
  const waiting = task.status === "active" && (!stage || stage.status === "done");
  const requestCancel = () => cancel.mutate({ path: { chatId: task.chatId, taskId: task.id } });

  return (
    <article
      aria-label={`Task ${task.shortId}: ${task.title}`}
      className="space-y-3 rounded-card border border-line bg-surface p-4 shadow-card"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] text-ink-3">{task.shortId}</span>
        <TaskPill status={task.status} />
        {!isClosed(task.status) ? (
          <Button
            type="button"
            size="xs"
            variant="secondary"
            className="ml-auto"
            disabled={cancel.isPending || task.status === "cancelling"}
            onClick={requestCancel}
          >
            {cancel.isPending
              ? "Requesting cancel…"
              : task.status === "cancelling"
                ? "Cancel requested"
                : "Cancel task"}
          </Button>
        ) : null}
      </div>
      <h3 className="text-[14px] font-semibold text-ink [overflow-wrap:anywhere]">{task.title}</h3>
      <p className="text-[12px] text-ink-3">
        Last event: <EventTime at={task.lastEventAt} />
      </p>
      {stage ? (
        <div className="space-y-1 text-[12px] text-ink-2 [overflow-wrap:anywhere]">
          <p>
            Latest stage: {stage.title} · {labels[stage.status]}
          </p>
          <p>
            Model: {stage.provider}/{stage.modelId} · {stage.thinkingLevel}
          </p>
          <p>Current tool: {stage.currentTool || "None"}</p>
        </div>
      ) : null}
      {waiting ? <p className="text-[12.5px] text-ink-2">Waiting for the next stage or summary.</p> : null}
      {task.status === "cancelling" ? (
        <p className="text-[12.5px] text-ink-2">
          Cancellation accepted. Execution has not yet been confirmed stopped. Previous tool actions are not
          undone.
        </p>
      ) : null}
      {task.status === "unknown" ? (
        <p className="text-[12.5px] text-ink-2">
          Execution status is unknown. The task may still be running.
        </p>
      ) : null}
      {task.status === "needs_input" ? (
        <p className="text-[12.5px] text-ink-2">
          This task needs your input. Reply in this chat and include {task.shortId}.
        </p>
      ) : null}
      {task.report ? (
        <div className="space-y-2 border-t border-line pt-3">
          <h4 className="text-[12px] font-medium text-ink">Saved report</h4>
          <Report text={task.report} />
        </div>
      ) : null}
      {task.stages.length ? (
        <details
          className="border-t border-line pt-3"
          open={stagesOpen}
          onToggle={(event) => setStagesOpen(event.currentTarget.open)}
        >
          <summary className="cursor-pointer text-[12.5px] font-medium text-ink">
            Stage details ({task.stages.length})
          </summary>
          <div className="mt-3 space-y-2">
            {stagesOpen ? task.stages.map((item) => <Stage key={item.id} stage={item} task={task} />) : null}
          </div>
        </details>
      ) : null}
      {cancel.isError ? (
        <Failure
          title="Could not cancel this task"
          error={cancel.error}
          retry={!isClosed(task.status) && task.status !== "cancelling" ? requestCancel : undefined}
        />
      ) : null}
    </article>
  );
}

export function Tasks({ chatId }: { chatId: number }) {
  const hash = useSyncExternalStore(subscribeHash, readHash);
  const link = stageLink.exec(hash);
  const taskId = link?.[1];
  const stageId = link?.[2];

  const tasks = useQuery({
    ...listTasksOptions({ path: { chatId } }),
    // Keep discovering tasks when the main chat creates one after the initial read.
    refetchInterval: (query) =>
      query.state.data?.tasks.some((task) => !isClosed(task.status)) ? 3000 : 10_000,
    refetchIntervalInBackground: false,
  });

  const listedTask = tasks.data?.tasks.find((task) => task.id === taskId);
  const missingTask = Boolean(taskId && !listedTask);

  const detail = useQuery({
    ...getTaskOptions({ path: { chatId, taskId: taskId ?? "" } }),
    enabled: missingTask && !tasks.isPending,
  });

  const linkedTask = listedTask ?? (missingTask ? detail.data : undefined);
  const missingStage = linkedTask && !linkedTask.stages.some((stage) => stage.id === stageId);

  return (
    <section aria-label="Background tasks" className="space-y-3">
      {tasks.isPending ? <Loading label="Loading tasks" /> : null}
      {tasks.isError ? (
        <Failure title="Could not refresh tasks" error={tasks.error} retry={() => void tasks.refetch()} />
      ) : null}
      {tasks.data?.tasks.length || linkedTask ? (
        <h2 className="text-[13px] font-semibold text-ink">Background tasks</h2>
      ) : null}
      {tasks.data?.tasks.map((task) => (
        <TaskCard key={task.id} task={task} />
      ))}
      {missingTask && !tasks.isPending && detail.isPending ? <Loading label="Loading linked task" /> : null}
      {missingTask && detail.isError ? (
        <Failure
          title="Could not load the linked task in this chat"
          error={detail.error}
          retry={() => void detail.refetch()}
        >
          The task may no longer exist or may belong to another chat.
        </Failure>
      ) : null}
      {missingStage ? <Failure title="The linked stage does not belong to this task" /> : null}
      {missingTask && linkedTask ? <TaskCard key={linkedTask.id} task={linkedTask} /> : null}
    </section>
  );
}
