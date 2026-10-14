import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Plus, RefreshCw } from "lucide-react";
import type { Provider, Settings } from "../api/generated/types.gen";
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
import { Failure } from "../components/feedback";
import { Card, Empty, Note, Page, PageHeader } from "../components/page";
import { Alert, AlertDescription, AlertTitle } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { Button as Pill } from "../components/atoms/Button";
import { ValuePill } from "../components/atoms/ValuePill";
import { cn } from "../lib/utils";
import { ModelChain, WithSettings } from "./agent";

export function ProvidersPage() {
  const [dialog, setDialog] = useState<Provider | "new" | null>(null);
  const [remove, setRemove] = useState<Provider | null>(null);

  return (
    <Page>
      <PageHeader
        title="Providers"
        description="A provider is a saved connection to a model service. Its models become available to the agent."
        actions={
          <Pill variant="primary" size="sm" onClick={() => setDialog("new")}>
            <Plus aria-hidden className="size-3.5" /> Add provider
          </Pill>
        }
      />
      <WithSettings>
        {(settings) => (
          <>
            <div className="mb-6">
              <ModelChain settings={settings} />
            </div>
            <div className="mb-6">
              <Note>
                <strong className="text-ink">Saved is not the same as working.</strong> dex saves a provider
                first, then lists its models. A failed listing keeps the provider saved and shows the error on
                its card. A listed model can still fail when the agent calls it.
              </Note>
            </div>
            {!settings.providers.length ? (
              <Empty
                title="No providers yet"
                body="Add a built-in service such as Anthropic, or a custom endpoint such as LM Studio on this computer."
                action={
                  <Pill variant="primary" size="sm" onClick={() => setDialog("new")}>
                    Add provider
                  </Pill>
                }
              />
            ) : (
              <div className="flex flex-col gap-4">
                {settings.providers.map((p) => (
                  <ProviderCard
                    key={p.id}
                    provider={p}
                    settings={settings}
                    onEdit={() => setDialog(p)}
                    onRemove={() => setRemove(p)}
                  />
                ))}
              </div>
            )}
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
    </Page>
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

  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h2 className="text-base font-semibold">{p.name}</h2>
          <span className="font-mono text-xs text-ink-3">{p.id}</span>
          <ValuePill>{p.api ? "Custom" : "Built-in"}</ValuePill>
          {inUse ? <ValuePill tone="green">Agent uses {settings.agent.modelId}</ValuePill> : null}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={onEdit} aria-label={`Edit ${p.name}`}>
            Edit
          </Button>
          <Button variant="ghost" size="sm" onClick={onRemove} aria-label={`Remove ${p.name}`}>
            Remove
          </Button>
        </div>
      </div>

      <dl className="mt-4 grid gap-3 text-[13px] sm:grid-cols-3">
        <div className="rounded-lg bg-inset px-3 py-2">
          <dt className="text-xs text-ink-3">Saved connection</dt>
          <dd className="mt-0.5 break-all text-ink">
            {p.api ? `${settings.apis[p.api] ?? p.api} · ${p.baseUrl}` : "Built-in endpoint"}
          </dd>
        </div>
        <div className="rounded-lg bg-inset px-3 py-2">
          <dt className="text-xs text-ink-3">API key</dt>
          <dd className="mt-0.5 text-ink">
            {p.keySet ? "Saved · write-only" : p.api ? "None · dex sends “none”" : "Missing"}
          </dd>
        </div>
        <div className="rounded-lg bg-inset px-3 py-2">
          <dt className="text-xs text-ink-3">Model listing</dt>
          <dd className={cn("mt-0.5", p.error ? "text-red" : "text-ink")}>
            {p.error
              ? "Failed"
              : p.models.length
                ? `Fixed list · ${p.models.length} IDs`
                : p.modelCount === undefined
                  ? "Not reported"
                  : `${p.modelCount} found`}
          </dd>
        </div>
      </dl>

      {p.error ? (
        <Alert variant="destructive" className="mt-3">
          <AlertTitle>Model listing failed. The provider is still saved.</AlertTitle>
          <AlertDescription>
            <p className="break-all font-mono text-xs">{p.error}</p>
            <p>Check the base URL and the key, or enter model IDs in Edit.</p>
            <Button
              variant="outline"
              size="sm"
              disabled={retry.isPending}
              onClick={() =>
                retry.mutate({
                  path: { providerId: p.id },
                  body: { api: p.api, name: p.name, baseUrl: p.baseUrl, apiKey: "", models: p.models },
                })
              }
            >
              <RefreshCw aria-hidden className="size-3.5" /> {retry.isPending ? "Listing…" : "Retry listing"}
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}
      {retry.isError ? (
        <div className="mt-3">
          <Failure title="Could not retry the listing" error={retry.error} />
        </div>
      ) : null}

      <div className="mt-4">
        <h3 className="mb-2 text-xs font-medium text-ink-2">
          Available models{own ? ` (${own.length})` : ""}
        </h3>
        {models.isError ? (
          <p className="text-[13px] text-red">Could not load the models.</p>
        ) : !own ? (
          <p className="text-[13px] text-ink-3">Loading…</p>
        ) : own.length ? (
          <ul className="flex flex-wrap gap-1.5">
            {own.map((m) => (
              <li
                key={m.id}
                title={m.name}
                className={cn(
                  "inline-flex max-w-full items-center rounded-full px-2.5 py-0.5 font-mono text-[12px] shadow-hairline",
                  inUse && settings.agent.modelId === m.id ? "bg-green-tint text-green" : "bg-field text-ink",
                )}
              >
                <span className="truncate">{m.id}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-[13px] text-ink-3">No models.</p>
        )}
      </div>
    </Card>
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

  // The agent model is a separate save. It runs only after the provider is gone.
  const replace = useMutation({
    mutationFn: async () => {
      const model = others.find((m) => `${m.provider}/${m.id}` === next);
      await saveAgent((agent) => ({ ...agent, provider: model?.provider ?? "", modelId: model?.id ?? "" }));
    },
  });

  async function confirm() {
    if (!remove.isSuccess) await remove.mutateAsync({ path: { providerId: provider.id } });

    if (current) await replace.mutateAsync();
    onClose();
    notify(
      current && !next
        ? `Removed ${provider.name}. The agent has no model now.`
        : `Removed ${provider.name}.`,
    );
  }

  return (
    <Confirm
      title={`Remove ${provider.name}?`}
      description="dex forgets the provider and its saved key. Past answers keep their model label."
      confirmLabel={remove.isSuccess ? "Save agent model" : "Remove provider"}
      onCancel={onClose}
      onConfirm={() => void confirm().catch(() => {})}
      pending={remove.isPending || replace.isPending}
      error={remove.error ?? replace.error}
    >
      {remove.isSuccess && replace.isError ? (
        <Note tone="warn">The provider is removed. The agent model did not change; try again.</Note>
      ) : null}
      {current ? (
        <div className="flex flex-col gap-2 rounded-xl bg-orange-tint p-3 text-sm text-ink">
          <p className="font-medium">The agent uses this provider now.</p>
          <label htmlFor="fallback-model" className="text-xs text-ink-2">
            After removal, the agent uses
          </label>
          <NativeSelect
            id="fallback-model"
            className="w-full"
            value={next}
            disabled={remove.isPending || replace.isPending}
            onChange={(e) => setNext(e.target.value)}
          >
            <NativeSelectOption value="">No model. Choose one later. Sending stops.</NativeSelectOption>
            {others.map((m) => (
              <NativeSelectOption key={`${m.provider}/${m.id}`} value={`${m.provider}/${m.id}`}>
                {m.provider} · {m.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      ) : null}
    </Confirm>
  );
}
