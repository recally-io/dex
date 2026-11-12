import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Automation, AutomationRun, InboxItem } from "../api/generated/types.gen";
import {
  listAutomationsOptions,
  getAutomationOptions,
  listAutomationRunsOptions,
  updateAutomationMutation,
  deleteAutomationMutation,
  runAutomationMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { inboxOptions, resultQuote } from "../lib/inbox";
import { createChat, readInboxItem, sendMessage, setChatProject } from "../api/generated/sdk.gen";
import { refreshAutomations, refreshChats, refreshWork } from "../lib/queries";
import { useHandled } from "../lib/handled";
import { useAgentModel } from "../lib/agent";
import { ago, runTime, scheduleText } from "../lib/automations";
import { formatDuration } from "../lib/sessions";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Confirm } from "../components/confirm";
import { AutomationDialog } from "../components/automation-dialog";
import { Empty, FramedPage, Note, Panel, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { Switch } from "../components/ui/switch";
import { Textarea } from "../components/ui/textarea";

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
  const outcome = run.status === "done" ? run.outcome : "";
  const changed = outcome === "changed";
  const failed = run.status === "failed" || outcome === "failed";

  const label =
    outcome === "unchanged" ? "Unchanged" : changed ? "Changed" : failed ? "Failed" : runLabel[run.status];

  return (
    <Badge
      variant={failed ? "destructive" : run.status === "done" ? "secondary" : "outline"}
      className={`h-5 shrink-0 px-2 text-[11.5px] ${changed ? "bg-green-tint text-green" : run.status === "running" ? "border-orange/30 bg-orange-tint text-orange" : ""} ${className}`}
    >
      {label}
    </Badge>
  );
}

// The update endpoint takes the whole automation, so a change to one field resends the others.
function useAutomationUpdate(automation: Automation) {
  const agentId = useAgentId();

  const update = useMutation({
    ...updateAutomationMutation(),
    onSuccess: () => refreshAutomations(agentId),
  });

  return {
    ...update,
    change: (patch: Partial<Pick<Automation, "enabled" | "session">>) =>
      update.mutate({
        path: { agentId, automationId: automation.id },
        body: {
          name: automation.name,
          prompt: automation.prompt,
          schedule: automation.schedule,
          timeZone: automation.timeZone,
          session: automation.session,
          enabled: automation.enabled,
          cwd: automation.cwd,
          ...patch,
        },
      }),
  };
}

function EnabledSwitch({ automation }: { automation: Automation }) {
  const update = useAutomationUpdate(automation);

  return (
    <span className="flex flex-col items-end gap-2">
      <span className="flex items-center gap-2">
        {update.isPending ? <Spinner /> : null}
        <Switch
          checked={automation.enabled}
          disabled={update.isPending}
          aria-label={`Enable ${automation.name}`}
          onCheckedChange={() => update.change({ enabled: !automation.enabled })}
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
          <Button disabled={noModel} onClick={() => setCreate(true)}>
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

function ReplyComposer({
  run,
  automation,
  botName,
  onCancel,
}: {
  run: AutomationRun;
  automation: Automation;
  botName: string;
  onCancel: () => void;
}) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const [text, setText] = useState("");
  // A retry after a partial failure reuses the chat and the message ID, so the quote is sent once.
  const continuation = useRef<{ chatId: number; messageId: string } | null>(null);
  const outcome = run.outcome || (run.status === "failed" ? "failed" : "unknown");
  const summary = run.summary || run.reason || "";

  const start = useMutation({
    mutationFn: async () => {
      if (!continuation.current) {
        const chat = await createChat({ path: { agentId }, throwOnError: true });
        continuation.current = { chatId: chat.data.id, messageId: crypto.randomUUID() };
      }

      const { chatId, messageId } = continuation.current;
      const path = { agentId, chatId };

      // The quote comes first: the chat takes its title from the start of the message.
      const quote = resultQuote({
        agentId,
        automationName: automation.name,
        runId: run.id,
        chatId: run.chatId!,
        summary,
        outcome,
      });

      await setChatProject({ path, body: { cwd: automation.cwd ?? "" }, throwOnError: true });

      await sendMessage({
        path,
        body: { id: messageId, text: text.trim() ? `${quote}\n\n${text.trim()}` : quote },
        throwOnError: true,
      });

      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chatId) } });
    },
  });

  return (
    <section aria-label="New chat" className="flex flex-col gap-2.5 border-2 border-foreground p-3.5">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-[11px] font-bold tracking-wide uppercase">
          New chat with {botName}
          {automation.cwd ? ` · ${automation.cwd}` : ""}
        </span>
        <Button size="xs" variant="outline" onClick={onCancel} disabled={start.isPending}>
          Cancel
        </Button>
      </div>
      <blockquote className="border-l-2 border-border py-1 pl-2.5 text-[12px] leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
        Follow up on the {automation.name} result ({outcome}) ·{" "}
        {runTime(run.dueAt ?? run.createdAt, automation.timeZone)}
        {summary ? <span className="block">{summary}</span> : null}
      </blockquote>
      <Textarea
        aria-label="Your message"
        rows={2}
        placeholder={`What should ${botName} do about it?`}
        value={text}
        onChange={(event) => setText(event.target.value)}
      />
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex-1 text-[11px] text-muted-foreground">
          Opens a normal chat. The automation and its instructions do not change.
        </span>
        <Button size="sm" disabled={start.isPending} onClick={() => start.mutate()}>
          {start.isPending ? "Starting…" : "Start chat"}
        </Button>
      </div>
      {start.isError ? <Failure title="Could not start chat" error={start.error} /> : null}
    </section>
  );
}

