import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
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
import { useAgentModel } from "../lib/agent";
import { ago, runTime, scheduleText } from "../lib/automations";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Crumb } from "../components/crumb";
import { Confirm } from "../components/confirm";
import { AutomationDialog } from "../components/automation-dialog";
import { Empty, Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { SegmentedControl } from "../components/atoms/SegmentedControl";
import { StatusPill } from "../components/atoms/StatusPill";
import { Switch } from "../components/atoms/Switch";

const runTone = { done: "green", failed: "red", skipped: "neutral", running: "orange" } as const;
const runLabel = { done: "Done", failed: "Failed", skipped: "Skipped", running: "Running" } as const;

function sessionText(automation: Automation) {
  return automation.session === "new" ? "New session each run" : "One session";
}

function nextText(automation: Automation) {
  return automation.nextRuns[0]
    ? `next ${runTime(automation.nextRuns[0], automation.timeZone)}`
    : automation.enabled
      ? "done"
      : "paused";
}

function RunPill({ run, className = "" }: { run: AutomationRun; className?: string }) {
  return (
    <StatusPill tone={runTone[run.status]} className={`h-5 shrink-0 px-2 text-[11.5px] ${className}`}>
      {runLabel[run.status]}
    </StatusPill>
  );
}

function EnabledSwitch({ automation }: { automation: Automation }) {
  const agentId = useAgentId();

  const update = useMutation({
    ...updateAutomationMutation(),
    onSuccess: () => refreshAutomations(agentId),
  });

  return (
    <span className="flex flex-col items-end gap-2">
      <span className="flex items-center gap-2">
        {update.isPending ? <Spinner /> : null}
        <Switch
          checked={automation.enabled}
          disabled={update.isPending}
          label={`Enable ${automation.name}`}
          onChange={() =>
            update.mutate({
              path: { agentId, automationId: automation.id },
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
      </span>
      {update.isError ? <Failure title="Could not change automation" error={update.error} /> : null}
    </span>
  );
}

export function AutomationsPage() {
  return (
    <FramedPage
      title="Automations"
      description="Prompts that the bot runs on a schedule. Each run has its own session."
    >
      <AutomationsSection />
    </FramedPage>
  );
}

function AutomationsSection() {
  const agentId = useAgentId();

  const automations = useQuery({
    ...listAutomationsOptions({ path: { agentId } }),
    refetchInterval: 10_000,
  });

  const settings = useAgentSettings(agentId);
  const [create, setCreate] = useState(false);
  const list = automations.data?.automations ?? [];
  const on = list.filter((a) => a.enabled).length;
  // Every automation uses the bot model. Without one, a new automation could not run.
  const noModel = !!settings.data && !settings.data.agent.modelId;

  return (
    <>
      <Section
        n={1}
        title="All automations"
        caption={`${on} of ${list.length} on. Open one to see its runs.`}
        action={
          <Button size="sm" variant="primary" disabled={noModel} onClick={() => setCreate(true)}>
            New automation
          </Button>
        }
      >
        {automations.isPending ? (
          <Loading />
        ) : automations.isError ? (
          <Failure
            title="Could not load automations"
            error={automations.error}
            retry={() => void automations.refetch()}
          />
        ) : list.length ? (
          <Panel>
            <ul className="divide-y divide-line">
              {list.map((automation) => (
                <li key={automation.id} className="flex items-start gap-3 px-4 py-3">
                  <Link
                    to="/a/$agentId/automations/$automationId"
                    params={{ agentId, automationId: automation.id }}
                    className="group min-w-0 flex-1"
                  >
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="text-[13.5px] font-medium text-ink [overflow-wrap:anywhere] group-hover:underline">
                        {automation.name}
                      </span>
                      {automation.lastRun && automation.lastRun.status !== "done" ? (
                        <StatusPill
                          tone={runTone[automation.lastRun.status]}
                          className="h-5 shrink-0 px-2 text-[11px]"
                        >
                          Last run {runLabel[automation.lastRun.status].toLowerCase()}
                        </StatusPill>
                      ) : null}
                    </span>
                    <span className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[12px] text-ink-3">
                      <span>{scheduleText(automation.schedule, automation.timeZone)}</span>
                      <span>{sessionText(automation)}</span>
                      <span className="font-mono text-[11.5px] tabular-nums">{nextText(automation)}</span>
                    </span>
                  </Link>
                  <EnabledSwitch automation={automation} />
                </li>
              ))}
            </ul>
          </Panel>
        ) : (
          <Empty title="No automations yet">
            {noModel
              ? "Choose a bot model first. Automations use the same model as every chat."
              : "Create one to run a prompt every few minutes, every day, on some weekdays, or once."}
          </Empty>
        )}
      </Section>
      {create ? <AutomationDialog onClose={() => setCreate(false)} /> : null}
    </>
  );
}

function RunRow({ run, timeZone, now }: { run: AutomationRun; timeZone: string; now: number }) {
  const agentId = useAgentId();

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5">
      <RunPill run={run} className="w-[84px] justify-start" />
      <span className="font-mono text-[11.5px] text-ink-2 tabular-nums">
        {runTime(run.dueAt ?? run.createdAt, timeZone)}
      </span>
      <span className="text-[12px] text-ink-3">{run.dueAt ? "Scheduled" : "Started by you"}</span>
      <span className="ml-auto flex items-center gap-2">
        <span className="font-mono text-[11px] text-ink-3 tabular-nums">{ago(run.createdAt, now)} ago</span>
        {run.chatId ? (
          <Link
            to="/a/$agentId/c/$chatId"
            params={{ agentId, chatId: String(run.chatId) }}
            className="text-[12px] font-medium text-accent-ink hover:underline"
          >
            Open session
          </Link>
        ) : null}
      </span>
      {run.reason ? (
        <p className="w-full pl-[96px] text-[12px] text-ink-2 [overflow-wrap:anywhere] max-sm:pl-0">
          {run.reason}
        </p>
      ) : null}
    </li>
  );
}

type Filter = "All" | "Problems";

export function AutomationPage({ automationId }: { automationId: string }) {
  const agentId = useAgentId();
  const navigate = useNavigate();

  const automation = useQuery({
    ...getAutomationOptions({ path: { agentId, automationId } }),
    refetchInterval: 10_000,
  });

  const runs = useQuery({
    ...listAutomationRunsOptions({ path: { agentId, automationId } }),
    refetchInterval: 10_000,
  });

  const settings = useAgentSettings(agentId);
  const model = useAgentModel(settings.data);
  const [edit, setEdit] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [filter, setFilter] = useState<Filter>("All");
  const run = useMutation({ ...runAutomationMutation(), onSuccess: () => refreshAutomations(agentId) });

  const remove = useMutation({
    ...deleteAutomationMutation(),
    onSuccess: async () => {
      await refreshAutomations(agentId);
      await navigate({ to: "/a/$agentId/automations", params: { agentId } });
    },
  });

  const history = runs.data?.runs ?? [];
  const latestRun = history.find((h) => h.id === run.data?.id) ?? run.data;

  const shown =
    filter === "All" ? history : history.filter((h) => h.status === "failed" || h.status === "skipped");

  const busy = history.some((h) => h.status === "running");
  const blocked = model && !model.ok ? "Choose an available bot model first." : undefined;

  const crumb = <Crumb to="/a/$agentId/automations">Automations</Crumb>;

  if (automation.isPending || automation.isError)
    return (
      <FramedPage eyebrow={crumb} title={automation.isError ? "Could not load automation" : "Automation"}>
        <div className="px-5 py-8 sm:px-8">
          {automation.isPending ? (
            <Loading />
          ) : (
            <Failure
              title="Could not load automation"
              error={automation.error}
              retry={() => void automation.refetch()}
            />
          )}
        </div>
      </FramedPage>
    );

  const data = automation.data;

  return (
    <FramedPage
      eyebrow={crumb}
      title={data.name}
      badge={
        <StatusPill tone={data.enabled ? "green" : "neutral"} className="h-5 px-2 text-[11.5px]">
          {data.enabled ? "On" : "Paused"}
        </StatusPill>
      }
      description={`${scheduleText(data.schedule, data.timeZone)} · ${data.timeZone}`}
      actions={
        <>
          <Button size="sm" variant="secondary" onClick={() => setEdit(true)}>
            Edit
          </Button>
          <Button
            size="sm"
            variant="primary"
            disabled={run.isPending || remove.isPending || busy || !!blocked}
            title={blocked ?? (busy ? "A run is in progress." : undefined)}
            onClick={() => run.mutate({ path: { agentId, automationId } })}
          >
            {run.isPending ? <Spinner className="border-canvas/40 border-t-canvas" /> : null}
            {run.isPending ? "Starting…" : "Run now"}
          </Button>
        </>
      }
    >
      {blocked || run.isError || latestRun ? (
        <div className="flex flex-col gap-3 border-b border-dashed border-line-strong px-5 py-4 sm:px-8">
          {blocked ? <Note tone="warn">Run now is off. {blocked}</Note> : null}
          {run.isError ? <Failure title="Could not run automation" error={run.error} /> : null}
          {latestRun ? (
            <div
              role="status"
              className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-card bg-inset px-3.5 py-2.5 text-[12.5px] shadow-hairline"
            >
              <span className="text-ink-2">Run now</span>
              <RunPill run={latestRun} />
              {latestRun.chatId ? (
                <Link
                  to="/a/$agentId/c/$chatId"
                  params={{ agentId, chatId: String(latestRun.chatId) }}
                  className="font-medium text-accent-ink hover:underline"
                >
                  Open session
                </Link>
              ) : null}
              {latestRun.reason ? (
                <p className="w-full text-ink-2 [overflow-wrap:anywhere]">{latestRun.reason}</p>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
      <Section n={1} title="Prompt" caption="Sent to the bot as a user message on each run.">
        <Panel className="px-4 py-3.5">
          <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">
            {data.prompt}
          </p>
        </Panel>
      </Section>
      <Section n={2} title="Schedule" action={<EnabledSwitch automation={data} />}>
        <Panel>
          <Facts
            rows={[
              ["Repeats", scheduleText(data.schedule, data.timeZone)],
              ["Time zone", <Mono key="tz">{data.timeZone}</Mono>],
              [
                "Session for each run",
                data.session === "reuse" ? "One session for this automation" : "New session each run",
              ],
              [
                "Next runs",
                data.nextRuns.length ? (
                  <span
                    key="next"
                    className="flex flex-col items-end gap-0.5 font-mono text-[12px] text-ink-2 tabular-nums"
                  >
                    {data.nextRuns.map((t) => (
                      <span key={t}>{runTime(t, data.timeZone)}</span>
                    ))}
                  </span>
                ) : (
                  <span key="none" className="text-ink-3">
                    {data.enabled ? "No future run" : "Paused"}
                  </span>
                ),
              ],
            ]}
          />
        </Panel>
      </Section>
      <Section
        n={3}
        title="Run history"
        caption={
          runs.data
            ? `${history.length} ${history.length === 1 ? "run" : "runs"} · ${data.timeZone}`
            : undefined
        }
        action={
          history.length ? (
            <SegmentedControl<Filter> options={["All", "Problems"]} value={filter} onChange={setFilter} />
          ) : null
        }
      >
        {runs.isPending ? (
          <Loading />
        ) : runs.isError ? (
          <Failure title="Could not load run history" error={runs.error} retry={() => void runs.refetch()} />
        ) : (
          <Panel>
            {shown.length ? (
              <ul className="divide-y divide-line">
                {shown.map((h) => (
                  <RunRow key={h.id} run={h} timeZone={data.timeZone} now={runs.dataUpdatedAt} />
                ))}
              </ul>
            ) : (
              <p className="px-4 py-6 text-center text-[12.5px] text-ink-3">
                {history.length
                  ? "No failed or skipped runs."
                  : "No runs yet. Use Run now to try the prompt."}
              </p>
            )}
          </Panel>
        )}
      </Section>
      <Section n={4} title="Delete" caption="The sessions of past runs stay.">
        <Button
          size="sm"
          variant="secondary"
          className="text-red"
          onClick={() => {
            remove.reset();
            setConfirm(true);
          }}
        >
          Delete automation
        </Button>
      </Section>
      {edit ? <AutomationDialog existing={data} onClose={() => setEdit(false)} /> : null}
      {confirm ? (
        <Confirm
          title="Delete automation?"
          confirmLabel="Delete"
          description="This deletes the automation and its run history. Its sessions stay available."
          pending={remove.isPending}
          error={remove.error}
          onCancel={() => setConfirm(false)}
          onConfirm={() => remove.mutate({ path: { agentId, automationId } })}
        />
      ) : null}
    </FramedPage>
  );
}
