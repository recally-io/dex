import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { ChevronDown } from "lucide-react";
import type { BotSettings as Settings } from "../lib/bot-scope";
import { useBotId } from "../lib/bot-scope";
import type { Session, SessionSettings, Model, ThinkingLevel } from "../api/generated/types.gen";
import { setSessionSettings } from "../api/generated/sdk.gen";
import { listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { findModel, thinkingNames } from "../lib/bot";
import { refreshSessions } from "../lib/queries";
import { SessionActivity } from "./session-activity";
import { SessionRunner } from "./session-runner";
import { SessionProject } from "./session-project";
import { ModelPicker } from "./model-picker";
import { Button } from "./ui/button";

// A change keeps the other field. null clears a field so that it follows the bot again.
function changed(
  own: SessionSettings,
  change: { model: Model | null } | { thinking: ThinkingLevel | null },
): SessionSettings {
  const next: SessionSettings = {};

  const model =
    "model" in change ? change.model : own.modelId ? { provider: own.provider ?? "", id: own.modelId } : null;

  const thinking = "thinking" in change ? change.thinking : (own.thinkingLevel ?? null);

  if (model) {
    next.provider = model.provider;
    next.modelId = model.id;
  }

  if (thinking) next.thinkingLevel = thinking;

  return next;
}

// Task overrides apply from the next message. An unset field follows the bot.
export function SessionSettings({
  session,
  settings,
  started,
}: {
  session: Session;
  settings: Settings;
  started: boolean;
}) {
  const botId = useBotId();
  const models = useQuery(listModelsOptions());
  const saving = useRef(false);
  const [pending, setPending] = useState(false);
  const own = session.settings;
  const bot = findModel(settings.bot.provider, settings.bot.modelId, models.data?.models);
  const effective = own.modelId ? findModel(own.provider ?? "", own.modelId, models.data?.models) : bot;

  async function change(next: SessionSettings) {
    if (saving.current) throw new Error("Session settings are saving. Try again.");
    saving.current = true;
    setPending(true);

    try {
      await setSessionSettings({ path: { botId, sessionId: session.id }, body: next, throwOnError: true });
      await refreshSessions(botId);
    } finally {
      saving.current = false;
      setPending(false);
    }
  }

  return (
    <div
      role="region"
      aria-label="Task settings"
      className="flex min-w-0 items-center gap-0.5 overflow-hidden"
    >
      <SessionRunner session={session} started={started} />
      <SessionProject sessionId={session.id} cwd={session.cwd} runnerId={session.runnerId} />
      <SessionActivity sessionId={session.id} writable={!session.readOnly} />
      <ModelPicker
        side="top"
        thinking={{
          value: own.thinkingLevel ?? null,
          defaultValue: settings.bot.thinkingLevel,
          levels: effective.model?.reasoning ? effective.model.thinkingLevels : [],
          pending,
          save: (thinking) => change(changed(own, { thinking })),
        }}
        override={{
          value: own.modelId ? { provider: own.provider ?? "", modelId: own.modelId } : null,
          defaultName: bot.label,
          save: (model: Model | null) => change(changed(own, { model })),
        }}
        trigger={
          <Button
            variant="ghost"
            size="sm"
            className="min-w-0 max-w-56 flex-initial gap-1 px-1.5 sm:max-w-80"
            aria-label="Set model"
            disabled={pending}
            title={
              effective.model?.reasoning
                ? `${effective.label} · ${thinkingNames[own.thinkingLevel ?? settings.bot.thinkingLevel]}`
                : effective.label
            }
          >
            <span className="truncate">
              {effective.label}
              {effective.model?.reasoning
                ? ` · ${thinkingNames[own.thinkingLevel ?? settings.bot.thinkingLevel]}`
                : ""}
            </span>
            <ChevronDown aria-hidden="true" />
          </Button>
        }
      />
    </div>
  );
}
