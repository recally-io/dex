import { X } from "lucide-react";
import type { DraftFile, Uploads } from "./attachments";
import { useAgentId } from "./agent-scope";
import {
  createContext,
  useEffect,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
  type ReactNode,
} from "react";

type Notice = { id: number; text: string };

type ComposerDraft = { text: string; files: DraftFile[] };

const emptyComposer: ComposerDraft = { text: "", files: [] };

type AppState = {
  composers: Record<string, ComposerDraft>;
  setComposer: (agentId: string, change: (draft: ComposerDraft) => ComposerDraft) => void;
  composerUploads: { current: Uploads };

  // The unsaved instructions. It lives above the routes, so it survives navigation.
  drafts: Record<string, string | null>;
  setDraft: (agentId: string, draft: string | null) => void;
  // Clears the draft after a save of `saved`, unless the draft changed since. Returns true when
  // the draft is kept. It reads the current draft, so a callback from an old page is safe.
  settleDraft: (agentId: string, saved: string) => boolean;
  notify: (text: string) => void;
};

const AppContext = createContext<AppState | null>(null);

export function useApp() {
  const state = useContext(AppContext);

  if (!state) throw new Error("useApp needs an AppProvider.");

  const agentId = useAgentId();

  const setComposerText: Dispatch<SetStateAction<string>> = (value) =>
    state.setComposer(agentId, (draft) => ({
      ...draft,
      // React state setters accept a value or an updater.
      // eslint-disable-next-line anti-slop/no-runtime-typeof
      text: typeof value === "function" ? value(draft.text) : value,
    }));

  const setComposerFiles: Dispatch<SetStateAction<DraftFile[]>> = (value) =>
    state.setComposer(agentId, (draft) => ({
      ...draft,
      // React state setters accept a value or an updater.
      // eslint-disable-next-line anti-slop/no-runtime-typeof
      files: typeof value === "function" ? value(draft.files) : value,
    }));

  return {
    ...state,
    composer: state.composers[agentId] ?? emptyComposer,
    setComposerText,
    setComposerFiles,
    draft: state.drafts[agentId] ?? null,
    setDraft: (value: string | null) => state.setDraft(agentId, value),
    settleDraft: (saved: string) => state.settleDraft(agentId, saved),
  };
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [composers, setComposers] = useState<Record<string, ComposerDraft>>({});
  const composerUploads = useRef<Uploads>(new Map());

  const setComposer = useCallback((agentId: string, change: (draft: ComposerDraft) => ComposerDraft) => {
    setComposers((previous) => ({ ...previous, [agentId]: change(previous[agentId] ?? emptyComposer) }));
  }, []);

  useEffect(() => {
    const uploads = composerUploads.current;

    return () => {
      for (const upload of uploads.values()) {
        upload.abort.abort();

        if (upload.preview) URL.revokeObjectURL(upload.preview);
      }
    };
  }, []);

  const [drafts, setDraftState] = useState<Record<string, string | null>>({});
  const current = useRef<Record<string, string | null>>({});
  const [notices, setNotices] = useState<Notice[]>([]);
  const next = useRef(0);

  const setDraft = useCallback((agentId: string, value: string | null) => {
    current.current = { ...current.current, [agentId]: value };
    setDraftState(current.current);
  }, []);

  const settleDraft = useCallback(
    (agentId: string, saved: string) => {
      const kept = current.current[agentId] != null && current.current[agentId] !== saved;

      if (!kept) setDraft(agentId, null);

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
    () => ({ composers, setComposer, composerUploads, drafts, setDraft, settleDraft, notify }),
    [composers, setComposer, drafts, setDraft, settleDraft, notify],
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
            className="pointer-events-auto flex max-w-[440px] animate-[fade-up_300ms_var(--ease-out-strong)_both] items-center gap-2 rounded-full bg-card py-1.5 pr-1.5 pl-4 text-[12.5px] font-medium text-foreground shadow-raised"
          >
            <span>{notice.text}</span>
            <button
              type="button"
              aria-label="Dismiss notification"
              onClick={() => dismiss(notice.id)}
              className="flex size-6 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <X size={14} />
            </button>
          </div>
        ))}
      </div>
    </AppContext.Provider>
  );
}
