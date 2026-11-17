import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useAgentSettings, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  getMainChatOptions,
  listEnvironmentsOptions,
  resetAgentEnvironmentMutation,
  setChatSettingsMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshChats, refreshEnvironments } from "../lib/queries";

import { saveAgent, thinkingNames, useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ModelPicker } from "../components/model-picker";
import { ThinkingPicker } from "../components/thinking-picker";
import { Failure, Spinner } from "../components/feedback";
import { FramedPage, Note, Panel, Section } from "../components/page";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Input, Select } from "../components/form";
import { InstructionsEditor } from "../components/instructions-editor";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { WithSettings } from "../components/with-settings";
import { cn } from "../lib/utils";

export function AgentPage() {
  const agentId = useAgentId();
  const saved = useAgentSettings(agentId);
  const model = useAgentModel(saved.data);

  return (
    <FramedPage
      title="General"
      description={`The name and the model of ${saved.data?.agent.name ?? "dex"}. The timeline, its channels and its routines use them.`}
      toolbar={<BotSettingsSwitcher segment="agent" />}
      badge={
        model && !model.ok ? (
          <Badge variant="secondary" className="h-5 bg-orange-tint px-2 text-[11.5px] text-orange">
            No available model
          </Badge>
        ) : null
      }
    >
      <WithSettings>{(settings) => <Agent settings={settings} />}</WithSettings>
    </FramedPage>
  );
}

export function InstructionsPage() {
  const agentId = useAgentId();
  const saved = useAgentSettings(agentId);

  return (
    <FramedPage
      title="Instructions"
      description={`Added to the system prompt of every run of ${saved.data?.agent.name ?? "dex"}. A change applies from the next message.`}
      toolbar={<BotSettingsSwitcher segment="instructions" />}
    >
      <WithSettings>
        {(settings) => (
          <Panel className="p-1">
            <InstructionsEditor saved={settings.agent.instructions} rows={18} />
          </Panel>
        )}
      </WithSettings>
    </FramedPage>
  );
}

function Agent({ settings }: { settings: Settings }) {
  const agentId = useAgentId();
  const { notify } = useApp();
  const model = useAgentModel(settings);
  const provider = settings.providers.find((p) => p.id === settings.agent.provider);

  return (
    <>
      <Section n={1} title="Profile" caption="A change applies from the next message.">
        <Panel>
          <NameRow name={settings.agent.name} />
          <MainDefaults />
          <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p
                className={cn(
                  "truncate text-[14px] font-medium",
                  model?.ok ? "text-foreground" : "text-orange",
                )}
              >
                {settings.agent.modelId ? model?.label : "No model chosen"}
              </p>
              {settings.agent.modelId ? (
                <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                  {settings.agent.provider}/{settings.agent.modelId}
                </p>
              ) : (
                <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                  dex cannot answer until you choose one.
                </p>
              )}
            </div>
            <ModelPicker
              trigger={
                <Button variant="secondary">
                  {settings.agent.modelId ? "Change model" : "Choose model"}
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
                </Button>
              }
            />
          </div>
          <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p className="truncate text-[14px] font-medium text-foreground">
                Thinking: {thinkingNames[settings.agent.thinkingLevel]}
              </p>
              <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                {!model?.model
                  ? "Choose an available model to set the thinking level."
                  : model.model.reasoning
                    ? "The provider uses the nearest level that the model supports."
                    : `${model.model.name} does not support thinking.`}
              </p>
            </div>
            {model?.model?.reasoning ? (
              <ThinkingPicker
                value={settings.agent.thinkingLevel}
                levels={model.model.thinkingLevels}
                save={async (level) => {
                  if (!level) return;
                  await saveAgent(agentId, (agent) => ({ ...agent, thinkingLevel: level }));
                  notify(`Thinking is now ${thinkingNames[level]}. It applies from the next message.`);
                }}
                trigger={
                  <Button variant="secondary">
                    Change thinking
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
                  </Button>
                }
              />
            ) : (
              <Button variant="secondary" disabled>
                Change thinking
              </Button>
            )}
          </div>
          {!model?.ok && settings.agent.modelId ? (
            <div className="border-t border-border px-4 py-3">
              <Note tone="warn">
                {provider
                  ? `${provider.name} does not list this model now.`
                  : "The provider of this model is removed."}{" "}
                New messages stay off until you choose an available model. Stop still works.
              </Note>
            </div>
          ) : null}
          {!settings.providers.length ? (
            <div className="border-t border-border px-4 py-3">
              <Note>
                Add a provider first.{" "}
                <Link className="font-medium text-primary underline" to="/providers">
                  Go to Providers
                </Link>
              </Note>
            </div>
          ) : null}
          <ExecutionEnvironmentRow environmentId={settings.agent.environmentId} />
        </Panel>
      </Section>
    </>
  );
}

