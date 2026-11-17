import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Environment, EnvironmentConfig, EnvironmentTestResult } from "../api/generated/types.gen";
import {
  createEnvironmentMutation,
  deleteEnvironmentMutation,
  listEnvironmentsOptions,
  testEnvironmentMutation,
  updateEnvironmentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { errorText, refreshEnvironments } from "../lib/queries";
import { Confirm } from "./confirm";
import { Modal } from "./dialog";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Input } from "./form";
import { Facts, Mono, Panel, Section } from "./page";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function ExecutionEnvironments({ n }: { n: number }) {
  const query = useQuery(listEnvironmentsOptions());
  const [dialog, setDialog] = useState<Environment | "new" | null>(null);
  const [removeTarget, setRemoveTarget] = useState<Environment | null>(null);
  const envs = query.data?.environments ?? [];

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
        <Button size="sm" onClick={() => setDialog("new")}>
          Add environment
        </Button>
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
              onEdit={() => setDialog(env)}
              onRemove={() => setRemoveTarget(env)}
            />
          ))}
        </div>
      )}

      {dialog ? (
        <EnvironmentDialog existing={dialog === "new" ? undefined : dialog} onClose={() => setDialog(null)} />
      ) : null}

      {removeTarget ? (
        <DeleteEnvironmentDialog env={removeTarget} onClose={() => setRemoveTarget(null)} />
      ) : null}
    </Section>
  );
}

function EnvironmentCard({
  env,
  onEdit,
  onRemove,
}: {
  env: Environment;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { notify } = useApp();
  const [probe, setProbe] = useState<EnvironmentTestResult | null>(null);

  const test = useMutation({
    ...testEnvironmentMutation(),
    onSuccess: (result) => {
      setProbe(result);
      notify(result.ok ? `Connected to ${env.name}.` : `Test failed: ${result.error || "unreachable"}`);
    },
    onError: (err) => notify(errorText(err)),
  });

  const rows: [string, ReactNode][] = [["Target", <Mono key="target">{formatSshTarget(env.config)}</Mono>]];

  if (env.config.identityFile) {
    rows.push(["Identity file", <Mono key="id">{env.config.identityFile}</Mono>]);
  }

  rows.push(["Workspace", <Mono key="ws">{env.config.workspaceDir || "~/.dex/agents"}</Mono>]);

  return (
    <Panel>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <h3 className="text-[14px] font-medium text-foreground">{env.name}</h3>
          <Mono className="text-[11.5px]">{env.kind}</Mono>
        </div>
        <Button
          size="xs"
          variant="outline"
          disabled={test.isPending}
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
          {probe.ok ? (
            <div className="rounded-card bg-green-tint px-3.5 py-2.5 text-[12.5px] text-foreground">
              <p className="font-medium text-green">
                Connected · {probe.platform}/{probe.arch}
              </p>
              {probe.home ? (
                <p className="mt-0.5 text-muted-foreground">
                  Remote home: <Mono>{probe.home}</Mono>
                </p>
              ) : null}
            </div>
          ) : (
            <Failure title="Connection test failed">
              <p className="font-mono text-[11.5px]">{probe.error || "Unknown error"}</p>
            </Failure>
          )}
        </div>
      ) : null}
    </Panel>
  );
}

