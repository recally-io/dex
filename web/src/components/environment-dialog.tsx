import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import type {
  Environment,
  EnvironmentKind,
  EnvironmentTestResult,
  SshHostEntry,
} from "../api/generated/types.gen";
import {
  createEnvironmentMutation,
  forgetEnvironmentHostKeyMutation,
  probeEnvironmentMutation,
  updateEnvironmentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { refreshEnvironments } from "../lib/queries";
import { cn } from "../lib/utils";
import { Modal } from "./dialog";
import { SandboxEnvironmentFields, useSandboxEnvironmentForm } from "./environment-sandbox-fields";
import { SshEnvironmentFields, useSshEnvironmentForm } from "./environment-ssh-fields";
import { Failure, Spinner } from "./feedback";
import { ProbeResultPanel } from "./probe-result-panel";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Field } from "./form";

const environmentChoices: readonly {
  kind: EnvironmentKind;
  title: string;
  subtitle: string;
  traits: string;
}[] = [
  {
    kind: "pi-env",
    title: "SSH Machine",
    subtitle: "Remote server, Mac, or local VM over SSH",
    traits: "Persistent disk · Login shell",
  },
  {
    kind: "e2b",
    title: "Sandbox",
    subtitle: "Isolated E2B Cloud, Docker, or microVM",
    traits: "Isolated disk · Disposable per bot",
  },
  {
    kind: "runner",
    title: "Outbound runner",
    subtitle: "Linux host that connects to Dex over HTTPS",
    traits: "Persistent disk · No inbound port",
  },
];

export function EnvironmentDialog({
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
  const [tried, setTried] = useState(false);
  const [runnerName, setRunnerName] = useState(existing?.kind === "runner" ? existing.name : "");
  const [probeResult, setProbeResult] = useState<EnvironmentTestResult | null>(null);

  const resetProbe = () => {
    setProbeResult(null);
  };

  const ssh = useSshEnvironmentForm(existing?.kind === "pi-env" ? existing : undefined, sshHosts, resetProbe);

  const sandbox = useSandboxEnvironmentForm(existing?.kind === "e2b" ? existing : undefined, resetProbe);

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
      await probe.mutateAsync({ body: ssh.probeBody(existing.id) });
    },
  });

  const pending = create.isPending || update.isPending;
  const mutationError = create.error ?? update.error;
  const active = kind === "pi-env" ? ssh : sandbox;

  const showSsh = (key: keyof typeof ssh.errors) => (tried ? ssh.errors[key] : undefined);
  const showSandbox = (key: keyof typeof sandbox.errors) => (tried ? sandbox.errors[key] : undefined);

  function runProbe() {
    setTried(true);

    if (active.hasConfigErrors || pending || probe.isPending) return;

    probe.mutate({ body: active.probeBody(existing?.id) });
  }

  function submit() {
    setTried(true);

    if (pending || (kind === "runner" ? !runnerName.trim() : active.hasErrors)) return;

    const body = kind === "runner" ? { name: runnerName.trim(), kind, config: {} } : active.submitBody();

    if (existing) {
      update.mutate({ path: { environmentId: existing.id }, body });
    } else {
      create.mutate({ body });
    }
  }

  return (
    <Modal
      title={existing ? `Edit ${existing.name}` : "Add execution environment"}
      description="Choose where a bot runs shell commands and stores its workspace."
      pending={pending}
      onClose={onClose}
      footer={
        <>
          {kind !== "runner" ? (
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
          ) : null}
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending}>
            {pending ? (
              <Spinner className="border-primary-foreground/40 border-t-primary-foreground" />
            ) : null}
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
          <div role="radiogroup" aria-label="Environment type" className="grid gap-2.5 sm:grid-cols-2">
            {environmentChoices.map((choice) => {
              const selected = kind === choice.kind;

              return (
                <button
                  key={choice.kind}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  aria-label={choice.title}
                  onClick={() => {
                    setKind(choice.kind);
                    setTried(false);
                    setProbeResult(null);
                    probe.reset();
                  }}
                  className={cn(
                    "flex flex-col items-start rounded-md border p-3 text-left transition-colors",
                    selected
                      ? "border-foreground bg-accent/60 shadow-card"
                      : "border-border bg-card hover:bg-accent/40",
                  )}
                >
                  <span className="text-[13.5px] font-semibold text-foreground">{choice.title}</span>
                  <span className="mt-1 text-[12px] leading-snug text-muted-foreground">
                    {choice.subtitle}
                  </span>
                  <span className="mt-2 text-[11px] font-medium text-foreground/80">{choice.traits}</span>
                </button>
              );
            })}
          </div>
        ) : null}

        {kind === "pi-env" ? (
          <SshEnvironmentFields
            controller={ssh}
            sshHosts={sshHosts}
            identityFiles={identityFiles}
            show={showSsh}
          />
        ) : kind === "e2b" ? (
          <SandboxEnvironmentFields controller={sandbox} show={showSandbox} />
        ) : (
          <Field label="Name" error={tried && !runnerName.trim() ? "Enter a name." : undefined}>
            {(ids) => (
              <Input
                {...ids}
                maxLength={60}
                value={runnerName}
                onChange={(event) => setRunnerName(event.target.value)}
              />
            )}
          </Field>
        )}

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
