// Copied from slev12397/beautiful-ui components/atoms/Switch.tsx (MIT).
// dex change: type="button" and a disabled state for a pending save.
"use client";

export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative h-6 w-10 shrink-0 rounded-full transition-colors duration-200
        ${checked ? "bg-ink" : "bg-line-strong"} disabled:opacity-50`}
    >
      <span
        className="absolute top-0.5 left-0.5 size-5 rounded-full bg-white
          shadow-[0_1px_2px_rgba(0,0,0,0.2)] transition-transform duration-200"
        style={{
          transform: checked ? "translateX(16px)" : "translateX(0)",
          transitionTimingFunction: "cubic-bezier(0.23, 1, 0.32, 1)",
        }}
      />
    </button>
  );
}
