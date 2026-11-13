import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { ProviderModel, ProviderModelsInput } from "../api/generated/types.gen";
import { listProviderModels } from "../api/generated/sdk.gen";
import { errorText } from "../lib/queries";
import { RefreshCw } from "lucide-react";
import { Button } from "./ui/button";
import { IconButton } from "./icon-button";
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
  const [text, setText] = useState("");

  const listed = listing.data ?? [];

  // Enabled IDs that the endpoint does not list stay visible, so a save never drops them silently.
  const rows: ProviderModel[] = [
    ...listed,
    ...selected.flatMap((id) => (listed.some((m) => m.id === id) ? [] : [{ id, name: id }])),
  ];

  const pending = !!input && (settled !== input || listing.isFetching);
  // A short list needs no filter. Without a list, the field is the only way to add a model.
  const showField = rows.length > 8 || (!!input && !pending && !listed.length);
  const needle = text.trim();

  const shown = needle
    ? rows.filter(
        (m) =>
          m.id.toLowerCase().includes(needle.toLowerCase()) ||
          m.name.toLowerCase().includes(needle.toLowerCase()),
      )
    : rows;

  const canAdd = !!needle && !rows.some((m) => m.id === needle);

  function toggle(id: string) {
    if (all) {
      onAllChange(false);
      onSelectedChange(rows.flatMap((m) => (m.id === id ? [] : [m.id])));
    } else
      onSelectedChange(selected.includes(id) ? selected.filter((value) => value !== id) : [...selected, id]);
  }

  function toggleAll() {
    onAllChange(!all);

    // Clearing "All" starts an empty choice, so the user picks only what they need.
    if (all) onSelectedChange([]);
  }

  function add() {
    if (!canAdd) return;

    onAllChange(false);
    onSelectedChange([...(all ? rows.map((m) => m.id) : selected), needle]);
    setText("");
  }

  return (
    <div role="group" aria-labelledby={titleId} className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-center justify-between gap-3">
        <span id={titleId} className="text-[12.5px] font-medium text-foreground">
          Models
        </span>
        <span className="flex items-center gap-1 text-[12px] text-muted-foreground">
          <span aria-live="polite">
            {!input
              ? "Enter the base URL first"
              : pending
                ? "Listing…"
                : listing.isError
                  ? ""
                  : `${listed.length} listed`}
          </span>
          <IconButton
            label="List models again"
            className="size-7"
            disabled={!input || pending}
            onClick={() => void listing.refetch()}
          >
            <RefreshCw size={14} aria-hidden className={pending ? "animate-spin" : undefined} />
          </IconButton>
        </span>
      </div>

      {listing.isError && !pending ? (
        <p className="text-[12px] text-red">
          {errorText(listing.error)}. Check the URL and the key, or add a model ID.
        </p>
      ) : null}

      {rows.length ? (
        <ul className="max-h-60 overflow-y-auto rounded-md bg-card py-1 shadow-card">
          <li className="border-b border-border">
            <label className="flex cursor-pointer items-center gap-2.5 px-3 py-1.5 text-[13px] hover:bg-muted">
              <input
                type="checkbox"
                className="accent-primary"
                checked={all}
                ref={(el) => {
                  if (el) el.indeterminate = !all && selected.length > 0;
                }}
                onChange={toggleAll}
              />
              <span className="font-medium text-foreground">All models</span>
              <span className="ml-auto text-[12px] text-muted-foreground">Includes new ones</span>
            </label>
          </li>
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
            <li className="px-3 py-1.5 text-[12px] text-muted-foreground">No model matches.</li>
          ) : null}
        </ul>
      ) : null}

      {showField ? (
        <div className="flex gap-2">
          <Input
            aria-label="Filter or add a model ID"
            className="font-mono"
            placeholder={rows.length ? "Filter or add a model ID" : "Add a model ID"}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              // Enter adds the ID; it must not submit the provider form.
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
          />
          {canAdd ? (
            <Button type="button" variant="secondary" onClick={add}>
              Add
            </Button>
          ) : null}
        </div>
      ) : null}

      {error ? <p className="text-[12px] text-red">{error}</p> : null}
    </div>
  );
}
