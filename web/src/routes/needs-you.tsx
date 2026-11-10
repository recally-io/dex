import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { getWorkOptions } from "../api/generated/@tanstack/react-query.gen";
import { useHandled } from "../lib/handled";
import { needsTask } from "../lib/work";
import { Empty, FramedPage } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";

export function NeedsYouPage() {
  const [now] = useState(() => Date.now());

  const work = useQuery({
    ...getWorkOptions({ query: { from: now, to: now + 7 * 86400000 } }),
    refetchInterval: 10000,
  });

  const change = useHandled();

  const tasks = (work.data?.sessions ?? []).filter(
    ({ chat }) => chat.task && needsTask({ ...chat.task, handledAt: undefined }),
  );

  const runs = (work.data?.inbox ?? []).filter((item) => item.outcome === "failed");
  const setup = (work.data?.automations ?? []).filter((item) => item.setup);
  const names = new Map(work.data?.agents.map((agent) => [agent.id ?? "default", agent.name]));
  const openTasks = tasks.filter(({ chat }) => chat.task?.handledAt === undefined);
  const handledTasks = tasks.filter(({ chat }) => chat.task?.handledAt !== undefined);
  const openRuns = runs.filter((run) => !run.dismissed);
  const handledRuns = runs.filter((run) => run.dismissed);

  return (
    <FramedPage
      headerTitle="Needs you"
      title="Needs you"
      description="Each item stays here until you act on it. Opening a session only marks it read."
    >
      {work.isPending ? (
        <Loading />
      ) : work.isError ? (
        <Failure title="Could not load work" error={work.error} retry={() => void work.refetch()} />
      ) : null}
      {change.isError ? <Failure title="Could not change this item" error={change.error} /> : null}
      {!work.isPending && !work.isError && !openTasks.length && !openRuns.length && !setup.length ? (
        <Empty title="You're all caught up">No work needs your attention.</Empty>
      ) : null}
      <div className="space-y-4">
        {openTasks.map(
          ({ agentId, chat }) =>
            chat.task && (
              <article
                key={chat.task.id}
                className="space-y-2 border border-border border-l-4 border-l-orange p-4"
              >
                <p className="text-xs text-muted-foreground">
                  {names.get(agentId)} · {chat.task.reportOutcome === "blocked" ? "Blocked" : "Task failed"}
                </p>
                <h2 className="font-semibold">{chat.task.title}</h2>
                <p className="text-sm text-muted-foreground">
                  Opening does not clear this item. Mark it handled when you are done.
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button
                    render={
                      <Link to="/a/$agentId/tasks/$taskId" params={{ agentId, taskId: chat.task.id }} />
                    }
                  >
                    Open session
                  </Button>
                  <Button
                    variant="outline"
                    disabled={change.isPending}
                    onClick={() => change.mutate({ kind: "task", agentId, id: chat.task!.id, undo: false })}
                  >
                    Mark handled
                  </Button>
                </div>
              </article>
            ),
        )}
        {openRuns.map((run) => (
          <article key={run.runId} className="space-y-2 border border-border border-l-4 border-l-orange p-4">
            <p className="text-xs text-muted-foreground">{run.agentName} · Run failed</p>
            <h2 className="font-semibold">{run.automationName}</h2>
            <p className="text-sm">{run.summary}</p>
            <p className="text-sm text-muted-foreground">
              Opening does not clear this item. Mark it handled when you are done.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button render={<Link to="/inbox/$runId" params={{ runId: run.runId }} />}>Open result</Button>
              <Button
                variant="outline"
                disabled={change.isPending}
                onClick={() => change.mutate({ kind: "run", id: run.runId, undo: false })}
              >
                Mark handled
              </Button>
            </div>
          </article>
        ))}
        {setup.map(({ agentId, automation }) => (
          <article
            key={automation.id}
            className="space-y-2 border border-border border-l-4 border-l-orange p-4"
          >
            <p className="text-xs text-muted-foreground">{names.get(agentId)} · Setup</p>
            <h2 className="font-semibold">{automation.name}</h2>
            <p className="text-sm">This bot has no available model. Choose one to run this automation.</p>
            <Button render={<Link to="/a/$agentId/agent" params={{ agentId }} />}>Choose a model</Button>
            <p className="text-xs text-muted-foreground">Leaves when fixed.</p>
          </article>
        ))}
        {handledTasks.length || handledRuns.length ? (
          <h2 className="pt-4 text-xs text-muted-foreground">HANDLED</h2>
        ) : null}
        {handledTasks.map(
          ({ agentId, chat }) =>
            chat.task && (
              <div key={chat.task.id} className="flex items-center justify-between border border-border p-3">
                <span>{chat.task.title}</span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={change.isPending}
                  onClick={() => change.mutate({ kind: "task", agentId, id: chat.task!.id, undo: true })}
                >
                  Undo
                </Button>
              </div>
            ),
        )}
        {handledRuns.map((run) => (
          <div key={run.runId} className="flex items-center justify-between border border-border p-3">
            <span>{run.automationName}</span>
            <Button
              size="sm"
              variant="outline"
              disabled={change.isPending}
              onClick={() => change.mutate({ kind: "run", id: run.runId, undo: true })}
            >
              Undo
            </Button>
          </div>
        ))}
      </div>
    </FramedPage>
  );
}
