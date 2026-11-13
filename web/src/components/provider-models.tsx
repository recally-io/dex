import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { ProviderModel, ProviderModelsInput } from "../api/generated/types.gen";
import { listProviderModels } from "../api/generated/sdk.gen";
import { errorText } from "../lib/queries";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";
import { Input } from "./form";

/**
 * Lists the models of a custom endpoint while the user types, and lets the user enable all of them
 * or some of them. `input` is null until the endpoint fields are valid. The caller keeps `input`
 * stable between renders, because a new object starts a new listing.
 */
export function ProviderModels({
  input,
  all,
  onAllChange,
  selected,
  onSelectedChange,
  error,
}: {
  input: ProviderModelsInput | null;
  all: boolean;
  onAllChange: (all: boolean) => void;
  selected: string[];
  onSelectedChange: (selected: string[]) => void;
  error?: string;
}) {
  const [settled, setSettled] = useState(input);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(input), 500);

    return () => clearTimeout(timer);
  }, [input]);

  const listing = useQuery({
    queryKey: ["provider-models", settled],
    queryFn: async ({ signal }) => {
      // SAFETY: enabled is false while settled is null.
      const { data } = await listProviderModels({ body: settled!, signal, throwOnError: true });

      return data.models;
    },
    enabled: !!settled && settled === input,
    retry: false,
    staleTime: 0,
    // The key holds the API key, so the cache must not keep it.
    gcTime: 0,
  });

  const titleId = useId();
  const [filter, setFilter] = useState("");
  const [manual, setManual] = useState("");

  const listed = listing.data ?? [];

  // Enabled IDs that the endpoint does not list stay visible, so a save never drops them silently.
  const rows: ProviderModel[] = [
    ...listed,
    ...selected.flatMap((id) => (listed.some((m) => m.id === id) ? [] : [{ id, name: id }])),
  ];

  const needle = filter.trim().toLowerCase();

  const shown = needle
    ? rows.filter((m) => m.id.toLowerCase().includes(needle) || m.name.toLowerCase().includes(needle))
    : rows;

  const pending = !!input && (settled !== input || listing.isFetching);

  function toggle(id: string) {
    if (all) {
      onAllChange(false);
      onSelectedChange(rows.flatMap((m) => (m.id === id ? [] : [m.id])));
    } else
      onSelectedChange(selected.includes(id) ? selected.filter((value) => value !== id) : [...selected, id]);
  }

  function add() {
    const id = manual.trim();

    if (!id) return;
    onAllChange(false);

    if (!selected.includes(id)) onSelectedChange([...selected, id]);
    setManual("");
  }

  return (
    <div role="group" aria-labelledby={titleId} className="flex min-w-0 flex-col gap-2">
      <div className="flex items-center justify-between gap-3">
        <span id={titleId} className="text-[12.5px] font-medium text-foreground">
          Models
        </span>
        <Button
          type="button"
          size="xs"
          variant="secondary"
          disabled={!input || pending}
          onClick={() => void listing.refetch()}
        >
          {pending ? "Listing…" : "List again"}
        </Button>
      </div>

      <p aria-live="polite" className="text-[12px] text-muted-foreground">
        {!input ? (
          "Enter the base URL. dex then lists the models of the endpoint."
        ) : pending ? (
          "Listing the models of the endpoint…"
        ) : listing.isError ? (
          <span className="text-red">
            Could not list models: {errorText(listing.error)} Check the URL and the key, or add model IDs
            below.
          </span>
        ) : (
          `The endpoint lists ${listed.length} models.`
        )}
      </p>

      <label className="flex items-center justify-between gap-3">
        <span className="flex flex-col">
          <span className="text-[13px] text-foreground">Enable all models</span>
          <span className="text-[12px] text-muted-foreground">
            Includes the models that the endpoint adds later.
          </span>
        </span>
        <Switch checked={all} onCheckedChange={onAllChange} aria-label="Enable all models" />
      </label>

      {rows.length > 8 ? (
        <Input
          aria-label="Filter models"
          placeholder="Filter models"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      ) : null}

      {rows.length ? (
        <ul className="max-h-56 overflow-y-auto rounded-md bg-card py-1 shadow-card">
          {shown.map((m) => (
            <li key={m.id}>
              <label className="flex cursor-pointer items-center gap-2.5 px-3 py-1.5 text-[13px] hover:bg-muted">
                <input
                  type="checkbox"
                  className="accent-primary"
                  checked={all || selected.includes(m.id)}
                  onChange={() => toggle(m.id)}
                />
                <span className="min-w-0 truncate font-mono text-[12px]">{m.id}</span>
                {m.name !== m.id ? (
                  <span className="min-w-0 truncate text-[12px] text-muted-foreground">{m.name}</span>
                ) : null}
              </label>
            </li>
          ))}
          {!shown.length ? (
            <li className="px-3 py-1.5 text-[12px] text-muted-foreground">No model matches the filter.</li>
          ) : null}
        </ul>
      ) : null}

      <div className="flex gap-2">
        <Input
          aria-label="Model ID to add"
          className="font-mono"
          placeholder="Add a model ID"
          value={manual}
          onChange={(e) => setManual(e.target.value)}
          onKeyDown={(e) => {
            // Enter adds the ID; it must not submit the provider form.
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button type="button" variant="secondary" disabled={!manual.trim()} onClick={add}>
          Add
        </Button>
      </div>

      {error ? <p className="text-[12px] text-red">{error}</p> : null}
    </div>
  );
}
