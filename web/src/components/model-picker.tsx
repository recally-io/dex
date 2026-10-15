import { useState, type ReactElement } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getSettingsOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Model, Settings } from "../api/generated/types.gen";
import { saveModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ValuePill } from "./atoms/ValuePill";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { Command, CommandGroup, CommandInput, CommandItem, CommandList } from "./ui/command";
import { Spinner } from "./ui/spinner";
import { Failure } from "./feedback";

type Group = { id: string; name: string; error?: string; models: Model[] };

const byName = (a: { name: string; id: string }, b: { name: string; id: string }) =>
  a.name.localeCompare(b.name, undefined, { sensitivity: "base" }) || a.id.localeCompare(b.id);

// Every saved provider gets a group, also one without models. Models of a provider that is not
// saved (the faux provider in tests) get a group too. Groups and models sort by name, then ID.
function groups(settings: Settings, models: Model[]): Group[] {
  const byProvider = Object.groupBy(models, (m) => m.provider);

  const saved = settings.providers.map((p) => ({
    id: p.id,
    name: p.name,
    error: p.error,
    models: byProvider[p.id] ?? [],
  }));

  const other = Object.entries(byProvider)
    .filter(([id]) => !settings.providers.some((p) => p.id === id))
    .map(([id, list]) => ({ id, name: id, models: list ?? [] }));

  return [...saved, ...other].map((g) => ({ ...g, models: g.models.toSorted(byName) })).toSorted(byName);
}

// A provider that matches by name or ID keeps all its models. Otherwise only the models that
// match by name or ID stay, and a group without them goes.
function search(list: Group[], query: string): Group[] {
  const q = query.trim().toLowerCase();

  if (!q) return list;

  const has = (x: { name: string; id: string }) =>
    x.name.toLowerCase().includes(q) || x.id.toLowerCase().includes(q);

  return list.flatMap((g) => {
    const models = has(g) ? g.models : g.models.filter(has);

    return models.length ? [{ ...g, models }] : [];
  });
}

// An anchored, non-modal model list. A click on a model saves it at once; the list stays open
// with the error when the save fails.
export function ModelPicker({ trigger }: { trigger: ReactElement }) {
  const { notify } = useApp();
  const [open, setOpen] = useState(false);

  const save = useMutation({
    mutationFn: (model: Model) => saveModel(model.provider, model.id),
    onSuccess: (_, model) => {
      setOpen(false);
      notify(`The agent model is now ${model.name}. It applies from the next message.`);
    },
  });

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        // The save result shows in the list, so it stays open until the save ends.
        if (save.isPending) return;

        if (next) save.reset();
        setOpen(next);
      }}
    >
      <PopoverTrigger render={trigger} />
      <PopoverContent
        align="start"
        aria-label="Agent model"
        // The search takes focus when it mounts, also after the models load.
        initialFocus={false}
        className="max-h-(--available-height) w-96 max-w-[calc(100vw-1rem)] gap-0 p-0"
      >
        <ModelList
          pending={save.isPending ? save.variables : undefined}
          error={save.isError ? save.error : undefined}
          onPick={(model, current) => {
            if (current) setOpen(false);
            else save.mutate(model);
          }}
          onLeave={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  );
}

function ModelList({
  pending,
  error,
  onPick,
  onLeave,
}: {
  pending?: Model;
  error?: unknown;
  onPick: (model: Model, current: boolean) => void;
  onLeave: () => void;
}) {
  const settings = useQuery(getSettingsOptions());
  const models = useQuery({ ...listModelsOptions(), refetchOnMount: "always" });
  const { draft } = useApp();
  const [query, setQuery] = useState("");
  const agent = settings.data?.agent;
  const current = agent?.modelId ? `${agent.provider}/${agent.modelId}` : "";
  const all = models.data?.models ?? [];
  const dirty = draft !== null && draft !== agent?.instructions;

  if (settings.isPending || models.isPending)
    return (
      <div role="status" className="flex items-center gap-2 p-3 text-sm text-ink-2">
        <Spinner aria-hidden /> Loading models…
      </div>
    );

  if (settings.isError || models.isError)
    return (
      <div className="p-2">
        <Failure
          title="Could not load the models"
          error={settings.error ?? models.error}
          retry={() => {
            void settings.refetch();
            void models.refetch();
          }}
        />
      </div>
    );

  if (!settings.data.providers.length && !all.length)
    return (
      <p className="p-3 text-sm text-ink-2">
        No providers yet.{" "}
        <Link className="text-brand underline" to="/providers" onClick={onLeave}>
          Add a provider
        </Link>{" "}
        to see its models.
      </p>
    );

  const shown = search(groups(settings.data, all), query);

  return (
    // The list is already filtered and sorted; cmdk only handles the keyboard.
    <Command shouldFilter={false} className="h-auto min-h-0 rounded-lg! p-0">
      <CommandInput
        autoFocus
        aria-label="Search models"
        placeholder="Search providers and models"
        value={query}
        onValueChange={setQuery}
      />
      <CommandList className="max-h-88 min-h-0 flex-1">
        {!shown.length ? (
          <p role="status" className="px-3 py-6 text-center text-sm text-ink-2">
            No models match “{query.trim()}”.
          </p>
        ) : (
          shown.map((group) => (
            <CommandGroup
              key={group.id}
              heading={
                <span className="flex flex-wrap items-center gap-2">
                  {group.name}
                  <span className="font-mono font-normal text-ink-3">{group.id}</span>
                  {group.error ? <ValuePill tone="red">Model listing failed</ValuePill> : null}
                </span>
              }
            >
              {!group.models.length ? (
                <p className="px-2 py-1 text-xs text-ink-3">
                  No models.{" "}
                  {group.error
                    ? "Fix the listing error on Providers, or enter model IDs there."
                    : "Check the provider on Providers."}
                </p>
              ) : (
                group.models.map((m) => {
                  const value = `${m.provider}/${m.id}`;
                  const checked = value === current;

                  return (
                    <CommandItem
                      key={value}
                      value={value}
                      data-checked={checked}
                      disabled={!!pending}
                      onSelect={() => onPick(m, checked)}
                      className="min-w-0"
                    >
                      <span className="min-w-0 truncate text-ink" title={m.name}>
                        {m.name}
                      </span>
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-3" title={m.id}>
                        {m.id}
                      </span>
                      {checked ? <span className="sr-only">(current)</span> : null}
                      {pending && `${pending.provider}/${pending.id}` === value ? (
                        <Spinner aria-label="Saving" className="ml-auto size-3.5" />
                      ) : null}
                    </CommandItem>
                  );
                })
              )}
            </CommandGroup>
          ))
        )}
      </CommandList>
      {dirty ? (
        <p className="border-t border-line px-3 py-2 text-xs text-ink-2">
          Your unsaved instructions stay as a draft. A model change does not save or discard them.
        </p>
      ) : null}
      {error ? (
        <div className="border-t border-line p-2">
          <Failure title="The model did not change" error={error} />
        </div>
      ) : null}
    </Command>
  );
}
