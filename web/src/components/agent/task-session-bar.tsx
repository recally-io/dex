import { useMutation } from "@tanstack/react-query";
import { Square } from "lucide-react";
import type { TaskSummary } from "../../api/generated/types.gen";
import { cancelTaskMutation } from "../../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../../lib/agent-scope";
import { refreshChats } from "../../lib/queries";
import { taskTime } from "../../lib/tasks";
import { Failure } from "../feedback";
import { Button } from "../ui/button";

/** The status bar of a running delegated session: it has no composer, only Stop. */
export function TaskSessionBar({ task }: { task: TaskSummary }) {
  const agentId = useAgentId();

  const stop = useMutation({
    ...cancelTaskMutation(),
    onSuccess: () => refreshChats(agentId),
  });

  return (
    <div className="shrink-0 px-3 pb-3 sm:px-8 sm:pb-5">
      <div className="mx-auto flex w-full max-w-[720px] flex-col gap-2">
        {stop.isError ? <Failure title="Could not stop the task" error={stop.error} /> : null}
        <div className="flex flex-wrap items-center gap-3 rounded-md bg-muted px-3.5 py-2.5 text-[12.5px] shadow-card">
          <span className="flex items-center gap-2 font-medium text-foreground">
            <span aria-hidden className="size-1.5 rounded-full bg-primary" />
            {task.status === "running" ? "Working" : "Starting"}
            {task.startedAt ? (
              <span className="font-normal text-muted-foreground">since {taskTime(task.startedAt)}</span>
            ) : null}
          </span>
          <span className="min-w-0 flex-1 text-muted-foreground">You can chat here after the task ends.</span>
          <Button
            size="xs"
            variant="outline"
            className="text-destructive"
            disabled={stop.isPending}
            onClick={() => stop.mutate({ path: { agentId, taskId: task.id } })}
          >
            <Square aria-hidden="true" className="size-3 fill-current" />
            Stop
          </Button>
        </div>
      </div>
    </div>
  );
}
