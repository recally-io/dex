import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getSettingsOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Model, Settings } from "../api/generated/types.gen";
import { saveModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ValuePill } from "./atoms/ValuePill";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Button } from "./ui/button";
import { Failure, Loading } from "./feedback";
import { Note } from "./page";

type Group = { id: string; name: string; error?: string; models: Model[] };

// Every saved provider gets a group, also one without models. Models of a provider that is not
// saved (the faux provider in tests) get a group too.
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

  return [...saved, ...other];
}

export function ModelDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open ? <ModelChooser onClose={() => onOpenChange(false)} /> : null}
    </Dialog>
  );
}

function ModelChooser({ onClose }: { onClose: () => void }) {
  const settings = useQuery(getSettingsOptions());
  const models = useQuery({ ...listModelsOptions(), refetchOnMount: "always" });
  const { draft, notify } = useApp();
  const agent = settings.data?.agent;
  const current = agent?.modelId ? `${agent.provider}/${agent.modelId}` : "";
  const [pick, setPick] = useState(current);

  const save = useMutation({
    mutationFn: (model: Model) => saveModel(model.provider, model.id),
    onSuccess: (_, model) => {
      onClose();
      notify(`The agent model is now ${model.name}. It applies from the next message.`);
    },
  });

  const all = models.data?.models ?? [];
  const chosen = all.find((m) => `${m.provider}/${m.id}` === pick);
  const dirty = draft !== null && draft !== agent?.instructions;

  return (
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle className="pr-8">Agent model</DialogTitle>
        <DialogDescription>
          One model answers every chat, channel and automation. A change applies from the next message.
        </DialogDescription>
      </DialogHeader>
      {settings.isPending || models.isPending ? (
        <Loading rows={2} />
      ) : settings.isError || models.isError ? (
        <Failure
          error={settings.error ?? models.error}
          retry={() => {
            void settings.refetch();
            void models.refetch();
          }}
        />
      ) : !settings.data.providers.length && !all.length ? (
        <Note>
          No providers yet.{" "}
          <Link className="text-brand underline" to="/providers" onClick={onClose}>
            Add a provider
          </Link>{" "}
          to see its models.
        </Note>
      ) : (
        <div className="flex flex-col gap-4">
          {groups(settings.data, all).map((group) => (
            <fieldset key={group.id} className="flex min-w-0 flex-col gap-1">
              <legend className="mb-1 flex flex-wrap items-center gap-2 text-[13px] font-medium text-ink">
                {group.name}
                <span className="font-mono text-xs text-ink-3">{group.id}</span>
                {group.error ? <ValuePill tone="red">Model listing failed</ValuePill> : null}
              </legend>
              {!group.models.length ? (
                <p className="text-xs text-ink-3">
                  No models.{" "}
                  {group.error
                    ? "Fix the listing error on Providers, or enter model IDs there."
                    : "Check the provider on Providers."}
                </p>
              ) : (
                group.models.map((m) => {
                  const value = `${m.provider}/${m.id}`;

                  return (
                    <label
                      key={value}
                      className="flex min-w-0 cursor-pointer items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-hover has-[:checked]:bg-inset has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ink"
                    >
                      <input
                        type="radio"
                        name="agent-model"
                        value={value}
                        checked={pick === value}
                        onChange={() => setPick(value)}
                        className="size-4 shrink-0 accent-[var(--ink)]"
                      />
                      <span className="shrink-0 text-sm text-ink">{m.name}</span>
                      <span className="min-w-0 truncate font-mono text-xs text-ink-3">{m.id}</span>
                    </label>
                  );
                })
              )}
            </fieldset>
          ))}
        </div>
      )}
      {dirty ? (
        <Note>Your unsaved instructions stay as a draft. A model change does not save or discard them.</Note>
      ) : null}
      {save.isError ? <Failure title="The model did not change" error={save.error} /> : null}
      <DialogFooter>
        <Button variant="outline" onClick={onClose} disabled={save.isPending}>
          Cancel
        </Button>
        <Button
          disabled={!chosen || pick === current || save.isPending}
          onClick={() => {
            if (chosen) save.mutate(chosen);
          }}
        >
          {save.isPending ? "Saving…" : "Use this model"}
        </Button>
      </DialogFooter>
    </DialogContent>
  );
}