function RunBlock({
  run,
  automation,
  botName,
  now,
  item,
}: {
  run: AutomationRun;
  automation: Automation;
  botName: string;
  now: number;
  item?: InboxItem;
}) {
  const agentId = useAgentId();
  const handle = useHandled();
  const [replying, setReplying] = useState(false);
  const timeZone = automation.timeZone;
  const failed = run.status === "failed" || run.outcome === "failed";
  const canHandle = failed && item !== undefined && !item.dismissed;
  const handled = failed && item?.dismissed === true;

  return (
    <article
      className={`flex flex-col gap-2.5 p-3.5 ${replying ? "border-2 border-foreground" : run.status === "skipped" ? "border border-dashed border-border" : "border border-border"}`}
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-semibold">{runTime(run.dueAt ?? run.createdAt, timeZone)}</span>
        <span className="text-[11.5px] text-muted-foreground">
          {run.dueAt ? `Planned · recorded ${runTime(run.createdAt, timeZone)}` : "Started by you"}
        </span>
        {run.duplicate ? <Badge variant="outline">duplicate</Badge> : null}
        <span className="flex-1" />
        <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
          {ago(run.createdAt, now)} ago
        </span>
        <RunPill run={run} />
        {handled ? <span className="text-[11.5px] text-muted-foreground">Handled</span> : null}
      </div>
      {run.admittedAt !== undefined ? (
        <p className="text-xs text-muted-foreground">
          Received {runTime(run.admittedAt, timeZone)}
          {run.settledAt !== undefined && run.settledAt >= run.admittedAt
            ? ` · ${formatDuration(run.settledAt - run.admittedAt)} until settled`
            : ""}
        </p>
      ) : null}
      {run.summary || run.reason ? (
        <p className="text-[13px] leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
          {run.summary || run.reason}
        </p>
      ) : null}
      {run.status !== "running" ? (
        <div className="flex flex-wrap items-center gap-2">
          {run.chatId ? (
            <Button size="sm" variant="outline" disabled={replying} onClick={() => setReplying(true)}>
              Reply in a new chat
            </Button>
          ) : null}
          {canHandle ? (
            <Button
              size="sm"
              variant="outline"
              disabled={handle.isPending}
              onClick={() => handle.mutate({ id: run.id, undo: false })}
            >
              Mark handled
            </Button>
          ) : null}
          {run.chatId ? (
            <Link
              to="/a/$agentId/c/$chatId"
              params={{ agentId, chatId: String(run.chatId) }}
              className="ml-auto text-[12px] font-medium text-primary hover:underline"
            >
              Run transcript
            </Link>
          ) : null}
        </div>
      ) : null}
      {handle.isError ? <Failure title="Could not mark handled" error={handle.error} /> : null}
      {replying ? (
        <ReplyComposer
          key={run.id}
          run={run}
          automation={automation}
          botName={botName}
          onCancel={() => setReplying(false)}
        />
      ) : null}
    </article>
  );
}

const eachRun = [
  {
    value: "new",
    label: "Start fresh",
    hint: "Each run starts with no earlier conversation. Bot memory still loads.",
  },
  {
    value: "reuse",
    label: "Continue from last run",
    hint: "The bot sees earlier runs. The context grows. A busy session skips the run.",
  },
] as const;

function Rail({ automation, onEdit }: { automation: Automation; onEdit: () => void }) {
  const update = useAutomationUpdate(automation);

  return (
    <aside
      aria-label="Automation settings"
      className="flex w-full flex-col gap-3 lg:w-72 lg:shrink-0 lg:border-l lg:border-border lg:pl-5"
    >
      <h2 className="text-[11px] tracking-wide text-muted-foreground uppercase">Instructions</h2>
      <p className="text-[13px] leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]">
        {automation.prompt}
      </p>
      <Button size="sm" variant="outline" className="self-start" onClick={onEdit}>
        Edit instructions
      </Button>
      <h2 className="mt-2 border-t border-border pt-3 text-[11px] tracking-wide text-muted-foreground uppercase">
        Each run
      </h2>
      <div role="group" aria-label="Each run" className="flex flex-col gap-1.5">
        {eachRun.map((mode) => {
          const on = automation.session === mode.value;

          return (
            <button
              key={mode.value}
              type="button"
              aria-pressed={on}
              disabled={update.isPending}
              onClick={() => (on ? undefined : update.change({ session: mode.value }))}
              className={`flex w-full flex-col items-start gap-0.5 p-2.5 text-left disabled:opacity-60 ${on ? "border-2 border-foreground bg-muted" : "border border-border hover:bg-muted"}`}
            >
              <span className="font-semibold">{mode.label}</span>
              <span className="text-[11px] leading-snug text-muted-foreground">{mode.hint}</span>
            </button>
          );
        })}
      </div>
      {update.isError ? <Failure title="Could not change the setting" error={update.error} /> : null}
      <p className="text-[11px] leading-relaxed text-muted-foreground">
        Each run is still its own run record. This page lists them in one place.
      </p>
    </aside>
  );
}

