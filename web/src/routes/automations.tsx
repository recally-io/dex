import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, Clock, Plus, Play, Pencil, Trash2 } from "lucide-react";
import type { Automation, AutomationRun } from "../api/generated/types.gen";
import {
  listAutomationsOptions,
  getAutomationOptions,
  listAutomationRunsOptions,
  updateAutomationMutation,
  deleteAutomationMutation,
  runAutomationMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshAutomations } from "../lib/queries";
import { runTime, scheduleText } from "../lib/automations";
import { Button } from "../components/ui/button";
import { Failure, Loading } from "../components/feedback";
import { Confirm } from "../components/confirm";
import { AutomationDialog, EnabledSwitch } from "../components/automation-dialog";
import { cn } from "../lib/utils";

function SessionPill({ automation }: { automation: Automation }) {
  return (
    <span className="rounded-full bg-inset px-2 py-0.5 text-xs text-ink-2">
      {automation.session === "new" ? "new chat each run" : "one chat"}
    </span>
  );
}

function RunStatus({ run }: { run: AutomationRun }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-2 capitalize",
        run.status === "failed" && "text-destructive",
      )}
    >
      {run.status === "running" ? <span className="busy-dot" /> : null}
      {run.status}
    </span>
  );
}

function AutomationRow({ automation }: { automation: Automation }) {
  const update = useMutation({ ...updateAutomationMutation(), onSuccess: refreshAutomations });

  return (
    <article className="border-b border-line p-4 last:border-b-0 sm:p-5">
      <div className="flex items-start justify-between gap-4">
        <Link
          to="/automations/$automationId"
          params={{ automationId: automation.id }}
          className="min-w-0 flex-1 rounded-md hover:text-brand focus-visible:outline-2 focus-visible:outline-ring"
        >
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="break-words font-medium">{automation.name}</h2>
            <SessionPill automation={automation} />
          </div>
          <p className="mt-2 text-sm text-ink-2">{scheduleText(automation.schedule, automation.timeZone)}</p>
          <p className="mt-1 text-xs text-ink-3">{automation.timeZone}</p>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-2 text-xs text-ink-2">
            <span>
              Next:{" "}
              {automation.nextRuns[0]
                ? runTime(automation.nextRuns[0], automation.timeZone)
                : automation.enabled
                  ? "done"
                  : "paused"}
            </span>
            <span>Last: {automation.lastRun ? <RunStatus run={automation.lastRun} /> : "No runs yet"}</span>
          </div>
        </Link>
        <EnabledSwitch
          enabled={automation.enabled}
          disabled={update.isPending}
          label={`Enable ${automation.name}`}
          onChange={() =>
            update.mutate({
              path: { automationId: automation.id },
              body: {
                name: automation.name,
                prompt: automation.prompt,
                schedule: automation.schedule,
                timeZone: automation.timeZone,
                session: automation.session,
                enabled: !automation.enabled,
              },
            })
          }
        />
      </div>
      {update.isError ? (
        <div className="mt-3">
          <Failure title="Could not change automation" error={update.error} />
        </div>
      ) : null}
    </article>
  );
}

export function AutomationsPage() {
  const automations = useQuery({ ...listAutomationsOptions(), refetchInterval: 10_000 });
  const [create, setCreate] = useState(false);

  return (
    <div className="mx-auto max-w-[900px] px-5 py-8 md:px-8 md:py-10">
      <header className="mb-8 flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="flex items-center gap-3 text-2xl font-semibold tracking-tight">
            <Clock className="size-6 text-ink-2" />
            Automations
          </h1>
          <p className="mt-2 text-sm text-ink-2">Run a prompt on a schedule.</p>
        </div>
        <Button onClick={() => setCreate(true)}>
          <Plus />
          New automation
        </Button>
      </header>
      {automations.isPending ? (
        <Loading />
      ) : automations.isError ? (
        <Failure
          title="Could not load automations"
          error={automations.error}
          retry={() => void automations.refetch()}
        />
      ) : automations.data.automations.length ? (
        <div className="surface overflow-hidden">
          {automations.data.automations.map((automation) => (
            <AutomationRow key={automation.id} automation={automation} />
          ))}
        </div>
      ) : (
        <div className="surface px-6 py-12 text-center">
          <Clock className="mx-auto mb-4 size-8 text-ink-3" />
          <h2 className="font-medium">No automations yet</h2>
          <p className="mt-2 text-sm text-ink-2">Create an automation to run your first scheduled prompt.</p>
          <Button className="mt-5" variant="outline" onClick={() => setCreate(true)}>
            New automation
          </Button>
        </div>
      )}
      {create ? <AutomationDialog onClose={() => setCreate(false)} /> : null}
    </div>
  );
}

