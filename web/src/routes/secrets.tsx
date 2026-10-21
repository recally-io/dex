import { useAgentId } from "../lib/agent-scope";
import { Link } from "@tanstack/react-router";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Extensions } from "../api/generated/types.gen";
import {
  getExtensionsOptions,
  listAgentsOptions,
  getExtensionsQueryKey,
  putExtensionEnvMutation,
  deleteExtensionEnvMutation,
  reloadExtensionsMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useApp } from "../lib/app-state";
import { errorText, queryClient } from "../lib/queries";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Field, Input, Select } from "../components/form";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { buttonVariants } from "../components/atoms/Button";
import { WithSettings } from "../components/with-settings";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function SecretsPage() {
  const agentId = useAgentId();

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Secrets"
      toolbar={<GlobalSettingsSwitcher page="/secrets" />}
      description="Set tool credentials for the current bot or all dex bots. Values stay in .env files. Narrower values win."
    >
      <ToolEnvironment key={agentId} agentId={agentId} />
      <WithSettings>
        {(settings) => (
          <Section
            n={3}
            title="Provider and channel credentials"
            caption="Credentials that dex itself needs."
          >
            <Panel>
              <Facts
                rows={[
                  ...settings.providers.map((p): [string, string] => [
                    `${p.name} API key`,
                    p.keySet ? "Saved" : "None",
                  ]),
                  ["Channel credentials", "Manage in each bot’s Channels tab"],
                  ["Stored in", <Mono key="s">control.sqlite in the data directory</Mono>],
                  ["Shown in the web", "Never. You can only replace them"],
                ]}
              />
            </Panel>
            <div className="mt-3 flex flex-wrap gap-2">
              <Link to="/providers" className={buttonVariants({ size: "xs", variant: "secondary" })}>
                Provider keys
              </Link>
              <Link
                to="/a/$agentId/channels/$channelId"
                params={{ agentId, channelId: "telegram" }}
                className={buttonVariants({ size: "xs", variant: "secondary" })}
              >
                Telegram token
              </Link>
            </div>
          </Section>
        )}
      </WithSettings>
      <Section n={4} title="Secret storage">
        <Note tone="warn">
          Use mode 0600 for .env files and keep them out of Git. The model can read the files or run env. Tool
          output becomes part of the transcript. dex stores credentials as plain text. Do not put passwords in
          chats, instructions or memory. Memory is a git repository, so a password stays in its history.
        </Note>
      </Section>
    </FramedPage>
  );
}

