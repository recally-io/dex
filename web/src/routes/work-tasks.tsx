import { useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getBoardTaskOptions,
  getHealthOptions,
  listAgentsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import {
  createBoardEntry,
  getMainChat,
  reviewBoardTask,
  sendMessage,
  updateBoardTask,
} from "../api/generated/sdk.gen";
import type {
  Agent,
  BoardReviewerKind,
  BoardReview,
  BoardStatus,
  BoardTaskDetail,
  BoardTaskEntry,
  BoardTaskQuestion,
  BoardTaskSummary,
  BoardTaskUpdate,
} from "../api/generated/types.gen";
import { Failure, Loading } from "../components/feedback";
import { Field, Select } from "../components/form";
import { Empty, FramedPage, Section } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Textarea } from "../components/ui/textarea";
import { TaskLinks } from "../components/task-links";
import { GitHubPullForm } from "../plugins/github/pull-form";
import { invalidateBoard, useBoardStream } from "../lib/board-stream";
import { isClosed, nextStatuses, statusLabel } from "../lib/board";
import { errorText, refreshWork } from "../lib/queries";
import { newId } from "../lib/utils";

export function WorkTaskPage({ taskKey }: { taskKey: string }) {
  useBoardStream();
  const detail = useQuery(getBoardTaskOptions({ path: { taskKey } }));
  const bots = useQuery(listAgentsOptions()).data?.agents ?? [];
  const task = detail.data?.task;

  return (
    <FramedPage
      headerTitle="Board"
      title={task?.title ?? "Task"}
      eyebrow={
        <Link className="underline" to="/board">
          Back to Board
        </Link>
      }
      badge={
        task ? (
          <>
            <Badge variant="outline" className="font-mono">
              {task.key}
            </Badge>
            {task.blocked ? <Badge variant="secondary">Blocked</Badge> : null}
          </>
        ) : null
      }
    >
      {detail.isPending ? <Loading /> : null}
      {detail.isError ? (
        <Failure title="Could not load task" error={detail.error} retry={() => void detail.refetch()} />
      ) : null}
      {detail.data ? <TaskDetail detail={detail.data} bots={bots} /> : null}
    </FramedPage>
  );
}

