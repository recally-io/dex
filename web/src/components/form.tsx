import {
  useId,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { Button } from "./ui/button";
import { ToggleGroup, ToggleGroupItem } from "./ui/toggle-group";
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
  return (
    <fieldset className={cn("min-w-0", className)}>
      <legend className="sr-only">{label}</legend>
      <ToggleGroup
        value={[value]}
        // SAFETY: ToggleGroup values come from the readonly options list above.
        onValueChange={(next) => next[0] && onChange(next[0] as T)}
        className="h-8 rounded-full bg-line/60 p-0.5"
      >
        {options.map((option) => (
          <ToggleGroupItem
            key={option.value}
            value={option.value}
            aria-label={option.label}
            className="h-7 rounded-full px-3 text-[13px]"
          >
            {option.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
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
