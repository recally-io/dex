import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { listHeldEventsOptions, listWorkTasksOptions } from "../api/generated/@tanstack/react-query.gen";
import { attachHeldEvent, dismissHeldEvent } from "../api/generated/sdk.gen";
import type { HeldEvent, WorkTask } from "../api/generated/types.gen";
import { Failure, Loading } from "./feedback";
import { Select } from "./form";
import { Section } from "./page";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

const reasons: Record<HeldEvent["reason"], string> = {
  unmatched: "No Task",
  conflict: "Links to several Tasks",
};

// A source link comes from outside Dex. Only a web address can open.
function sourceHref(url: string) {
  return /^https?:\/\//i.test(url) ? url : undefined;
}

export function HeldEvents({ tasks }: { tasks: WorkTask[] }) {
  const held = useQuery({ ...listHeldEventsOptions(), refetchInterval: 10_000 });
  const open = tasks.filter((task) => task.lifecycle === "open");

  return (
    <Section title="Held events">
      {held.isPending ? (
        <Loading />
      ) : held.isError ? (
        <Failure title="Could not load held events" error={held.error} />
      ) : held.data.events.length ? (
        <ul className="divide-y divide-border">
          {held.data.events.map((event) => (
            <HeldEventItem key={event.id} event={event} tasks={open} />
          ))}
        </ul>
      ) : (
        <p className="px-2 py-3 text-sm text-muted-foreground">No held events.</p>
      )}
    </Section>
  );
}

function HeldEventItem({ event, tasks }: { event: HeldEvent; tasks: WorkTask[] }) {
  const [selected, setSelected] = useState("");
  const client = useQueryClient();
  const task = tasks.find((item) => item.id === selected) ?? tasks[0];
  const href = sourceHref(event.sourceUrl);

  const refresh = async () => {
    await client.invalidateQueries({ queryKey: listHeldEventsOptions().queryKey });
    await client.invalidateQueries({ queryKey: listWorkTasksOptions().queryKey.slice(0, 1) });
  };

  const attach = useMutation({
    mutationFn: (target: WorkTask) =>
      attachHeldEvent({
        path: { eventId: event.id },
        body: { agentId: target.agentId, taskId: target.id },
        throwOnError: true,
      }),
    onSuccess: refresh,
  });

  const dismiss = useMutation({
    mutationFn: () => dismissHeldEvent({ path: { eventId: event.id }, throwOnError: true }),
    onSuccess: refresh,
  });

  return (
    <li className="space-y-2 px-2 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{event.eventType}</span>
        <Badge variant="outline">{reasons[event.reason]}</Badge>
        <span className="text-sm text-muted-foreground">
          {event.authorLogin || "Unknown author"} · {event.authorAssociation || "No author class"}
        </span>
        {href ? (
          <a className="text-sm underline" href={href} target="_blank" rel="noreferrer">
            Source
          </a>
        ) : null}
      </div>
      <p className="line-clamp-4 whitespace-pre-wrap break-words text-sm">{event.content}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          aria-label="Task"
          value={task?.id ?? ""}
          disabled={!tasks.length}
          onChange={(change) => setSelected(change.target.value)}
        >
          {tasks.map((item) => (
            <option key={item.id} value={item.id}>
              {item.title}
            </option>
          ))}
        </Select>
        <Button size="sm" disabled={!task || attach.isPending} onClick={() => task && attach.mutate(task)}>
          Attach
        </Button>
        <Button size="sm" variant="ghost" disabled={dismiss.isPending} onClick={() => dismiss.mutate()}>
          Dismiss
        </Button>
      </div>
      {attach.error ? <Failure title="Could not attach event" error={attach.error} /> : null}
      {dismiss.error ? <Failure title="Could not dismiss event" error={dismiss.error} /> : null}
    </li>
  );
}
