import type { AgentSettings as Settings } from "../lib/agent-scope";
import { scopedSettingsOptions, useAgentId } from "../lib/agent-scope";
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";

import { saveAgent, thinkingNames, useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ModelPicker } from "../components/model-picker";
import { ThinkingPicker } from "../components/thinking-picker";
import { Failure } from "../components/feedback";
import { FramedPage, Note, Panel, Section } from "../components/page";
import { Input } from "../components/form";
import { InstructionsEditor } from "../components/instructions-editor";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { WithSettings } from "../components/with-settings";
import { cn } from "../lib/utils";

export function AgentPage() {
  const agentId = useAgentId();
  const saved = useQuery(scopedSettingsOptions(agentId));
  const model = useAgentModel(saved.data);

  return (
    <FramedPage
      title="Settings"
      description={`The name and the model of ${saved.data?.agent.name ?? "dex"}. The timeline, its channels and its automations use them.`}
      badge={
        model && !model.ok ? (
          <StatusPill tone="orange" className="h-5 px-2 text-[11.5px]">
            No available model
          </StatusPill>
        ) : null
      }
    >
      <WithSettings>{(settings) => <Agent settings={settings} />}</WithSettings>
    </FramedPage>
  );
}

export function InstructionsPage() {
  const agentId = useAgentId();
  const saved = useQuery(scopedSettingsOptions(agentId));

  return (
    <FramedPage
      title="Instructions"
      description={`Added to the system prompt of every run of ${saved.data?.agent.name ?? "dex"}. A change applies from the next message.`}
    >
      <WithSettings>
        {(settings) => (
          <div className="px-5 py-6 sm:px-8">
            <Panel className="p-1">
              <InstructionsEditor saved={settings.agent.instructions} rows={18} />
            </Panel>
          </div>
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
          <div className="flex flex-wrap items-center gap-3 border-t border-line px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p className={cn("truncate text-[14px] font-medium", model?.ok ? "text-ink" : "text-orange")}>
                {settings.agent.modelId ? model?.label : "No model chosen"}
              </p>
              {settings.agent.modelId ? (
                <p className="mt-0.5 truncate font-mono text-[11.5px] text-ink-3">
                  {settings.agent.provider}/{settings.agent.modelId}
                </p>
              ) : (
                <p className="mt-0.5 text-[12.5px] text-ink-3">dex cannot answer until you choose one.</p>
              )}
            </div>
            <ModelPicker
              trigger={
                <Button variant="secondary" size="sm">
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
                    className="text-ink-3"
                  >
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </Button>
              }
            />
          </div>
          <div className="flex flex-wrap items-center gap-3 border-t border-line px-4 py-3.5">
            <div className="min-w-0 flex-1">
              <p className="truncate text-[14px] font-medium text-ink">
                Thinking: {thinkingNames[settings.agent.thinkingLevel]}
              </p>
              <p className="mt-0.5 text-[12.5px] text-ink-3">
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
                  <Button variant="secondary" size="sm">
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
                      className="text-ink-3"
                    >
                      <path d="M6 9l6 6 6-6" />
                    </svg>
                  </Button>
                }
              />
            ) : (
              <Button variant="secondary" size="sm" disabled>
                Change thinking
              </Button>
            )}
          </div>
          {!model?.ok && settings.agent.modelId ? (
            <div className="border-t border-line px-4 py-3">
              <Note tone="warn">
                {provider
                  ? `${provider.name} does not list this model now.`
                  : "The provider of this model is removed."}{" "}
                New messages stay off until you choose an available model. Stop still works.
              </Note>
            </div>
          ) : null}
          {!settings.providers.length ? (
            <div className="border-t border-line px-4 py-3">
              <Note>
                Add a provider first.{" "}
                <Link className="font-medium text-accent-ink underline" to="/providers">
                  Go to Providers
                </Link>
              </Note>
            </div>
          ) : null}
        </Panel>
      </Section>
    </>
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
          <label htmlFor="bot-name" className="text-[14px] font-medium text-ink">
            Name
          </label>
          <p id="bot-name-hint" className="mt-0.5 text-[12.5px] text-ink-3">
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
