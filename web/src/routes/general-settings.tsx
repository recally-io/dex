import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  getAgentOptions,
  getSettingsOptions,
  listAgentsQueryKey,
  listEnvironmentsOptions,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import type { Agent } from "../api/generated/types.gen";
import { findModel, saveAgent, thinkingNames } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { environmentOptionLabel } from "../lib/environments";
import { queryClient } from "../lib/queries";
import { Failure, Loading } from "../components/feedback";
import { Select } from "../components/form";
import { ModelPicker } from "../components/model-picker";
import { FramedPage, Note, Panel, Section } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";
import { ThinkingPicker } from "../components/thinking-picker";
import { Button } from "../components/ui/button";

function Chevron() {
  return (
    <svg
      width="11"
      height="11"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="text-muted-foreground"
    >
      <path d="M6 9l6 6 6-6" />
    </svg>
  );
}

export function GeneralSettingsPage() {
  const { notify } = useApp();
  const settings = useQuery(getSettingsOptions());
  // New bots copy these fields from the built-in default bot.
  const template = useQuery(getAgentOptions({ path: { agentId: "default" } }));
  const models = useQuery(listModelsOptions());
  const environments = useQuery(listEnvironmentsOptions());

  const save = useMutation({
    mutationFn: (patch: Partial<Agent>) => saveAgent("default", (saved) => ({ ...saved, ...patch })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: listAgentsQueryKey() }),
  });

  const defaults = template.data;
  const envList = environments.data?.environments ?? [];

  const primary = defaults ? findModel(defaults.provider, defaults.modelId, models.data?.models) : undefined;

  const fallback = defaults
    ? findModel(defaults.fallbackProvider, defaults.fallbackModelId, models.data?.models)
    : undefined;

  const currentEnv = defaults?.environmentId
    ? envList.find((env) => env.id === defaults.environmentId)
    : undefined;

  const missingEnv = Boolean(
    defaults?.environmentId && !environments.isPending && !environments.isError && !currentEnv,
  );

  async function updateDefaults(patch: Partial<Agent>, message: string) {
    await save.mutateAsync(patch);
    notify(message);
  }

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="General"
      toolbar={<GlobalSettingsSwitcher page="/settings/general" />}
      description="New bots copy these settings from the default bot. Existing bots keep their own settings."
    >
      {settings.isPending || template.isPending ? (
        <Loading />
      ) : settings.isError || template.isError ? (
        <div className="px-5 py-8 sm:px-8">
          <Failure
            title="Could not load settings"
            error={settings.error ?? template.error}
            retry={() => void Promise.all([settings.refetch(), template.refetch()])}
          />
        </div>
      ) : defaults && settings.data ? (
        <Section title="Bot defaults" caption="The default bot uses them, and a new bot starts with a copy.">
          <Panel>
            <div className="flex flex-wrap items-center gap-3 px-4 py-3.5">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[14px] font-medium text-foreground">
                  {defaults.modelId ? `Default model: ${primary?.label}` : "No default model"}
                </p>
                {defaults.modelId ? (
                  <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                    {defaults.provider}/{defaults.modelId}
                  </p>
                ) : (
                  <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                    New bots start without a primary model unless you choose one.
                  </p>
                )}
              </div>
              <ModelPicker
                override={{
                  value: defaults.modelId ? { provider: defaults.provider, modelId: defaults.modelId } : null,
                  defaultName: "None",
                  clearLabel: "No default model",
                  save: async (selected) => {
                    await updateDefaults(
                      selected
                        ? { provider: selected.provider, modelId: selected.id }
                        : { provider: "", modelId: "", fallbackProvider: "", fallbackModelId: "" },
                      selected ? `Default model is now ${selected.name}.` : "Cleared the default model.",
                    );
                  },
                }}
                trigger={
                  <Button variant="secondary" aria-label="Default model">
                    {defaults.modelId ? "Change model" : "Choose model"}
                    <Chevron />
                  </Button>
                }
              />
            </div>

            <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[14px] font-medium text-foreground">
                  Thinking: {thinkingNames[defaults.thinkingLevel]}
                </p>
                <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                  {!primary?.model
                    ? "Choose an available default model to set the thinking level."
                    : primary.model.reasoning
                      ? "New bots start with this reasoning level."
                      : `${primary.model.name} does not support thinking.`}
                </p>
              </div>
              {primary?.model?.reasoning ? (
                <ThinkingPicker
                  value={defaults.thinkingLevel}
                  levels={primary.model.thinkingLevels}
                  save={async (level) => {
                    if (!level) return;
                    await updateDefaults(
                      { thinkingLevel: level },
                      `Default thinking is now ${thinkingNames[level]}.`,
                    );
                  }}
                  trigger={
                    <Button variant="secondary" aria-label="Default thinking">
                      Change thinking
                      <Chevron />
                    </Button>
                  }
                />
              ) : (
                <Button variant="secondary" disabled aria-label="Default thinking">
                  Change thinking
                </Button>
              )}
            </div>

            <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[14px] font-medium text-foreground">
                  {defaults.fallbackModelId ? `Fallback model: ${fallback?.label}` : "No fallback model"}
                </p>
                {defaults.fallbackModelId ? (
                  <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                    {defaults.fallbackProvider}/{defaults.fallbackModelId}
                  </p>
                ) : (
                  <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                    Used automatically when a bot&apos;s primary model is unavailable or fails before
                    answering.
                  </p>
                )}
              </div>
              <ModelPicker
                override={{
                  value: defaults.fallbackModelId
                    ? { provider: defaults.fallbackProvider, modelId: defaults.fallbackModelId }
                    : null,
                  defaultName: "None",
                  clearLabel: "No fallback model",
                  save: async (selected) => {
                    await updateDefaults(
                      selected
                        ? { fallbackProvider: selected.provider, fallbackModelId: selected.id }
                        : { fallbackProvider: "", fallbackModelId: "" },
                      selected
                        ? `Default fallback model is now ${selected.name}.`
                        : "Cleared the default fallback model.",
                    );
                  },
                }}
                trigger={
                  <Button
                    variant="secondary"
                    aria-label="Default fallback model"
                    disabled={!defaults.modelId}
                  >
                    {defaults.fallbackModelId ? "Change fallback" : "Choose fallback"}
                    <Chevron />
                  </Button>
                }
              />
            </div>

            <div className="border-t border-border px-4 py-3.5">
              <div className="flex flex-wrap items-center gap-3">
                <div className="min-w-0 flex-1">
                  <label htmlFor="default-environment" className="text-[14px] font-medium text-foreground">
                    Default execution environment
                  </label>
                  <p id="default-environment-hint" className="mt-0.5 text-[12.5px] text-muted-foreground">
                    {currentEnv
                      ? `${currentEnv.name} (${currentEnv.kind}).`
                      : missingEnv
                        ? `Configured environment (${defaults.environmentId}) is missing.`
                        : "Runs tools and workspaces on this computer."}{" "}
                    <Link className="font-medium text-primary underline" to="/settings/environment">
                      Manage environments
                    </Link>
                  </p>
                </div>
                <Select
                  id="default-environment"
                  aria-label="Default execution environment"
                  aria-describedby="default-environment-hint"
                  aria-invalid={save.isError || missingEnv}
                  className="w-full sm:w-[220px]"
                  value={defaults.environmentId}
                  disabled={save.isPending || environments.isPending}
                  onChange={(event) => {
                    const nextId = event.target.value;
                    const target = envList.find((env) => env.id === nextId);
                    void updateDefaults(
                      { environmentId: nextId },
                      target
                        ? `Default execution environment is now ${target.name}.`
                        : "Default execution environment is now this computer.",
                    ).catch(() => {});
                  }}
                >
                  <option value="">This computer</option>
                  {missingEnv ? (
                    <option value={defaults.environmentId}>Missing ({defaults.environmentId})</option>
                  ) : null}
                  {envList.map((env) => (
                    <option key={env.id} value={env.id}>
                      {environmentOptionLabel(env)}
                    </option>
                  ))}
                </Select>
              </div>
              {environments.isError ? (
                <div className="mt-3">
                  <Failure title="Could not load environments" error={environments.error} />
                </div>
              ) : null}
              {save.isError ? (
                <div className="mt-3">
                  <Failure title="Could not save bot defaults" error={save.error} />
                </div>
              ) : null}
            </div>

            {!settings.data.providers.length ? (
              <div className="border-t border-border px-4 py-3">
                <Note>
                  Add a provider first.{" "}
                  <Link className="font-medium text-primary underline" to="/settings/providers">
                    Go to Providers
                  </Link>
                </Note>
              </div>
            ) : null}
          </Panel>
        </Section>
      ) : null}
    </FramedPage>
  );
}