// Older versions let Main keep session overrides. Keep the reset next to the Bot defaults.
function MainDefaults() {
  const agentId = useAgentId();
  const main = useQuery(getMainChatOptions({ path: { agentId } }));

  const reset = useMutation({
    ...setChatSettingsMutation(),
    onSuccess: () => refreshChats(agentId),
  });

  if (!main.data || !(main.data.settings.modelId || main.data.settings.thinkingLevel)) return null;

  return (
    <div className="space-y-2 border-t border-border px-4 py-3">
      <Note tone="warn">
        Main has saved model or thinking overrides. It does not follow all Bot defaults.
      </Note>
      <Button
        variant="outline"
        size="sm"
        aria-label="Use Bot defaults for Main"
        disabled={reset.isPending}
        onClick={() => reset.mutate({ path: { agentId, chatId: main.data!.id }, body: {} })}
      >
        Use Bot defaults for Main
      </Button>
      {reset.isError ? <Failure title="Could not reset Main settings" error={reset.error} /> : null}
    </div>
  );
}

function NameRow({ name }: { name: string }) {
  const agentId = useAgentId();
  const [draft, setDraft] = useState<string | null>(null);
  const value = draft ?? name;

  const save = useMutation({
    mutationFn: (nextName: string) => saveAgent(agentId, (agent) => ({ ...agent, name: nextName })),
    onSuccess: () => setDraft(null),
  });

  function submit() {
    if (value.trim() === name) {
      setDraft(null);

      return;
    }

    if (!save.isPending) save.mutate(value);
  }

  return (
    <div className="px-4 py-3.5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <label htmlFor="bot-name" className="text-[14px] font-medium text-foreground">
            Name
          </label>
          <p id="bot-name-hint" className="mt-0.5 text-[12.5px] text-muted-foreground">
            Shown in the sidebar and the composer.
          </p>
        </div>
        <Input
          id="bot-name"
          aria-describedby="bot-name-hint"
          aria-invalid={save.isError}
          className="w-full sm:w-[220px]"
          value={value}
          disabled={save.isPending}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={submit}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.nativeEvent.isComposing) event.currentTarget.blur();
          }}
        />
      </div>
      {save.isError ? (
        <div className="mt-3">
          <Failure title="Could not save name" error={save.error} />
        </div>
      ) : null}
    </div>
  );
}

function ExecutionEnvironmentRow({ environmentId }: { environmentId: string }) {
  const agentId = useAgentId();
  const { notify } = useApp();
  const environments = useQuery(listEnvironmentsOptions());
  const list = environments.data?.environments ?? [];
  const current = list.find((env) => env.id === environmentId);
  const isE2B = current?.kind === "e2b";

  const save = useMutation({
    mutationFn: (nextId: string) => saveAgent(agentId, (agent) => ({ ...agent, environmentId: nextId })),
    onSuccess: async (_, nextId) => {
      const target = list.find((env) => env.id === nextId);
      await refreshEnvironments();

      notify(
        target
          ? `Execution environment is now ${target.name}. Open sessions reload when idle.`
          : "Execution environment is now Local. Open sessions reload when idle.",
      );
    },
  });

  const resetSandbox = useMutation({
    ...resetAgentEnvironmentMutation(),
    onSuccess: () => notify("E2B sandbox reset. The next message starts a fresh sandbox."),
    onError: (err) => notify(errorText(err)),
  });

  const missingSelection = !!environmentId && !environments.isPending && !current;

  return (
    <div className="border-t border-border px-4 py-3.5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <label htmlFor="bot-environment" className="text-[14px] font-medium text-foreground">
            Execution environment
          </label>
          <p id="bot-environment-hint" className="mt-0.5 text-[12.5px] text-muted-foreground">
            {current
              ? `Runs tools in ${current.name} (${current.kind}).`
              : missingSelection
                ? `Configured environment (${environmentId}) is missing.`
                : "Runs tools and workspaces on this computer."}{" "}
            <Link className="font-medium text-primary underline" to="/environment">
              Manage environments
            </Link>
          </p>
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
          {isE2B ? (
            <Button
              size="sm"
              variant="outline"
              disabled={resetSandbox.isPending}
              onClick={() => resetSandbox.mutate({ path: { agentId } })}
            >
              {resetSandbox.isPending ? <Spinner /> : null}
              {resetSandbox.isPending ? "Resetting…" : "Reset sandbox"}
            </Button>
          ) : null}
          <Select
            id="bot-environment"
            aria-label="Execution environment"
            aria-describedby="bot-environment-hint"
            aria-invalid={save.isError || missingSelection}
            className="w-full sm:w-[220px]"
            value={environmentId}
            disabled={save.isPending || environments.isPending}
            onChange={(event) => save.mutate(event.target.value)}
          >
            <option value="">Local (this computer)</option>
            {missingSelection ? <option value={environmentId}>Missing ({environmentId})</option> : null}
            {list.map((env) => (
              <option key={env.id} value={env.id}>
                {env.name} ({env.kind})
              </option>
            ))}
          </Select>
        </div>
      </div>
      {environments.isError ? (
        <div className="mt-3">
          <Failure title="Could not load environments" error={environments.error} />
        </div>
      ) : null}
      {save.isError ? (
        <div className="mt-3">
          <Failure title="Could not save execution environment" error={save.error} />
        </div>
      ) : null}
      {resetSandbox.isError ? (
        <div className="mt-3">
          <Failure title="Could not reset sandbox" error={resetSandbox.error} />
        </div>
      ) : null}
    </div>
  );
}
