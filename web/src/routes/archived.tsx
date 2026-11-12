import { useMutation, useQuery } from "@tanstack/react-query";
import { listArchivedAgentsOptions, restoreAgentMutation } from "../api/generated/@tanstack/react-query.gen";
import { queryClient, refreshWork } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { Failure, Loading } from "../components/feedback";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";
import { Button } from "../components/ui/button";

export function ArchivedPage() {
  const archived = useQuery(listArchivedAgentsOptions());

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Archived bots"
      toolbar={<GlobalSettingsSwitcher page="/archived" />}
      description="An archived bot is hidden. Its channels are stopped and its routines are paused. Its chats, memory and settings stay. Restore a bot to use it again."
    >
      {archived.data ? (
        <Section n={1} title="Bots" caption={`${archived.data.agents.length} archived`}>
          {archived.data.agents.length ? (
            <Panel>
              <ul className="divide-y divide-border">
                {archived.data.agents.map((agent) => (
                  <ArchivedRow key={agent.id} id={agent.id!} name={agent.name} at={agent.archivedAt} />
                ))}
              </ul>
            </Panel>
          ) : (
            <Empty title="No archived bots">
              When the owner approves a bot_delete question, the bot is listed here.
            </Empty>
          )}
        </Section>
      ) : archived.error ? (
        <div className="px-5 py-8 sm:px-8">
          <Failure
            title="Could not load archived bots"
            error={archived.error}
            retry={() => void archived.refetch()}
          />
        </div>
      ) : (
        <Loading />
      )}
    </FramedPage>
  );
}

function ArchivedRow({ id, name, at }: { id: string; name: string; at?: number }) {
  const { notify } = useApp();

  const restore = useMutation({
    ...restoreAgentMutation(),
    onSuccess: async () => {
      await Promise.all([
        refreshWork(),
        queryClient.invalidateQueries({ queryKey: [{ _id: "listArchivedAgents" }] }),
      ]);

      notify(`${name} is restored.`);
    },
  });

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <p className="truncate text-[14px] font-medium">{name}</p>
        {at ? (
          <p className="mt-0.5 text-[12.5px] text-muted-foreground">
            Archived {new Date(at).toLocaleString()}
          </p>
        ) : null}
        {restore.isError ? <Failure title="Could not restore" error={restore.error} /> : null}
      </div>
      <Button
        size="sm"
        variant="secondary"
        disabled={restore.isPending}
        onClick={() => restore.mutate({ path: { agentId: id } })}
      >
        {restore.isPending ? "Restoring…" : "Restore"}
      </Button>
    </li>
  );
}
