import { useEffect, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { issueRunnerCodeMutation } from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { machineAddress, machineInstallCommand } from "../lib/runners";
import { Mono } from "./page";
import { Button } from "./ui/button";
import { Spinner } from "./feedback";

// RunnerInstall shows the one command that connects a machine to Dex. Dex issues the code only
// for a saved environment, so the caller renders this after the save.
export function RunnerInstall({
  runnerId,
  autoIssue = false,
}: {
  runnerId: string;
  // autoIssue creates the first command without another click, for a new environment.
  autoIssue?: boolean;
}) {
  const { notify } = useApp();
  const [issued, setIssued] = useState<{ code: string } | null>(null);

  // The machine reaches Dex at the address the operator uses for the web UI, so the command
  // needs no address setting: it names the origin of this page.
  const origin = window.location.origin;
  const command = issued ? machineInstallCommand(origin, runnerId, issued.code) : null;
  const reachable = machineAddress(origin) !== null;

  const issue = useMutation({
    ...issueRunnerCodeMutation(),
    onSuccess: (result) => setIssued(result),
  });

  const autoIssued = useRef(false);

  useEffect(() => {
    if (!autoIssue || autoIssued.current || !reachable) return;

    autoIssued.current = true;
    issue.mutate({ path: { runnerId } });
  }, [autoIssue, runnerId, issue, reachable]);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="xs"
          variant="outline"
          disabled={issue.isPending || !reachable}
          onClick={() => {
            setIssued(null);
            issue.reset();
            issue.mutate({ path: { runnerId } });
          }}
        >
          {issue.isPending ? <Spinner /> : null}
          {issued ? "New install command" : "Get install command"}
        </Button>
        <span className="text-[12px] text-muted-foreground">
          Run the command on the machine as the account that will use it.
        </span>
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
          <p className="text-muted-foreground">
            The command expires in 10 minutes. Any work sent to this runner can read the install account's
            files and use its credentials. On Linux, add <Mono>--dedicated</Mono> to use a separate account.
          </p>
        </div>
      ) : null}
      {!reachable ? (
        <p className="text-sm text-orange">
          Open Dex at the HTTPS address that the machine uses, then get the command again.
        </p>
      ) : null}
      {issue.isError ? (
        <p className="text-sm text-red">Could not create an install command. Try again.</p>
      ) : null}
    </div>
  );
}
