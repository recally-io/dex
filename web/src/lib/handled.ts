import { useMutation } from "@tanstack/react-query";
import { dismissInboxItem, restoreInboxItem } from "../api/generated/sdk.gen";
import { refreshWork } from "./queries";

export type HandledChange = { id: string; undo: boolean };

// Mark handled and its Undo for a failed routine run, the one item that leaves Needs you this way.
export function useHandled() {
  return useMutation({
    mutationFn: async ({ id, undo }: HandledChange) => {
      const path = { runId: id };

      await (undo
        ? restoreInboxItem({ path, throwOnError: true })
        : dismissInboxItem({ path, throwOnError: true }));
      await refreshWork();
    },
  });
}
