import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";

type Notice = { id: number; text: string };

type AppState = {
  // The unsaved instructions. It lives above the routes, so it survives navigation.
  draft: string | null;
  setDraft: (draft: string | null) => void;
  // Clears the draft after a save of `saved`, unless the draft changed since. Returns true when
  // the draft is kept. It reads the current draft, so a callback from an old page is safe.
  settleDraft: (saved: string) => boolean;
  notify: (text: string) => void;
};

const AppContext = createContext<AppState | null>(null);

export function useApp() {
  const state = useContext(AppContext);

  if (!state) throw new Error("useApp needs an AppProvider.");

  return state;
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [draft, setDraftState] = useState<string | null>(null);
  const current = useRef<string | null>(null);
  const [notices, setNotices] = useState<Notice[]>([]);
  const next = useRef(0);

  const setDraft = useCallback((value: string | null) => {
    current.current = value;
    setDraftState(value);
  }, []);

  const settleDraft = useCallback(
    (saved: string) => {
      const kept = current.current !== null && current.current !== saved;

      if (!kept) setDraft(null);

      return kept;
    },
    [setDraft],
  );

  const dismiss = useCallback((id: number) => setNotices((all) => all.filter((n) => n.id !== id)), []);

  const notify = useCallback(
    (text: string) => {
      const id = ++next.current;
      setNotices((all) => [...all.slice(-2), { id, text }]);
      setTimeout(() => dismiss(id), 6000);
    },
    [dismiss],
  );

  const state = useMemo(
    () => ({ draft, setDraft, settleDraft, notify }),
    [draft, setDraft, settleDraft, notify],
  );

  return (
    <AppContext.Provider value={state}>
      {children}
      {/* The live region stays mounted, so screen readers announce each new notice. */}
      <div
        role="status"
        aria-live="polite"
        className="pointer-events-none fixed inset-x-0 bottom-4 z-[60] flex flex-col items-center gap-2 px-4"
      >
        {notices.map((notice) => (
          <div
            key={notice.id}
            className="pointer-events-auto flex max-w-[440px] animate-[fade-up_300ms_var(--ease-out-strong)_both] items-center gap-2 rounded-full bg-surface py-1.5 pr-1.5 pl-4 text-[12.5px] font-medium text-ink shadow-overlay"
          >
            <span>{notice.text}</span>
            <button
              type="button"
              aria-label="Dismiss notification"
              onClick={() => dismiss(notice.id)}
              className="flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover hover:text-ink"
            >
              <IconCrossSmall size={14} />
            </button>
          </div>
        ))}
      </div>
    </AppContext.Provider>
  );
}
