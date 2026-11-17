import { useState, type ClipboardEvent, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type {
  Agent,
  Environment,
  EnvironmentConfig,
  EnvironmentInput,
  EnvironmentKind,
  EnvironmentTestResult,
  SshHostEntry,
} from "../api/generated/types.gen";
import {
  createEnvironmentMutation,
  deleteEnvironmentMutation,
  discoverSshConfigOptions,
  forgetEnvironmentHostKeyMutation,
  listAgentsOptions,
  listEnvironmentsOptions,
  probeEnvironmentMutation,
  testEnvironmentMutation,
  updateEnvironmentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { createEnvironment } from "../api/generated/sdk.gen";
import { useApp } from "../lib/app-state";
import { saveAgent } from "../lib/agent";
import { errorText, refreshEnvironments } from "../lib/queries";
import { inferE2BDomain, parseSshTargetInput } from "../lib/ssh-parse";
import { Confirm } from "./confirm";
import { Modal } from "./dialog";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Input, RadioSegments, SavedSecret, Select } from "./form";
import { Facts, Mono, Panel, Section } from "./page";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";

const environmentKinds = [
  { value: "pi-env", label: "SSH (pi-env)" },
  { value: "e2b", label: "E2B Sandbox" },
] as const;

const e2bEndpointModes = [
  { value: "cloud", label: "E2B Cloud" },
  { value: "custom", label: "Custom endpoint" },
] as const;

export function ExecutionEnvironments({ n }: { n: number }) {
  const query = useQuery(listEnvironmentsOptions());
  const agentsQuery = useQuery(listAgentsOptions());
  const sshConfigQuery = useQuery(discoverSshConfigOptions());
  const [dialog, setDialog] = useState<Environment | "new" | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<Environment | null>(null);

  const envs = query.data?.environments ?? [];
  const sshHosts = sshConfigQuery.data?.hosts ?? [];
  const identityFiles = sshConfigQuery.data?.identityFiles ?? [];

  const usersByEnv = new Map<string, Agent[]>();

  for (const agent of agentsQuery.data?.agents ?? []) {
    if (!agent.environmentId) continue;

    const current = usersByEnv.get(agent.environmentId) ?? [];
    current.push(agent);
    usersByEnv.set(agent.environmentId, current);
  }

  return (
    <Section
      n={n}
      title="Execution environments"
      caption={
        envs.length
          ? `${envs.length + 1} available (1 built-in, ${envs.length} remote)`
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
            Add environment
          </Button>
        </div>
      }
    >
      {query.isPending ? (
        <Loading />
      ) : query.isError ? (
        <Failure
          title="Could not load execution environments"
          error={query.error}
          retry={() => void query.refetch()}
        />
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
                ["Kind", "local"],
                ["Workspace", <Mono key="w">{"<dataDir>/agents/<bot-id>/workspace"}</Mono>],
              ]}
            />
          </Panel>

          {envs.map((env) => (
            <EnvironmentCard
              key={env.id}
              env={env}
              users={usersByEnv.get(env.id) ?? []}
              sshHosts={sshHosts}
              onEdit={() => setDialog(env)}
              onRemove={() => setRemoveTarget(env)}
            />
          ))}
        </div>
      )}

      {dialog ? (
        <EnvironmentDialog
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
        <DeleteEnvironmentDialog
          env={removeTarget}
          users={usersByEnv.get(removeTarget.id) ?? []}
          others={envs.filter((item) => item.id !== removeTarget.id)}
          onClose={() => setRemoveTarget(null)}
        />
      ) : null}
    </Section>
  );
}

