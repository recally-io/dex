import {
  useId,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { Button } from "./atoms/Button";
import { cn } from "../lib/utils";

const fieldClass =
  "w-full rounded-control bg-surface px-3 text-[13px] text-ink shadow-btn outline-none transition-shadow placeholder:text-ink-3 disabled:opacity-60 aria-invalid:shadow-[0_0_0_1px_var(--red)]";

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cn(fieldClass, "h-9", className)} />;
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cn(fieldClass, "block resize-y py-2.5 leading-6", className)} />;
}

export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cn(fieldClass, "h-9 appearance-auto pr-2", className)} />;
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
      <label htmlFor={id} className="text-[12.5px] font-medium text-ink">
        {label}
      </label>
      {children({ id, describedBy, invalid: !!error })}
      {hint ? (
        <p id={hintId} className="text-[12px] text-ink-3">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} className="text-[12px] text-red">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/**
 * A choice of one option in a form. It looks like the upstream SegmentedControl, but uses radio
 * inputs, so the choice has form semantics and arrow keys move between the options.
 */
export function RadioSegments<T extends string>({
  label,
  options,
  value,
  onChange,
  className,
}: {
  label: string;
  options: readonly { value: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
  className?: string;
}) {
  const name = useId();
  const index = options.findIndex((o) => o.value === value);

  return (
    <fieldset className={cn("min-w-0", className)}>
      <legend className="sr-only">{label}</legend>
      <div
        className="relative grid h-8 rounded-full bg-line/60 p-0.5 select-none"
        style={{ gridTemplateColumns: `repeat(${options.length}, 1fr)` }}
      >
        <span
          aria-hidden
          className="absolute inset-y-0.5 left-0.5 rounded-full bg-surface shadow-hairline transition-transform duration-200 ease-[cubic-bezier(0.23,1,0.32,1)]"
          style={{
            width: `calc((100% - 4px) / ${options.length})`,
            transform: `translateX(${index * 100}%)`,
          }}
        />
        {options.map((option) => (
          <label
            key={option.value}
            className={cn(
              "relative z-10 flex cursor-pointer items-center justify-center rounded-full px-3 text-[13px] font-medium whitespace-nowrap transition-colors duration-150 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-dex-accent",
              option.value === value ? "text-ink" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {/* The input covers its segment, so a click on the segment hits the input. */}
            <input
              type="radio"
              name={name}
              className="absolute inset-0 cursor-pointer appearance-none rounded-full opacity-0"
              checked={option.value === value}
              onChange={() => onChange(option.value)}
            />
            {option.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** A saved credential: the value stays hidden, and a button opens a field to replace it. */
export function SavedSecret({
  label,
  text,
  hint,
  action,
  onReplace,
}: {
  label: string;
  text: string;
  hint: string;
  action: string;
  onReplace: () => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[12.5px] font-medium text-ink">{label}</span>
      <div className="flex items-center justify-between gap-3 rounded-control bg-inset px-3 py-2 shadow-hairline">
        <span className="text-[13px] text-ink-2">{text}</span>
        <Button type="button" size="xs" variant="secondary" onClick={onReplace}>
          {action}
        </Button>
      </div>
      <p className="text-[12px] text-ink-3">{hint}</p>
    </div>
  );
}
