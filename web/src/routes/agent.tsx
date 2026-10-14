import { useState, type ReactNode } from "react";
import { Link, type LinkProps } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight } from "lucide-react";
import type { Settings } from "../api/generated/types.gen";
import { getSettingsOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { saveAgent, useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { ModelDialog } from "../components/model-dialog";
import { Failure, Loading } from "../components/feedback";
import { Card, Note, NotAvailable, Page, PageHeader, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { Textarea } from "../components/ui/textarea";
import { Button as Pill } from "../components/atoms/Button";
import { ValuePill } from "../components/atoms/ValuePill";
import { cn } from "../lib/utils";

// Shows the page only with the saved settings; it has no fallback data.
export function WithSettings({ children }: { children: (settings: Settings) => ReactNode }) {
  const settings = useQuery(getSettingsOptions());

  return settings.isPending ? (
    <Loading />
  ) : settings.isError ? (
    <Failure title="Could not load settings" error={settings.error} retry={() => void settings.refetch()} />
  ) : (
    children(settings.data)
  );
}

function Step({
  label,
  value,
  to,
  warn,
}: {
  label: string;
  value: string;
  to: LinkProps["to"];
  warn: boolean;
}) {
  return (
    <Link
      to={to}
      className="flex min-w-0 flex-1 flex-col rounded-xl border border-line bg-surface px-4 py-3 hover:bg-hover focus-visible:outline-2 focus-visible:outline-ink"
    >
      <span className="text-xs text-ink-3">{label}</span>
      <span className={cn("truncate text-sm font-medium", warn ? "text-orange" : "text-ink")}>{value}</span>
    </Link>
  );
}

// Providers → available models → agent model, as one chain.
export function ModelChain({ settings }: { settings: Settings }) {
  const models = useQuery(listModelsOptions());
  const model = useAgentModel(settings);
  const count = models.data?.models.length;

  return (
    <div
      role="group"
      aria-label="How the agent gets a model"
      className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center"
    >
      <Step
        label="Providers"
        value={`${settings.providers.length} saved`}
        to="/providers"
        warn={!settings.providers.length}
      />
      <ArrowRight aria-hidden className="hidden size-4 shrink-0 text-ink-3 sm:block" />
      <Step
        label="Available models"
        value={count === undefined ? (models.isError ? "Could not load" : "Loading…") : `${count} models`}
        to="/providers"
        warn={count === 0 || models.isError}
      />
      <ArrowRight aria-hidden className="hidden size-4 shrink-0 text-ink-3 sm:block" />
      <Step
        label="Agent model"
        value={settings.agent.modelId ? (model?.label ?? "") : "Not chosen"}
        to="/agent"
        warn={!model?.ok}
      />
    </div>
  );
}

export function AgentPage() {
  return (
    <Page>
      <PageHeader
        title="Agent"
        description="dex runs one agent. Every chat, channel and automation uses the same model, instructions, tools and memory."
      />
      <WithSettings>{(settings) => <Agent settings={settings} />}</WithSettings>
    </Page>
  );
}

function Agent({ settings }: { settings: Settings }) {
  const { draft, setDraft, settleDraft, notify } = useApp();
  const [picker, setPicker] = useState(false);
  const model = useAgentModel(settings);
  const saved = settings.agent.instructions;
  const text = draft ?? saved;
  const dirty = draft !== null && draft !== saved;

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

  const channels = ["Web", ...(settings.telegram.enabled ? ["Telegram"] : [])];

  return (
    <>
      <Section title="Model" description="The model that answers every run.">
        <Card>
          <div className="flex flex-col gap-4">
            <ModelChain settings={settings} />
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0 text-sm">
                {!settings.agent.modelId ? (
                  <p className="text-orange">No model chosen. dex cannot answer until you choose one.</p>
                ) : model?.ok ? (
                  <p className="text-ink-2">
                    Uses <span className="font-medium text-ink">{model.label}</span>{" "}
                    <span className="break-all font-mono text-xs">
                      {settings.agent.provider}/{settings.agent.modelId}
                    </span>
                  </p>
                ) : (
                  <p className="text-orange">
                    {settings.agent.provider}/{settings.agent.modelId} is not in the available models. Choose
                    another model.
                  </p>
                )}
              </div>
              <Pill variant="primary" size="sm" onClick={() => setPicker(true)}>
                {settings.agent.modelId ? "Change model" : "Choose model"}
              </Pill>
            </div>
            {!settings.providers.length ? (
              <Note>
                Add a provider first.{" "}
                <Link className="text-brand underline" to="/providers">
                  Go to Providers
                </Link>
              </Note>
            ) : null}
          </div>
        </Card>
      </Section>

      <Section
        title="Instructions"
        description="Added to every run. A draft stays here when you change the model or open another page."
      >
        <Card>
          <label htmlFor="instructions" className="sr-only">
            Agent instructions
          </label>
          <Textarea
            id="instructions"
            value={text}
            rows={8}
            className="font-mono text-[13px] leading-6"
            placeholder="For example: Reply in the language I write in."
            aria-describedby="instructions-status"
            onChange={(e) => setDraft(e.target.value)}
          />
          <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
            <p id="instructions-status" className="text-xs text-ink-3" aria-live="polite">
              {dirty ? "Unsaved draft. Runs use the saved instructions until you save." : "Saved."}{" "}
              {text.length.toLocaleString()} characters
            </p>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={!dirty || save.isPending}
                onClick={() => {
                  save.reset();
                  setDraft(null);
                }}
              >
                Discard draft
              </Button>
              <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate(text)}>
                {save.isPending ? "Saving…" : "Save instructions"}
              </Button>
            </div>
          </div>
          {save.isError ? (
            <div className="mt-3">
              <Failure title="Instructions not saved. Your draft is kept." error={save.error} />
            </div>
          ) : null}
        </Card>
      </Section>

      <Section
        title="What the agent can use"
        description="A read-only summary. Change each part on its own page."
      >
        <div className="grid gap-3 md:grid-cols-2">
          <Card>
            <h3 className="mb-2 text-sm font-semibold">Built-in tools</h3>
            <p className="mb-3 text-[13px] text-ink-2">
              The agent runs these tools on this computer without a sandbox, in its workspace folder. dex does
              not ask before each call.
            </p>
            <div className="flex flex-wrap gap-1.5">
              {["read", "write", "edit", "bash"].map((tool) => (
                <ValuePill key={tool}>
                  <span className="font-mono">{tool}</span>
                </ValuePill>
              ))}
            </div>
          </Card>
          <Card>
            <h3 className="mb-2 text-sm font-semibold">Memory</h3>
            <p className="text-[13px] text-ink-2">
              One memory, a git repository in the data folder, shared by every chat and channel. A fact
              learned in Telegram can appear in a web chat. dex has no memory page yet.
            </p>
          </Card>
          <Card>
            <div className="mb-2 flex items-center gap-2">
              <h3 className="text-sm font-semibold">
                <Link to="/extensions" className="hover:underline">
                  Extensions
                </Link>
              </h3>
              <NotAvailable />
            </div>
            <p className="text-[13px] text-ink-2">
              Skills and MCP servers are not available in this version. The agent uses only its built-in
              tools.
            </p>
          </Card>
          <Card>
            <h3 className="mb-2 text-sm font-semibold">
              <Link to="/channels" className="hover:underline">
                Who can reach it
              </Link>
            </h3>
            <p className="mb-3 text-[13px] text-ink-2">
              Every channel talks to this agent. There is no per-channel or per-chat model.
            </p>
            <div className="flex flex-wrap gap-1.5">
              {channels.map((channel) => (
                <ValuePill key={channel}>{channel}</ValuePill>
              ))}
            </div>
          </Card>
        </div>
      </Section>
      <ModelDialog open={picker} onOpenChange={setPicker} />
    </>
  );
}
