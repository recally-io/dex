import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { addDays, format, isSameDay, startOfWeek } from "date-fns";
import { getWorkOptions } from "../api/generated/@tanstack/react-query.gen";
import {
  boardBucket,
  resolveRange,
  runTone,
  toneClass,
  toneLabel,
  tones,
  type EventTone,
  type WorkSearch,
} from "../lib/work";
import { readSeen } from "../lib/tasks";
import { useHandled } from "../lib/handled";
import { Empty, FramedPage } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button, buttonVariants } from "../components/ui/button";

function sessionTone(bucket: "needs" | "working" | "idle", unread: boolean): EventTone {
  if (bucket !== "idle") return bucket;

  return unread ? "result" : "quiet";
}

const kinds = [
  [undefined, "All"],
  ["once", "One-off"],
  ["repeat", "Repeating"],
] as const;

export function WorkPage({
  search,
  onSearch,
}: {
  search: WorkSearch;
  onSearch: (patch: Partial<WorkSearch>) => void;
}) {
  const calendar = search.view === "calendar";
  const [now] = useState(() => Date.now());
  const [selected, setSelected] = useState("");
  const handle = useHandled();
  const week = startOfWeek(now, { weekStartsOn: 1 });
  const days = Array.from({ length: 7 }, (_, i) => addDays(week, i));
  const { name: rangeName, start, end } = resolveRange(search.range, now);

  const work = useQuery({
    ...getWorkOptions({ query: { from: week.getTime(), to: addDays(week, 7).getTime() } }),
    refetchInterval: 10000,
  });

  const names = new Map(work.data?.agents.map((agent) => [agent.id ?? "default", agent.name]));
  const inbox = work.data?.inbox ?? [];
  const items = new Map(inbox.map((item) => [item.runId, item]));
  const inRange = (at: number) => at >= start.getTime() && at < end.getTime();
  const kindOk = (repeat: boolean) => !search.kind || (search.kind === "repeat") === repeat;

  const failures = new Set(
    inbox.filter((item) => item.outcome === "failed" && !item.dismissed).map((item) => item.automationId),
  );

  const sessions = (work.data?.sessions ?? []).map(({ agentId, chat }) => ({
    id: `chat:${agentId}:${chat.id}`,
    agentId,
    title: chat.main ? "Main" : chat.title || "Chat",
    at: chat.updatedAt,
    bucket: boardBucket(chat),
    chat,
    automation: undefined,
    setup: false,
    unread:
      !!chat.task &&
      chat.task.endedAt !== undefined &&
      (readSeen(agentId)[chat.task.id] ?? 0) < chat.task.endedAt,
  }));

  const automations = (work.data?.automations ?? []).map((item) => ({
    id: `automation:${item.automation.id}`,
    agentId: item.agentId,
    title: item.automation.name,
    at: item.automation.lastRun?.createdAt ?? item.automation.updatedAt,
    bucket:
      item.setup || failures.has(item.automation.id)
        ? ("needs" as const)
        : item.automation.lastRun?.status === "running"
          ? ("working" as const)
          : ("idle" as const),
    chat: undefined,
    automation: item.automation,
    setup: item.setup,
    unread: false,
  }));

  const all = [...sessions, ...automations].filter((card) => kindOk(!!card.automation));

  const cards = all
    .filter((card) => card.bucket !== "idle" || inRange(card.at))
    .sort((a, b) => Number(b.unread) - Number(a.unread) || b.at - a.at);

  const current = all.find((card) => card.id === selected);

  // Mark handled is the way out for a blocked or failed task and for failed runs of an automation.
  const openFailures = current?.automation
    ? inbox.filter(
        (item) =>
          item.automationId === current.automation!.id && item.outcome === "failed" && !item.dismissed,
      )
    : [];

  const handledTask = current?.bucket === "needs" ? current.chat?.task : undefined;

  const events: { key: string; id: string; at: number; label: string; tone: EventTone; repeat: boolean }[] = [
    ...sessions
      .filter((item) => item.chat?.task?.startedAt)
      .map((item) => ({
        key: item.id,
        id: item.id,
        at: item.chat!.task!.startedAt!,
        label: item.title,
        tone: sessionTone(item.bucket, item.unread),
        repeat: false,
      })),
    ...(work.data?.automations ?? []).flatMap((item) => [
      ...item.runs.map((run) => ({
        key: run.id,
        id: `automation:${item.automation.id}`,
        at: run.dueAt ?? run.createdAt,
        label: `${item.automation.name}${run.status === "skipped" ? " · skipped" : ""}`,
        tone: runTone(run, items.get(run.id)),
        repeat: true,
      })),
      ...item.scheduled.map((at) => ({
        key: `${item.automation.id}:${at}`,
        id: `automation:${item.automation.id}`,
        at,
        label: `${item.automation.name} · scheduled`,
        tone: "planned" as const,
        repeat: true,
      })),
    ]),
  ].filter((event) => kindOk(event.repeat));

  return (
    <FramedPage headerTitle="Work" title="Work" description="Sessions and automation runs across all bots.">
      <div className="mb-5 flex flex-wrap gap-x-6 gap-y-2" aria-label="Work view, date range and kind">
        <div className="flex gap-2" role="group" aria-label="View">
          <Button
            size="sm"
            variant={calendar ? "outline" : "default"}
            aria-pressed={!calendar}
            onClick={() => onSearch({ view: undefined })}
          >
            Board
          </Button>
          <Button
            size="sm"
            variant={calendar ? "default" : "outline"}
            aria-pressed={calendar}
            onClick={() => onSearch({ view: "calendar" })}
          >
            Calendar
          </Button>
        </div>
        <div className="flex flex-wrap gap-2" role="group" aria-label="Date range">
          <Button
            size="sm"
            variant={rangeName === "today" ? "secondary" : "outline"}
            aria-pressed={rangeName === "today"}
            onClick={() => onSearch({ range: "today" })}
          >
            Today
          </Button>
          <Button
            size="sm"
            variant={rangeName === "week" ? "secondary" : "outline"}
            aria-pressed={rangeName === "week"}
            onClick={() => onSearch({ range: undefined })}
          >
            This week
          </Button>
          {rangeName === "day" ? (
            <Button size="sm" variant="secondary" onClick={() => onSearch({ range: undefined })}>
              {format(start, "EEE d MMM")} ×
            </Button>
          ) : null}
        </div>
        <div className="flex gap-2" role="group" aria-label="Kind">
          {kinds.map(([value, label]) => (
            <Button
              key={label}
              size="sm"
              variant={search.kind === value ? "secondary" : "outline"}
              aria-pressed={search.kind === value}
              onClick={() => onSearch({ kind: value })}
            >
              {label}
            </Button>
          ))}
        </div>
      </div>
      {work.isPending ? (
        <Loading />
      ) : work.isError ? (
        <Failure title="Could not load work" error={work.error} retry={() => void work.refetch()} />
      ) : null}
      <div className="flex flex-wrap gap-4">
        <div className="min-w-0 flex-1">
          {calendar ? (
            <>
              <div className="grid grid-cols-7 overflow-x-auto border-t border-l border-border">
                {days.map((day) => (
                  <div
                    key={day.toISOString()}
                    className={`min-h-72 min-w-28 space-y-1 border-r border-b border-border p-2 ${inRange(day.getTime()) ? "" : "opacity-50"}`}
                  >
                    <button
                      type="button"
                      className="w-full text-left text-sm font-semibold"
                      onClick={() => onSearch({ range: format(day, "yyyy-MM-dd") })}
                    >
                      {format(day, "EEE d")}
                    </button>
                    {events
                      .filter((event) => isSameDay(event.at, day))
                      .sort((a, b) => a.at - b.at)
                      .map((event) => (
                        <button
                          key={event.key}
                          type="button"
                          className={`block w-full border p-1 text-left text-xs ${toneClass[event.tone]} ${selected === event.id ? "outline-2 -outline-offset-1 outline-foreground" : ""}`}
                          onClick={() => setSelected(event.id)}
                        >
                          {format(event.at, "HH:mm")} {event.label}
                        </button>
                      ))}
                  </div>
                ))}
              </div>
              <ul
                aria-label="Legend"
                className="flex flex-wrap gap-x-4 gap-y-1 pt-3 text-[11px] text-muted-foreground"
              >
                <li>Each row is one run or one session start. Click a day to filter both views.</li>
                {tones.map((tone) => (
                  <li key={tone} className="flex items-center gap-1.5">
                    <span aria-hidden className={`h-2.5 w-3.5 border ${toneClass[tone]}`} />
                    {toneLabel[tone]}
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <div className="grid gap-4 md:grid-cols-3">
              {(
                [
                  ["needs", "Needs you"],
                  ["working", "Working"],
                  ["idle", "Idle"],
                ] as const
              ).map(([bucket, label]) => (
                <section key={bucket} aria-label={label} className="space-y-2">
                  <h2 className="border-b border-border pb-2 font-semibold">
                    {label} · {cards.filter((card) => card.bucket === bucket).length}
                  </h2>
                  <p className="text-xs text-muted-foreground">
                    {bucket === "idle" ? "Unread first." : "Any date."}
                  </p>
                  {cards
                    .filter((card) => card.bucket === bucket)
                    .map((card) => (
                      <button
                        key={card.id}
                        type="button"
                        className={`block w-full space-y-2 border p-3 text-left ${selected === card.id ? "border-foreground" : "border-border"}`}
                        onClick={() => setSelected(card.id)}
                      >
                        <span className="block text-xs text-muted-foreground">
                          {names.get(card.agentId)} {card.unread ? "· Unread" : ""}
                        </span>
                        <span className="block font-semibold">
                          {card.automation ? "↻ " : ""}
                          {card.title}
                        </span>
                        <span className="block text-xs text-muted-foreground">
                          {format(card.at, "d MMM HH:mm")}
                        </span>
                        {card.chat?.task?.status === "running" &&
                        Date.now() - (card.chat.task.lastActivityAt ?? card.chat.task.startedAt ?? now) >=
                          30 * 60000 ? (
                          <span className="block text-xs text-orange">No output · still Working</span>
                        ) : null}
                      </button>
                    ))}
                </section>
              ))}
            </div>
          )}
        </div>
        <aside aria-label="Selected session" className="w-full space-y-3 border-l border-border pl-4 md:w-56">
          <h2 className="text-xs text-muted-foreground">SELECTED</h2>
          {current ? (
            <>
              <p className="font-semibold">
                {current.automation ? "↻ " : ""}
                {current.title}
              </p>
              <p className="text-sm text-muted-foreground">
                {names.get(current.agentId)} ·{" "}
                {current.bucket === "needs" ? "Needs you" : current.bucket === "working" ? "Working" : "Idle"}
              </p>
              <p className="text-xs text-muted-foreground">
                {current.bucket !== "needs"
                  ? "Opening it marks it read."
                  : current.setup && !openFailures.length
                    ? "Leaves Needs you when fixed."
                    : "Opening does not clear it. Mark handled when you are done."}
              </p>
              <div className="flex flex-wrap gap-2">
                {current.automation ? (
                  <Link
                    className={buttonVariants({ size: "sm" })}
                    to="/a/$agentId/automations/$automationId"
                    params={{ agentId: current.agentId, automationId: current.automation.id }}
                  >
                    Open runs
                  </Link>
                ) : current.chat?.task ? (
                  <Link
                    className={buttonVariants({ size: "sm" })}
                    to="/a/$agentId/tasks/$taskId"
                    params={{ agentId: current.agentId, taskId: current.chat.task.id }}
                  >
                    Open session
                  </Link>
                ) : current.chat?.main ? (
                  <Link
                    className={buttonVariants({ size: "sm" })}
                    to="/a/$agentId"
                    params={{ agentId: current.agentId }}
                  >
                    Open session
                  </Link>
                ) : current.chat ? (
                  <Link
                    className={buttonVariants({ size: "sm" })}
                    to="/a/$agentId/c/$chatId"
                    params={{ agentId: current.agentId, chatId: String(current.chat.id) }}
                  >
                    Open session
                  </Link>
                ) : null}
                {current.setup ? (
                  <Link
                    className={buttonVariants({ size: "sm", variant: "outline" })}
                    to="/a/$agentId/agent"
                    params={{ agentId: current.agentId }}
                  >
                    Choose a model
                  </Link>
                ) : null}
                {handledTask || openFailures.length ? (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={handle.isPending}
                    onClick={() =>
                      handle.mutate(
                        handledTask
                          ? { kind: "task", agentId: current!.agentId, id: handledTask.id, undo: false }
                          : openFailures.map((item) => ({ kind: "run", id: item.runId, undo: false })),
                      )
                    }
                  >
                    Mark handled
                  </Button>
                ) : null}
              </div>
              {handle.isError ? <Failure title="Could not mark handled" error={handle.error} /> : null}
            </>
          ) : (
            <Empty title="Select a session">Choose a card or calendar event.</Empty>
          )}
        </aside>
      </div>
    </FramedPage>
  );
}
