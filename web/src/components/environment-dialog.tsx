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
import { DexAddressField, RemoteMachinesOff, RunnerInstall } from "./runner-install";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Field } from "./form";

// A place is what the user picks first: where the bot runs. The access method is a
// property of a machine, so it comes second and only when the place is a machine.
type Place = "machine" | "sandbox";

const environmentPlaces: readonly {
  place: Place;
  title: string;
  subtitle: string;
  traits: string;
}[] = [
  {
    place: "machine",
    title: "Another machine",
    subtitle: "A Linux or macOS machine that you own",
    traits: "Persistent disk · Your files",
  },
  {
    place: "sandbox",
    title: "Sandbox",
    subtitle: "An isolated cloud VM that Dex creates for one bot",
    traits: "Isolated disk · Disposable",
  },
];

const machineAccess: readonly {
  kind: EnvironmentKind;
  title: string;
  subtitle: string;
}[] = [
  {
    kind: "pi-env",
    title: "Over SSH",
    subtitle: "The machine runs sshd and Dex can reach it",
  },
  {
    kind: "runner",
    title: "dex runner",
    subtitle: "The machine connects out to Dex. No sshd, no inbound port.",
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

  const [machineKind, setMachineKind] = useState<EnvironmentKind>(
    existing?.kind === "runner" ? "runner" : "pi-env",
  );

  const [tried, setTried] = useState(false);
  const [serviceName, setServiceName] = useState(existing?.kind === "runner" ? existing.name : "");
  const [address, setAddress] = useState("");
  const [probeResult, setProbeResult] = useState<EnvironmentTestResult | null>(null);

  // A dex runner environment must exist before Dex can issue its install command, so the
  // dialog stays open after the save and shows that command.
  const [created, setCreated] = useState<Environment | null>(null);

  const resetProbe = () => {
    setProbeResult(null);
  };

  const ssh = useSshEnvironmentForm(existing?.kind === "pi-env" ? existing : undefined, sshHosts, resetProbe);

  const sandbox = useSandboxEnvironmentForm(existing?.kind === "e2b" ? existing : undefined, resetProbe);

  const create = useMutation({
    ...createEnvironmentMutation(),
    onSuccess: async (saved) => {
      await refreshEnvironments();

      notify(`Added ${saved.name}.`);

      if (saved.kind === "runner") {
        setCreated(saved);

        return;
      }

      onClose();
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
  const place: Place = kind === "e2b" ? "sandbox" : "machine";

  const showSsh = (key: keyof typeof ssh.errors) => (tried ? ssh.errors[key] : undefined);
  const showSandbox = (key: keyof typeof sandbox.errors) => (tried ? sandbox.errors[key] : undefined);

  function runProbe() {
    setTried(true);

    if (active.hasConfigErrors || pending || probe.isPending) return;

    probe.mutate({ body: active.probeBody(existing?.id) });
  }

  function submit() {
    setTried(true);

    if (pending || (kind === "runner" ? !serviceName.trim() : active.hasErrors)) return;

    const body = kind === "runner" ? { name: serviceName.trim(), kind, config: {} } : active.submitBody();

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
        created ? (
          <Button onClick={onClose}>Done</Button>
        ) : (
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
        )
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
          <div role="radiogroup" aria-label="Where the bot runs" className="grid gap-2.5 sm:grid-cols-2">
            {environmentPlaces.map((choice) => {
              const selected = place === choice.place;

              return (
                <button
                  key={choice.place}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  aria-label={choice.title}
                  disabled={!!created}
                  onClick={() => {
                    setKind(choice.place === "sandbox" ? "e2b" : machineKind);
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

        {!existing && place === "machine" ? (
          <div
            role="radiogroup"
            aria-label="How Dex reaches the machine"
            className="grid gap-2 sm:grid-cols-2"
          >
            {machineAccess.map((choice) => {
              const selected = kind === choice.kind;

              return (
                <button
                  key={choice.kind}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  aria-label={choice.title}
                  disabled={!!created}
                  onClick={() => {
                    setKind(choice.kind);
                    setMachineKind(choice.kind);
                    setTried(false);
                    setProbeResult(null);
                    probe.reset();
                  }}
                  className={cn(
                    "rounded-md border px-3 py-2 text-left transition-colors",
                    selected ? "border-foreground bg-accent/40" : "border-border hover:bg-accent/30",
                  )}
                >
                  <span className="text-[13px] font-medium text-foreground">{choice.title}</span>
                  <span className="mt-0.5 block text-[11.5px] leading-snug text-muted-foreground">
                    {choice.subtitle}
                  </span>
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
          <>
            <Field label="Name" error={tried && !serviceName.trim() ? "Enter a name." : undefined}>
              {(ids) => (
                <Input
                  {...ids}
                  maxLength={60}
                  value={created?.name ?? serviceName}
                  disabled={!!created}
                  onChange={(event) => setServiceName(event.target.value)}
                />
              )}
            </Field>
            <DexAddressField address={address} onChange={setAddress} />
            <RemoteMachinesOff />
            {created ? <RunnerInstall environmentId={created.id} address={address} autoIssue /> : null}
          </>
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
