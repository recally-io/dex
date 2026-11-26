import type { BotSettings as Settings } from "../lib/bot-scope";
import { useBotSettings, useBotId } from "../lib/bot-scope";
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  listRunnersOptions,
  listModelsOptions,
  resetBotRunnerMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshRunners } from "../lib/queries";

import { findModel, saveBot, thinkingNames, useBotModel } from "../lib/bot";
import { useApp } from "../lib/app-state";
import { runnerBotHint, runnerOptionLabel } from "../lib/runners";
import { ModelPicker } from "../components/model-picker";
import { ThinkingPicker } from "../components/thinking-picker";
import { Failure, Loading, Spinner } from "../components/feedback";
import { FramedPage, Note, Panel, Section } from "../components/page";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Input, Select } from "../components/form";
import { InstructionsEditor } from "../components/instructions-editor";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { cn } from "../lib/utils";

function WithSettings({ children }: { children: (settings: Settings) => React.ReactNode }) {
  const botId = useBotId();
  const settings = useBotSettings(botId);

  return settings.isPending ? (
    <Loading />
  ) : settings.isError ? (
    <div className="px-5 py-8 sm:px-8">
      <Failure title="Could not load settings" error={settings.error} retry={() => void settings.refetch()} />
    </div>
  ) : (
    children(settings.data)
  );
}

export function BotPage() {
  const botId = useBotId();
  const saved = useBotSettings(botId);
  const models = useQuery(listModelsOptions());
  const model = useBotModel(saved.data);

  const fallback = saved.data
    ? findModel(saved.data.bot.fallbackProvider, saved.data.bot.fallbackModelId, models.data?.models)
    : undefined;

  return (
    <FramedPage
      title="General"
      description={`The name and the model of ${saved.data?.bot.name ?? "dex"}. The timeline, its channels and its automations use them.`}
      toolbar={<BotSettingsSwitcher segment="bot" />}
      badge={
        model && !model.ok && !fallback?.ok ? (
          <Badge variant="secondary" className="h-5 bg-orange-tint px-2 text-[11.5px] text-orange">
            No available model
          </Badge>
        ) : null
      }
    >
      <WithSettings>{(settings) => <Bot settings={settings} />}</WithSettings>
    </FramedPage>
  );
}

