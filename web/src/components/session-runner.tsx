import { useMutation, useMutationState, useQuery } from "@tanstack/react-query";
import {
  listAgentRunnersOptions,
  setSessionRunnerMutation,
} from "../api/generated/@tanstack/react-query.gen";
import type { Session } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { errorText, refreshSessions } from "../lib/queries";
import { Select } from "./form";

const saveKey = ["setSessionRunner"];

// The first message must not run before the selection is saved: it would run in the default
// environment, and the selection would then fail with 409. This returns why sending waits, from
// the latest selection of the session.
export function useRunnerSendBlock(sessionId: number | undefined): string | undefined {
  const states = useMutationState({
    filters: { mutationKey: saveKey },
    select: (m) => {
      // SAFETY: only SessionRunner uses saveKey, and it passes the setSessionRunner options.
      const variables = m.state.variables as { path: { sessionId: number } } | undefined;

      return { sessionId: variables?.path.sessionId, status: m.state.status };
    },
  });

  const latest = states.filter((s) => s.sessionId === sessionId).at(-1);

  if (latest?.status === "pending") return "Saving the Runner…";

  if (latest?.status === "error") return "The Runner was not saved. Choose it again before you send.";

  return undefined;
}

// A session picks its environment before the first message. After that the name is read-only.
export function SessionRunner({ session, started }: { session: Session; started: boolean }) {
  const agentId = useAgentId();
  const runners = useQuery(listAgentRunnersOptions({ path: { agentId } }));
  const list = runners.data?.runners ?? [];
  const own = list.find((env) => env.default);
  const selected = session.runnerId ?? "";

  const save = useMutation({
    ...setSessionRunnerMutation(),
    mutationKey: saveKey,
    onSuccess: () => refreshSessions(agentId),
  });

  if (started || session.readOnly) {
    const name = list.find((env) => env.id === selected)?.name;

    return selected ? (
      <span
        className="min-w-0 max-w-32 shrink truncate px-1.5 text-[12.5px] text-muted-foreground sm:max-w-40"
        title={`Runner: ${name ?? selected}`}
      >
        {name ?? selected}
      </span>
    ) : null;
  }

  if (!runners.data || (list.length < 2 && own?.id === "local" && !selected)) return null;

  return (
    <>
      <Select
        aria-label="Runner"
        className="h-8 w-auto max-w-40 min-w-0 shrink bg-transparent px-1.5 text-[12.5px] shadow-none"
        value={selected}
        disabled={save.isPending}
        onChange={(event) =>
          save.mutate({ path: { agentId, sessionId: session.id }, body: { runnerId: event.target.value } })
        }
      >
        <option value="">Follow bot ({own?.name ?? "default"})</option>
        {selected && !list.some((env) => env.id === selected) ? (
          <option value={selected} disabled>
            {selected} (removed)
          </option>
        ) : null}
        {list.map((env) => (
          <option key={env.id} value={env.id} disabled={env.status !== "available"}>
            {env.name}
            {env.status === "available" ? "" : ` (${env.status})`}
          </option>
        ))}
      </Select>
      {save.isError ? (
        <span role="alert" className="truncate text-[12px] text-red">
          {errorText(save.error)}
        </span>
      ) : null}
    </>
  );
}