function PauseButton({ automation }: { automation: Automation }) {
  const update = useAutomationUpdate(automation);

  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        disabled={update.isPending}
        onClick={() => update.change({ enabled: !automation.enabled })}
      >
        {automation.enabled ? "Pause" : "Resume"}
      </Button>
      {update.isError ? <Failure title="Could not change automation" error={update.error} /> : null}
    </>
  );
}

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
  const run = useMutation({ ...runAutomationMutation(), onSuccess: () => refreshAutomations(agentId) });

  const remove = useMutation({
    ...deleteAutomationMutation(),
    onSuccess: async () => {
      await refreshAutomations(agentId);
      await navigate({ to: "/a/$agentId/automations", params: { agentId } });
    },
  });

  // Opening the page reads every result of this automation once. A failed request is not retried;
  // the result stays unread and the next visit asks again.
  const asked = useRef(new Set<string>());
  const items = inbox.data?.items;

  useEffect(() => {
    const unread = (items ?? []).filter(
      (item) => item.automationId === automationId && !item.read && !asked.current.has(item.runId),
    );

    if (!unread.length) return;

    for (const item of unread) asked.current.add(item.runId);
    void Promise.all(unread.map((item) => readInboxItem({ path: { runId: item.runId } }))).then(refreshWork);
  }, [items, automationId]);

  const history = runs.data?.runs ?? [];
  const latestRun = history.find((h) => h.id === run.data?.id) ?? run.data;
  const busy = history.some((h) => h.status === "running");
  const blocked = model && !model.ok ? "Choose an available bot model first." : undefined;

  const crumb = (
    <Link to="/a/$agentId/automations" params={{ agentId }} className="hover:underline">
      Routines
    </Link>
  );

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
  const botName = settings.data?.agent.name ?? "the bot";
  const byRun = new Map((items ?? []).map((item) => [item.runId, item]));

  return (
    <FramedPage
      wide
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
      description={[botName, scheduleText(data.schedule, data.timeZone), nextText(data), data.cwd]
        .filter(Boolean)
        .join(" · ")}
      actions={
        <>
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
          <PauseButton automation={data} />
          <Button size="sm" variant="secondary" onClick={() => setEdit(true)}>
            Edit
          </Button>
          <Button
            size="sm"
            variant="secondary"
            className="text-red"
            onClick={() => {
              remove.reset();
              setConfirm(true);
            }}
          >
            Delete
          </Button>
        </>
      }
    >
      {blocked || run.isError || latestRun ? (
        <div className="mb-6 flex flex-col gap-3 border-b border-dashed border-ring pb-4">
          {blocked ? <Note tone="warn">Run now is off. {blocked}</Note> : null}
          {run.isError ? <Failure title="Could not run automation" error={run.error} /> : null}
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
      <div className="flex flex-wrap gap-6">
        <div className="flex min-w-0 flex-1 basis-96 flex-col gap-4">
          {runs.isPending ? (
            <Loading />
          ) : runs.isError ? (
            <Failure
              title="Could not load run history"
              error={runs.error}
              retry={() => void runs.refetch()}
            />
          ) : (
            <>
              {data.nextRuns.map((at) => (
                <article
                  key={at}
                  className="flex items-baseline gap-3 border border-dashed border-border p-3.5 text-muted-foreground"
                >
                  <span className="font-semibold">{runTime(at, data.timeZone)}</span>
                  <span className="flex-1" />
                  <span className="text-[11.5px] font-bold">Scheduled</span>
                </article>
              ))}
              {history.map((h) => (
                <RunBlock
                  key={h.id}
                  run={h}
                  automation={data}
                  botName={botName}
                  now={runs.dataUpdatedAt}
                  item={byRun.get(h.id)}
                />
              ))}
              {!history.length ? (
                <p className="py-6 text-center text-[12.5px] text-muted-foreground">
                  No runs yet. Use Run now to try the prompt.
                </p>
              ) : null}
            </>
          )}
        </div>
        <Rail automation={data} onEdit={() => setEdit(true)} />
      </div>
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