function EnvironmentDialog({ existing, onClose }: { existing?: Environment; onClose: () => void }) {
  const { notify } = useApp();
  const [name, setName] = useState(existing?.name ?? "");
  const [host, setHost] = useState(existing?.config.host ?? "");
  const [user, setUser] = useState(existing?.config.user ?? "");
  const [port, setPort] = useState(existing?.config.port ? String(existing.config.port) : "");
  const [identityFile, setIdentityFile] = useState(existing?.config.identityFile ?? "");
  const [workspaceDir, setWorkspaceDir] = useState(existing?.config.workspaceDir ?? "");
  const [tried, setTried] = useState(false);

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

  const pending = create.isPending || update.isPending;
  const mutationError = create.error ?? update.error;
  const trimmedPort = port.trim();
  const parsedPort = trimmedPort ? Number(trimmedPort) : undefined;
  const trimmedWS = workspaceDir.trim();

  const errors: Partial<Record<"name" | "host" | "port" | "workspaceDir", string>> = {};

  if (!name.trim() || Array.from(name.trim()).length > 60) {
    errors.name = "Enter a name between 1 and 60 characters.";
  }

  if (!host.trim()) {
    errors.host = "Enter a host name or SSH config alias.";
  }

  if (
    trimmedPort &&
    (parsedPort === undefined || !Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535)
  ) {
    errors.port = "Port must be between 1 and 65535.";
  }

  if (trimmedWS && !trimmedWS.startsWith("/") && trimmedWS !== "~" && !trimmedWS.startsWith("~/")) {
    errors.workspaceDir = "Use an absolute path or start with ~.";
  }

  const show = (key: keyof typeof errors) => (tried ? errors[key] : undefined);

  function submit() {
    setTried(true);

    if (Object.keys(errors).length > 0 || pending) return;

    const config: EnvironmentConfig = {
      host: host.trim(),
      user: user.trim() || undefined,
      port: parsedPort,
      identityFile: identityFile.trim() || undefined,
      workspaceDir: trimmedWS || undefined,
    };

    const body = { name: name.trim(), kind: "pi-env" as const, config };

    if (existing) {
      update.mutate({ path: { environmentId: existing.id }, body });
    } else {
      create.mutate({ body });
    }
  }

  return (
    <Modal
      title={existing ? `Edit ${existing.name}` : "Add execution environment"}
      description="Run bots remotely over SSH using pi-env."
      pending={pending}
      onClose={onClose}
      footer={
        <>
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
        <Field label="Name" error={show("name")}>
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              placeholder="Build server"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          )}
        </Field>

        <Field label="Host" hint="Hostname, IP address, or ~/.ssh/config alias." error={show("host")}>
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              className="font-mono"
              placeholder="dev.example.com"
              value={host}
              onChange={(event) => setHost(event.target.value)}
            />
          )}
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="User" hint="Optional; uses SSH config default.">
            {({ id, describedBy }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                className="font-mono"
                placeholder="ubuntu"
                value={user}
                onChange={(event) => setUser(event.target.value)}
              />
            )}
          </Field>

          <Field label="Port" hint="Optional; defaults to 22." error={show("port")}>
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="22"
                value={port}
                onChange={(event) => setPort(event.target.value)}
              />
            )}
          </Field>
        </div>

        <Field label="Identity file" hint="Optional private key path on this computer.">
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              className="font-mono"
              placeholder="~/.ssh/id_ed25519"
              value={identityFile}
              onChange={(event) => setIdentityFile(event.target.value)}
            />
          )}
        </Field>

        <Field
          label="Workspace directory"
          hint="Optional base directory on the remote host (default ~/.dex/agents)."
          error={show("workspaceDir")}
        >
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              className="font-mono"
              placeholder="~/.dex/agents"
              value={workspaceDir}
              onChange={(event) => setWorkspaceDir(event.target.value)}
            />
          )}
        </Field>

        <button type="submit" hidden />
      </form>

      {mutationError ? <Failure title="Could not save environment" error={mutationError} /> : null}
    </Modal>
  );
}

function DeleteEnvironmentDialog({ env, onClose }: { env: Environment; onClose: () => void }) {
  const { notify } = useApp();

  const remove = useMutation({
    ...deleteEnvironmentMutation(),
    onSuccess: async () => {
      await refreshEnvironments();
      onClose();
      notify(`Removed ${env.name}.`);
    },
  });

  return (
    <Confirm
      title={`Remove ${env.name}?`}
      description="dex deletes this environment configuration and forgets any stored SSH host key for it."
      confirmLabel="Remove environment"
      pending={remove.isPending}
      error={remove.error}
      onCancel={onClose}
      onConfirm={() => remove.mutate({ path: { environmentId: env.id } })}
    />
  );
}

function formatSshTarget(cfg: EnvironmentConfig): string {
  const prefix = cfg.user ? `${cfg.user}@` : "";
  const suffix = cfg.port ? `:${cfg.port}` : "";

  return `${prefix}${cfg.host ?? ""}${suffix}`;
}
