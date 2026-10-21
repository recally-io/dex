/* oxlint-disable anti-slop/require-readable-spacing */
import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useSyncExternalStore } from "react";

// This browser preference is independent of dex settings. `system` follows the OS until a choice is made.
const key = "dex-theme";
const listeners = new Set<() => void>();
const system = () => matchMedia("(prefers-color-scheme: dark)");
type ThemeChoice = "light" | "dark" | "system";

function choice(): ThemeChoice {
  const saved = localStorage.getItem(key);
  return saved === "light" || saved === "dark" || saved === "system" ? saved : "system";
}

function isDarkFor(next: ThemeChoice) {
  return next === "dark" || (next === "system" && system().matches);
}

// main.tsx calls this before React renders, avoiding a light flash for dark/system users.
export function applySavedTheme() {
  document.documentElement.classList.toggle("dark", isDarkFor(choice()));
}

function isDark() {
  return document.documentElement.classList.contains("dark");
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function apply(dark: boolean) {
  const root = document.documentElement;
  root.classList.add("theme-switching");
  root.classList.toggle("dark", dark);
  requestAnimationFrame(() => requestAnimationFrame(() => root.classList.remove("theme-switching")));
  listeners.forEach((listener) => listener());
}

export function useSystemTheme() {
  useEffect(() => {
    const media = system();
    const follow = () => {
      if (choice() === "system") apply(media.matches);
    };
    media.addEventListener("change", follow);
    return () => media.removeEventListener("change", follow);
  }, []);
}

/** Three choices keep the stored browser preference explicit while preserving the existing compact control. */
export function ThemeToggle() {
  const dark = useSyncExternalStore(subscribe, isDark, () => false);
  const selected = choice();
  const options: Array<{ value: ThemeChoice; label: string; icon: typeof Sun }> = [
    { value: "light", label: "Light mode", icon: Sun },
    { value: "system", label: "System mode", icon: Monitor },
    { value: "dark", label: "Dark mode", icon: Moon },
  ];

  function pick(next: ThemeChoice) {
    localStorage.setItem(key, next);
    apply(isDarkFor(next));
  }

  return (
    <div
      role="group"
      aria-label="Theme"
      className="relative inline-grid h-9 shrink-0 grid-cols-3 items-center rounded-none bg-field p-0.5"
    >
      {options.map(({ value, label, icon: Icon }) => (
        <button
          key={value}
          type="button"
          aria-label={label}
          aria-pressed={selected === value || (value === "system" && selected === "system" && !dark)}
          onClick={() => pick(value)}
          className={`relative z-10 flex size-8 items-center justify-center rounded-none transition-colors duration-150 ${selected === value ? "text-ink" : "text-ink-3 hover:text-ink-2"}`}
        >
          <Icon size={13} aria-hidden />
        </button>
      ))}
    </div>
  );
}
