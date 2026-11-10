import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { addDays, format, isSameDay, startOfDay, startOfWeek } from "date-fns";
import { getWorkOptions } from "../api/generated/@tanstack/react-query.gen";
import { boardBucket } from "../lib/work";
import { readSeen } from "../lib/tasks";
import { Empty, FramedPage } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button, buttonVariants } from "../components/ui/button";

type Range = "today" | "week" | `day:${string}`;

function initialRange(): Range {
  const saved = sessionStorage.getItem("dex-work-range");

  if (saved === "today" || saved === "week") return saved;

  if (saved?.startsWith("day:") && !Number.isNaN(Date.parse(saved.slice(4)))) return `day:${saved.slice(4)}`;

  return "week";
}

export function WorkPage({ calendar }: { calendar: boolean }) {
  const [now] = useState(() => Date.now());
  const [range, setRange] = useState<Range>(initialRange);
  const [selected, setSelected] = useState("");
  const week = startOfWeek(now, { weekStartsOn: 1 });
  const days = Array.from({ length: 7 }, (_, i) => addDays(week, i));

  const work = useQuery({
    ...getWorkOptions({ query: { from: week.getTime(), to: addDays(week, 7).getTime() } }),
    refetchInterval: 10000,
  });

  const names = new Map(work.data?.agents.map((agent) => [agent.id ?? "default", agent.name]));

  const start =
    range === "week" ? week : range === "today" ? startOfDay(now) : startOfDay(new Date(range.slice(4)));

  const end = range === "week" ? addDays(week, 7) : addDays(start, 1);

  const inRange = (at: number) => at >= start.getTime() && at < end.getTime();

  const selectRange = (next: Range) => {
    sessionStorage.setItem("dex-work-range", next);
    setRange(next);
  };

  const failures = new Set(
    (work.data?.inbox ?? [])
      .filter((item) => item.outcome === "failed" && !item.dismissed)
      .map((item) => item.automationId),
  );

  const sessions = (work.data?.sessions ?? []).map(({ agentId, chat }) => ({
    id: `chat:${agentId}:${chat.id}`,
    agentId,
    title: chat.main ? "Main" : chat.title || "Chat",
    at: chat.updatedAt,
    bucket: boardBucket(chat),
    chat,
    automation: undefined,
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
    unread: false,
  }));

  const cards = [...sessions, ...automations]
    .filter((card) => card.bucket !== "idle" || inRange(card.at))
    .sort((a, b) => Number(b.unread) - Number(a.unread) || b.at - a.at);

  const current = [...sessions, ...automations].find((card) => card.id === selected);

  const events = [
    ...sessions
      .filter((item) => item.chat?.task?.startedAt)
      .map((item) => ({
        key: item.id,
        id: item.id,
        at: item.chat!.task!.startedAt!,
        label: item.title,
        scheduled: false,
      })),
    ...(work.data?.automations ?? []).flatMap((item) => [
      ...item.runs.map((run) => ({
        key: run.id,
        id: `automation:${item.automation.id}`,
        at: run.dueAt ?? run.createdAt,
        label: `${item.automation.name}${run.status === "skipped" ? " · skipped" : ""}`,
        scheduled: false,
      })),
      ...item.scheduled.map((at) => ({
        key: `${item.automation.id}:${at}`,
        id: `automation:${item.automation.id}`,
        at,
        label: `${item.automation.name} · scheduled`,
        scheduled: true,
      })),
    ]),
  ];

  return (
    <FramedPage headerTitle="Work" title="Work" description="Sessions and automation runs across all bots.">
      <div className="mb-5 flex flex-wrap gap-2" aria-label="Work view and date range">
        <Link
          className={buttonVariants({ variant: calendar ? "outline" : "default", size: "sm" })}
          to="/work"
        >
          Board
        </Link>
        <Link
          className={buttonVariants({ variant: calendar ? "default" : "outline", size: "sm" })}
          to="/work/calendar"
        >
          Calendar
        </Link>
        <Button
          size="sm"
          variant={range === "today" ? "secondary" : "outline"}
          onClick={() => selectRange("today")}
        >
          Today
        </Button>
        <Button
          size="sm"
          variant={range === "week" ? "secondary" : "outline"}
          onClick={() => selectRange("week")}
        >
          This week
        </Button>
        {range.startsWith("day:") ? (
          <Button size="sm" variant="secondary" onClick={() => selectRange("week")}>
            {format(start, "EEE d MMM")} ×
          </Button>
        ) : null}
      </div>
      {work.isPending ? (
        <Loading />
      ) : work.isError ? (
        <Failure title="Could not load work" error={work.error} retry={() => void work.refetch()} />
      ) : null}
      <div className="flex flex-wrap gap-4">
        <div className="min-w-0 flex-1">
          {calendar ? (
            <div className="grid grid-cols-7 overflow-x-auto border-t border-l border-border">
              {days.map((day) => (
                <div
                  key={day.toISOString()}
                  className={`min-h-72 min-w-28 space-y-1 border-r border-b border-border p-2 ${inRange(day.getTime()) ? "" : "opacity-50"}`}
                >
                  <button
                    type="button"
                    className="w-full text-left text-sm font-semibold"
                    onClick={() => selectRange(`day:${format(day, "yyyy-MM-dd'T'HH:mm:ss")}`)}
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
                        className={`block w-full border p-1 text-left text-xs ${event.scheduled ? "border-dashed" : "border-border"}`}
                        onClick={() => setSelected(event.id)}
                      >
                        {format(event.at, "HH:mm")} {event.label}
                      </button>
                    ))}
                </div>
              ))}
            </div>
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
              <p className="font-semibold">{current.title}</p>
              <p className="text-sm text-muted-foreground">
                {names.get(current.agentId)} ·{" "}
                {current.bucket === "needs" ? "Needs you" : current.bucket === "working" ? "Working" : "Idle"}
              </p>
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
            </>
          ) : (
            <Empty title="Select a session">Choose a card or calendar event.</Empty>
          )}
        </aside>
      </div>
    </FramedPage>
  );
}
