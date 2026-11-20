import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getHealthOptions, issueRunnerCodeMutation } from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { machineAddress, machineInstallCommand } from "../lib/environments";
import { Field, Input, Select } from "./form";
import { Mono } from "./page";
import { Button } from "./ui/button";
import { Spinner } from "./feedback";

const customOption = "__custom__";

// DexAddressField picks the address a machine uses for Dex. One host can have several
// networks, so Dex offers each address it detects and keeps a free text option.
export function DexAddressField({
  address,
  onChange,
}: {
  address: string;
  onChange: (value: string) => void;
}) {
  const health = useQuery(getHealthOptions());
  const [typed, setTyped] = useState(false);

  const machineUrls = health.data?.machineUrls ?? [];
  const target = address || machineUrls.at(0) || "";

  if (typed || !machineUrls.length) {
    return (
      <Field
        label="Address the machine uses for Dex"
        hint="Use an address the machine can reach."
        error={
          target && !machineAddress(target)
            ? "Enter an HTTPS address without a path. Do not use localhost."
            : undefined
        }
      >
        {(ids) => (
          <Input
            {...ids}
            value={target}
            placeholder="https://192.0.2.10:8788"
            onChange={(event) => onChange(event.target.value)}
          />
        )}
      </Field>
    );
  }

  return (
    <Field label="Address the machine uses for Dex" hint="Choose the network the machine shares.">
      {(ids) => (
        <Select
          {...ids}
          value={target}
          onChange={(event) => {
            if (event.target.value === customOption) {
              setTyped(true);

              return;
            }

            onChange(event.target.value);
          }}
        >
          {machineUrls.map((url) => (
            <option key={url} value={url}>
              {url}
            </option>
          ))}
          <option value={customOption}>Other address…</option>
        </Select>
      )}
    </Field>
  );
}

// RemoteMachinesOff explains an empty address list. A loopback-only Dex has no address
// that another machine can reach.
export function RemoteMachinesOff() {
  const health = useQuery(getHealthOptions());

  if (health.data?.machineUrls?.length) return null;

  return (
    <p className="text-sm text-orange">
      Remote machines are off: this Dex listens on loopback only. Start it with a reachable host, or set{" "}
      <Mono>--machine-listen</Mono>.
    </p>
  );
}

// RunnerInstall shows the one command that connects a machine to Dex. Dex issues the code
// only for a saved environment, so the caller renders this after the save.
export function RunnerInstall({
  environmentId,
  address,
  autoIssue = false,
}: {
  environmentId: string;
  address: string;
  // autoIssue creates the first command without another click, for a new environment.
  autoIssue?: boolean;
}) {
  const { notify } = useApp();
  const health = useQuery(getHealthOptions());
  const [issued, setIssued] = useState<{ code: string } | null>(null);

  const machineUrls = health.data?.machineUrls ?? [];
  const pin = health.data?.machinePin;
  const target = address || machineUrls.at(0) || "";

  const command =
    issued && pin !== undefined ? machineInstallCommand(target, environmentId, pin, issued.code) : null;

  const issue = useMutation({
    ...issueRunnerCodeMutation(),
    onSuccess: (result) => setIssued(result),
  });

  const autoIssued = useRef(false);

  useEffect(() => {
    if (!autoIssue || autoIssued.current || !machineUrls.length || pin === undefined) return;

    autoIssued.current = true;
    issue.mutate({ path: { environmentId } });
  }, [autoIssue, environmentId, pin, machineUrls.length, issue]);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="xs"
          variant="outline"
          disabled={!machineUrls.length || issue.isPending}
          onClick={() => {
            setIssued(null);
            issue.reset();
            issue.mutate({ path: { environmentId } });
          }}
        >
          {issue.isPending ? <Spinner /> : null}
          {issued ? "New install command" : "Get install command"}
        </Button>
        <span className="text-[12px] text-muted-foreground">Run the command on the machine with sudo.</span>
      </div>
      {command ? (
        <div className="flex flex-col gap-2 text-sm">
          <div className="overflow-x-auto rounded-md border border-border p-2">
            <Mono>{command}</Mono>
          </div>
          <div>
            <Button
              size="xs"
              onClick={() =>
                void navigator.clipboard.writeText(command).then(
                  () => notify("Copied install command."),
                  () => notify("Copy the command from the text above."),
                )
              }
            >
              Copy install command
            </Button>
          </div>
          <p className="text-muted-foreground">The command enrolls one machine and expires in 10 minutes.</p>
        </div>
      ) : null}
      {issued && !command ? (
        <p className="text-sm text-red">Check the address: it must be reachable from the machine.</p>
      ) : null}
      {issue.isError ? (
        <p className="text-sm text-red">Could not create an install command. Try again.</p>
      ) : null}
    </div>
  );
}
