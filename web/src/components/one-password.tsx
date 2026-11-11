import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { OnePassword, OnePasswordMode } from "../api/generated/types.gen";
import {
  getOnePasswordOptions,
  getOnePasswordQueryKey,
  setOnePasswordMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { errorText, queryClient } from "../lib/queries";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Input, RadioSegments, SavedSecret } from "./form";
import { Mono, Note, Panel, Section } from "./page";
import { Button } from "./ui/button";

const modes = [
  { value: "off", label: "Off" },
  { value: "service-account", label: "Service account" },
  { value: "connect", label: "Connect" },
] as const satisfies readonly { value: OnePasswordMode; label: string }[];

/**
 * 1Password credentials for the op CLI of tools. dex does not read secrets: the prompt tells
 * bots to run op read. Tokens are write-only.
 */
export function OnePasswordSettings({ n }: { n: number }) {
  const query = useQuery(getOnePasswordOptions());

  return (
    <Section
      n={n}
      title="1Password"
      caption="Bots read secrets with the op CLI. dex gives op the credentials and never reads a secret itself."
    >
      {query.isPending ? (
        <Loading />
      ) : query.isError ? (
        <Failure error={query.error} retry={() => void query.refetch()} />
      ) : (
        <OnePasswordForm saved={query.data} />
      )}
    </Section>
  );
}

function OnePasswordForm({ saved }: { saved: OnePassword }) {
  const { notify } = useApp();
  const [mode, setMode] = useState<OnePasswordMode>(saved.mode);
  const [host, setHost] = useState(saved.connectHost);
  const [token, setToken] = useState("");
  const [replace, setReplace] = useState(false);

  const save = useMutation({
    ...setOnePasswordMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getOnePasswordQueryKey(), data);
      void queryClient.invalidateQueries({ queryKey: [{ _id: "getExtensions" }] });
      setToken("");
      setReplace(false);
      notify("Saved. Bots apply it when all bots are idle.");
    },
    onError: (error) => notify(errorText(error)),
  });

  const tokenSet = mode === "connect" ? saved.connectTokenSet : saved.serviceAccountTokenSet;
  const tokenLabel = mode === "connect" ? "Connect token" : "Service account token";

  return (
    <Panel>
      <form
        className="grid gap-4 p-4"
        onSubmit={(event) => {
          event.preventDefault();
          save.mutate({
            body:
              mode === "connect"
                ? { mode, connectHost: host, connectToken: token }
                : mode === "service-account"
                  ? { mode, serviceAccountToken: token }
                  : { mode },
          });
        }}
      >
        <RadioSegments label="Mode" options={modes} value={mode} onChange={setMode} />
        {mode === "connect" ? (
          <Field label="Connect host" hint="The URL of your 1Password Connect server.">
            {({ id, describedBy }) => (
              <Input
                id={id}
                value={host}
                onChange={(event) => setHost(event.target.value)}
                aria-describedby={describedBy}
                placeholder="https://connect.example.com"
                required
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </Field>
        ) : null}
        {mode !== "off" ? (
          tokenSet && !replace ? (
            <SavedSecret
              label={tokenLabel}
              text="Saved token ••••"
              hint="dex never shows a saved token."
              action="Replace token"
              onReplace={() => setReplace(true)}
            />
          ) : (
            <Field label={tokenLabel} hint="Use a token that can read only the vaults for bots.">
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  type="password"
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                  aria-describedby={describedBy}
                  required
                  autoComplete="new-password"
                />
              )}
            </Field>
          )
        ) : null}
        {mode !== "off" ? (
          saved.cli ? (
            <p className="text-[12px] text-muted-foreground">
              op CLI: <Mono>{saved.cli}</Mono>
            </p>
          ) : (
            <Note tone="warn">
              The op CLI is not in the PATH of your login shell. Install it, then bots can use 1Password.
            </Note>
          )
        ) : null}
        <div>
          <Button type="submit" size="sm" disabled={save.isPending}>
            {save.isPending ? <Spinner /> : null}Save
          </Button>
        </div>
      </form>
    </Panel>
  );
}