function EnvironmentCard({
  env,
  users,
  sshHosts,
  onEdit,
  onRemove,
}: {
  env: Environment;
  users: Agent[];
  sshHosts: SshHostEntry[];
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { notify } = useApp();
  const [probe, setProbe] = useState<EnvironmentTestResult | null>(null);
  const isE2B = env.kind === "e2b";
  const isCustomE2B = isE2B && isCustomE2BConfig(env.config);

  const test = useMutation({
    ...testEnvironmentMutation(),
    onSuccess: (result) => {
      setProbe(result);
      notify(result.ok ? `Connected to ${env.name}.` : `Test failed: ${result.error || "unreachable"}`);
    },
    onError: (err) => notify(errorText(err)),
  });

  const forgetHostKey = useMutation({
    ...forgetEnvironmentHostKeyMutation(),
    onSuccess: async () => {
      notify(`Cleared old SSH host key for ${env.name}. Retrying connection…`);
      await test.mutateAsync({ path: { environmentId: env.id } });
    },
    onError: (err) => notify(errorText(err)),
  });

  const rows: [string, ReactNode][] = [];

  if (isE2B) {
    rows.push([
      "Endpoint",
      isCustomE2B ? <Mono key="ep">{formatE2BEndpointSummary(env.config)}</Mono> : "E2B Cloud (e2b.app)",
    ]);

    if (env.config.envdUrl) {
      rows.push(["Gateway", <Mono key="gw">{env.config.envdUrl}</Mono>]);
    }

    rows.push(["Template", <Mono key="tpl">{env.config.template || "base"}</Mono>]);
    rows.push(["Workspace", <Mono key="ws">{env.config.workspaceDir || "/home/user/workspace"}</Mono>]);
    rows.push([
      "Lifecycle",
      env.config.autoPause === false
        ? `Expire after ${env.config.timeoutSeconds ?? 300}s · auto-pause off`
        : `Auto-pause after ${env.config.timeoutSeconds ?? 300}s`,
    ]);
  } else {
    rows.push(["Target", <Mono key="target">{formatSshTarget(env.config)}</Mono>]);

    const matched = sshHosts.find((item) => item.alias === env.config.host);

    if (matched) {
      rows.push(["SSH config", <Mono key="sc">{formatResolvedSshHost(matched)}</Mono>]);
    }

    if (env.config.identityFile) {
      rows.push(["Identity file", <Mono key="id">{env.config.identityFile}</Mono>]);
    }

    rows.push(["Workspace", <Mono key="ws">{env.config.workspaceDir || "~/.dex/agents"}</Mono>]);
  }

  return (
    <Panel>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <h3 className="text-[14px] font-medium text-foreground">{env.name}</h3>
          <Mono className="text-[11.5px]">{env.kind}</Mono>
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
                  : isCustomE2B
                    ? "h-5 px-2 text-[11px] text-muted-foreground"
                    : "h-5 bg-orange-tint px-2 text-[11px] text-orange"
              }
            >
              {env.secretSet ? "API key saved" : isCustomE2B ? "No API key" : "API key missing"}
            </Badge>
          ) : null}
        </div>
        <Button
          size="xs"
          variant="outline"
          disabled={test.isPending || forgetHostKey.isPending}
          onClick={() => test.mutate({ path: { environmentId: env.id } })}
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

      <Facts rows={rows} />

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
                ? () => forgetHostKey.mutate({ path: { environmentId: env.id } })
                : undefined
            }
          />
        </div>
      ) : null}
    </Panel>
  );
}

