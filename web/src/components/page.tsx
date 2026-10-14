import { useId, type ReactNode } from "react";
import { cn } from "../lib/utils";

// The page title takes focus after navigation; see Shell.
export function Page({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("mx-auto w-full max-w-5xl px-4 py-6 sm:px-8 sm:py-10", className)}>{children}</div>
  );
}

export function PageHeader({
  title,
  description,
  actions,
  eyebrow,
  badge,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  eyebrow?: ReactNode;
  badge?: ReactNode;
}) {
  return (
    <header className="mb-8 flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0">
        {eyebrow ? <div className="mb-2 text-xs text-ink-3">{eyebrow}</div> : null}
        <div className="flex flex-wrap items-center gap-2.5">
          <h1
            id="page-title"
            tabIndex={-1}
            className="break-words text-2xl font-semibold tracking-tight text-ink outline-none"
          >
            {title}
          </h1>
          {badge}
        </div>
        {description ? <p className="mt-2 max-w-2xl text-sm leading-6 text-ink-2">{description}</p> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  );
}

export function Section({
  title,
  description,
  actions,
  badge,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  badge?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();

  return (
    <section aria-labelledby={id} className="mb-10">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={id} className="text-base font-semibold text-ink">
              {title}
            </h2>
            {badge}
          </div>
          {description ? <p className="mt-1 max-w-2xl text-sm text-ink-2">{description}</p> : null}
        </div>
        {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("rounded-[14px] border border-line bg-surface p-4 shadow-card sm:p-5", className)}>
      {children}
    </div>
  );
}

export type FieldIds = { id: string; describedBy: string | undefined; invalid: boolean };

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: string;
  children: (ids: FieldIds) => ReactNode;
}) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = [hint ? hintId : "", error ? errorId : ""].filter(Boolean).join(" ") || undefined;

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-[13px] font-medium text-ink">
        {label}
      </label>
      {children({ id, describedBy, invalid: !!error })}
      {hint ? (
        <p id={hintId} className="text-xs leading-5 text-ink-3">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} className="text-xs font-medium text-red">
          {error}
        </p>
      ) : null}
    </div>
  );
}

export function Note({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "warn" }) {
  return (
    <div
      className={cn(
        "rounded-xl px-4 py-3 text-[13px] leading-6",
        tone === "warn" ? "bg-orange-tint text-ink" : "bg-inset text-ink-2",
      )}
    >
      {children}
    </div>
  );
}

export function Empty({ title, body, action }: { title: string; body: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-[14px] border border-dashed border-line px-6 py-12 text-center">
      <p className="text-sm font-medium text-ink">{title}</p>
      <p className="max-w-md text-sm text-ink-2">{body}</p>
      {action}
    </div>
  );
}

export function Rows({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid gap-x-6 gap-y-3 text-[13px] sm:grid-cols-[180px_1fr]">
      {rows.map(([term, value]) => (
        <div key={term} className="contents">
          <dt className="text-ink-3">{term}</dt>
          <dd className="min-w-0 break-words text-ink">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// Marks a capability that this version of dex does not have.
export function NotAvailable() {
  return (
    <span className="inline-flex h-5 shrink-0 items-center rounded-full border border-dashed border-line-strong px-2 text-[11px] font-medium text-ink-2">
      Not available yet
    </span>
  );
}
