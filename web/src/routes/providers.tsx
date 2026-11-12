import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Agent, Provider, Settings } from "../api/generated/types.gen";
import {
  deleteProviderMutation,
  getSettingsOptions,
  listAgentsOptions,
  listModelsOptions,
  setProviderMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { saveAgent } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ProviderDialog } from "../components/provider-dialog";
import { Confirm } from "../components/confirm";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Field, Select } from "../components/form";
import { Empty, Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

// The bots that have a model from each provider. A bot without a model ID uses none.
type Users = Map<string, Agent[]>;

function usersOf(agents: Agent[]): Users {
  const users: Users = new Map();

  for (const agent of agents) {
    if (agent.modelId) users.set(agent.provider, [...(users.get(agent.provider) ?? []), agent]);
  }

  return users;
}

export function ProvidersPage() {
  const [dialog, setDialog] = useState<Provider | "new" | null>(null);
  const [remove, setRemove] = useState<Provider | null>(null);
  const settings = useQuery(getSettingsOptions());
  const agents = useQuery(listAgentsOptions());
  const failed = settings.error ?? agents.error;

  const data: Data | undefined =
    settings.data && agents.data
      ? { settings: settings.data, users: usersOf(agents.data.agents) }
      : undefined;

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Providers"
      toolbar={<GlobalSettingsSwitcher page="/providers" />}
      description="A provider is a saved connection to a model service. Each bot chooses its model from one of them."
      actions={<Button onClick={() => setDialog("new")}>Add provider</Button>}
    >
      {data ? (
        <>
          <Connections {...data} onEdit={setDialog} onRemove={setRemove} />
          <Section n={2} title="Keys" caption="Write-only.">
            <Panel>
              <Facts
                rows={[
                  ["Where keys are stored", <Mono key="s">control.sqlite in the data directory</Mono>],
                  ["Shown in the web", "Never. Only whether a key is saved"],
                  ["To change a key", "Edit the provider and replace the key"],
                ]}
              />
            </Panel>
          </Section>
          {dialog ? (
            <ProviderDialog
              settings={data.settings}
              existing={dialog === "new" ? undefined : dialog}
              onClose={() => setDialog(null)}
            />
          ) : null}
          {remove ? (
            <RemoveProvider
              provider={remove}
              users={data.users.get(remove.id) ?? []}
              onClose={() => setRemove(null)}
            />
          ) : null}
        </>
      ) : failed ? (
        <div className="px-5 py-8 sm:px-8">
          <Failure
            title="Could not load settings"
            error={failed}
            retry={() => void Promise.all([settings.refetch(), agents.refetch()])}
          />
        </div>
      ) : (
        <Loading />
      )}
    </FramedPage>
  );
}

type Data = { settings: Settings; users: Users };

function Connections({
  settings,
  users,
  onEdit,
  onRemove,
}: Data & {
  onEdit: (provider: Provider) => void;
  onRemove: (provider: Provider) => void;
}) {
  const models = useQuery(listModelsOptions());
  const failed = settings.providers.filter((p) => p.error).length;
  const count = models.data?.models.length;

  return (
    <Section
      n={1}
      title="Connections"
      caption={
        settings.providers.length
          ? `${settings.providers.length} saved${count === undefined ? "" : ` · ${count} models`}`
          : undefined
      }
      action={
        failed ? (
          <Badge variant="destructive" className="h-5 px-2 text-[11.5px]">
            {failed} {failed === 1 ? "needs" : "need"} attention
          </Badge>
        ) : null
      }
    >
      {settings.providers.length ? (
        <div className="flex flex-col gap-3">
          {settings.providers.map((p) => (
            <ProviderCard
              key={p.id}
              provider={p}
              settings={settings}
              users={users.get(p.id) ?? []}
              onEdit={() => onEdit(p)}
              onRemove={() => onRemove(p)}
            />
          ))}
          <Note>
            <strong className="font-medium text-foreground">Saved is not the same as working.</strong> dex
            saves a provider first, then lists its models. A failed listing keeps the provider saved and shows
            the error on its card. A listed model can still fail when a bot calls it.
          </Note>
        </div>
      ) : (
        <Empty title="No providers yet">
          Add a built-in service such as Anthropic, or a custom endpoint such as LM Studio on this computer.
        </Empty>
      )}
    </Section>
  );
}

