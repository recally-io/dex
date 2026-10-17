import { useMutation } from "@tanstack/react-query";
import { saveAgent } from "./agent";
import { useApp } from "./app-state";

export const instructionsLimit = 32000;

/** The unsaved draft differs from what the agent runs with. */
export function instructionsDirty(draft: string | null, saved: string | undefined) {
  return draft !== null && draft !== saved;
}

/**
 * The instructions draft and its save. The draft lives in app state, so the editor keeps it when
 * the page changes or the side panel closes.
 */
export function useInstructions(saved: string) {
  const { draft, setDraft, settleDraft, notify } = useApp();

  const save = useMutation({
    mutationFn: (instructions: string) => saveAgent((agent) => ({ ...agent, instructions })),
    onSuccess: (_, value) => {
      // Edits typed during the save stay as a new draft, also when they were typed on a new visit
      // to this page.
      const kept = settleDraft(value);

      notify(
        kept
          ? "Instructions saved. Your later edits are still an unsaved draft."
          : "Instructions saved. They apply from the next message.",
      );
    },
  });

  return { text: draft ?? saved, dirty: instructionsDirty(draft, saved), setDraft, save };
}
