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
import { inboxOptions } from "../lib/inbox";
import { refreshAutomations } from "../lib/queries";
import { useAgentModel } from "../lib/agent";
import { ago, runTime, scheduleText } from "../lib/automations";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Crumb } from "../components/crumb";
import { Confirm } from "../components/confirm";
import { AutomationDialog } from "../components/automation-dialog";
import { Empty, Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { ToggleGroup, ToggleGroupItem } from "../components/ui/toggle-group";
import { Badge } from "../components/ui/badge";
import { Switch } from "../components/ui/switch";

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
    <Badge
      variant={run.status === "failed" ? "destructive" : run.status === "done" ? "secondary" : "outline"}
      className={`h-5 shrink-0 px-2 text-[11.5px] ${run.status === "done" ? "bg-green-tint text-green" : run.status === "running" ? "border-orange/30 bg-orange-tint text-orange" : ""} ${className}`}
    >
      {runLabel[run.status]}
    </Badge>
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
          aria-label={`Enable ${automation.name}`}
          onCheckedChange={() =>
            update.mutate({
              path: { agentId, automationId: automation.id },
              body: {
                name: automation.name,
                prompt: automation.prompt,
                schedule: automation.schedule,
                timeZone: automation.timeZone,
                session: automation.session,
                enabled: !automation.enabled,
                cwd: automation.cwd,
              },
            })
          }
        />
      </span>
      {update.isError ? <Failure title="Could not change routine" error={update.error} /> : null}
    </span>
  );
}