function ProviderCard({
  provider: p,
  settings,
  users,
  onEdit,
  onRemove,
}: {
  provider: Provider;
  settings: Settings;
  users: Agent[];
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { notify } = useApp();
  const models = useQuery(listModelsOptions());
  const own = models.data?.models.filter((m) => m.provider === p.id);
  const used = new Set(users.map((agent) => agent.modelId));

  // A save with an empty key keeps the saved key, and makes the worker list the models again.
  const retry = useMutation({
    ...setProviderMutation(),
    onSuccess: async (result) => {
      await refreshSettings();
      notify(
        result.error
          ? `${p.name}: model listing failed again.`
          : `${p.name}: ${result.modelCount ?? 0} models found.`,
      );
    },
  });

  const rows: [string, ReactNode][] = [
    ["Connection", p.api ? `${settings.apis[p.api] ?? p.api} · custom` : "Built-in"],
  ];

  if (p.baseUrl) rows.push(["Base URL", <Mono key="u">{p.baseUrl}</Mono>]);
  rows.push(
    [
      "API key",
      p.keySet ? (
        <span key="k" className="text-muted-foreground">
          Saved · write-only
        </span>
      ) : (
        <span key="k" className={p.api ? "text-muted-foreground" : "text-orange"}>
          {p.api ? "None · dex sends “none”" : "Missing"}
        </span>
      ),
    ],
    [
      "Models",
      p.error ? (
        <span key="m" className="text-red">
          Listing failed
        </span>
      ) : (
        <span key="m" className="font-mono text-[12px] tabular-nums">
          {own?.length ?? p.modelCount ?? 0} {p.models.length ? "fixed" : "listed"}
        </span>
      ),
    ],
  );

  return (
    <Panel>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <h3 className="text-[14px] font-medium text-foreground">{p.name}</h3>
          <Mono className="text-[11.5px] text-muted-foreground">{p.id}</Mono>
          {users.length ? (
            <Badge variant="secondary" className="h-5 max-w-full bg-primary/10 px-2 text-[11px] text-primary">
              <span className="truncate">Used by {users.map((agent) => agent.name).join(", ")}</span>
            </Badge>
          ) : null}
        </div>
        <Button size="xs" variant="ghost" onClick={onEdit} aria-label={`Edit ${p.name}`}>
          Edit
        </Button>
        <Button
          size="xs"
          variant="ghost"
          className="text-red"
          onClick={onRemove}
          aria-label={`Remove ${p.name}`}
        >
          Remove
        </Button>
      </div>
      <Facts rows={rows} />
      {p.error ? (
        <div className="border-t border-border p-3">
          <Failure title="Model listing failed. The provider is still saved.">
            <p className="font-mono text-[11.5px]">{p.error}</p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button
                size="xs"
                variant="secondary"
                disabled={retry.isPending}
                onClick={() =>
                  retry.mutate({
                    path: { providerId: p.id },
                    body: { api: p.api, name: p.name, baseUrl: p.baseUrl, apiKey: "", models: p.models },
                  })
                }
              >
                {retry.isPending ? <Spinner /> : null}
                {retry.isPending ? "Listing…" : "Retry listing"}
              </Button>
              <span className="text-[12px] text-muted-foreground">
                Or check the base URL and the key, or enter model IDs in Edit.
              </span>
            </div>
          </Failure>
        </div>
      ) : null}
      {retry.isError ? (
        <div className="border-t border-border p-3">
          <Failure title="Could not retry the listing" error={retry.error} />
        </div>
      ) : null}
      {models.isError ? (
        <p className="border-t border-border px-4 py-3 text-[12.5px] text-red">Could not load the models.</p>
      ) : !p.error && own?.length ? (
        <ul
          aria-label={`${p.name} models`}
          className="flex flex-wrap gap-1.5 border-t border-border px-4 py-3"
        >
          {own.map((m) => (
            <li
              key={m.id}
              title={m.id}
              className={
                used.has(m.id)
                  ? "inline-flex h-6 max-w-full items-center rounded-full bg-primary/10 px-2.5 text-[12px] font-medium text-primary"
                  : "inline-flex h-6 max-w-full items-center rounded-full bg-muted px-2.5 text-[12px] font-medium text-muted-foreground shadow-card"
              }
            >
              <span className="truncate">{m.name}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </Panel>
  );
}

function RemoveProvider({
  provider,
  users,
  onClose,
}: {
  provider: Provider;
  users: Agent[];
  onClose: () => void;
}) {
  const { notify } = useApp();
  const models = useQuery(listModelsOptions());
  const current = users.length > 0;
  const names = users.map((agent) => agent.name).join(", ");
  const others = models.data?.models.filter((m) => m.provider !== provider.id) ?? [];
  const [next, setNext] = useState("");

  const remove = useMutation({
    ...deleteProviderMutation(),
    onSettled: () => refreshSettings(),
  });

  // The bot models are separate saves. They run only after the provider is gone. A bot that
  // changed its provider since the dialog opened keeps its new model.
  const replace = useMutation({
    mutationFn: async () => {
      const model = others.find((m) => `${m.provider}/${m.id}` === next);

      await Promise.all(
        users.map((user) =>
          saveAgent(user.id!, (agent) =>
            agent.provider === provider.id
              ? { ...agent, provider: model?.provider ?? "", modelId: model?.id ?? "" }
              : agent,
          ),
        ),
      );
    },
  });

  async function confirm() {
    if (!remove.isSuccess) await remove.mutateAsync({ path: { providerId: provider.id } });

    if (current) await replace.mutateAsync();
    onClose();
    notify(
      current && !next
        ? `Removed ${provider.name}. ${names} ${users.length === 1 ? "has" : "have"} no model now.`
        : `Removed ${provider.name}.`,
    );
  }

  return (
    <Confirm
      title={`Remove ${provider.name}?`}
      description="dex forgets the provider and its saved key. Past answers keep their model label."
      confirmLabel={remove.isSuccess ? "Save bot models" : "Remove provider"}
      onCancel={onClose}
      onConfirm={() => void confirm().catch(() => {})}
      pending={remove.isPending || replace.isPending}
      error={remove.error ?? replace.error}
    >
      {remove.isSuccess && replace.isError ? (
        <Note tone="warn">The provider is removed. Some bot models did not change; try again.</Note>
      ) : null}
      {current ? (
        <div className="flex flex-col gap-3 rounded-md bg-orange-tint p-3">
          <p className="text-[13px] font-medium text-foreground">
            {names} {users.length === 1 ? "uses" : "use"} this provider now.
          </p>
          <Field label={users.length === 1 ? "After removal, the bot uses" : "After removal, these bots use"}>
            {({ id }) => (
              <Select
                id={id}
                value={next}
                disabled={remove.isPending || replace.isPending}
                onChange={(e) => setNext(e.target.value)}
              >
                <option value="">No model. Choose one later. Sending stops.</option>
                {others.map((m) => (
                  <option key={`${m.provider}/${m.id}`} value={`${m.provider}/${m.id}`}>
                    {m.provider} · {m.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
      ) : null}
    </Confirm>
  );
}
