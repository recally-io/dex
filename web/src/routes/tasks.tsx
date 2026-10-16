import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { IconChevronRightSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronRightSmall";
import type { Task } from "../api/generated/types.gen";
import {
  getSettingsOptions,
  getTaskOptions,
  listTasksOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { orderedTasks, taskTimes } from "../lib/tasks";
import { useChatStream } from "../lib/chat-stream";
import { mergeHistory } from "../lib/chat-state";
import { Markdown, Transcript, RetryNotice } from "../components/agent/transcript";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { LoadEarlierButton } from "../components/timeline-actions";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";

const labels = {
  queued: "Queued",
  running: "Running",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
} as const;

const tones = {
  queued: "neutral",
  running: "orange",
  done: "green",
  failed: "red",
  cancelled: "neutral",
  interrupted: "red",
} as const;

function TaskPill({ task }: { task: Task }) {
  return (
    <StatusPill tone={tones[task.status]} className="h-5 shrink-0 px-2 text-[11.5px]">
      {labels[task.status]}
    </StatusPill>
  );
}

export function TasksPage() {
  const settings = useQuery(getSettingsOptions());
  const tasks = useQuery({ ...listTasksOptions(), refetchInterval: 3000 });
  const name = settings.data?.agent.name ?? "dex";
  const list = orderedTasks(tasks.data?.tasks ?? []);
  const running = list.filter((task) => task.status === "running").length;

  return (
    <FramedPage
      title="Tasks"
      description={`Background work that ${name} started. Read-only. Done means that the task returned a report. Read the report to see the result.`}
    >
      <Section n={1} title="All tasks" caption={`${running} running · newest first`}>
        {tasks.isPending ? (
          <Loading />
        ) : tasks.isError ? (
          <Failure title="Could not load tasks" error={tasks.error} retry={() => void tasks.refetch()} />
        ) : list.length ? (
          <Panel>
            <ul>
              {list.map((task) => (
                <li
                  key={task.id}
                  className="flex items-center gap-3 border-t border-line px-4 py-3.5 first:border-t-0"
                >
                  <Link
                    to="/tasks/$taskId"
                    params={{ taskId: task.id }}
                    className="min-w-0 flex-1 hover:underline"
                  >
                    <span className="block text-[13.5px] font-medium text-ink [overflow-wrap:anywhere]">
                      {task.title}
                    </span>
                    <span className="mt-0.5 block text-[12px] text-ink-3">{taskTimes(task)}</span>
                  </Link>
                  <TaskPill task={task} />
                  <IconChevronRightSmall size={16} className="shrink-0 text-ink-3" />
                </li>
              ))}
            </ul>
          </Panel>
        ) : (
          <Empty title="No tasks yet">Ask {name} to do work in the background. It shows here.</Empty>
        )}
      </Section>
    </FramedPage>
  );
}

export function TaskPage({ taskId }: { taskId: string }) {
  const task = useQuery({ ...getTaskOptions({ path: { taskId } }), refetchInterval: 3000 });

  return (
    <FramedPage
      eyebrow={
        <Link to="/tasks" className="hover:text-ink hover:underline">
          Tasks
        </Link>
      }
      title={task.data?.title ?? "Task"}
      badge={task.data ? <TaskPill task={task.data} /> : null}
      description={task.data ? taskTimes(task.data, true) : undefined}
    >
      {task.isPending ? (
        <Loading />
      ) : task.isError ? (
        <Failure title="Could not load task" error={task.error} retry={() => void task.refetch()} />
      ) : (
        <>
          <Section n={1} title="Report" caption="Saved when the task ended.">
            <Panel className="px-4 py-3.5">
              <Markdown text={task.data.report || "No report yet."} />
            </Panel>
          </Section>
          <Section n={2} title="Work" caption="What the task did. Read-only.">
            <TaskWork key={task.data.conversationId} chatId={task.data.conversationId} />
          </Section>
        </>
      )}
    </FramedPage>
  );
}

function TaskWork({ chatId }: { chatId: number }) {
  const { chat, state, setState, connection, streamError, reconnect } = useChatStream(chatId);

  return chat.isPending ? (
    <Loading />
  ) : chat.isError ? (
    <Failure title="Could not load work" error={chat.error} retry={() => void chat.refetch()} />
  ) : (
    <div className="flex min-w-0 flex-col gap-5">
      {connection !== "ready" ? (
        <div role="status" className="text-[12px] text-ink-3">
          {streamError ||
            (connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the agent…")}
          <Button size="xs" variant="secondary" onClick={() => reconnect.current?.()}>
            Reconnect
          </Button>
        </div>
      ) : null}
      <LoadEarlierButton
        chatId={chatId}
        before={state.historyBefore}
        onPage={(page) => setState((previous) => mergeHistory(previous, page))}
      />
      <Transcript items={state.items} running={state.running} retry={() => {}} canRetry={false} />
      {state.retry ? <RetryNotice retry={state.retry} /> : null}
      {!state.items.length && connection === "ready" ? (
        <p className="text-[13px] text-ink-3">No messages yet.</p>
      ) : null}
    </div>
  );
}
