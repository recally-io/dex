import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Settings } from "../api/generated/types.gen";
import { getHealthOptions, getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";
import { saveAgent, thinkingNames, useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ModelPicker } from "../components/model-picker";
import { ThinkingPicker } from "../components/thinking-picker";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Textarea } from "../components/form";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { cn } from "../lib/utils";

// Shows the page only with the saved settings; it has no fallback data.
export function WithSettings({ children }: { children: (settings: Settings) => ReactNode }) {
  const settings = useQuery(getSettingsOptions());

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

const tools = [
  ["read", "Reads a file"],
  ["write", "Writes a file"],
  ["edit", "Replaces text in a file"],
  ["bash", "Runs a shell command"],
] as const;

export function AgentPage() {
  const saved = useQuery(getSettingsOptions());
  const model = useAgentModel(saved.data);

  return (
    <FramedPage
      title="Agent"
      description="dex has one agent. Every chat, channel and automation uses these settings. A web chat can choose its own model and thinking level."
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

function Agent({ settings }: { settings: Settings }) {
  const { draft, setDraft, settleDraft, notify } = useApp();
  const model = useAgentModel(settings);
  const health = useQuery(getHealthOptions());
  const saved = settings.agent.instructions;
  const text = draft ?? saved;
  const dirty = draft !== null && draft !== saved;
  const provider = settings.providers.find((p) => p.id === settings.agent.provider);

  const save = useMutation({
    mutationFn: (instructions: string) => saveAgent((agent) => ({ ...agent, instructions })),
    onSuccess: (_, value) => {
      // Edits typed during the save stay as a new draft, also when they were typed on a new visit
      // to this page.
      const kept = settleDraft(value);

      notify(
        kept
          ? "Instructions saved. Your later edits are still an unsaved draft."
          : "Instructions saved. They apply from the next message.",
      );
    },
  });

  const dataDir = health.data?.dataDir;

  return (
    <>
      <Section
        n={1}
        title="Model"
        caption="The default of every chat. A change applies from the next message."
      >
        <Panel>
          <div className="flex flex-wrap items-center gap-3 px-4 py-3.5">
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
                  await saveAgent((agent) => ({ ...agent, thinkingLevel: level }));
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

      <Section
        n={2}
        title="Instructions"
        caption="Added to the system prompt of every run."
        action={
          dirty ? (
            <StatusPill tone="orange" className="h-5 px-2 text-[11.5px]">
              Draft
            </StatusPill>
          ) : (
            <StatusPill tone="green" className="h-5 px-2 text-[11.5px]">
              Saved
            </StatusPill>
          )
        }
      >
        <Panel className="p-1">
          <Textarea
            aria-label="Agent instructions"
            aria-describedby="instructions-status"
            rows={7}
            value={text}
            maxLength={32000}
            placeholder="For example: Reply in the language I write in."
            onChange={(e) => setDraft(e.target.value)}
            className="rounded-[11px] font-mono text-[12.5px] shadow-none focus:shadow-none"
          />
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-3 py-2">
            <span className="font-mono text-[11px] text-ink-3 tabular-nums">
              {text.length.toLocaleString()} / 32,000
            </span>
            <span id="instructions-status" aria-live="polite" className="min-w-0 text-[11.5px] text-ink-3">
              {dirty ? "Unsaved draft. Runs use the saved instructions until you save." : null}
            </span>
            <span className="flex-1" />
            <Button
              size="xs"
              variant="quiet"
              aria-label="Discard draft"
              disabled={!dirty || save.isPending}
              onClick={() => {
                save.reset();
                setDraft(null);
              }}
            >
              Discard
            </Button>
            <Button
              size="xs"
              variant="primary"
              aria-label="Save instructions"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate(text)}
            >
              {save.isPending ? <Spinner className="border-canvas/40 border-t-canvas" /> : null}
              {save.isPending ? "Saving" : "Save"}
            </Button>
          </div>
          {save.isError ? (
            <div className="border-t border-line p-3">
              <Failure title="Instructions not saved. Your draft is kept." error={save.error} />
            </div>
          ) : null}
        </Panel>
      </Section>

      <Section n={3} title="Tools" caption="Fixed. The agent always has these four.">
        <Panel>
          <Facts
            monoLabels
            rows={tools.map(([name, summary]) => [
              name,
              <span key={name} className="font-normal text-ink-2">
                {summary}
              </span>,
            ])}
          />
        </Panel>
        <div className="mt-3">
          <Note tone="warn">
            Tools run on this computer as your user, without a sandbox. The working directory is the
            workspace. dex does not ask before each call.
          </Note>
        </div>
      </Section>

      <Section n={4} title="Memory and workspace" caption="Shared by every chat and channel.">
        <Panel>
          <Facts
            rows={[
              ["Memory", <Mono key="m">{dataDir ? `${dataDir}/memory` : "memory"}</Mono>],
              ["Always in the prompt", <Mono key="i">MEMORY.md</Mono>],
              ["History", "A git repository. dex commits each change with the chat ID"],
              [
                "Workspace",
                <Mono key="w">
                  {dataDir ? `${dataDir}/agents/default/workspace` : "agents/default/workspace"}
                </Mono>,
              ],
              ["Channels", ["Web", ...(settings.telegram.enabled ? ["Telegram"] : [])].join(", ")],
            ]}
          />
        </Panel>
        <p className="mt-3 px-0.5 text-[12px] text-ink-3">
          dex has no memory editor yet. The agent reads and changes memory with its tools. Each chat shows
          these calls in its Context panel. Skills and MCP servers are{" "}
          <Link to="/extensions" className="font-medium text-accent-ink underline">
            not available yet
          </Link>
          .
        </p>
      </Section>
    </>
  );
}
