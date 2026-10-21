import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery, useMutation } from "@tanstack/react-query";
import { IconChevronRightSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronRightSmall";
import { IconBubble2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBubble2";
import { IconTasks } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconTasks";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconPlusSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPlusSmall";
import type { Chat, TaskSummary } from "../api/generated/types.gen";
import {
  getTaskOptions,
  listChatsOptions,
  createChatMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { activeSessionCount, sessionKind, taskTime, taskTimes } from "../lib/tasks";
import { refreshChats } from "../lib/queries";
import { useChatStream } from "../lib/chat-stream";
import { mergeHistory } from "../lib/chat-state";
import { Markdown, Transcript, RetryNotice } from "../components/agent/transcript";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { LoadEarlierButton } from "../components/timeline-actions";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { Select } from "../components/form";

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
  running: "accent",
  done: "green",
  failed: "red",
  cancelled: "neutral",
  interrupted: "red",
} as const;

function TaskPill({ task }: { task: Pick<TaskSummary, "status"> }) {
  return (
    <StatusPill tone={tones[task.status]} className="h-5 shrink-0 px-2 text-[11.5px]">
      {labels[task.status]}
    </StatusPill>
  );
}

export function SessionsPage() {
  const agentId = useAgentId();
  const settings = useAgentSettings(agentId);

  const sessions = useQuery({
    ...listChatsOptions({ path: { agentId } }),
    placeholderData: (previous) => previous,
    refetchInterval: 3000,
  });

  const navigate = useNavigate();

  const create = useMutation({
    ...createChatMutation(),
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
    },
  });

  const [kind, setKind] = useState("All kinds");
  const name = settings.data?.agent.name ?? "dex";
  // Archived Chats live in the Chats sidebar of the Chat tab, not here.
  const all = (sessions.data?.chats ?? []).filter((chat) => !chat.archivedAt);
  const list = all.filter((chat) => kind === "All kinds" || sessionKind(chat) === kind);

  return (
    <FramedPage
      title="Sessions"
      description={`Conversations of ${name} apart from the main timeline. Each session has its own context. Chat sessions take messages. The other kinds are read-only.`}
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={create.isPending}
          onClick={() => create.mutate({ path: { agentId } })}
        >
          <IconPlusSmall size={16} /> {create.isPending ? "Opening…" : "New session"}
        </Button>
      }
    >
      <Section
        n={1}
        title="All sessions"
        caption={all.length ? `${activeSessionCount(list)} active · newest first` : undefined}
        action={
          all.length ? (
            <Select
              aria-label="Kind"
              className="h-8 w-auto text-[12.5px]"
              value={kind}
              onChange={(event) => setKind(event.target.value)}
            >
              {["All kinds", "Chat", "Task", "Automation"].map((value) => (
                <option key={value}>{value}</option>
              ))}
            </Select>
          ) : null
        }
      >
        {create.isError ? <Failure title="Could not open a new session" error={create.error} /> : null}
        {sessions.isPending ? (
          <Loading />
        ) : sessions.isError ? (
          <Failure
            title="Could not load sessions"
            error={sessions.error}
            retry={() => void sessions.refetch()}
          />
        ) : list.length ? (
          <Panel>
            <ul>
              {list.map((chat) => (
                <SessionRow key={chat.id} chat={chat} />
              ))}
            </ul>
          </Panel>
        ) : all.length ? (
          <Empty title="No sessions of this kind">Choose All kinds to see every session.</Empty>
        ) : (
          <Empty title="No sessions yet">
            Tasks, automation runs and your Chat sessions show here. Use New session for work that needs a
            clean context.
          </Empty>
        )}
      </Section>
    </FramedPage>
  );
}

function SessionRow({ chat }: { chat: Chat }) {
  const agentId = useAgentId();
  const kind = sessionKind(chat);

  const Icon = kind === "Chat" ? IconBubble2 : kind === "Task" ? IconTasks : IconClock;

  const content = (
    <>
      <span className="block text-[13.5px] font-medium text-ink [overflow-wrap:anywhere]">
        {chat.title || "New session"}
      </span>
      <span className="mt-0.5 block text-[12px] text-ink-3">
        {kind} · {chat.task ? taskTimes(chat.task) : taskTime(chat.updatedAt)}
      </span>
    </>
  );

  return (
    <li className="flex items-center gap-3 border-t border-line px-4 py-3.5 first:border-t-0">
      <Icon size={16} className="mt-0.5 shrink-0 self-start text-ink-3" />
      {chat.task ? (
        <Link
          to="/a/$agentId/tasks/$taskId"
          params={{ agentId, taskId: chat.task.id }}
          className="min-w-0 flex-1 hover:underline"
        >
          {content}
        </Link>
      ) : (
        <Link
          to="/a/$agentId/c/$chatId"
          params={{ agentId, chatId: String(chat.id) }}
          className="min-w-0 flex-1 hover:underline"
        >
          {content}
        </Link>
      )}
      {chat.task ? (
        <TaskPill task={chat.task} />
      ) : chat.busy ? (
        <StatusPill tone="accent" className="h-5 shrink-0 px-2 text-[11.5px]">
          Running
        </StatusPill>
      ) : null}
      <IconChevronRightSmall size={16} className="shrink-0 text-ink-3" />
    </li>
  );
}

export function TaskPage({ taskId }: { taskId: string }) {
  const agentId = useAgentId();

  const task = useQuery({
    ...getTaskOptions({ path: { agentId, taskId } }),
    refetchInterval: ({ state }) =>
      state.data?.status === "queued" || state.data?.status === "running" ? 3000 : false,
  });

  return (
    <FramedPage
      eyebrow={
        <Link to="/a/$agentId/sessions" params={{ agentId }} className="hover:text-ink hover:underline">
          Sessions
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
            (connection === "error" ? "Connection lost. Reconnecting…" : "Connecting to the bot…")}
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
