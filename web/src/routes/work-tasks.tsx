import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getWorkTaskOptions,
  listAgentsOptions,
  listWorkTasksOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { appendWorkTaskNote, createWorkTask, setWorkTaskLifecycle } from "../api/generated/sdk.gen";
import type { WorkTaskLifecycle } from "../api/generated/types.gen";
import { Failure, Loading } from "../components/feedback";
import { Select } from "../components/form";
import { Empty, FramedPage, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Textarea } from "../components/ui/textarea";
import { inboxOptions } from "../lib/inbox";

export function TaskInboxPage() {
  const [closed, setClosed] = useState(false);
  const [title, setTitle] = useState("");
  const [selectedAgent, setSelectedAgent] = useState("");
  const agents = useQuery(listAgentsOptions());

  const tasks = useQuery({
    ...listWorkTasksOptions({ query: { includeClosed: closed } }),
    refetchInterval: 3000,
  });

  const routines = useQuery(inboxOptions());
  const client = useQueryClient();
  const navigate = useNavigate();
  const agentId = selectedAgent || agents.data?.agents[0]?.id || "";

  const create = useMutation({
    mutationFn: () => createWorkTask({ path: { agentId }, body: { title }, throwOnError: true }),
    onSuccess: async ({ data }) => {
      await client.invalidateQueries({ queryKey: listWorkTasksOptions().queryKey.slice(0, 1) });
      await navigate({ to: "/a/$agentId/inbox/$taskId", params: { agentId: data.agentId, taskId: data.id } });
    },
  });

  return (
    <FramedPage title="Inbox" description="Track work across your bots.">
      <Section title="New task">
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate();
          }}
        >
          <Select aria-label="Bot" value={agentId} onChange={(event) => setSelectedAgent(event.target.value)}>
            {(agents.data?.agents ?? []).map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}
              </option>
            ))}
          </Select>
          <Input
            className="w-auto flex-1"
            aria-label="Task title"
            value={title}
            maxLength={160}
            onChange={(event) => setTitle(event.target.value)}
          />
          <Button type="submit" disabled={!agentId || !title.trim() || create.isPending}>
            Create task
          </Button>
        </form>
        {create.error ? <Failure title="Could not create task" error={create.error} /> : null}
      </Section>
      <Section
        title="Tasks"
        action={
          <Button variant="ghost" size="sm" aria-pressed={closed} onClick={() => setClosed(!closed)}>
            Show closed
          </Button>
        }
      >
        {tasks.isPending ? (
          <Loading />
        ) : tasks.isError ? (
          <Failure title="Could not load tasks" error={tasks.error} />
        ) : tasks.data.tasks.length ? (
          <ul className="divide-y divide-border">
            {tasks.data.tasks.map((task) => (
              <li key={task.id}>
                <Link
                  className="flex justify-between gap-2 px-2 py-3 hover:bg-accent"
                  to="/a/$agentId/inbox/$taskId"
                  params={{ agentId: task.agentId, taskId: task.id }}
                >
                  <span className="min-w-0 break-words">{task.title}</span>
                  <span className="text-sm capitalize text-muted-foreground">{task.lifecycle}</span>
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No tasks">Create a task to track work here.</Empty>
        )}
      </Section>
      <Section title="Routine results">
        {routines.isPending ? (
          <Loading />
        ) : routines.isError ? (
          <Failure title="Could not load routine results" error={routines.error} />
        ) : (
          <ul className="divide-y divide-border">
            {routines.data.items
              .filter((item) => !item.dismissed)
              .map((item) => (
                <li key={item.runId}>
                  <Link
                    className="block px-2 py-3 hover:bg-accent"
                    to="/inbox/$runId"
                    params={{ runId: item.runId }}
                  >
                    {item.automationName}
                  </Link>
                </li>
              ))}
          </ul>
        )}
      </Section>
    </FramedPage>
  );
}

export function WorkTaskPage({ agentId, taskId }: { agentId: string; taskId: string }) {
  const [note, setNote] = useState("");
  const path = { agentId, taskId };
  const options = getWorkTaskOptions({ path });
  const detail = useQuery({ ...options, refetchInterval: 3000 });
  const client = useQueryClient();

  const lifecycle = useMutation({
    mutationFn: (value: WorkTaskLifecycle) =>
      setWorkTaskLifecycle({ path, body: { lifecycle: value }, throwOnError: true }),
    onSuccess: () => client.invalidateQueries({ queryKey: options.queryKey }),
  });

  const append = useMutation({
    mutationFn: () => appendWorkTaskNote({ path, body: { content: note }, throwOnError: true }),
    onSuccess: async () => {
      setNote("");
      await client.invalidateQueries({ queryKey: options.queryKey });
    },
  });

  const task = detail.data?.task;

  return (
    <FramedPage title={task?.title ?? "Task"}>
      <Link className="text-sm underline" to="/inbox">
        Back to Inbox
      </Link>
      {detail.isPending ? (
        <Loading />
      ) : detail.isError ? (
        <Failure title="Could not load task" error={detail.error} />
      ) : (
        <>
          <Section title="Status" caption={detail.data.task.lifecycle}>
            {detail.data.task.lifecycle === "open" ? (
              <div className="flex gap-2">
                <Button disabled={lifecycle.isPending} onClick={() => lifecycle.mutate("completed")}>
                  Complete task
                </Button>
                <Button
                  variant="secondary"
                  disabled={lifecycle.isPending}
                  onClick={() => lifecycle.mutate("cancelled")}
                >
                  Cancel task
                </Button>
              </div>
            ) : (
              <Button disabled={lifecycle.isPending} onClick={() => lifecycle.mutate("open")}>
                Reopen task
              </Button>
            )}
            {lifecycle.error ? <Failure title="Could not update task" error={lifecycle.error} /> : null}
          </Section>
          <Section title="Timeline">
            <form
              className="space-y-2"
              onSubmit={(event) => {
                event.preventDefault();
                append.mutate();
              }}
            >
              <Textarea
                aria-label="Task note"
                value={note}
                maxLength={32000}
                onChange={(event) => setNote(event.target.value)}
              />
              <Button type="submit" disabled={!note.trim() || append.isPending}>
                Add note
              </Button>
            </form>
            {append.error ? <Failure title="Could not add note" error={append.error} /> : null}
            <ul className="divide-y divide-border">
              {detail.data.entries.map((entry) => (
                <li key={entry.id} className="py-3">
                  <time
                    dateTime={new Date(entry.createdAt).toISOString()}
                    className="text-xs text-muted-foreground"
                  >
                    {new Date(entry.createdAt).toLocaleString()}
                  </time>
                  <p className="whitespace-pre-wrap break-words">{entry.content}</p>
                </li>
              ))}
            </ul>
          </Section>
        </>
      )}
    </FramedPage>
  );
}