export function AutomationPage({ automationId }: { automationId: string }) {
  const navigate = useNavigate();

  const automation = useQuery({
    ...getAutomationOptions({ path: { automationId } }),
    refetchInterval: 10_000,
  });

  const runs = useQuery({
    ...listAutomationRunsOptions({ path: { automationId } }),
    refetchInterval: 10_000,
  });

  const [edit, setEdit] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const run = useMutation({ ...runAutomationMutation(), onSuccess: refreshAutomations });

  const remove = useMutation({
    ...deleteAutomationMutation(),
    onSuccess: async () => {
      await refreshAutomations();
      await navigate({ to: "/automations" });
    },
  });

  const data = automation.data;
  const latestRun = runs.data?.runs.find((history) => history.id === run.data?.id) ?? run.data;

  return (
    <div className="mx-auto max-w-[900px] px-5 py-8 md:px-8 md:py-10">
      <Link
        to="/automations"
        className="mb-6 inline-flex items-center gap-2 text-sm text-ink-2 hover:text-ink"
      >
        <ArrowLeft className="size-4" />
        Automations
      </Link>
      {automation.isPending ? (
        <Loading />
      ) : automation.isError ? (
        <Failure
          title="Could not load automation"
          error={automation.error}
          retry={() => void automation.refetch()}
        />
      ) : data ? (
        <>
          <header className="mb-6">
            <div className="flex flex-wrap items-center gap-3">
              <h1 className="break-words text-2xl font-semibold tracking-tight">{data.name}</h1>
              <SessionPill automation={data} />
            </div>
            <p className="mt-3 text-sm text-ink-2">
              {scheduleText(data.schedule, data.timeZone)} · {data.timeZone}
            </p>
            <p className="mt-2 text-xs text-ink-2">
              Next:{" "}
              {data.nextRuns[0] ? runTime(data.nextRuns[0], data.timeZone) : data.enabled ? "done" : "paused"}
            </p>
            <div className="mt-5 flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => setEdit(true)}>
                <Pencil />
                Edit
              </Button>
              <Button
                disabled={run.isPending || remove.isPending}
                onClick={() => run.mutate({ path: { automationId } })}
              >
                <Play />
                {run.isPending ? "Starting…" : "Run now"}
              </Button>
              <Button
                variant="destructive"
                onClick={() => {
                  remove.reset();
                  setConfirm(true);
                }}
              >
                <Trash2 />
                Delete
              </Button>
            </div>
          </header>
          {run.isError ? (
            <div className="mb-5">
              <Failure title="Could not run automation" error={run.error} />
            </div>
          ) : null}
          {latestRun ? (
            <div role="status" className="mb-5 rounded-lg bg-inset p-4 text-sm">
              <span className="mr-2">Run now:</span>
              <RunStatus run={latestRun} />
              {latestRun.reason ? <p className="mt-2 text-ink-2">{latestRun.reason}</p> : null}
              {latestRun.chatId ? (
                <Link
                  to="/c/$chatId"
                  params={{ chatId: String(latestRun.chatId) }}
                  className="ml-3 text-brand underline"
                >
                  Open chat
                </Link>
              ) : null}
            </div>
          ) : null}
          <section className="surface mb-8 p-5">
            <h2 className="mb-3 text-sm font-medium">Prompt</h2>
            <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-ink-2">
              {data.prompt}
            </p>
          </section>
          <section>
            <h2 className="mb-4 font-medium">Run history</h2>
            {runs.isPending ? (
              <Loading rows={2} />
            ) : runs.isError ? (
              <Failure
                title="Could not load run history"
                error={runs.error}
                retry={() => void runs.refetch()}
              />
            ) : !runs.data.runs.length ? (
              <p className="surface p-5 text-sm text-ink-2">No runs yet.</p>
            ) : (
              <div className="surface overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="border-b border-line text-xs text-ink-2">
                    <tr>
                      <th className="px-4 py-3 font-medium">Time · {data.timeZone}</th>
                      <th className="px-4 py-3 font-medium">Status</th>
                      <th className="px-4 py-3 font-medium">Reason</th>
                      <th className="px-4 py-3 font-medium">Chat</th>
                    </tr>
                  </thead>
                  <tbody>
                    {runs.data.runs.map((history) => (
                      <tr key={history.id} className="border-b border-line last:border-b-0">
                        <td className="whitespace-nowrap px-4 py-3">
                          {runTime(history.dueAt ?? history.createdAt, data.timeZone)}
                        </td>
                        <td className="px-4 py-3">
                          <RunStatus run={history} />
                        </td>
                        <td className="min-w-32 px-4 py-3 text-ink-2">{history.reason ?? "—"}</td>
                        <td className="whitespace-nowrap px-4 py-3">
                          {history.chatId ? (
                            <Link
                              to="/c/$chatId"
                              params={{ chatId: String(history.chatId) }}
                              className="text-brand underline"
                            >
                              Open chat
                            </Link>
                          ) : (
                            "—"
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
          {edit ? <AutomationDialog existing={data} onClose={() => setEdit(false)} /> : null}
          {confirm ? (
            <Confirm
              title="Delete automation?"
              description="This deletes the automation and its run history. Its chats stay available."
              pending={remove.isPending}
              error={remove.error}
              onCancel={() => setConfirm(false)}
              onConfirm={() => remove.mutate({ path: { automationId } })}
            />
          ) : null}
        </>
      ) : null}
    </div>
  );
}
