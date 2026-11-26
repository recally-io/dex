import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { listAgentsOptions, listTasksOptions } from "../api/generated/@tanstack/react-query.gen";
import { heldEventsOptions } from "../lib/board-attention";
import { attachHeldEvent, dismissHeldEvent } from "../api/generated/sdk.gen";
import type { Agent, HeldEvent, TaskSummary } from "../api/generated/types.gen";
import { Failure } from "./feedback";
import { Select } from "./form";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

const reasons: Record<HeldEvent["reason"], string> = {
  unmatched: "No matching task",
  conflict: "Matches several tasks",
};

// A source link comes from outside Dex. Only a web address can open.
function sourceHref(url: string) {
  return /^https?:\/\//i.test(url) ? url : undefined;
}

export function HeldEventRows({ events, total }: { events: HeldEvent[]; total: number }) {
  const agents = useQuery(listAgentsOptions());

  const tasks = useQuery({
    ...listTasksOptions({ query: { includeClosed: false } }),
    enabled: events.length > 0,
  });

  if (!events.length) return null;

  const open = tasks.data?.tasks ?? [];
  const botList = agents.data?.agents ?? [];

  return (
    <>
      {tasks.isError ? <Failure title="Could not load tasks for attachment" error={tasks.error} /> : null}
      {events.map((event) => (
        <HeldEventRow key={event.id} event={event} tasks={open} agents={botList} />
      ))}
      {total > events.length ? (
        <li className="py-3.5 text-[13px] text-muted-foreground">
          Showing the newest {events.length} of {total} held events.
        </li>
      ) : null}
    </>
  );
}

function HeldEventRow({ event, tasks, agents }: { event: HeldEvent; tasks: TaskSummary[]; agents: Agent[] }) {
  const [selected, setSelected] = useState("");
  const client = useQueryClient();
  // An attach also links the event objects, and no API removes a link. The owner must pick the Task.
  const task = tasks.find((item) => item.id === selected);
  const href = sourceHref(event.sourceUrl);

  const refresh = async () => {
    await client.invalidateQueries({ queryKey: heldEventsOptions().queryKey });
    await client.invalidateQueries({ queryKey: listTasksOptions().queryKey.slice(0, 1) });
  };

  const attach = useMutation({
    mutationFn: (target: TaskSummary) =>
      attachHeldEvent({
        path: { eventId: event.id },
        body: { agentId: target.assigneeBotId, taskId: target.id },
        throwOnError: true,
      }),
    onSuccess: refresh,
  });

  const dismiss = useMutation({
    mutationFn: () => dismissHeldEvent({ path: { eventId: event.id }, throwOnError: true }),
    onSuccess: refresh,
  });

  return (
    <li className="space-y-2.5 py-3.5">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="font-medium text-foreground">{event.eventType}</span>
        <Badge variant="outline">{reasons[event.reason]}</Badge>
        <span className="text-muted-foreground">
          {event.authorLogin || "Unknown author"}
          {event.authorAssociation ? ` · ${event.authorAssociation}` : ""}
        </span>
        {href ? (
          <a
            className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground"
            href={href}
            target="_blank"
            rel="noreferrer"
          >
            Source
            <ExternalLink className="size-3" />
          </a>
        ) : null}
      </div>
      {event.content ? (
        <p className="line-clamp-3 whitespace-pre-wrap break-words text-[13px] text-muted-foreground">
          {event.content}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Select
          className="h-8 w-auto max-w-full text-[13px]"
          aria-label="Task"
          value={task?.id ?? ""}
          disabled={!tasks.length}
          onChange={(change) => setSelected(change.target.value)}
        >
          <option value="">Attach to task…</option>
          {tasks.map((item) => (
            <option key={item.id} value={item.id}>
              {item.key} · {item.title} (
              {agents.find((agent) => agent.id === item.assigneeBotId)?.name ?? item.assigneeBotId})
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
