import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getHealthOptions, issueRunnerCodeMutation } from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { machineAddress, machineInstallCommand } from "../lib/environments";
import { Field } from "./form";
import { Mono } from "./page";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Spinner } from "./feedback";

// RunnerInstall shows the one command that connects a machine to Dex. It creates a
// fresh single-use code on demand, so the command is always safe to copy.
export function RunnerInstall({
  environmentId,
  autoIssue = false,
}: {
  environmentId: string;
  // autoIssue creates the first command without another click, for a new environment.
  autoIssue?: boolean;
}) {
  const { notify } = useApp();
  const health = useQuery(getHealthOptions());
  const [address, setAddress] = useState("");
  const [issued, setIssued] = useState<{ code: string } | null>(null);

  const machineUrls = health.data?.machineUrls ?? [];

  const target = address || machineUrls.at(0) || "";

  const command = issued
    ? machineInstallCommand(target, environmentId, health.data?.machinePin ?? "", issued.code)
    : null;

  const issue = useMutation({
    ...issueRunnerCodeMutation(),
    onSuccess: (result) => setIssued(result),
  });

  const autoIssued = useRef(false);
  const pin = health.data?.machinePin;

  useEffect(() => {
    if (!autoIssue || autoIssued.current || !machineUrls.length || pin === undefined) return;

    autoIssued.current = true;
    issue.mutate({ path: { environmentId } });
  }, [autoIssue, environmentId, pin, machineUrls.length, issue]);

  return (
    <div className="flex flex-col gap-2">
      <p className="text-sm text-muted-foreground">
        Run one command on the machine. It needs sudo, and it connects the machine to Dex without an inbound
        port.
      </p>
      {machineUrls.length ? (
        <Field
          label="Address the machine uses for Dex"
          hint="Change it when the machine reaches Dex through another network."
          error={
            target && !machineAddress(target)
              ? "Enter an HTTPS address without a path. Do not use localhost."
              : undefined
          }
        >
          {(ids) => (
            <Input
              id={ids.id}
              aria-describedby={ids.describedBy}
              value={target}
              onChange={(event) => {
                setAddress(event.target.value);
                setIssued(null);
                issue.reset();
              }}
            />
          )}
        </Field>
      ) : (
        <p className="text-sm text-orange">
          Remote machines are off: this Dex listens on loopback only. Start it with a reachable host, or set{" "}
          <Mono>--machine-listen</Mono>.
        </p>
      )}
      <div>
        <Button
          size="xs"
          variant="outline"
          disabled={!machineUrls.length || issue.isPending}
          onClick={() => issue.mutate({ path: { environmentId } })}
        >
          {issue.isPending ? <Spinner /> : null}
          {issued ? "New install command" : "Get install command"}
        </Button>
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
