import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type {
  Agent,
  TaskExecution,
  Runner,
  RunnerTestResult,
  SshHostEntry,
} from "../api/generated/types.gen";
import {
  deleteRunnerMutation,
  discoverSshConfigOptions,
  forgetRunnerHostKeyMutation,
  getHealthOptions,
  revokeRunnerMutation,
  listAgentsOptions,
  listTaskExecutionsOptions,
  listRunnersOptions,
  testRunnerMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { saveAgent } from "../lib/agent";
import {
  detectSandboxPreset,
  runnerKindBadge,
  runnerOptionLabel,
  formatResolvedSshHost,
  formatSandboxDiskSummary,
  formatSandboxRuntimeSummary,
  formatSshDiskSummary,
  formatSshTarget,
} from "../lib/runners";
import { errorText, refreshRunners } from "../lib/queries";
import { Confirm } from "./confirm";
import { RunnerDialog } from "./runner-dialog";
import { RunnerInstall } from "./runner-install";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Select } from "./form";
import { Facts, Mono, Panel, Section } from "./page";
import { ProbeResultPanel } from "./probe-result-panel";
import { ImportSshConfigDialog } from "./ssh-import-dialog";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function Runners() {
  const query = useQuery({
    ...listRunnersOptions(),
    refetchInterval: ({ state }) => (state.data?.runners.some((env) => env.kind === "runner") ? 5000 : false),
  });

  const healthQuery = useQuery(getHealthOptions());
  const agentsQuery = useQuery(listAgentsOptions());
  const sshConfigQuery = useQuery(discoverSshConfigOptions());

  const externalRuns = useQuery({
    ...listTaskExecutionsOptions({ query: { kind: "external_agent", state: "running" } }),
    enabled: query.data?.runners.some((env) => env.kind === "runner") ?? false,
    refetchInterval: 5000,
  });

  const [dialog, setDialog] = useState<Runner | "new" | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<Runner | null>(null);

  const serverVersion = healthQuery.data?.version;
  const envs = query.data?.runners ?? [];
  const sshHosts = sshConfigQuery.data?.hosts ?? [];
  const identityFiles = sshConfigQuery.data?.identityFiles ?? [];

  const usersByEnv = new Map<string, Agent[]>();

  for (const agent of agentsQuery.data?.agents ?? []) {
    if (!agent.runnerId) continue;

    const current = usersByEnv.get(agent.runnerId) ?? [];
    current.push(agent);
    usersByEnv.set(agent.runnerId, current);
  }

  return (
    <Section
      title="Available Runners"
      caption={
        envs.length
          ? `${envs.length + 1} total, including the built-in Local Runner`
          : "Where bots run commands and store workspaces."
      }
      action={
        <div className="flex flex-wrap items-center gap-2">
          {sshHosts.length > 0 ? (
            <Button size="sm" variant="outline" onClick={() => setImportOpen(true)}>
              Import SSH config
            </Button>
          ) : null}
          <Button size="sm" onClick={() => setDialog("new")}>
            Add Runner
          </Button>
        </div>
      }
    >
      {query.isPending ? (
        <Loading />
      ) : query.isError ? (
        <Failure title="Could not load Runners" error={query.error} retry={() => void query.refetch()} />
      ) : (
        <div className="flex flex-col gap-3">
          <Panel>
            <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
              <h3 className="text-[14px] font-medium text-foreground">Local</h3>
              <Badge variant="secondary" className="h-5 px-2 text-[11px]">
                Built-in
              </Badge>
            </div>
            <Facts
              rows={[
                ["Runtime", "Local host"],
                ["Shell", "Interactive login shell ($SHELL -l -i)"],
                ["Disk", <Mono key="w">{"Persistent · <dataDir>/agents/<bot-id>/workspace"}</Mono>],
              ]}
            />
          </Panel>

          {envs.map((env) => (
            <RunnerCard
              key={env.id}
              env={env}
              serverVersion={serverVersion}
              users={usersByEnv.get(env.id) ?? []}
              externalRuns={externalRuns.data?.executions.filter((run) => run.machineId === env.id) ?? []}
              externalRunsUnavailable={externalRuns.isError}
              sshHosts={sshHosts}
              onEdit={() => setDialog(env)}
              onRemove={() => setRemoveTarget(env)}
            />
          ))}
        </div>
      )}

      {dialog ? (
        <RunnerDialog
          existing={dialog === "new" ? undefined : dialog}
          sshHosts={sshHosts}
          identityFiles={identityFiles}
          onClose={() => setDialog(null)}
        />
      ) : null}

      {importOpen ? (
        <ImportSshConfigDialog hosts={sshHosts} existing={envs} onClose={() => setImportOpen(false)} />
      ) : null}

      {removeTarget ? (
        <DeleteRunnerDialog
          env={removeTarget}
          users={usersByEnv.get(removeTarget.id) ?? []}
          others={envs.filter((item) => item.id !== removeTarget.id)}
          onClose={() => setRemoveTarget(null)}
        />
      ) : null}
    </Section>
  );
}

