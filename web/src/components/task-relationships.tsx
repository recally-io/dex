import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getBoardTaskOptions } from "../api/generated/@tanstack/react-query.gen";
import { addBoardDependency, removeBoardDependency, splitBoardTask } from "../api/generated/sdk.gen";
import type { BoardTaskDetail, BoardTaskRef, BoardTaskSummary } from "../api/generated/types.gen";
import { invalidateBoard } from "../lib/board-stream";
import { statusLabel } from "../lib/board";
import { Failure } from "./feedback";
import { Select } from "./form";
import { Section } from "./page";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

function TaskLink({ task }: { task: BoardTaskRef }) {
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <Link className="break-words font-medium underline" to="/board/$taskKey" params={{ taskKey: task.key }}>
        <span className="font-mono text-muted-foreground">{task.key}</span> {task.title}
      </Link>
      <Badge variant="outline">{statusLabel[task.status]}</Badge>
    </div>
  );
}

export function TaskRelationships({ detail }: { detail: BoardTaskDetail }) {
  const { task, parentTask, subtasks, canSplit } = detail;
  const client = useQueryClient();
  const [title, setTitle] = useState("");

  const split = useMutation({
    mutationFn: (childTitle: string) =>
      splitBoardTask({ path: { taskKey: task.key }, body: { title: childTitle }, throwOnError: true }),
    onSuccess: () => {
      setTitle("");

      return invalidateBoard(client);
    },
  });

  return (
    <>
      {parentTask ? (
        <Section title="Parent Task">
          <TaskLink task={parentTask} />
        </Section>
      ) : null}
      {subtasks.length > 0 || canSplit ? (
        <Section title="Subtasks">
          {subtasks.length ? (
            <ul className="divide-y divide-border">
              {subtasks.map((subtask) => (
                <li className="py-2 text-sm" key={subtask.key}>
                  <TaskLink task={subtask} />
                </li>
              ))}
            </ul>
          ) : null}
          {canSplit ? (
            <form
              className="flex flex-wrap gap-2"
              onSubmit={(event) => {
                event.preventDefault();

                if (title.trim()) split.mutate(title.trim());
              }}
            >
              <Input
                className="min-w-40 flex-1"
                aria-label="Subtask title"
                placeholder="Subtask title"
                value={title}
                maxLength={160}
                onChange={(event) => setTitle(event.target.value)}
              />
              <Button type="submit" variant="secondary" disabled={!title.trim() || split.isPending}>
                Add subtask
              </Button>
            </form>
          ) : null}
          {split.isError ? <Failure title="Could not add subtask" error={split.error} /> : null}
        </Section>
      ) : null}
      {parentTask ? (
        <Dependencies task={task} parentKey={parentTask.key} dependencies={detail.dependencies} />
      ) : null}
    </>
  );
}

function Dependencies({
  task,
  parentKey,
  dependencies,
}: {
  task: BoardTaskSummary;
  parentKey: string;
  dependencies: BoardTaskRef[];
}) {
  const client = useQueryClient();
  const parent = useQuery(getBoardTaskOptions({ path: { taskKey: parentKey } }));
  const [selected, setSelected] = useState("");

  const siblings = (parent.data?.subtasks ?? []).filter(
    (sibling) =>
      sibling.key !== task.key && !dependencies.some((dependency) => dependency.key === sibling.key),
  );

  const change = useMutation({
    mutationFn: async ({ depKey, add }: { depKey: string; add: boolean }) => {
      if (add) {
        await addBoardDependency({
          path: { taskKey: task.key },
          body: { dependsOn: depKey },
          throwOnError: true,
        });
      } else {
        await removeBoardDependency({ path: { taskKey: task.key, depKey }, throwOnError: true });
      }
    },
    onSuccess: (_, { add }) => {
      if (add) setSelected("");

      return invalidateBoard(client);
    },
  });

  return (
    <Section title="Dependencies" caption="This Task waits for incomplete Tasks in this list.">
      {dependencies.length ? (
        <ul className="divide-y divide-border">
          {dependencies.map((dependency) => (
            <li
              className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm"
              key={dependency.key}
            >
              <TaskLink task={dependency} />
              <Button
                size="sm"
                variant="ghost"
                aria-label={`Remove dependency on ${dependency.key}`}
                disabled={change.isPending}
                onClick={() => change.mutate({ depKey: dependency.key, add: false })}
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No dependencies.</p>
      )}
      {siblings.length ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();

            if (siblings.some((sibling) => sibling.key === selected))
              change.mutate({ depKey: selected, add: true });
          }}
        >
          <Select
            aria-label="Sibling Task"
            value={selected}
            onChange={(event) => setSelected(event.target.value)}
          >
            <option value="">Select a sibling Task</option>
            {siblings.map((sibling) => (
              <option key={sibling.key} value={sibling.key}>
                {sibling.key} · {sibling.title}
              </option>
            ))}
          </Select>
          <Button type="submit" variant="secondary" disabled={!selected || change.isPending}>
            Add dependency
          </Button>
        </form>
      ) : null}
      {change.isError ? <Failure title="Could not change dependency" error={change.error} /> : null}
    </Section>
  );
}