function TaskDetail({ detail, bots }: { detail: BoardTaskDetail; bots: Agent[] }) {
  const { task } = detail;
  const open = !isClosed(task.status);
  const health = useQuery(getHealthOptions());
  const botNames = new Map(bots.map((bot) => [bot.id, bot.name]));

  return (
    <>
      <TaskEditor task={task} bots={bots} />
      {task.status === "in_review" ? <ReviewActions task={task} /> : null}
      <TaskBlockers detail={detail} />
      {detail.sessions.length ? (
        <Section title="Sessions">
          <ul className="divide-y divide-border">
            {detail.sessions.map((session) => (
              <li
                key={session.conversationId}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm"
              >
                <Link
                  to="/a/$agentId/c/$chatId"
                  params={{ agentId: session.agentId, chatId: String(session.conversationId) }}
                  className="font-medium underline"
                >
                  {session.title || "Untitled session"}
                </Link>
                <span className="text-muted-foreground">{session.agentName}</span>
                {session.isPrimary ? <Badge variant="secondary">Primary</Badge> : null}
                <Badge variant="outline" className="capitalize">
                  {session.state}
                </Badge>
                {session.endedAt ? (
                  <span className="text-muted-foreground">
                    Ended {new Date(session.endedAt).toLocaleString()}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      <TaskLinks links={detail.links} open={open}>
        {open && health.data?.plugins.includes("github") ? (
          <GitHubPullForm path={{ agentId: task.assigneeBotId, taskId: task.id }} />
        ) : null}
      </TaskLinks>
      <Timeline task={task} entries={detail.entries} botNames={botNames} />
    </>
  );
}

class TaskConflict extends Error {}

function TaskEditor({ task, bots }: { task: BoardTaskSummary; bots: Agent[] }) {
  const [draft, setDraft] = useState<{
    revision: number;
    baseStatus: BoardStatus;
    changes: BoardTaskUpdate;
  }>();

  const client = useQueryClient();

  const save = useMutation({
    mutationFn: async ({ revision, changes }: NonNullable<typeof draft>) => {
      const { error, response } = await updateBoardTask({
        path: { taskKey: task.key },
        headers: { "If-Match": revision },
        body: changes,
      });

      if (response?.status === 412 || response?.status === 409)
        throw new TaskConflict("The Task changed before you saved it.");

      if (error) throw new Error(errorText(error));
    },
    onSuccess: async () => {
      await invalidateBoard(client);
      setDraft(undefined);
    },
  });

  const change = (patch: BoardTaskUpdate) => {
    setDraft({
      revision: draft?.revision ?? task.revision,
      baseStatus: draft?.baseStatus ?? task.status,
      changes: { ...draft?.changes, ...patch },
    });
  };

  const title = draft?.changes.title ?? task.title;
  const assignee = draft?.changes.assigneeBotId ?? task.assigneeBotId;
  const reviewerKind = draft?.changes.reviewerKind ?? task.reviewerKind;
  const reviewerId = draft?.changes.reviewerId ?? task.reviewerId;
  const baseStatus = draft?.baseStatus ?? task.status;
  const status = draft?.changes.status ?? baseStatus;
  const statuses = [baseStatus, ...nextStatuses(baseStatus, reviewerKind !== "")];
  const canSave = title.trim() && statuses.includes(status) && (reviewerKind !== "bot" || reviewerId);
  const conflict = draft && (draft.revision !== task.revision || save.error instanceof TaskConflict);

  return (
    <Section title="Task details">
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();

          if (draft && !conflict && canSave && !save.isPending) save.mutate(draft);
        }}
      >
        <fieldset disabled={save.isPending} className="grid gap-3 sm:grid-cols-2">
          <Field label="Title">
            {({ id }) => (
              <Input
                id={id}
                value={title}
                maxLength={160}
                required
                onChange={(event) => change({ title: event.target.value })}
              />
            )}
          </Field>
          <Field label="Status">
            {({ id }) => (
              <Select
                id={id}
                value={status}
                onChange={(event) =>
                  change({ status: statuses.find((value) => value === event.target.value) })
                }
              >
                {!statuses.includes(status) ? (
                  <option value={status} disabled>
                    {statusLabel[status]}
                  </option>
                ) : null}
                {statuses.map((value) => (
                  <option key={value} value={value}>
                    {statusLabel[value]}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Assignee Bot">
            {({ id }) => (
              <Select
                id={id}
                value={assignee}
                onChange={(event) => change({ assigneeBotId: event.target.value })}
              >
                {!bots.some((bot) => bot.id === assignee) ? (
                  <option value={assignee}>{assignee}</option>
                ) : null}
                {bots.map((bot) => (
                  <option key={bot.id} value={bot.id}>
                    {bot.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Reviewer">
            {({ id }) => (
              <Select
                id={id}
                value={reviewerKind}
                onChange={(event) => {
                  const value = event.target.value;
                  const kind: BoardReviewerKind = value === "owner" || value === "bot" ? value : "";
                  change({ reviewerKind: kind, reviewerId: kind === "bot" ? (bots[0]?.id ?? "") : "" });
                }}
              >
                <option value="">None</option>
                <option value="owner">Human Owner</option>
                <option value="bot">Bot</option>
              </Select>
            )}
          </Field>
          {reviewerKind === "bot" ? (
            <Field label="Reviewer Bot">
              {({ id }) => (
                <Select
                  id={id}
                  value={reviewerId}
                  onChange={(event) => change({ reviewerId: event.target.value })}
                >
                  {!bots.some((bot) => bot.id === reviewerId) ? (
                    <option value={reviewerId}>{reviewerId || "Choose a Bot"}</option>
                  ) : null}
                  {bots.map((bot) => (
                    <option key={bot.id} value={bot.id}>
                      {bot.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          ) : null}
        </fieldset>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={!draft || !canSave || !!conflict || save.isPending}>
            Save changes
          </Button>
          {draft ? (
            <Button
              type="button"
              variant="ghost"
              disabled={save.isPending}
              onClick={() => {
                save.reset();
                setDraft(undefined);

                if (conflict) void invalidateBoard(client);
              }}
            >
              {conflict ? "Load latest values" : "Discard changes"}
            </Button>
          ) : null}
        </div>
      </form>
      {conflict ? (
        <Failure title="This Task changed">Load the latest values before you edit again.</Failure>
      ) : save.error ? (
        <Failure title="Could not save Task" error={save.error} />
      ) : null}
    </Section>
  );
}

function ReviewActions({ task }: { task: BoardTaskSummary }) {
  const [feedback, setFeedback] = useState("");
  const client = useQueryClient();

  const review = useMutation({
    mutationFn: async (decision: BoardReview["decision"]) => {
      const { error, response } = await reviewBoardTask({
        path: { taskKey: task.key },
        body: {
          decision,
          reviewSeq: decision === "approve" ? task.reviewSeq : undefined,
          note: feedback.trim() || undefined,
        },
      });

      if (response?.status === 409) {
        await invalidateBoard(client);
        throw new TaskConflict("The review changed. Check the current submission before you try again.");
      }

      if (error) throw new Error(errorText(error));
    },
    onSuccess: () => invalidateBoard(client),
  });

  return (
    <Section title="In Review" caption={`Submitted at entry #${task.reviewSeq}.`}>
      <div className="space-y-2">
        <Textarea
          aria-label="Review feedback"
          placeholder="Feedback is required to request changes"
          value={feedback}
          maxLength={32000}
          onChange={(event) => setFeedback(event.target.value)}
        />
        <div className="flex flex-wrap gap-2">
          <Button disabled={review.isPending} onClick={() => review.mutate("approve")}>
            Approve
          </Button>
          <Button
            variant="secondary"
            disabled={!feedback.trim() || review.isPending}
            onClick={() => review.mutate("request_changes")}
          >
            Request Changes
          </Button>
        </div>
      </div>
      {review.error ? <Failure title="Could not submit review" error={review.error} /> : null}
    </Section>
  );
}

function TaskBlockers({ detail }: { detail: BoardTaskDetail }) {
  const client = useQueryClient();

  const answer = useMutation({
    mutationFn: ({ question, text }: { question: BoardTaskQuestion; text: string }) =>
      sendMessage({
        path: { agentId: question.agentId, chatId: question.conversationId },
        body: { id: newId(), text, answersQuestionId: question.id },
        throwOnError: true,
      }),
    onSuccess: () => Promise.all([invalidateBoard(client), refreshWork()]),
  });

  const questionPositions = new Map(
    detail.blockers.map((blocker) => [blocker.questionId, blocker.queuePosition]),
  );

  const waits = detail.blockers.filter((blocker) => blocker.kind === "wait");

  if (!detail.questions.length && !waits.length) return null;

  return (
    <Section title={detail.task.blocked ? "Blocked" : "Questions"}>
      {waits.length ? (
        <ul className="divide-y divide-border">
          {waits.map((wait) => (
            <li className="py-2 text-sm" key={wait.waitId}>
              Waiting for {wait.eventType} on {wait.objectType} {wait.objectKey} · {wait.namespace}
            </li>
          ))}
        </ul>
      ) : null}
      {detail.questions.length ? (
        <ul className="divide-y divide-border">
          {detail.questions.map((question) => {
            const position = questionPositions.get(question.id);

            return (
              <li key={question.id} className="space-y-2 py-3">
                {position ? (
                  <Badge variant="outline">Queue position {position}</Badge>
                ) : question.queued ? (
                  <Badge variant="outline">Queued</Badge>
                ) : null}
                <p className="whitespace-pre-wrap">{question.text}</p>
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
              </li>
            );
          })}
        </ul>
      ) : null}
      {answer.error ? <Failure title="Could not answer question" error={answer.error} /> : null}
    </Section>
  );
}

const kinds: Record<BoardTaskEntry["kind"], string> = {
  note: "Note",
  artifact: "Artifact",
  status: "Status",
  assign: "Assignment",
  review: "Review",
  execution: "Session",
  external: "External event",
};

function Timeline({
  task,
  entries,
  botNames,
}: {
  task: BoardTaskSummary;
  entries: BoardTaskEntry[];
  botNames: Map<string | undefined, string>;
}) {
  const [kind, setKind] = useState("");
  const [tag, setTag] = useState("");
  const tags = [...new Set(entries.map((entry) => entry.tag).filter(Boolean))];
  const visible = entries.filter((entry) => (!kind || kind === entry.kind) && (!tag || tag === entry.tag));

  return (
    <Section title="Timeline">
      <EntryComposer task={task} />
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Select
          className="w-auto"
          aria-label="Entry kind"
          value={kind}
          onChange={(event) => setKind(event.target.value)}
        >
          <option value="">All kinds</option>
          {Object.entries(kinds).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </Select>
        <Select
          className="w-auto"
          aria-label="Entry tag"
          value={tag}
          onChange={(event) => setTag(event.target.value)}
        >
          <option value="">All tags</option>
          {tag && !tags.includes(tag) ? <option value={tag}>{tag}</option> : null}
          {tags.map((value) => (
            <option key={value} value={value}>
              {value}
            </option>
          ))}
        </Select>
      </div>
      {visible.length ? (
        <ol className="divide-y divide-border">
          {visible.map((entry) => {
            const author = entryAuthor(entry, botNames);
            const at = new Date(entry.createdAt);

            return (
              <li key={entry.id} className="space-y-1 py-3">
                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span className="font-mono">#{String(entry.seq).padStart(2, "0")}</span>
                  <Badge variant="secondary">{author}</Badge>
                  <span>{kinds[entry.kind]}</span>
                  {entry.decision ? (
                    <span>{entry.decision === "approve" ? "Approved" : "Changes requested"}</span>
                  ) : entry.status ? (
                    <span>{statusLabel[entry.status]}</span>
                  ) : null}
                  {entry.tag ? <Badge variant="outline">{entry.tag}</Badge> : null}
                  <time dateTime={at.toISOString()}>{at.toLocaleString()}</time>
                </div>
                {entry.content ? (
                  <p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{entry.content}</p>
                ) : null}
              </li>
            );
          })}
        </ol>
      ) : (
        <Empty title="No entries">No timeline entries match these filters.</Empty>
      )}
    </Section>
  );
}

function entryAuthor(entry: BoardTaskEntry, botNames: Map<string | undefined, string>) {
  if (entry.trust === "external" || entry.actorKind === "external_agent") return "External";

  if (entry.trust === "agent") return `Bot: ${botNames.get(entry.actorBotId) ?? entry.actorBotId}`;

  if (entry.actorKind === "system") return "System";

  if (entry.actorKind === "unknown") return "Source not recorded";

  return "Owner";
}

function EntryComposer({ task }: { task: BoardTaskSummary }) {
  const [note, setNote] = useState("");
  const pending = useRef<{ id: string; text: string; agentId: string } | null>(null);
  const client = useQueryClient();
  const navigate = useNavigate();

  const append = useMutation({
    mutationFn: () =>
      createBoardEntry({ path: { taskKey: task.key }, body: { content: note }, throwOnError: true }),
    onSuccess: async () => {
      setNote("");
      await invalidateBoard(client);
    },
  });

  const send = useMutation({
    mutationFn: async () => {
      const text = note.trim();
      const agentId = task.assigneeBotId;

      const id =
        pending.current?.text === text && pending.current.agentId === agentId ? pending.current.id : newId();

      pending.current = { id, text, agentId };
      const main = await getMainChat({ path: { agentId }, throwOnError: true });
      await sendMessage({
        path: { agentId, chatId: main.data.id },
        body: { id, text, workTaskId: task.id },
        throwOnError: true,
      });

      return agentId;
    },
    onSuccess: async (agentId) => {
      pending.current = null;
      setNote("");
      await invalidateBoard(client);
      await navigate({ to: "/a/$agentId", params: { agentId } });
    },
  });

  const busy = append.isPending || send.isPending;

  return (
    <>
      <form
        className="space-y-2"
        onSubmit={(event) => {
          event.preventDefault();

          if (!busy && note.trim()) append.mutate();
        }}
      >
        <Textarea
          aria-label="Task note"
          placeholder="Write a note or send a message to Main"
          value={note}
          maxLength={32000}
          disabled={busy}
          onChange={(event) => setNote(event.target.value)}
        />
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={!note.trim() || busy}>
            Add note
          </Button>
          {!isClosed(task.status) ? (
            <Button
              type="button"
              variant="secondary"
              disabled={!note.trim() || busy}
              onClick={() => send.mutate()}
            >
              Send to Main
            </Button>
          ) : null}
        </div>
      </form>
      {append.error ? <Failure title="Could not add note" error={append.error} /> : null}
      {send.error ? <Failure title="Could not send to Main" error={send.error} /> : null}
    </>
  );
}
