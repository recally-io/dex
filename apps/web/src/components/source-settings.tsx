import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { listSourcesOptions } from "../api/generated/@tanstack/react-query.gen";
import { setSourceEnabled } from "../api/generated/sdk.gen";
import type { SourceSetting } from "../api/generated/types.gen";
import { Failure, Loading } from "./feedback";
import { Note, Panel, Section } from "./page";
import { Switch } from "./ui/switch";

const sourceNames = new Map([
  ["github", "GitHub"],
  ["webhook", "Webhook"],
  ["email", "Email"],
]);

// Dex reads the choices at start, so a change waits for a restart.
export function SourceSettings({
  caption = "Optional sources that run inside dex. Changes apply when dex starts again.",
}: {
  caption?: string;
}) {
  const plugins = useQuery(listSourcesOptions());
  const client = useQueryClient();

  const save = useMutation({
    mutationFn: (plugin: SourceSetting) =>
      setSourceEnabled({
        path: { name: plugin.name },
        body: { enabled: !plugin.enabled },
        throwOnError: true,
      }),
    onSuccess: () => client.invalidateQueries({ queryKey: listSourcesOptions().queryKey }),
  });

  if (plugins.isPending) return <Loading />;

  if (plugins.isError) return <Failure title="Could not load sources" error={plugins.error} />;

  // A plugin whose migrations failed does not run, and a restart alone does not fix it.
  const pending = plugins.data.sources.some((plugin) => !plugin.error && plugin.enabled !== plugin.running);

  return (
    <Section title="Sources" caption={caption}>
      <Panel>
        {plugins.data.sources.map((plugin) => (
          <div
            key={plugin.name}
            className="flex items-center gap-3 border-b border-border px-4 py-3.5 last:border-b-0"
          >
            <div className="min-w-0 flex-1">
              <p className="text-[14px] font-medium text-foreground">
                {sourceNames.get(plugin.name) ?? plugin.name}
              </p>
              {plugin.error ? (
                <p className="mt-0.5 break-words text-[12.5px] text-destructive">
                  Failed to start: {plugin.error}
                </p>
              ) : (
                <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                  {plugin.running ? "Running" : "Not running"}
                </p>
              )}
            </div>
            <Switch
              checked={plugin.enabled}
              disabled={save.isPending}
              aria-label={`Enable ${sourceNames.get(plugin.name) ?? plugin.name}`}
              onCheckedChange={() => save.mutate(plugin)}
            />
          </div>
        ))}
      </Panel>
      {pending ? <Note tone="warn">Restart Dex to apply source changes.</Note> : null}
      {save.error ? <Failure title="Could not change source" error={save.error} /> : null}
    </Section>
  );
}
