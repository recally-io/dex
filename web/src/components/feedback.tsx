import type { ReactNode } from "react";
import { Skeleton } from "./ui/skeleton";
import { Button } from "./ui/button";
import { errorText } from "../lib/queries";
import { cn } from "../lib/utils";

/** The failure block: a red dot, a title, the server text and an optional action. */
export function Failure({
  error,
  retry,
  title = "Could not load this data",
  children,
}: {
  error?: unknown;
  retry?: () => void;
  title?: string;
  children?: ReactNode;
}) {
  return (
    <div role="alert" className="rounded-card bg-red-tint px-3.5 py-3 text-[12.5px]">
      <div className="flex items-center gap-2 font-medium text-red">
        <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-red" />
        {title}
      </div>
      {error ? <p className="mt-1.5 pl-3.5 text-ink-2 [overflow-wrap:anywhere]">{errorText(error)}</p> : null}
      {children ? <div className="mt-1.5 pl-3.5 text-ink-2 [overflow-wrap:anywhere]">{children}</div> : null}
      {retry ? (
        <div className="mt-2.5 pl-3.5">
          <Button size="xs" variant="secondary" onClick={retry}>
            Try again
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// The label of LoadingState is the status region.
export function Loading({ label = "Loading" }: { label?: string }) {
  return (
    <div role="status" aria-label={label} className="flex min-h-24 items-center justify-center gap-1.5">
      <Skeleton className="size-2 rounded-full" />
      <Skeleton className="size-2 rounded-full" />
      <Skeleton className="size-2 rounded-full" />
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-3 shrink-0 animate-[spin_700ms_linear_infinite] rounded-full border-[1.5px] border-line-strong border-t-ink-2",
        className,
      )}
    />
  );
}
