import type { EnvironmentTestResult } from "../api/generated/types.gen";
import { Failure, Spinner } from "./feedback";
import { Mono } from "./page";
import { Button } from "./ui/button";

export function ProbeResultPanel({
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
