import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { listAgentsOptions, listBoardTasksOptions } from "../api/generated/@tanstack/react-query.gen";
import { createBoardTask } from "../api/generated/sdk.gen";
import type { BoardStatus, BoardTaskSummary } from "../api/generated/types.gen";
import { Failure, Loading } from "../components/feedback";
import { RadioSegments, Select } from "../components/form";
import { Empty, FramedPage, Section } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { invalidateBoard, useBoardStream } from "../lib/board-stream";
import { initialStatuses, statusLabel } from "../lib/board";

const lanes: readonly BoardStatus[] = ["backlog", "ready", "in_progress", "in_review", "done"];

const views = [
  { label: "Kanban", value: "kanban" },
  { label: "List", value: "list" },
] as const;

export function BoardPage({ bot, onBotChange }: { bot?: string; onBotChange: (bot?: string) => void }) {
  useBoardStream();
  const [closed, setClosed] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [inReview, setInReview] = useState(false);
  const [source, setSource] = useState("");
  const [view, setView] = useState<"kanban" | "list">("kanban");
  const agents = useQuery(listAgentsOptions());
  const bots = agents.data?.agents ?? [];
  const botNames = new Map(bots.map((agent) => [agent.id, agent.name]));

  const tasks = useQuery(
    listBoardTasksOptions({
      query: {
        botId: bot,
        blocked: blocked || undefined,
        inReview: inReview || undefined,
        source: source || undefined,
        includeClosed: closed,
      },
    }),
  );

  return (
    <FramedPage title="Board" headerTitle="Board" description="Track Tasks across your Bots." wide>
      <Section title="New task">
        <NewTask key={bot} bot={bot} bots={bots} />
      </Section>
      <Section title="Tasks">
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <RadioSegments
            label="Board view"
            options={views}
            value={view}
            className="w-36"
            onChange={setView}
          />
          <Select
            className="w-auto"
            aria-label="Filter by Bot"
            value={bot ?? ""}
            onChange={(event) => onBotChange(event.target.value || undefined)}
          >
            <option value="">All Bots</option>
            {bot && !botNames.has(bot) ? <option value={bot}>{bot}</option> : null}
            {bots.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}
              </option>
            ))}
          </Select>
          <Select
            className="w-auto"
            aria-label="Source"
            value={source}
            onChange={(event) => setSource(event.target.value)}
          >
            <option value="">All Sources</option>
            <option value="github.com">GitHub</option>
            <option value="webhook">Webhook</option>
            <option value="email">Email</option>
          </Select>
          <Button
            variant={inReview ? "secondary" : "ghost"}
            size="sm"
            aria-pressed={inReview}
            onClick={() => setInReview(!inReview)}
          >
            In Review
          </Button>
          <Button
            variant={blocked ? "secondary" : "ghost"}
            size="sm"
            aria-pressed={blocked}
            onClick={() => setBlocked(!blocked)}
          >
            Blocked
          </Button>
          <Button
            variant={closed ? "secondary" : "ghost"}
            size="sm"
            aria-pressed={closed}
            onClick={() => setClosed(!closed)}
          >
            Show closed
          </Button>
        </div>
        {tasks.isPending ? (
          <Loading />
        ) : tasks.isError ? (
          <Failure title="Could not load tasks" error={tasks.error} retry={() => void tasks.refetch()} />
        ) : view === "kanban" ? (
          <div className="flex gap-3 overflow-x-auto pb-3">
            {(closed ? ([...lanes, "cancelled"] as const) : lanes).map((status) => {
              const items = tasks.data.tasks.filter((task) => task.status === status);

              return (
                <section
                  key={status}
                  aria-label={statusLabel[status]}
                  className="min-w-40 flex-1 rounded-md border bg-muted/50 p-2"
                >
                  <h3 className="mb-2 flex justify-between gap-1 px-1 text-sm font-medium">
                    {statusLabel[status]} <span className="text-muted-foreground">{items.length}</span>
                  </h3>
                  <ul className="grid gap-2">
                    {items.map((task) => (
                      <li key={task.id} className="rounded-md border bg-card p-2">
                        <TaskCard task={task} botName={botNames.get(task.assigneeBotId)} />
                      </li>
                    ))}
                  </ul>
                </section>
              );
            })}
          </div>
        ) : tasks.data.tasks.length ? (
          <ul className="divide-y divide-border">
            {tasks.data.tasks.map((task) => (
              <li key={task.id} className="px-2 py-3">
                <TaskCard task={task} botName={botNames.get(task.assigneeBotId)} />
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No tasks">No Tasks match these filters.</Empty>
        )}
      </Section>
    </FramedPage>
  );
}

type BotOption = { id?: string; name: string };

function NewTask({ bot, bots }: { bot?: string; bots: BotOption[] }) {
  const [title, setTitle] = useState("");
  const [selected, setSelected] = useState("");
  const [status, setStatus] = useState<(typeof initialStatuses)[number]>("ready");
  const client = useQueryClient();
  const navigate = useNavigate();
  const assigneeBotId = selected || bots.find((agent) => agent.id === bot)?.id || bots[0]?.id || "";

  const create = useMutation({
    mutationFn: () => createBoardTask({ body: { title, assigneeBotId, status }, throwOnError: true }),
    onSuccess: async ({ data }) => {
      setTitle("");
      await invalidateBoard(client);
      await navigate({ to: "/board/$taskKey", params: { taskKey: data.key } });
    },
  });

  return (
    <>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault();

          if (assigneeBotId && title.trim() && !create.isPending) create.mutate();
        }}
      >
        <Input
          className="w-auto min-w-40 flex-1"
          aria-label="Task title"
          placeholder="Task title"
          value={title}
          maxLength={160}
          onChange={(event) => setTitle(event.target.value)}
        />
        <Select
          className="w-auto"
          aria-label="Assignee Bot"
          value={assigneeBotId}
          onChange={(event) => setSelected(event.target.value)}
        >
          {bots.map((agent) => (
            <option key={agent.id} value={agent.id}>
              {agent.name}
            </option>
          ))}
        </Select>
        <Select
          className="w-auto"
          aria-label="Initial status"
          value={status}
          onChange={(event) =>
            setStatus(initialStatuses.find((value) => value === event.target.value) ?? status)
          }
        >
          {initialStatuses.map((value) => (
            <option key={value} value={value}>
              {statusLabel[value]}
            </option>
          ))}
        </Select>
        <Button type="submit" disabled={!assigneeBotId || !title.trim() || create.isPending}>
          Create task
        </Button>
      </form>
      {create.error ? <Failure title="Could not create task" error={create.error} /> : null}
    </>
  );
}

function TaskCard({ task, botName }: { task: BoardTaskSummary; botName?: string }) {
  const session = task.primarySession;

  return (
    <div className="grid min-w-0 gap-1 text-sm">
      <Link
        to="/board/$taskKey"
        params={{ taskKey: task.key }}
        className="block font-medium hover:underline [overflow-wrap:anywhere]"
      >
        <span className="mr-1 font-mono text-xs text-muted-foreground">{task.key}</span> {task.title}
      </Link>
      <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
        <span>{botName ?? task.assigneeBotId}</span>
        <Badge variant="outline">{statusLabel[task.status]}</Badge>
        {task.blocked ? <Badge variant="secondary">Blocked</Badge> : null}
      </div>
      {session ? (
        <Link
          className="text-xs underline [overflow-wrap:anywhere]"
          to="/a/$agentId/c/$chatId"
          params={{ agentId: session.agentId, chatId: String(session.conversationId) }}
        >
          {session.title || "Primary Session"}
        </Link>
      ) : null}
    </div>
  );
}
