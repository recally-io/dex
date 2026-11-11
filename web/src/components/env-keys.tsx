import { Link } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Extensions } from "../api/generated/types.gen";
import {
  getExtensionsOptions,
  getExtensionsQueryKey,
  putExtensionEnvMutation,
  deleteExtensionEnvMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { errorText, queryClient } from "../lib/queries";
import { Failure, Loading, Spinner } from "./feedback";
import { Field, Input } from "./form";
import { Mono, Note, Panel, Section } from "./page";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";

type Level = Extensions["env"][number]["scope"];

/**
 * The .env keys of one writable level and the wider levels it inherits. A bot writes its own
 * file and reads dex and home. The global page writes dex and reads home. Values never show.
 */
export type EnvTarget = { scope: "agent"; agentId: string } | { scope: "dex" };

const titles: Record<Level, string> = { agent: "This bot", dex: "All bots", home: "Home · Read-only" };

export function EnvKeys({ target, n }: { target: EnvTarget; n: number }) {
  const { notify } = useApp();
  // The dex file does not depend on the bot, so any bot can read and write it. The default bot
  // always exists.
  const agentId = target.scope === "agent" ? target.agentId : "default";

  // While a change waits for idle bots, poll so that the note goes away after the reload.
  const query = useQuery({
    ...getExtensionsOptions({ path: { agentId } }),
    // An invalid configuration does not reload until its files change, so it does not poll.
    refetchInterval: (q) => (q.state.data?.reloadPending && !q.state.data.errors.length ? 3000 : false),
  });

  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const form = useRef<HTMLFormElement>(null);

  const refresh = (data: Extensions) => {
    queryClient.setQueryData(getExtensionsQueryKey({ path: { agentId } }), data);
    void queryClient.invalidateQueries({ queryKey: [{ _id: "getExtensions" }] });
  };

  const save = useMutation({
    ...putExtensionEnvMutation(),
    gcTime: 0,
    onSuccess: refresh,
    onError: (error) => notify(errorText(error)),
  });

  const remove = useMutation({
    ...deleteExtensionEnvMutation(),
    onSuccess: refresh,
    onError: (error) => notify(errorText(error)),
  });

  const busy = save.isPending || remove.isPending;
  const catalog = query.data;
  const levels: Level[] = target.scope === "agent" ? ["agent", "dex", "home"] : ["dex", "home"];

  // The Tools page shows errors in its Configuration section. The global page reads them through
  // the default bot, so it keeps only the errors that name a dex or home file.
  const wideDirs = catalog?.levels.filter((l) => l.scope !== "agent").map((l) => l.dir) ?? [];

  const wideErrors =
    target.scope === "dex"
      ? (catalog?.errors.filter((error) => wideDirs.some((dir) => error.includes(dir))) ?? [])
      : [];

  // The API marks a key shadowed for one bot. On the global page, only a dex key hides a home key.
  const shadowed = (entry: Extensions["env"][number]) =>
    target.scope === "agent"
      ? entry.shadowed
      : entry.scope === "home" && !!catalog?.env.some((e) => e.scope === "dex" && e.key === entry.key);

  return (
    <>
      <Section
        n={n}
        title="Environment keys"
        caption={
          target.scope === "agent"
            ? "Tools of this bot read these. Values are write-only."
            : "Tools of every bot read these. A bot key with the same name wins. Values are write-only."
        }
      >
        <Panel>
          <form
            ref={form}
            className="grid gap-4 p-4"
            onSubmit={(event) => {
              event.preventDefault();
              save.mutate({ path: { agentId }, body: { scope: target.scope, key, value } });
              setValue("");
            }}
          >
            <Field
              label="Key"
              hint="Use letters, numbers, and underscores. Start with a letter or underscore."
            >
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  name="key"
                  value={key}
                  onChange={(event) => setKey(event.target.value)}
                  aria-describedby={describedBy}
                  required
                  pattern="[A-Za-z_][A-Za-z0-9_]*"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={busy}
                />
              )}
            </Field>
            <Field label="Value" hint="An empty value is allowed. Saved values are never shown.">
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  name="value"
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
              <Button type="submit" size="sm" disabled={busy || !key}>
                {save.isPending ? <Spinner /> : null}Save key
              </Button>
            </div>
          </form>
        </Panel>
        {catalog?.reloadPending && !catalog.errors.length ? (
          <div className="mt-4" role="status">
            <Note>The change applies automatically when all bots are idle.</Note>
          </div>
        ) : null}
      </Section>
      {catalog ? (
        <Section n={n + 1} title="Saved keys" caption="Names only. A narrower level wins.">
          <div className="space-y-4">
            {levels.map((level) => {
              const entries = catalog.env.filter((entry) => entry.scope === level);
              const writable = level === target.scope;

              return (
                <Panel key={level}>
                  <div className="flex flex-wrap items-start gap-2 border-b border-border px-4 py-3">
                    <div className="min-w-0 flex-1">
                      <p className="text-[13px] font-medium text-foreground">
                        {level === "dex" && !writable ? "All bots · Read-only" : titles[level]}
                      </p>
                      <p className="mt-1 text-[11.5px] text-muted-foreground [overflow-wrap:anywhere]">
                        <Mono>{catalog.levels.find((l) => l.scope === level)?.dir}/.env</Mono>
                      </p>
                    </div>
                    {level === "dex" && !writable ? (
                      <Link to="/environment" className="text-[12px] text-muted-foreground underline">
                        Edit in Environment
                      </Link>
                    ) : null}
                  </div>
                  {entries.length ? (
                    <ul className="divide-y divide-border">
                      {entries.map((entry) => (
                        <li
                          key={entry.key}
                          data-env-key={entry.key}
                          data-env-scope={level}
                          className="flex flex-wrap items-center gap-2 px-4 py-3"
                        >
                          <span className="min-w-0 w-full text-[12.5px] [overflow-wrap:anywhere] sm:w-auto sm:flex-1">
                            <Mono>{entry.key}</Mono>
                          </span>
                          {shadowed(entry) ? (
                            <Badge variant="outline" className="h-5 px-2 text-[11.5px]">
                              Shadowed
                            </Badge>
                          ) : null}
                          {writable ? (
                            <div className="flex gap-2">
                              <Button
                                type="button"
                                size="xs"
                                disabled={busy}
                                onClick={() => {
                                  setKey(entry.key);
                                  setValue("");

                                  const input =
                                    form.current?.querySelector<HTMLInputElement>('input[name="value"]');

                                  input?.focus();
                                  input?.scrollIntoView({ block: "center", behavior: "smooth" });
                                }}
                              >
                                Replace
                              </Button>
                              <Button
                                type="button"
                                size="xs"
                                disabled={busy}
                                onClick={() =>
                                  remove.mutate({
                                    path: { agentId },
                                    query: { scope: target.scope, key: entry.key },
                                  })
                                }
                              >
                                Delete
                              </Button>
                            </div>
                          ) : null}
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className="px-4 py-3 text-[12.5px] text-muted-foreground">No keys in this file.</p>
                  )}
                </Panel>
              );
            })}
          </div>
          {wideErrors.length ? (
            <div className="mt-4">
              <Failure title="dex cannot read some files">
                <ul className="[overflow-wrap:anywhere]">
                  {wideErrors.map((error) => (
                    <li key={error}>{error}</li>
                  ))}
                </ul>
              </Failure>
            </div>
          ) : null}
        </Section>
      ) : query.isError ? (
        <div className="px-5 py-4 sm:px-8">
          <Failure error={query.error} retry={() => void query.refetch()} />
        </div>
      ) : (
        <Loading />
      )}
    </>
  );
}