export function AutomationsPage() {
  return (
    <FramedPage
      title="Routines"
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
        title="All routines"
        caption={`${on} of ${list.length} on. Open one to see its runs.`}
        // SAFETY: ToggleGroup values come only from the two Filter items below.
        action={
          <Button size="sm" disabled={noModel} onClick={() => setCreate(true)}>
            New routine
          </Button>
        }
      >
        {automations.isPending ? (
          <Loading />
        ) : automations.isError ? (
          <Failure
            title="Could not load routines"
            error={automations.error}
            retry={() => void automations.refetch()}
          />
        ) : list.length ? (
          <Panel>
            <ul className="divide-y divide-border">
              {list.map((automation) => (
                <li key={automation.id} className="flex items-start gap-3 px-4 py-3">
                  <Link
                    to="/a/$agentId/automations/$automationId"
                    params={{ agentId, automationId: automation.id }}
                    className="group min-w-0 flex-1"
                  >
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="text-[13.5px] font-medium text-foreground [overflow-wrap:anywhere] group-hover:underline">
                        {automation.name}
                      </span>
                      {automation.lastRun && automation.lastRun.status !== "done" ? (
                        <Badge
                          variant={automation.lastRun.status === "failed" ? "destructive" : "outline"}
                          className={`h-5 shrink-0 px-2 text-[11px] ${automation.lastRun.status === "running" ? "border-orange/30 bg-orange-tint text-orange" : ""}`}
                        >
                          Last run {runLabel[automation.lastRun.status].toLowerCase()}
                        </Badge>
                      ) : null}
                    </span>
                    <span className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[12px] text-muted-foreground">
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
          <Empty title="No routines yet">
            {noModel
              ? "Choose a bot model first. Routines use the same model as every chat."
              : "Create one to run a prompt every few minutes, every day, on some weekdays, or once."}
          </Empty>
        )}
      </Section>
      {create ? <AutomationDialog onClose={() => setCreate(false)} /> : null}
    </>
  );
}

function RunRow({
  run,
  timeZone,
  now,
  inInbox,
}: {
  run: AutomationRun;
  timeZone: string;
  now: number;
  inInbox: boolean;
}) {
  const agentId = useAgentId();

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5">
      <span
        role="img"
        aria-label={run.outcome || "Unknown outcome"}
        className={`size-2 shrink-0 rounded-full ${run.outcome === "failed" ? "bg-red" : run.outcome === "changed" ? "bg-green" : "bg-muted-foreground"}`}
      />
      <RunPill run={run} className="w-[84px] justify-start" />
      {run.duplicate ? <Badge variant="outline">duplicate</Badge> : null}
      {inInbox ? (
        <Link to="/inbox/$runId" params={{ runId: run.id }} className="text-xs underline">
          In Inbox
        </Link>
      ) : null}
      <span className="font-mono text-[11.5px] text-muted-foreground tabular-nums">
        {runTime(run.dueAt ?? run.createdAt, timeZone)}
      </span>
      <span className="text-[12px] text-muted-foreground">{run.dueAt ? "Scheduled" : "Started by you"}</span>
      <span className="ml-auto flex items-center gap-2">
        <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
          {ago(run.createdAt, now)} ago
        </span>
        {run.chatId ? (
          <Link
            to="/a/$agentId/c/$chatId"
            params={{ agentId, chatId: String(run.chatId) }}
            className="text-[12px] font-medium text-primary hover:underline"
          >
            Run transcript
          </Link>
        ) : null}
      </span>
      {run.summary || run.reason ? (
        <p className="w-full pl-[96px] text-[12px] text-muted-foreground [overflow-wrap:anywhere] max-sm:pl-0">
          {run.summary || run.reason}
        </p>
      ) : null}
    </li>
  );
}

type Filter = "All" | "Problems";

export function AutomationPage({ automationId }: { automationId: string }) {
  const inbox = useQuery({ ...inboxOptions(), refetchInterval: 10_000 });
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

  const crumb = <Crumb to="/a/$agentId/automations">Routines</Crumb>;

  if (automation.isPending || automation.isError)
    return (
      <FramedPage eyebrow={crumb} title={automation.isError ? "Could not load routine" : "Routine"}>
        <div className="px-5 py-8 sm:px-8">
          {automation.isPending ? (
            <Loading />
          ) : (
            <Failure
              title="Could not load routine"
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
        <Badge
          variant={data.enabled ? "secondary" : "outline"}
          className={`h-5 px-2 text-[11.5px] ${data.enabled ? "bg-green-tint text-green" : ""}`}
        >
          {data.enabled ? "On" : "Paused"}
        </Badge>
      }
      description={`${scheduleText(data.schedule, data.timeZone)} · ${data.timeZone}`}
      actions={
        <>
          <Button size="sm" variant="secondary" onClick={() => setEdit(true)}>
            Edit
          </Button>
          <Button
            size="sm"
            variant="default"
            disabled={run.isPending || remove.isPending || busy || !!blocked}
            title={blocked ?? (busy ? "A run is in progress." : undefined)}
            onClick={() => run.mutate({ path: { agentId, automationId } })}
          >
            {run.isPending ? <Spinner className="border-background/40 border-t-canvas" /> : null}
            {run.isPending ? "Starting…" : "Run now"}
          </Button>
        </>
      }
    >
      {blocked || run.isError || latestRun ? (
        <div className="flex flex-col gap-3 border-b border-dashed border-ring px-5 py-4 sm:px-8">
          {blocked ? <Note tone="warn">Run now is off. {blocked}</Note> : null}
          {run.isError ? <Failure title="Could not run routine" error={run.error} /> : null}
          {latestRun ? (
            <div
              role="status"
              className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] shadow-card"
            >
              <span className="text-muted-foreground">Run now</span>
              <RunPill run={latestRun} />
              {latestRun.chatId ? (
                <Link
                  to="/a/$agentId/c/$chatId"
                  params={{ agentId, chatId: String(latestRun.chatId) }}
                  className="font-medium text-primary hover:underline"
                >
                  Run transcript
                </Link>
              ) : null}
              {latestRun.reason ? (
                <p className="w-full text-muted-foreground [overflow-wrap:anywhere]">{latestRun.reason}</p>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
      <Section n={1} title="Prompt" caption="Sent to the bot as a user message on each run.">
        <Panel className="px-4 py-3.5">
          <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
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
                data.session === "reuse" ? "One session for this routine" : "New session each run",
              ],
              [
                "Next runs",
                data.nextRuns.length ? (
                  <span
                    key="next"
                    className="flex flex-col items-end gap-0.5 font-mono text-[12px] text-muted-foreground tabular-nums"
                  >
                    {data.nextRuns.map((t) => (
                      <span key={t}>{runTime(t, data.timeZone)}</span>
                    ))}
                  </span>
                ) : (
                  <span key="none" className="text-muted-foreground">
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
        title="Check history"
        caption={
          runs.data
            ? `${history.length} ${history.length === 1 ? "run" : "runs"} · ${data.timeZone}`
            : undefined
        }
        action={
          history.length ? (
            <ToggleGroup
              value={[filter]}
              onValueChange={(value) => {
                if (!value[0]) return;
                // SAFETY: ToggleGroup values come only from the two Filter items below.
                setFilter(value[0] as Filter);
              }}
              variant="outline"
              size="sm"
            >
              <ToggleGroupItem value="All">All</ToggleGroupItem>
              <ToggleGroupItem value="Problems">Problems</ToggleGroupItem>
            </ToggleGroup>
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
              <ul className="divide-y divide-border">
                {shown.map((h) => (
                  <RunRow
                    key={h.id}
                    run={h}
                    timeZone={data.timeZone}
                    now={runs.dataUpdatedAt}
                    inInbox={
                      inbox.data?.items.some((item) => item.runId === h.id && !item.dismissed) ?? false
                    }
                  />
                ))}
              </ul>
            ) : (
              <p className="px-4 py-6 text-center text-[12.5px] text-muted-foreground">
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
          Delete routine
        </Button>
      </Section>
      {edit ? <AutomationDialog existing={data} onClose={() => setEdit(false)} /> : null}
      {confirm ? (
        <Confirm
          title="Delete routine?"
          confirmLabel="Delete"
          description="This deletes the routine and its run history. Its sessions stay available."
          pending={remove.isPending}
          error={remove.error}
          onCancel={() => setConfirm(false)}
          onConfirm={() => remove.mutate({ path: { agentId, automationId } })}
        />
      ) : null}
    </FramedPage>
  );
}