function Bot({ settings }: { settings: Settings }) {
  const botId = useBotId();
  const { notify } = useApp();
  const models = useQuery(listModelsOptions());
  const model = useBotModel(settings);

  const fallback = findModel(
    settings.bot.fallbackProvider,
    settings.bot.fallbackModelId,
    models.data?.models,
  );

  const provider = settings.providers.find((p) => p.id === settings.bot.provider);

  return (
    <>
      <Section title="Profile" caption="A change applies from the next message.">
        <Panel>
          <NameRow name={settings.bot.name} />
          <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p
                className={cn(
                  "truncate text-[14px] font-medium",
                  model?.ok ? "text-foreground" : "text-orange",
                )}
              >
                {settings.bot.modelId ? model?.label : "No model chosen"}
              </p>
              {settings.bot.modelId ? (
                <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                  {settings.bot.provider}/{settings.bot.modelId}
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
                  {settings.bot.modelId ? "Change model" : "Choose model"}
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
                Thinking: {thinkingNames[settings.bot.thinkingLevel]}
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
                value={settings.bot.thinkingLevel}
                levels={model.model.thinkingLevels}
                save={async (level) => {
                  if (!level) return;
                  await saveBot(botId, (bot) => ({ ...bot, thinkingLevel: level }));
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
          {!model?.ok && settings.bot.modelId ? (
            <div className="border-t border-border px-4 py-3">
              <Note tone="warn">
                {provider
                  ? `${provider.name} does not list this model now.`
                  : "The provider of this model is removed."}{" "}
                {fallback.ok
                  ? `Messages use the fallback model (${fallback.label}) until you choose an available primary model.`
                  : "New messages stay off until you choose an available model. Stop still works."}
              </Note>
            </div>
          ) : null}
          {!settings.providers.length ? (
            <div className="border-t border-border px-4 py-3">
              <Note>
                Add a provider first.{" "}
                <Link className="font-medium text-primary underline" to="/settings/providers">
                  Go to Providers
                </Link>
              </Note>
            </div>
          ) : null}
          <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p
                className={cn(
                  "truncate text-[14px] font-medium",
                  settings.bot.fallbackModelId && !fallback.ok ? "text-orange" : "text-foreground",
                )}
              >
                {settings.bot.fallbackModelId ? `Fallback: ${fallback.label}` : "No fallback model"}
              </p>
              {settings.bot.fallbackModelId ? (
                <p className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                  {settings.bot.fallbackProvider}/{settings.bot.fallbackModelId}
                </p>
              ) : (
                <p className="mt-0.5 text-[12.5px] text-muted-foreground">
                  Used automatically when the primary model is unavailable or fails before answering.
                </p>
              )}
            </div>
            <ModelPicker
              override={{
                value: settings.bot.fallbackModelId
                  ? {
                      provider: settings.bot.fallbackProvider,
                      modelId: settings.bot.fallbackModelId,
                    }
                  : null,
                defaultName: "None",
                clearLabel: "No fallback model",
                save: async (selected) => {
                  await saveBot(botId, (bot) => ({
                    ...bot,
                    fallbackProvider: selected?.provider ?? "",
                    fallbackModelId: selected?.id ?? "",
                  }));
                  notify(
                    selected
                      ? `Fallback model is now ${selected.name}. It applies from the next message.`
                      : "Cleared the fallback model.",
                  );
                },
              }}
              trigger={
                <Button variant="secondary" aria-label="Fallback model" disabled={!settings.bot.modelId}>
                  {settings.bot.fallbackModelId ? "Change fallback" : "Choose fallback"}
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
          <RunnerRow runnerId={settings.bot.runnerId} />
        </Panel>
      </Section>
      <div id="instructions">
        <Section
          title="Instructions"
          caption="Adds to this bot's system prompt. Changes apply from the next message."
        >
          <Panel className="p-1">
            <InstructionsEditor saved={settings.bot.instructions} rows={18} />
          </Panel>
        </Section>
      </div>
      <Section title="Archive" caption="Archived bots keep their sessions and settings.">
        <p className="text-sm text-muted-foreground">
          {botId === "default"
            ? "The default bot cannot be archived."
            : "To archive this bot, ask in Main and approve its confirmation question."}
        </p>
        <div className="mt-2 flex flex-wrap gap-4 text-sm">
          {botId !== "default" ? (
            <Link className="underline" to="/b/$botId" params={{ botId }}>
              Open Main
            </Link>
          ) : null}
          <Link className="underline" to="/settings/archived">
            Archived bots
          </Link>
        </div>
      </Section>
    </>
  );
}

function NameRow({ name }: { name: string }) {
  const botId = useBotId();
  const [draft, setDraft] = useState<string | null>(null);
  const value = draft ?? name;

  const save = useMutation({
    mutationFn: (nextName: string) => saveBot(botId, (bot) => ({ ...bot, name: nextName })),
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

function RunnerRow({ runnerId }: { runnerId: string }) {
  const botId = useBotId();
  const { notify } = useApp();
  const runners = useQuery(listRunnersOptions());
  const list = runners.data?.runners ?? [];
  const current = list.find((env) => env.id === runnerId);
  const isE2B = current?.kind === "e2b";

  const save = useMutation({
    mutationFn: (nextId: string) => saveBot(botId, (bot) => ({ ...bot, runnerId: nextId })),
    onSuccess: async (_, nextId) => {
      const target = list.find((env) => env.id === nextId);
      await refreshRunners();

      notify(
        target
          ? `Runner is now ${target.name}. Open sessions reload when idle.`
          : "Runner is now Local. Open sessions reload when idle.",
      );
    },
  });

  const resetSandbox = useMutation({
    ...resetBotRunnerMutation(),
    onSuccess: () => notify("Sandbox reset. The next message starts a fresh sandbox."),
    onError: (err) => notify(errorText(err)),
  });

  const missingSelection = !!runnerId && !runners.isPending && !current;

  return (
    <div className="border-t border-border px-4 py-3.5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <label htmlFor="bot-runner" className="text-[14px] font-medium text-foreground">
            Default Runner
          </label>
          <p id="bot-runner-hint" className="mt-0.5 text-[12.5px] text-muted-foreground">
            {current
              ? runnerBotHint(current)
              : missingSelection
                ? `Configured Runner (${runnerId}) is missing.`
                : "Runs tools and workspaces on this computer."}{" "}
            <Link className="font-medium text-primary underline" to="/settings/runners">
              Manage Runners
            </Link>
          </p>
        </div>
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
          {isE2B ? (
            <Button
              size="sm"
              variant="outline"
              disabled={resetSandbox.isPending}
              onClick={() => resetSandbox.mutate({ path: { botId } })}
            >
              {resetSandbox.isPending ? <Spinner /> : null}
              {resetSandbox.isPending ? "Resetting…" : "Reset sandbox"}
            </Button>
          ) : null}
          <Select
            id="bot-runner"
            aria-label="Default Runner"
            aria-describedby="bot-runner-hint"
            aria-invalid={save.isError || missingSelection}
            className="w-full sm:w-[220px]"
            value={runnerId}
            disabled={save.isPending || runners.isPending}
            onChange={(event) => save.mutate(event.target.value)}
          >
            <option value="">Local</option>
            {missingSelection ? <option value={runnerId}>Missing ({runnerId})</option> : null}
            {list.map((env) => (
              <option key={env.id} value={env.id}>
                {runnerOptionLabel(env)}
              </option>
            ))}
          </Select>
        </div>
      </div>
      {runners.isError ? (
        <div className="mt-3">
          <Failure title="Could not load Runners" error={runners.error} />
        </div>
      ) : null}
      {save.isError ? (
        <div className="mt-3">
          <Failure title="Could not save Runner" error={save.error} />
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