function runnerSyncHint(
  runner: Runner["runner"],
  serverVersion: string | undefined,
): { label: string; warn: boolean } | null {
  if (!runner?.online) return null;

  if (!runner.version) {
    return {
      label: "Legacy runner (update manually once to enable auto-sync)",
      warn: true,
    };
  }

  if (serverVersion && serverVersion !== "dev" && runner.version !== serverVersion) {
    if (runner.canSelfUpdate) {
      return {
        label: `Update to ${serverVersion} did not complete. Retries on reconnect.`,
        warn: true,
      };
    }

    const reason = runner.selfUpdateReason || "not-writable";

    return {
      label: `Manual update needed (${reason})`,
      warn: true,
    };
  }

  if (serverVersion && runner.version === serverVersion) {
    return { label: "Matches server", warn: false };
  }

  return null;
}

function RunnerCard({
  env,
  serverVersion,
  users,
  externalRuns,
  externalRunsUnavailable,
  sshHosts,
  onEdit,
  onRemove,
}: {
  env: Runner;
  serverVersion?: string;
  users: Agent[];
  externalRuns: TaskExecution[];
  externalRunsUnavailable: boolean;
  sshHosts: SshHostEntry[];
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { notify } = useApp();
  const [probe, setProbe] = useState<RunnerTestResult | null>(null);
  const isE2B = env.kind === "e2b";
  const isRunner = env.kind === "runner";

  const revoke = useMutation({
    ...revokeRunnerMutation(),
    onSuccess: async () => {
      setProbe(null);
      await refreshRunners();
      notify(`Revoked ${env.name}.`);
    },
  });

  const request = { path: { runnerId: env.id } };
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const isCustomEndpoint = isE2B && detectSandboxPreset(env.config) !== "cloud";

  const test = useMutation({
    ...testRunnerMutation(),
    onSuccess: async (result) => {
      setProbe(result);

      if (isRunner) await refreshRunners();

      notify(result.ok ? `Connected to ${env.name}.` : `Test failed: ${result.error || "unreachable"}`);
    },
    onError: (err) => notify(errorText(err)),
  });

  const forgetHostKey = useMutation({
    ...forgetRunnerHostKeyMutation(),
    onSuccess: async () => {
      notify(`Cleared old SSH host key for ${env.name}. Retrying connection…`);
      await test.mutateAsync({ path: { runnerId: env.id } });
    },
    onError: (err) => notify(errorText(err)),
  });

  const rows: [string, ReactNode][] = [];

  if (isE2B) {
    rows.push([
      "Runtime",
      <Mono key="rt">
        {formatSandboxRuntimeSummary(env.config)} · {env.config.template || "base"}
      </Mono>,
    ]);

    if (env.config.envdUrl) {
      rows.push(["Gateway", <Mono key="gw">{env.config.envdUrl}</Mono>]);
    }

    rows.push(["Disk", <Mono key="disk">{formatSandboxDiskSummary(env.config)}</Mono>]);
  } else if (isRunner) {
    const runner = env.runner;
    const online = runner?.online ?? false;
    const sync = runnerSyncHint(runner, serverVersion);

    rows.push([
      "Connection",
      online ? (runner?.os && runner.arch ? `Online (${runner.os}/${runner.arch})` : "Online") : "Offline",
    ]);

    rows.push([
      "Active external runs",
      externalRunsUnavailable ? (
        "Could not load runs"
      ) : externalRuns.length === 0 ? (
        "None"
      ) : (
        <ul key="external-runs" className="flex flex-col gap-1">
          {externalRuns.map((run) =>
            run.taskKey ? (
              <li key={run.id}>
                <Link to="/board/$taskKey" params={{ taskKey: run.taskKey }} className="underline">
                  {run.taskKey}
                </Link>
                {run.harness ? ` · ${run.harness}` : null}
              </li>
            ) : null,
          )}
        </ul>
      ),
    ]);

    if (runner?.online) {
      rows.push([
        "Version",
        <span key="ver" className="inline-flex flex-wrap items-center gap-2">
          <Mono>{runner.version || "unknown"}</Mono>
          {sync ? (
            <Badge
              variant="secondary"
              className={
                sync.warn
                  ? "h-5 bg-orange-tint px-2 text-[11px] text-orange"
                  : "h-5 bg-green-tint px-2 text-[11px] text-green"
              }
            >
              {sync.label}
            </Badge>
          ) : null}
        </span>,
      ]);
    }
  } else {
    rows.push(["Target", <Mono key="target">{formatSshTarget(env.config)}</Mono>]);

    const matched = sshHosts.find((item) => item.alias === env.config.host);

    if (matched) {
      rows.push(["SSH config", <Mono key="sc">{formatResolvedSshHost(matched)}</Mono>]);
    }

    rows.push(["Shell", env.config.loginShell === false ? "Non-login SSH shell" : "Login shell ($SHELL -l)"]);

    if (env.config.identityFile) {
      rows.push(["Identity file", <Mono key="id">{env.config.identityFile}</Mono>]);
    }

    rows.push(["Disk", <Mono key="disk">{formatSshDiskSummary(env.config)}</Mono>]);
  }

  return (
    <Panel>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <h3 className="text-[14px] font-medium text-foreground">{env.name}</h3>
          <Badge variant="secondary" className="h-5 px-2 text-[11px]">
            {runnerKindBadge(env)}
          </Badge>
          {users.length ? (
            <Badge variant="secondary" className="h-5 max-w-full bg-primary/10 px-2 text-[11px] text-primary">
              <span className="truncate">Used by {users.map((agent) => agent.name).join(", ")}</span>
            </Badge>
          ) : null}
          {isE2B ? (
            <Badge
              variant="secondary"
              className={
                env.secretSet
                  ? "h-5 bg-green-tint px-2 text-[11px] text-green"
                  : isCustomEndpoint
                    ? "h-5 px-2 text-[11px] text-muted-foreground"
                    : "h-5 bg-orange-tint px-2 text-[11px] text-orange"
              }
            >
              {env.secretSet ? "API key saved" : isCustomEndpoint ? "No API key" : "API key missing"}
            </Badge>
          ) : null}
        </div>
        <Button
          size="xs"
          variant="outline"
          disabled={test.isPending || forgetHostKey.isPending}
          onClick={() => test.mutate({ path: { runnerId: env.id } })}
        >
          {test.isPending ? <Spinner /> : null}
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        <Button size="xs" variant="ghost" onClick={onEdit} aria-label={`Edit ${env.name}`}>
          Edit
        </Button>
        <Button
          size="xs"
          variant="ghost"
          className="text-red"
          onClick={onRemove}
          aria-label={`Remove ${env.name}`}
        >
          Remove
        </Button>
      </div>

      {env.desc ? (
        <p className="px-4 pt-3 text-sm break-words whitespace-pre-wrap text-muted-foreground">{env.desc}</p>
      ) : null}
      <Facts rows={rows} />

      {isRunner ? (
        <div className="flex flex-col gap-2 border-t border-border p-3">
          <RunnerInstall runnerId={env.id} />
          <div className="flex flex-wrap gap-2">
            <Button
              size="xs"
              variant="ghost"
              disabled={revoke.isPending}
              onClick={() => setConfirmRevoke(true)}
            >
              Revoke machine
            </Button>
          </div>
          {confirmRevoke ? (
            <div className="flex flex-wrap gap-2">
              <Button
                size="xs"
                variant="destructive"
                onClick={() => {
                  revoke.mutate(request);
                  setConfirmRevoke(false);
                }}
              >
                Disconnect and revoke
              </Button>
              <Button size="xs" variant="ghost" onClick={() => setConfirmRevoke(false)}>
                Cancel
              </Button>
            </div>
          ) : null}
          {revoke.isError ? <Failure title="Could not revoke the machine" error={revoke.error} /> : null}
        </div>
      ) : null}

      {test.isError ? (
        <div className="border-t border-border p-3">
          <Failure title="Connection test failed" error={test.error} />
        </div>
      ) : null}

      {probe ? (
        <div className="border-t border-border p-3">
          <ProbeResultPanel
            probe={probe}
            forgetting={forgetHostKey.isPending}
            onForgetHostKey={
              !isE2B && probe.hostKeyChanged
                ? () => forgetHostKey.mutate({ path: { runnerId: env.id } })
                : undefined
            }
          />
        </div>
      ) : null}
    </Panel>
  );
}

function DeleteRunnerDialog({
  env,
  users,
  others,
  onClose,
}: {
  env: Runner;
  users: Agent[];
  others: Runner[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const [targetEnvId, setTargetEnvId] = useState("");
  const current = users.length > 0;
  const names = users.map((agent) => agent.name).join(", ");

  const remove = useMutation({
    ...deleteRunnerMutation(),
    onSettled: () => refreshRunners(),
  });

  const reassign = useMutation({
    mutationFn: async () => {
      await Promise.all(
        users.map((user) =>
          saveAgent(user.id!, (agent) =>
            agent.runnerId === env.id ? { ...agent, runnerId: targetEnvId } : agent,
          ),
        ),
      );
    },
  });

  const pending = reassign.isPending || remove.isPending;

  async function confirm() {
    if (current && !reassign.isSuccess) await reassign.mutateAsync();
    await remove.mutateAsync({ path: { runnerId: env.id } });
    onClose();
    notify(`Removed ${env.name}.`);
  }

  return (
    <Confirm
      title={`Remove ${env.name}?`}
      description="dex deletes this Runner configuration."
      confirmLabel={current ? "Reassign bots and remove" : "Remove Runner"}
      pending={pending}
      error={reassign.error ?? remove.error}
      onCancel={onClose}
      onConfirm={() => void confirm().catch(() => {})}
    >
      {current ? (
        <div className="flex flex-col gap-3 rounded-md bg-orange-tint p-3">
          <p className="text-[13px] font-medium text-foreground">
            {names} {users.length === 1 ? "uses" : "use"} this Runner now.
          </p>
          <Field
            label={users.length === 1 ? "Before removal, move the bot to" : "Before removal, move bots to"}
          >
            {({ id }) => (
              <Select
                id={id}
                value={targetEnvId}
                disabled={pending}
                onChange={(event) => setTargetEnvId(event.target.value)}
              >
                <option value="">Local</option>
                {others.map((item) => (
                  <option key={item.id} value={item.id}>
                    {runnerOptionLabel(item)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
      ) : null}
    </Confirm>
  );
}
