import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Provider } from "../api/generated/types.gen";
import {
  deleteProviderMutation,
  listModelsOptions,
  setProviderMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { saveAgent } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ProviderDialog } from "../components/provider-dialog";
import { Confirm } from "../components/confirm";
import { Failure, Spinner } from "../components/feedback";
import { Field, Select } from "../components/form";
import { Empty, Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { WithSettings } from "../components/with-settings";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function ProvidersPage() {
  const [dialog, setDialog] = useState<Provider | "new" | null>(null);
  const [remove, setRemove] = useState<Provider | null>(null);

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Providers"
      toolbar={<GlobalSettingsSwitcher page="/providers" />}
      description="A provider is a saved connection to a model service. The bot model comes from one of them."
      actions={
        <Button size="sm" variant="primary" onClick={() => setDialog("new")}>
          Add provider
        </Button>
      }
    >
      <WithSettings>
        {(settings) => (
          <>
            <Connections settings={settings} onEdit={setDialog} onRemove={setRemove} />
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
                settings={settings}
                existing={dialog === "new" ? undefined : dialog}
                onClose={() => setDialog(null)}
              />
            ) : null}
            {remove ? (
              <RemoveProvider provider={remove} settings={settings} onClose={() => setRemove(null)} />
            ) : null}
          </>
        )}
      </WithSettings>
    </FramedPage>
  );
}

function Connections({
  settings,
  onEdit,
  onRemove,
}: {
  settings: Settings;
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
          <StatusPill tone="red" className="h-5 px-2 text-[11.5px]">
            {failed} {failed === 1 ? "needs" : "need"} attention
          </StatusPill>
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
              onEdit={() => onEdit(p)}
              onRemove={() => onRemove(p)}
            />
          ))}
          <Note>
            <strong className="font-medium text-ink">Saved is not the same as working.</strong> dex saves a
            provider first, then lists its models. A failed listing keeps the provider saved and shows the
            error on its card. A listed model can still fail when the bot calls it.
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
  onEdit,
  onRemove,
}: {
  provider: Provider;
  settings: Settings;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { notify } = useApp();
  const models = useQuery(listModelsOptions());
  const own = models.data?.models.filter((m) => m.provider === p.id);
  const inUse = settings.agent.provider === p.id && !!settings.agent.modelId;

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
        <span key="k" className="text-ink-2">
          Saved · write-only
        </span>
      ) : (
        <span key="k" className={p.api ? "text-ink-3" : "text-orange"}>
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
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line px-4 py-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <h3 className="text-[14px] font-medium text-ink">{p.name}</h3>
          <Mono className="text-[11.5px] text-ink-3">{p.id}</Mono>
          {inUse ? (
            <StatusPill tone="accent" className="h-5 px-2 text-[11px]">
              Bot model
            </StatusPill>
          ) : null}
        </div>
        <Button size="xs" variant="quiet" onClick={onEdit} aria-label={`Edit ${p.name}`}>
          Edit
        </Button>
        <Button
          size="xs"
          variant="quiet"
          className="text-red"
          onClick={onRemove}
          aria-label={`Remove ${p.name}`}
        >
          Remove
        </Button>
      </div>
      <Facts rows={rows} />
      {p.error ? (
        <div className="border-t border-line p-3">
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
              <span className="text-[12px] text-ink-3">
                Or check the base URL and the key, or enter model IDs in Edit.
              </span>
            </div>
          </Failure>
        </div>
      ) : null}
      {retry.isError ? (
        <div className="border-t border-line p-3">
          <Failure title="Could not retry the listing" error={retry.error} />
        </div>
      ) : null}
      {models.isError ? (
        <p className="border-t border-line px-4 py-3 text-[12.5px] text-red">Could not load the models.</p>
      ) : !p.error && own?.length ? (
        <ul aria-label={`${p.name} models`} className="flex flex-wrap gap-1.5 border-t border-line px-4 py-3">
          {own.map((m) => (
            <li
              key={m.id}
              title={m.id}
              className={
                inUse && settings.agent.modelId === m.id
                  ? "inline-flex h-6 max-w-full items-center rounded-full bg-dex-accent-tint px-2.5 text-[12px] font-medium text-dex-accent-ink"
                  : "inline-flex h-6 max-w-full items-center rounded-full bg-inset px-2.5 text-[12px] font-medium text-ink-2 shadow-btn"
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
  settings,
  onClose,
}: {
  provider: Provider;
  settings: Settings;
  onClose: () => void;
}) {
  const { notify } = useApp();
  const models = useQuery(listModelsOptions());
  const current = settings.agent.provider === provider.id && !!settings.agent.modelId;
  const others = models.data?.models.filter((m) => m.provider !== provider.id) ?? [];
  const [next, setNext] = useState("");

  const remove = useMutation({
    ...deleteProviderMutation(),
    onSettled: () => refreshSettings(),
  });

  // The bot model is a separate save. It runs only after the provider is gone.
  const replace = useMutation({
    mutationFn: async () => {
      const model = others.find((m) => `${m.provider}/${m.id}` === next);
      await saveAgent("default", (agent) => ({
        ...agent,
        provider: model?.provider ?? "",
        modelId: model?.id ?? "",
      }));
    },
  });

  async function confirm() {
    if (!remove.isSuccess) await remove.mutateAsync({ path: { providerId: provider.id } });

    if (current) await replace.mutateAsync();
    onClose();
    notify(
      current && !next ? `Removed ${provider.name}. The bot has no model now.` : `Removed ${provider.name}.`,
    );
  }

  return (
    <Confirm
      title={`Remove ${provider.name}?`}
      description="dex forgets the provider and its saved key. Past answers keep their model label."
      confirmLabel={remove.isSuccess ? "Save bot model" : "Remove provider"}
      onCancel={onClose}
      onConfirm={() => void confirm().catch(() => {})}
      pending={remove.isPending || replace.isPending}
      error={remove.error ?? replace.error}
    >
      {remove.isSuccess && replace.isError ? (
        <Note tone="warn">The provider is removed. The bot model did not change; try again.</Note>
      ) : null}
      {current ? (
        <div className="flex flex-col gap-3 rounded-card bg-orange-tint p-3">
          <p className="text-[13px] font-medium text-ink">The bot uses this provider now.</p>
          <Field label="After removal, the bot uses">
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