function ProbeResultPanel({
  probe,
  forgetting,
  onForgetHostKey,
}: {
  probe: EnvironmentTestResult;
  forgetting?: boolean;
  onForgetHostKey?: () => void;
}) {
  if (probe.ok) {
    return (
      <div className="rounded-card bg-green-tint px-3.5 py-2.5 text-[12.5px] text-foreground">
        <p className="font-medium text-green">
          Connected · {probe.platform || "unknown"}/{probe.arch || "unknown"}
        </p>
        {probe.home ? (
          <p className="mt-0.5 text-muted-foreground">
            Remote home: <Mono>{probe.home}</Mono>
          </p>
        ) : null}
        {probe.warnings?.length ? (
          <ul className="mt-1.5 list-disc space-y-0.5 pl-4 text-[12px] text-orange">
            {probe.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        ) : null}
      </div>
    );
  }

  return (
    <Failure title="Connection test failed">
      <p className="font-mono text-[11.5px]">{probe.error || "Unknown error"}</p>
      {probe.hostKeyChanged ? (
        <div className="mt-2 flex flex-wrap items-center gap-2 rounded-md bg-orange-tint px-2.5 py-2 text-[12px] text-foreground">
          <span className="min-w-0 flex-1">
            The remote host key changed. Clear the saved key only if you trust the new host (for example after
            a rebuild).
          </span>
          {onForgetHostKey ? (
            <Button size="xs" variant="outline" disabled={forgetting} onClick={onForgetHostKey}>
              {forgetting ? <Spinner /> : null}
              {forgetting ? "Clearing…" : "Forget old host key & retry"}
            </Button>
          ) : null}
        </div>
      ) : null}
    </Failure>
  );
}

function ImportSshConfigDialog({
  hosts,
  existing,
  onClose,
}: {
  hosts: SshHostEntry[];
  existing: Environment[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const existingSshHosts = new Set<string>();

  for (const env of existing) {
    if (env.kind === "pi-env" && env.config.host) existingSshHosts.add(env.config.host);
  }

  const [selected, setSelected] = useState<string[]>([]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const available = hosts.filter((item) => !existingSshHosts.has(item.alias));
  const selectedSet = new Set(selected);

  function toggle(alias: string) {
    setSelected((prev) => (prev.includes(alias) ? prev.filter((item) => item !== alias) : [...prev, alias]));
  }

  async function submit() {
    if (selected.length === 0 || pending) return;

    setPending(true);
    setError(null);

    try {
      for (const alias of selected) {
        // eslint-disable-next-line no-await-in-loop
        await createEnvironment({
          body: { name: alias, kind: "pi-env", config: { host: alias } },
          throwOnError: true,
        });
      }

      await refreshEnvironments();
      onClose();
      notify(
        selected.length === 1 ? `Imported ${selected[0]}.` : `Imported ${selected.length} SSH environments.`,
      );
    } catch (cause) {
      await refreshEnvironments();
      setError(cause);
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      title="Import from ~/.ssh/config"
      description="Selected aliases are stored by alias so ProxyJump, IdentityAgent, and SSH defaults continue to apply."
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={() => void submit()} disabled={pending || selected.length === 0}>
            {pending ? <Spinner className="border-background/40 border-t-canvas" /> : null}
            {pending
              ? "Importing…"
              : selected.length
                ? `Import ${selected.length} host${selected.length === 1 ? "" : "s"}`
                : "Import selected"}
          </Button>
        </>
      }
    >
      <div className="flex items-center justify-between gap-2 text-[12px] text-muted-foreground">
        <span>
          {hosts.length} host{hosts.length === 1 ? "" : "s"} found
        </span>
        {available.length > 0 ? (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            disabled={pending}
            onClick={() =>
              setSelected(selected.length === available.length ? [] : available.map((item) => item.alias))
            }
          >
            {selected.length === available.length ? "Clear selection" : "Select all new"}
          </Button>
        ) : null}
      </div>

      <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
        {hosts.map((entry) => {
          const alreadyAdded = existingSshHosts.has(entry.alias);
          const checked = selectedSet.has(entry.alias);

          return (
            <li key={entry.alias}>
              <label className="flex cursor-pointer items-start gap-3 px-3.5 py-2.5 transition-colors hover:bg-accent/50">
                <input
                  type="checkbox"
                  className="mt-1 size-4 accent-primary"
                  disabled={alreadyAdded || pending}
                  checked={alreadyAdded || checked}
                  onChange={() => toggle(entry.alias)}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px] font-medium text-foreground">{entry.alias}</span>
                    {alreadyAdded ? (
                      <Badge variant="secondary" className="h-5 px-2 text-[11px]">
                        Already added
                      </Badge>
                    ) : null}
                    {entry.source ? (
                      <span className="text-[11px] text-muted-foreground">{entry.source}</span>
                    ) : null}
                  </div>
                  <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                    {formatResolvedSshHost(entry)}
                  </p>
                </div>
              </label>
            </li>
          );
        })}
      </ul>

      {error ? <Failure title="Could not import SSH hosts" error={error} /> : null}
    </Modal>
  );
}

function EnvironmentDialog({
  existing,
  sshHosts,
  identityFiles,
  onClose,
}: {
  existing?: Environment;
  sshHosts: SshHostEntry[];
  identityFiles: string[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const [kind, setKind] = useState<EnvironmentKind>(existing?.kind ?? "pi-env");
  const [name, setName] = useState(existing?.name ?? "");
  const [nameEdited, setNameEdited] = useState(!!existing);
  const [host, setHost] = useState(existing?.config.host ?? "");
  const [user, setUser] = useState(existing?.config.user ?? "");
  const [port, setPort] = useState(existing?.config.port ? String(existing.config.port) : "");
  const [identityFile, setIdentityFile] = useState(existing?.config.identityFile ?? "");
  const [workspaceDir, setWorkspaceDir] = useState(existing?.config.workspaceDir ?? "");
  const [selectedAlias, setSelectedAlias] = useState("");

  const [e2bMode, setE2bMode] = useState<"cloud" | "custom">(
    existing && isCustomE2BConfig(existing.config) ? "custom" : "cloud",
  );

  const [apiUrl, setApiUrl] = useState(existing?.config.apiUrl ?? "");
  const [domain, setDomain] = useState(existing?.config.domain ?? "");
  const [envdUrl, setEnvdUrl] = useState(existing?.config.envdUrl ?? "");
  const [autoPause, setAutoPause] = useState(existing?.config.autoPause ?? true);
  const [secret, setSecret] = useState("");
  const [replaceSecret, setReplaceSecret] = useState(!existing?.secretSet);
  const [template, setTemplate] = useState(existing?.config.template ?? "base");
  const [timeoutSeconds, setTimeoutSeconds] = useState(String(existing?.config.timeoutSeconds ?? 300));
  const [tried, setTried] = useState(false);
  const [probeResult, setProbeResult] = useState<EnvironmentTestResult | null>(null);

  const create = useMutation({
    ...createEnvironmentMutation(),
    onSuccess: async (saved) => {
      await refreshEnvironments();
      onClose();
      notify(`Added ${saved.name}.`);
    },
  });

  const update = useMutation({
    ...updateEnvironmentMutation(),
    onSuccess: async (saved) => {
      await refreshEnvironments();
      onClose();
      notify(`Updated ${saved.name}.`);
    },
  });

  const probe = useMutation({
    ...probeEnvironmentMutation(),
    onSuccess: (result) => {
      setProbeResult(result);
    },
  });

  const forgetHostKey = useMutation({
    ...forgetEnvironmentHostKeyMutation(),
    onSuccess: async () => {
      if (!existing) return;

      notify(`Cleared old SSH host key for ${existing.name}. Retrying…`);
      await probe.mutateAsync({
        body: {
          environmentId: existing.id,
          kind,
          config: buildConfig(),
        },
      });
    },
  });

  const pending = create.isPending || update.isPending;
  const mutationError = create.error ?? update.error;
  const trimmedPort = port.trim();
  const parsedPort = trimmedPort ? Number(trimmedPort) : undefined;
  const trimmedTimeout = timeoutSeconds.trim();
  const parsedTimeout = trimmedTimeout ? Number(trimmedTimeout) : 300;
  const trimmedWS = workspaceDir.trim();
  const trimmedApiUrl = apiUrl.trim();
  const trimmedDomain = domain.trim();
  const trimmedEnvdUrl = envdUrl.trim();
  const inferredDomain = inferE2BDomain(trimmedApiUrl);
  const matchedSshHost = sshHosts.find((item) => item.alias === host.trim());

  const errors: Partial<
    Record<
      | "name"
      | "host"
      | "port"
      | "secret"
      | "apiUrl"
      | "domain"
      | "envdUrl"
      | "template"
      | "timeoutSeconds"
      | "workspaceDir",
      string
    >
  > = {};

  if (!name.trim() || Array.from(name.trim()).length > 60) {
    errors.name = "Enter a name between 1 and 60 characters.";
  }

  if (kind === "pi-env") {
    if (!host.trim()) {
      errors.host = "Enter a host name or SSH config alias.";
    }

    if (
      trimmedPort &&
      (parsedPort === undefined || !Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535)
    ) {
      errors.port = "Port must be between 1 and 65535.";
    }
  } else {
    if (e2bMode === "custom") {
      if (!trimmedApiUrl && !trimmedDomain && !trimmedEnvdUrl) {
        errors.apiUrl = "Enter an API URL, sandbox domain, or gateway URL.";
      }

      if (trimmedApiUrl) {
        const err = urlFieldError(trimmedApiUrl, "http://localhost:3000 or https://api.example.com");

        if (err) errors.apiUrl = err;
      }

      if (trimmedDomain && (/[\s/?#@]/.test(trimmedDomain) || trimmedDomain.includes("://"))) {
        errors.domain = "Enter a hostname without scheme or path (for example sandbox.example.com).";
      }

      if (trimmedEnvdUrl) {
        const expanded = trimmedEnvdUrl.replaceAll("{sandboxId}", "sbx").replaceAll("{port}", "49983");
        const err = urlFieldError(expanded, "http://localhost:3000");

        if (err) errors.envdUrl = err;
      }
    } else if (!existing?.secretSet && !secret.trim()) {
      errors.secret = "Enter your E2B API key.";
    }

    if (!template.trim()) {
      errors.template = "Enter a template ID (for example base).";
    }

    if (!Number.isInteger(parsedTimeout) || parsedTimeout < 1 || parsedTimeout > 86400) {
      errors.timeoutSeconds = "Timeout must be between 1 and 86400 seconds.";
    }
  }

  if (trimmedWS && !trimmedWS.startsWith("/") && trimmedWS !== "~" && !trimmedWS.startsWith("~/")) {
    errors.workspaceDir = "Use an absolute path or start with ~.";
  }

  const show = (key: keyof typeof errors) => (tried ? errors[key] : undefined);

  function buildConfig(): EnvironmentConfig {
    if (kind === "e2b") {
      return {
        apiUrl: e2bMode === "custom" ? trimmedApiUrl || undefined : undefined,
        domain: e2bMode === "custom" ? trimmedDomain || undefined : undefined,
        envdUrl: e2bMode === "custom" ? trimmedEnvdUrl || undefined : undefined,
        template: template.trim() || "base",
        timeoutSeconds: parsedTimeout,
        autoPause,
        workspaceDir: trimmedWS || undefined,
      };
    }

    return {
      host: host.trim(),
      user: user.trim() || undefined,
      port: parsedPort,
      identityFile: identityFile.trim() || undefined,
      workspaceDir: trimmedWS || undefined,
    };
  }

  function hasConfigErrors(): boolean {
    const configErrorKeys: (keyof typeof errors)[] = [
      "host",
      "port",
      "secret",
      "apiUrl",
      "domain",
      "envdUrl",
      "template",
      "timeoutSeconds",
      "workspaceDir",
    ];

    return configErrorKeys.some((key) => !!errors[key]);
  }

  function runProbe() {
    setTried(true);

    if (hasConfigErrors() || pending || probe.isPending) return;

    probe.mutate({
      body: {
        environmentId: existing?.id,
        kind,
        config: buildConfig(),
        secret: kind === "e2b" && secret.trim() ? secret.trim() : undefined,
      },
    });
  }

  function submit() {
    setTried(true);

    if (Object.keys(errors).length > 0 || pending) return;

    const body: EnvironmentInput = {
      name: name.trim(),
      kind,
      config: buildConfig(),
      secret: kind === "e2b" && secret.trim() ? secret.trim() : undefined,
    };

    if (existing) {
      update.mutate({ path: { environmentId: existing.id }, body });
    } else {
      create.mutate({ body });
    }
  }

  function handleSshHostSelect(alias: string) {
    setSelectedAlias(alias);
    setProbeResult(null);

    if (!alias) return;

    setHost(alias);
    setUser("");
    setPort("");
    setIdentityFile("");

    if (!nameEdited) setName(alias);
  }

  function handleHostPaste(event: ClipboardEvent<HTMLInputElement>) {
    const pasted = event.clipboardData.getData("text");
    const parsed = parseSshTargetInput(pasted);

    if (!parsed) return;

    event.preventDefault();
    setSelectedAlias("");
    setProbeResult(null);
    setHost(parsed.host);

    if (parsed.user) setUser(parsed.user);

    if (parsed.port !== undefined) setPort(String(parsed.port));

    if (parsed.identityFile) setIdentityFile(parsed.identityFile);

    if (!existing && !nameEdited && parsed.suggestedName) setName(parsed.suggestedName);
  }

  return (
    <Modal
      title={existing ? `Edit ${existing.name}` : "Add execution environment"}
      description="Connect to a remote machine over SSH (pi-env) or an E2B-compatible sandbox endpoint."
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button
            type="button"
            variant="outline"
            className="mr-auto"
            onClick={runProbe}
            disabled={pending || probe.isPending}
          >
            {probe.isPending ? <Spinner /> : null}
            {probe.isPending ? "Testing…" : "Test connection"}
          </Button>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending}>
            {pending ? <Spinner className="border-background/40 border-t-canvas" /> : null}
            {pending ? "Saving…" : existing ? "Save changes" : "Add environment"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {!existing ? (
          <div className="flex flex-col gap-1.5">
            <span className="text-[12.5px] font-medium text-foreground">Kind</span>
            <RadioSegments
              label="Environment kind"
              options={environmentKinds}
              value={kind}
              onChange={(next) => {
                setKind(next);
                setTried(false);
                setProbeResult(null);
                probe.reset();
              }}
            />
          </div>
        ) : null}

        <Field label="Name" error={show("name")}>
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              placeholder={kind === "e2b" ? "E2B Sandbox" : "Build server"}
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                setNameEdited(true);
              }}
            />
          )}
        </Field>

        {kind === "pi-env" ? (
          <>
            {!existing && sshHosts.length > 0 ? (
              <Field
                label="Import from ~/.ssh/config"
                hint="Keeps your ProxyJump, IdentityAgent, and SSH defaults."
              >
                {({ id, describedBy }) => (
                  <Select
                    id={id}
                    aria-describedby={describedBy}
                    value={selectedAlias}
                    onChange={(event) => handleSshHostSelect(event.target.value)}
                  >
                    <option value="">Choose a host from ~/.ssh/config…</option>
                    {sshHosts.map((entry) => (
                      <option key={entry.alias} value={entry.alias}>
                        {entry.alias} ({formatResolvedSshHost(entry)})
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            ) : null}

            <Field
              label="Host"
              hint={
                matchedSshHost
                  ? `Matches ~/.ssh/config: ${formatResolvedSshHost(matchedSshHost)}`
                  : "Hostname, ~/.ssh/config alias, or paste user@host:port / ssh command."
              }
              error={show("host")}
            >
              {({ id, describedBy, invalid }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  aria-invalid={invalid}
                  className="font-mono"
                  placeholder="dev.example.com"
                  value={host}
                  onPaste={handleHostPaste}
                  onChange={(event) => {
                    const nextHost = event.target.value;

                    setHost(nextHost);
                    setSelectedAlias("");
                    setProbeResult(null);

                    if (!existing && !nameEdited) setName(nextHost.trim());
                  }}
                />
              )}
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="User" hint="Optional; leave blank to use SSH config.">
                {({ id, describedBy }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    className="font-mono"
                    placeholder={matchedSshHost?.user || "ubuntu"}
                    value={user}
                    onChange={(event) => setUser(event.target.value)}
                  />
                )}
              </Field>

              <Field label="Port" hint="Optional; defaults to SSH config or 22." error={show("port")}>
                {({ id, describedBy, invalid }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    placeholder={matchedSshHost?.port ? String(matchedSshHost.port) : "22"}
                    value={port}
                    onChange={(event) => setPort(event.target.value)}
                  />
                )}
              </Field>
            </div>

            <Field label="Identity file" hint="Optional private key path on this computer.">
              {({ id, describedBy }) => (
                <div className="flex flex-col gap-2">
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    className="font-mono"
                    placeholder={matchedSshHost?.identityFile || "~/.ssh/id_ed25519"}
                    value={identityFile}
                    onChange={(event) => setIdentityFile(event.target.value)}
                  />
                  {identityFiles.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5">
                      {identityFiles.map((filePath) => (
                        <Button
                          key={filePath}
                          type="button"
                          size="xs"
                          variant={identityFile === filePath ? "secondary" : "outline"}
                          className="font-mono text-[11px]"
                          onClick={() => setIdentityFile(filePath)}
                        >
                          {filePath}
                        </Button>
                      ))}
                    </div>
                  ) : null}
                </div>
              )}
            </Field>
          </>
        ) : (
          <>
            <div className="flex flex-col gap-1.5">
              <span className="text-[12.5px] font-medium text-foreground">Endpoint</span>
              <RadioSegments
                label="E2B endpoint"
                options={e2bEndpointModes}
                value={e2bMode}
                onChange={(next) => {
                  setE2bMode(next);
                  setTried(false);
                  setProbeResult(null);
                  probe.reset();
                }}
              />
              <p className="text-[12px] text-muted-foreground">
                {e2bMode === "cloud"
                  ? "Managed E2B Cloud at api.e2b.app."
                  : "Self-hosted, compatible clouds (Alibaba, Novita), Kubernetes, or local Docker/VM gateways (edvabe, NovitaBox)."}
              </p>
            </div>

            {e2bMode === "custom" ? (
              <>
                <Field
                  label="API URL"
                  hint="Control-plane base URL (supports http:// for local Docker/VM runtimes)."
                  error={show("apiUrl")}
                >
                  {({ id, describedBy, invalid }) => (
                    <Input
                      id={id}
                      aria-describedby={describedBy}
                      aria-invalid={invalid}
                      className="font-mono"
                      placeholder="http://localhost:3000"
                      value={apiUrl}
                      onChange={(event) => setApiUrl(event.target.value)}
                    />
                  )}
                </Field>

                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    label="Sandbox domain"
                    hint="Optional; auto-derived from API URL when omitted."
                    error={show("domain")}
                  >
                    {({ id, describedBy, invalid }) => (
                      <Input
                        id={id}
                        aria-describedby={describedBy}
                        aria-invalid={invalid}
                        className="font-mono"
                        placeholder={inferredDomain || "sandbox.example.com"}
                        value={domain}
                        onChange={(event) => setDomain(event.target.value)}
                      />
                    )}
                  </Field>

                  <Field
                    label="Gateway / Envd URL"
                    hint="Optional single-proxy URL or {sandboxId}/{port} template."
                    error={show("envdUrl")}
                  >
                    {({ id, describedBy, invalid }) => (
                      <Input
                        id={id}
                        aria-describedby={describedBy}
                        aria-invalid={invalid}
                        className="font-mono"
                        placeholder="http://localhost:3000"
                        value={envdUrl}
                        onChange={(event) => setEnvdUrl(event.target.value)}
                      />
                    )}
                  </Field>
                </div>
              </>
            ) : null}

            {existing?.secretSet && !replaceSecret ? (
              <SavedSecret
                label="API key"
                text="Saved API key"
                hint="Stored write-only on this computer. Leave unchanged or replace it."
                action="Replace"
                onReplace={() => setReplaceSecret(true)}
              />
            ) : (
              <Field
                label={e2bMode === "custom" ? "API key (optional)" : "API key"}
                hint={
                  e2bMode === "custom"
                    ? "Optional write-only key. Leave blank if your gateway requires no auth."
                    : "Write-only E2B API key. Never returned after saving."
                }
                error={show("secret")}
              >
                {({ id, describedBy, invalid }) => (
                  <Input
                    id={id}
                    type="password"
                    autoComplete="off"
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    placeholder="e2b_..."
                    value={secret}
                    onChange={(event) => setSecret(event.target.value)}
                  />
                )}
              </Field>
            )}

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Template" hint="E2B sandbox template or image ID." error={show("template")}>
                {({ id, describedBy, invalid }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    placeholder="base"
                    value={template}
                    onChange={(event) => setTemplate(event.target.value)}
                  />
                )}
              </Field>

              <Field
                label={autoPause ? "Auto-pause timeout (s)" : "Sandbox timeout (s)"}
                hint={
                  autoPause
                    ? "Idle sandboxes auto-pause after this delay."
                    : "Sandbox lifetime before expiry."
                }
                error={show("timeoutSeconds")}
              >
                {({ id, describedBy, invalid }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    placeholder="300"
                    value={timeoutSeconds}
                    onChange={(event) => setTimeoutSeconds(event.target.value)}
                  />
                )}
              </Field>
            </div>

            <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3.5 py-2.5">
              <div className="min-w-0 flex-1">
                <p className="text-[12.5px] font-medium text-foreground">Auto-pause on idle</p>
                <p className="text-[12px] text-muted-foreground">
                  Turn off for local Docker or container gateways without snapshot pause.
                </p>
              </div>
              <Switch checked={autoPause} onCheckedChange={setAutoPause} aria-label="Auto-pause on idle" />
            </div>
          </>
        )}

        <Field
          label="Workspace directory"
          hint={
            kind === "e2b"
              ? "Directory inside the sandbox (default /home/user/workspace)."
              : "Optional base directory on the remote host (default ~/.dex/agents)."
          }
          error={show("workspaceDir")}
        >
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              className="font-mono"
              placeholder={kind === "e2b" ? "/home/user/workspace" : "~/.dex/agents"}
              value={workspaceDir}
              onChange={(event) => setWorkspaceDir(event.target.value)}
            />
          )}
        </Field>

        <button type="submit" hidden />
      </form>

      {probe.isError ? <Failure title="Connection test failed" error={probe.error} /> : null}
      {probeResult ? (
        <ProbeResultPanel
          probe={probeResult}
          forgetting={forgetHostKey.isPending}
          onForgetHostKey={
            existing && kind === "pi-env" && probeResult.hostKeyChanged
              ? () => forgetHostKey.mutate({ path: { environmentId: existing.id } })
              : undefined
          }
        />
      ) : null}
      {mutationError ? <Failure title="Could not save environment" error={mutationError} /> : null}
    </Modal>
  );
}

function DeleteEnvironmentDialog({
  env,
  users,
  others,
  onClose,
}: {
  env: Environment;
  users: Agent[];
  others: Environment[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const [targetEnvId, setTargetEnvId] = useState("");
  const current = users.length > 0;
  const names = users.map((agent) => agent.name).join(", ");

  const remove = useMutation({
    ...deleteEnvironmentMutation(),
    onSettled: () => refreshEnvironments(),
  });

  const reassign = useMutation({
    mutationFn: async () => {
      await Promise.all(
        users.map((user) =>
          saveAgent(user.id!, (agent) =>
            agent.environmentId === env.id ? { ...agent, environmentId: targetEnvId } : agent,
          ),
        ),
      );
    },
  });

  const pending = reassign.isPending || remove.isPending;

  async function confirm() {
    if (current && !reassign.isSuccess) await reassign.mutateAsync();
    await remove.mutateAsync({ path: { environmentId: env.id } });
    onClose();
    notify(`Removed ${env.name}.`);
  }

  return (
    <Confirm
      title={`Remove ${env.name}?`}
      description="dex deletes this environment configuration."
      confirmLabel={current ? "Reassign bots and remove" : "Remove environment"}
      pending={pending}
      error={reassign.error ?? remove.error}
      onCancel={onClose}
      onConfirm={() => void confirm().catch(() => {})}
    >
      {current ? (
        <div className="flex flex-col gap-3 rounded-md bg-orange-tint p-3">
          <p className="text-[13px] font-medium text-foreground">
            {names} {users.length === 1 ? "uses" : "use"} this environment now.
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
                <option value="">Local (this computer)</option>
                {others.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name} ({item.kind})
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

function isCustomE2BConfig(cfg: EnvironmentConfig): boolean {
  return !!(cfg.apiUrl || cfg.domain || cfg.envdUrl);
}

function urlFieldError(raw: string, example: string): string {
  try {
    const parsed = new URL(raw);

    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "Use an http or https URL.";

    if (parsed.username || parsed.password) return "Do not include credentials in the URL.";

    if (parsed.search || parsed.hash) return "Remove query parameters and fragments from the URL.";

    return "";
  } catch {
    return `Enter a full URL, for example ${example}.`;
  }
}

function formatSshTarget(cfg: EnvironmentConfig): string {
  const prefix = cfg.user ? `${cfg.user}@` : "";
  const suffix = cfg.port ? `:${cfg.port}` : "";

  return `${prefix}${cfg.host ?? ""}${suffix}`;
}

function formatResolvedSshHost(entry: SshHostEntry): string {
  const prefix = entry.user ? `${entry.user}@` : "";
  const host = entry.hostName || entry.alias;
  const suffix = entry.port ? `:${entry.port}` : "";
  const jump = entry.proxyJump ? ` via ${entry.proxyJump}` : "";

  return `${prefix}${host}${suffix}${jump}`;
}

function formatE2BEndpointSummary(cfg: EnvironmentConfig): string {
  if (cfg.apiUrl) return cfg.apiUrl;

  if (cfg.domain) return `https://api.${cfg.domain}`;

  if (cfg.envdUrl) return cfg.envdUrl;

  return "E2B Cloud (e2b.app)";
}
