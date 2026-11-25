import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  deleteVaultMutation,
  listVaultOptions,
  putVaultMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { errorText, queryClient } from "../lib/queries";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Input } from "./form";
import { Mono, Panel, Section } from "./page";
import { Button } from "./ui/button";

type VaultTarget = { scope: "agent"; agentId: string } | { scope: "dex" };

export function Vault({ target }: { target: VaultTarget }) {
  const { notify } = useApp();
  const agentId = target.scope === "agent" ? target.agentId : "default";
  const path = { agentId };
  const scope = target.scope;
  const query = useQuery(listVaultOptions({ path }));
  const [name, setName] = useState("");
  const [value, setValue] = useState("");

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: [{ _id: "listVault" }] });
    void queryClient.invalidateQueries({ queryKey: [{ _id: "getExtensions" }] });
  };

  const save = useMutation({
    ...putVaultMutation(),
    gcTime: 0,
    onSuccess: () => {
      refresh();
      notify("Secret saved. The change applies when the bot is idle.");
    },
    onError: (error) => notify(errorText(error)),
  });

  const remove = useMutation({
    ...deleteVaultMutation(),
    onSuccess: refresh,
    onError: (error) => notify(errorText(error)),
  });

  const busy = save.isPending || remove.isPending;
  const entries = query.data?.filter((entry) => entry.scope === scope) ?? [];

  return (
    <Section
      title="Secret vault"
      caption={
        scope === "agent"
          ? "These secrets are for this bot. A bot secret overrides a shared secret with the same name."
          : "All bots can use these secrets. A bot secret with the same name takes precedence."
      }
    >
      <Panel>
        <form
          className="grid gap-4 p-4"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate({ path, body: { scope, name, value } }, { onSettled: () => save.reset() });
            setValue("");
          }}
        >
          <Field
            label="Secret name"
            hint="Use letters, numbers, and underscores. Start with a letter or underscore."
          >
            {({ id, describedBy }) => (
              <Input
                id={id}
                value={name}
                onChange={(event) => setName(event.target.value)}
                aria-describedby={describedBy}
                required
                pattern="[A-Za-z_][A-Za-z0-9_]*"
                autoComplete="off"
                spellCheck={false}
                disabled={busy}
              />
            )}
          </Field>
          <Field label="Secret value" hint="dex encrypts the value. You cannot read a saved value here.">
            {({ id, describedBy }) => (
              <Input
                id={id}
                type="password"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                aria-describedby={describedBy}
                autoComplete="new-password"
                disabled={busy}
              />
            )}
          </Field>
          <div>
            <Button type="submit" size="sm" disabled={busy || !name}>
              {save.isPending ? <Spinner /> : null}Save secret
            </Button>
          </div>
        </form>
        {query.isError ? (
          <Failure error={query.error} retry={() => void query.refetch()} />
        ) : query.isPending ? (
          <Loading />
        ) : entries.length ? (
          <ul className="divide-y divide-border border-t border-border">
            {entries.map((entry) => (
              <li key={entry.name} className="flex flex-wrap items-center gap-2 px-4 py-3">
                <Mono className="min-w-0 flex-1 break-all">vault:{entry.name}</Mono>
                <time dateTime={entry.updatedAt} className="text-xs text-muted-foreground">
                  {new Date(entry.updatedAt).toLocaleString()}
                </time>
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={`Delete ${entry.name}`}
                  disabled={busy}
                  onClick={() => remove.mutate({ path, query: { scope, name: entry.name } })}
                >
                  Delete
                </Button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="px-4 pb-4 text-sm text-muted-foreground">No secrets at this level.</p>
        )}
      </Panel>
      {scope === "agent" ? (
        <Link to="/settings/secrets" className="mt-3 inline-block text-sm text-muted-foreground underline">
          Manage secrets shared by all bots
        </Link>
      ) : null}
    </Section>
  );
}
