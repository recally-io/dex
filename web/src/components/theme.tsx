import { useEffect, useSyncExternalStore } from "react";

// A UI preference of this browser; it is not a dex setting. Only "light"
// and "dark" are a pick. Any other value, also "system" from older builds, follows the system.
const key = "dex-theme";
const listeners = new Set<() => void>();
const system = () => matchMedia("(prefers-color-scheme: dark)");

function picked() {
  const saved = localStorage.getItem(key);

  return saved === "light" || saved === "dark";
}

// main.tsx calls this before the first render.
export function applySavedTheme() {
  document.documentElement.classList.toggle(
    "dark",
    picked() ? localStorage.getItem(key) === "dark" : system().matches,
  );
}

function isDark() {
  return document.documentElement.classList.contains("dark");
}

function subscribe(listener: () => void) {
  listeners.add(listener);

  return () => {
    listeners.delete(listener);
  };
}

function apply(dark: boolean) {
  // Freeze transitions while every token flips, as upstream ThemeToggle does.
  const root = document.documentElement;
  root.classList.add("theme-switching");
  root.classList.toggle("dark", dark);
  requestAnimationFrame(() => requestAnimationFrame(() => root.classList.remove("theme-switching")));
  listeners.forEach((listener) => listener());
}

// Follows the system theme until the user picks one. The shell mounts it once.
export function useSystemTheme() {
  useEffect(() => {
    const media = system();

    const follow = () => {
      if (!picked()) apply(media.matches);
    };

    media.addEventListener("change", follow);

    return () => media.removeEventListener("change", follow);
  }, []);
}

/** Sun/moon segmented pill. Markup from upstream components/site/ThemeToggle.tsx. */
export function ThemeToggle() {
  const dark = useSyncExternalStore(subscribe, isDark);

  function pick(next: boolean) {
    localStorage.setItem(key, next ? "dark" : "light");
    apply(next);
  }

  return (
    <div
      role="group"
      aria-label="Theme"
      className="relative inline-grid h-9 shrink-0 grid-cols-2 items-center rounded-full bg-field p-0.5"
    >
      <span
        aria-hidden
        className="absolute inset-y-0.5 left-0.5 w-8 rounded-full bg-surface shadow-btn transition-transform duration-200 ease-out-strong"
        style={{ transform: dark ? "translateX(32px)" : "translateX(0)" }}
      />
      <button
        type="button"
        aria-label="Light mode"
        aria-pressed={!dark}
        onClick={() => pick(false)}
        className={`relative z-10 flex size-8 items-center justify-center rounded-full transition-colors duration-150 ${dark ? "text-ink-3 hover:text-ink-2" : "text-ink"}`}
      >
        <svg
          width="13"
          height="13"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          aria-hidden
        >
          <circle cx="12" cy="12" r="4" fill="currentColor" stroke="none" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      </button>
      <button
        type="button"
        aria-label="Dark mode"
        aria-pressed={dark}
        onClick={() => pick(true)}
        className={`relative z-10 flex size-8 items-center justify-center rounded-full transition-colors duration-150 ${dark ? "text-ink" : "text-ink-3 hover:text-ink-2"}`}
      >
        <svg width="13" height="13" viewBox="0 0 24 24" fill="currentColor" aria-hidden>
          <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
        </svg>
      </button>
    </div>
  );
}
