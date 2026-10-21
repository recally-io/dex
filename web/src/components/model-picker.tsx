import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState, type ReactElement } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Model } from "../api/generated/types.gen";
import { saveModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { Command, CommandInput, CommandList, CommandGroup, CommandItem } from "./ui/command";
import { Badge } from "./ui/badge";
import { Failure, Spinner } from "./feedback";

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

/** The model of one chat. null follows the bot's model; save reports its own success. */
export type ModelOverride = {
  value: { provider: string; modelId: string } | null;
  defaultName: string;
  allowDefault?: boolean;
  save: (model: Model | null) => Promise<void>;
};

// An anchored, non-modal model list. A click on a model saves it at once; the list stays open
// with the error when the save fails. Without override, the list sets the bot's model.
export function ModelPicker({ trigger, override }: { trigger: ReactElement; override?: ModelOverride }) {
  const agentId = useAgentId();
  const { notify } = useApp();
  const [open, setOpen] = useState(false);

  const save = useMutation({
    mutationFn: (model: Model | null) =>
      override
        ? override.save(model)
        : model
          ? saveModel(agentId, model.provider, model.id)
          : Promise.resolve(),
    onSuccess: (_, model) => {
      setOpen(false);

      if (!override && model) notify(`The bot model is now ${model.name}. It applies from the next message.`);
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
        sideOffset={6}
        aria-label={override ? "Chat model" : "Bot model"}
        className="z-[60] flex max-h-[min(420px,var(--available-height))] w-[352px] max-w-[calc(100vw-1rem)] flex-col overflow-hidden rounded-[12px] bg-card p-0 shadow-raised outline-none"
      >
        <ModelList
          override={override}
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
  override,
  pending,
  error,
  onPick,
  onLeave,
}: {
  override?: ModelOverride;
  // undefined: no save runs; null: the save of "Use default" runs.
  pending?: Model | null;
  error?: unknown;
  onPick: (model: Model | null, current: boolean) => void;
  onLeave: () => void;
}) {
  const agentId = useAgentId();
  const settings = useAgentSettings(agentId);
  const models = useQuery({ ...listModelsOptions(), refetchOnMount: "always" });
  const { draft } = useApp();
  const [query, setQuery] = useState("");
  const agent = settings.data?.agent;
  const chosen = override ? override.value : agent?.modelId ? agent : null;
  const current = chosen ? `${chosen.provider}/${chosen.modelId}` : "";
  const all = models.data?.models ?? [];
  const dirty = !override && draft !== null && draft !== agent?.instructions;
  const busy = pending !== undefined;

  if (settings.isPending || models.isPending)
    return (
      <div role="status" className="flex items-center gap-2 p-3 text-[12.5px] text-muted-foreground">
        <Spinner /> Loading models…
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
      <p className="p-3 text-[12.5px] text-muted-foreground">
        No providers yet.{" "}
        <Link className="font-medium text-primary underline" to="/providers" onClick={onLeave}>
          Add a provider
        </Link>{" "}
        to see its models.
      </p>
    );

  const shown = search(groups(settings.data, all), query);

  return (
    // The list is already filtered and sorted; cmdk only handles the keyboard.
    <Command shouldFilter={false} className="flex min-h-0 flex-1 flex-col">
      <CommandInput
        autoFocus
        aria-label="Search models"
        placeholder="Search providers and models"
        value={query}
        onValueChange={setQuery}
        className="text-[13px] text-foreground placeholder:text-muted-foreground"
      />
      {/* The popover limits the height; the stock max-h-72 would cut the list shorter. */}
      <CommandList className="max-h-none min-h-0 flex-1 overscroll-contain p-1">
        {override && override.allowDefault !== false && !query.trim() ? (
          <CommandItem
            value="default"
            data-checked={!current}
            disabled={busy}
            onSelect={() => onPick(null, !current)}
            className="mb-1 flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left transition-colors duration-100 data-[disabled=true]:cursor-wait data-[selected=true]:bg-accent"
          >
            <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-foreground">
              Use default ({override.defaultName})
            </span>
            <Mark checked={!current} saving={pending === null} />
          </CommandItem>
        ) : null}
        {!shown.length ? (
          <p role="status" className="px-3 py-6 text-center text-[12.5px] text-muted-foreground">
            No models match “{query.trim()}”.
          </p>
        ) : (
          shown.map((group) => (
            <CommandGroup
              key={group.id}
              className="pb-1"
              heading={
                <span className="flex flex-wrap items-center gap-1.5 px-2 pt-2 pb-1 text-[11.5px] font-medium text-muted-foreground">
                  <span className="text-muted-foreground">{group.name}</span>
                  <span className="font-mono font-normal">{group.id}</span>
                  {group.error ? <Badge variant="destructive">Model listing failed</Badge> : null}
                </span>
              }
            >
              {!group.models.length ? (
                <p className="px-2 pb-1 text-[12px] text-muted-foreground">
                  No models.{" "}
                  {group.error
                    ? "Fix the listing error on Providers, or enter model IDs there."
                    : "Check the provider on Providers."}
                </p>
              ) : (
                group.models.map((m) => {
                  const value = `${m.provider}/${m.id}`;
                  const checked = value === current;
                  const saving = !!pending && `${pending.provider}/${pending.id}` === value;

                  return (
                    <CommandItem
                      key={value}
                      value={value}
                      data-checked={checked}
                      disabled={busy}
                      onSelect={() => onPick(m, checked)}
                      className="flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left transition-colors duration-100 data-[disabled=true]:cursor-wait data-[selected=true]:bg-accent"
                    >
                      <span
                        className="min-w-0 truncate text-[12.5px] font-medium text-foreground"
                        title={m.name}
                      >
                        {m.name}
                      </span>
                      <span
                        className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground"
                        title={m.id}
                      >
                        {m.id}
                      </span>
                      <Mark checked={checked} saving={saving} />
                    </CommandItem>
                  );
                })
              )}
            </CommandGroup>
          ))
        )}
      </CommandList>
      {dirty ? (
        <p className="shrink-0 border-t border-border px-3 py-2 text-[11.5px] text-muted-foreground">
          Your unsaved instructions stay as a draft. A model change does not save or discard them.
        </p>
      ) : null}
      {error ? (
        <div className="shrink-0 border-t border-border p-2">
          <Failure title="The model did not change" error={error} />
        </div>
      ) : null}
    </Command>
  );
}

/** The stock item draws the check from data-checked; this adds the saving state and screen reader text. */
export function Mark({ checked, saving }: { checked: boolean; saving: boolean }) {
  return (
    <>
      {checked ? <span className="sr-only">(current)</span> : null}
      {saving ? (
        <>
          <Spinner />
          <span className="sr-only">Saving</span>
        </>
      ) : null}
    </>
  );
}