function ToolEnvironment({ agentId }: { agentId: string }) {
  const { notify } = useApp();
  const query = useQuery(getExtensionsOptions({ path: { agentId } }));
  // Secrets is a global page, so name the agent that "agent" scope writes to.
  const agentName = useQuery(listAgentsOptions()).data?.agents.find((agent) => agent.id === agentId)?.name;
  const agentLabel = agentName ? `Bot: ${agentName}` : "This bot";
  const [scope, setScope] = useState<"agent" | "dex">("agent");
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [changed, setChanged] = useState(false);
  const form = useRef<HTMLFormElement>(null);

  const saved = (data: Extensions) => {
    queryClient.setQueryData(getExtensionsQueryKey({ path: { agentId } }), data);
    void queryClient.invalidateQueries({ queryKey: [{ _id: "getExtensions" }] });
    setChanged(true);
  };

  const save = useMutation({
    ...putExtensionEnvMutation(),
    gcTime: 0,
    onSuccess: saved,
    onError: (error) => notify(errorText(error)),
  });

  const remove = useMutation({
    ...deleteExtensionEnvMutation(),
    onSuccess: saved,
    onError: (error) => notify(errorText(error)),
  });

  const reload = useMutation({
    ...reloadExtensionsMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getExtensionsQueryKey({ path: { agentId } }), data);
      void queryClient.invalidateQueries({ queryKey: [{ _id: "getExtensions" }] });
      setChanged(false);
      notify("Extensions reloaded for the selected bot.");
    },
    onError: (error) => notify(errorText(error)),
  });

  const busy = save.isPending || remove.isPending || reload.isPending;
  const catalog = query.data;

  return (
    <>
      <Section n={1} title="Tool environment" caption="Values are write-only. Dex scope applies to all bots.">
        <div className="mb-4 flex flex-wrap gap-2">
          <Link
            to="/a/$agentId/extensions"
            params={{ agentId }}
            className={buttonVariants({ size: "xs", variant: "secondary" })}
          >
            Current bot’s Extensions
          </Link>
        </div>
        <Panel>
          <form
            ref={form}
            className="grid gap-4 p-4"
            onSubmit={(event) => {
              event.preventDefault();
              save.mutate({ path: { agentId }, body: { scope, key, value } });
              setValue("");
            }}
          >
            <Field label="Scope">
              {({ id }) => (
                <Select
                  id={id}
                  name="scope"
                  value={scope}
                  disabled={busy}
                  onChange={(event) => setScope(event.target.value === "dex" ? "dex" : "agent")}
                >
                  <option value="agent">{agentLabel}</option>
                  <option value="dex">All bots (dex)</option>
                </Select>
              )}
            </Field>
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
              <Button type="submit" size="sm" variant="primary" disabled={busy || !key}>
                {save.isPending ? <Spinner /> : null}Save key
              </Button>
            </div>
          </form>
        </Panel>
        {changed ? (
          <div className="mt-4" role="status">
            <Note>
              <p>The change applies after Reload. Reload applies to the selected agent.</p>
              <Button
                className="mt-3"
                size="sm"
                disabled={busy}
                onClick={() => reload.mutate({ path: { agentId } })}
              >
                {reload.isPending ? <Spinner /> : null}Reload
              </Button>
            </Note>
          </div>
        ) : null}
      </Section>
      {catalog ? (
        <Section n={2} title="Saved keys" caption="Names only. A narrower level wins.">
          <div className="space-y-4">
            {(["agent", "dex", "home"] as const).map((level) => (
              <Panel key={level}>
                <div className="border-b border-line px-4 py-3">
                  <p className="text-[13px] font-medium text-ink">
                    {level === "agent" ? agentLabel : level === "dex" ? "All bots (dex)" : "Home · Read-only"}
                  </p>
                  <p className="mt-1 text-[11.5px] text-ink-3 [overflow-wrap:anywhere]">
                    <Mono>{catalog.levels.find((l) => l.scope === level)?.dir}/.env</Mono>
                  </p>
                </div>
                {catalog.env.some((entry) => entry.scope === level) ? (
                  <ul className="divide-y divide-line">
                    {catalog.env
                      .filter((entry) => entry.scope === level)
                      .map((entry) => (
                        <li
                          key={entry.key}
                          data-env-key={entry.key}
                          data-env-scope={level}
                          className="flex flex-wrap items-center gap-2 px-4 py-3"
                        >
                          <span className="min-w-0 w-full text-[12.5px] [overflow-wrap:anywhere] sm:w-auto sm:flex-1">
                            <Mono>{entry.key}</Mono>
                          </span>
                          {entry.shadowed ? (
                            <StatusPill className="h-5 px-2 text-[11.5px]">Shadowed</StatusPill>
                          ) : null}
                          {level !== "home" ? (
                            <div className="flex gap-2">
                              <Button
                                type="button"
                                size="xs"
                                disabled={busy}
                                onClick={() => {
                                  setScope(level);
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
                                    query: { scope: level, key: entry.key },
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
                  <p className="px-4 py-3 text-[12.5px] text-ink-3">No keys in this file.</p>
                )}
              </Panel>
            ))}
          </div>
          {catalog.errors.length ? (
            <div className="mt-4">
              <Failure title="dex cannot read some files">
                <ul className="[overflow-wrap:anywhere]">
                  {catalog.errors.map((error) => (
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
