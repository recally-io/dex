import { useMutation } from "@tanstack/react-query";
import { acknowledgeTask, dismissInboxItem, restoreInboxItem, restoreTask } from "../api/generated/sdk.gen";
import { refreshWork } from "./queries";

export type HandledChange =
  | { kind: "task"; agentId: string; id: string; undo: boolean }
  | { kind: "run"; id: string; undo: boolean };

function apply(change: HandledChange) {
  if (change.kind === "run") {
    const path = { runId: change.id };

    return change.undo
      ? restoreInboxItem({ path, throwOnError: true })
      : dismissInboxItem({ path, throwOnError: true });
  }

  const path = { agentId: change.agentId, taskId: change.id };

  return change.undo
    ? restoreTask({ path, throwOnError: true })
    : acknowledgeTask({ path, throwOnError: true });
}

// Mark handled and its Undo for the two item kinds that leave Needs you this way.
export function useHandled() {
  return useMutation({
    mutationFn: async (changes: HandledChange | HandledChange[]) => {
      await Promise.all([changes].flat().map(apply));
      await refreshWork();
    },
  });
}
