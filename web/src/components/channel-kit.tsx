import type { ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Feather, MessageCircle, Send, type LucideIcon } from "lucide-react";
import {
  getNotificationTargetsOptions,
  setPrimaryNotificationTargetMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { Crumb } from "./crumb";
import { Failure } from "./feedback";
import { FramedPage, Mono, Note, Panel, Section } from "./page";
import { BotSettingsSwitcher } from "./settings-switcher";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

// Every channel page uses this frame and the same sections in the same order: Connection, Who can
// use it, Notifications, How to use, Turn off. The channel files own only the first two.

export type ChannelId = "telegram" | "wechat" | "feishu";

export const channelInfo: Record<ChannelId, { name: string; icon: LucideIcon }> = {
  telegram: { name: "Telegram", icon: Send },
  wechat: { name: "WeChat", icon: MessageCircle },
  feishu: { name: "Feishu / Lark", icon: Feather },
};

export function ChannelIcon({ channel }: { channel: ChannelId }) {
  const Icon = channelInfo[channel].icon;

  return (
    <span
      data-channel-icon={channel}
      className="flex size-8 shrink-0 items-center justify-center rounded-card bg-muted text-muted-foreground shadow-card"
    >
      <Icon size={16} aria-hidden />
    </span>
  );
}

// Feishu reports needs_login until an owner pairs. WeChat reports it when the saved login expires.
function stateLabel(channel: ChannelId, state: string) {
  if (state === "needs_login") return channel === "feishu" ? "Needs owner" : "Scan again";

  return { running: "Connected", off: "Off", error: "Error" }[state] ?? state;
}

export function ChannelState({ channel, state }: { channel: ChannelId; state: string }) {
  return (
    <Badge
      variant={state === "running" ? "secondary" : state === "error" ? "destructive" : "outline"}
      className={`h-5 shrink-0 px-2 text-[11.5px] ${state === "running" ? "bg-green-tint text-green" : ""}`}
    >
      {stateLabel(channel, state)}
    </Badge>
  );
}

export function ChannelFrame({
  channel,
  state,
  description,
  children,
}: {
  channel: ChannelId;
  state: string | undefined;
  description: string;
  children: ReactNode;
}) {
  return (
    <FramedPage
      eyebrow={<Crumb to="/a/$agentId/channels">Channels</Crumb>}
      title={channelInfo[channel].name}
      badge={state ? <ChannelState channel={channel} state={state} /> : null}
      description={description}
      toolbar={<BotSettingsSwitcher segment="channels" />}
    >
      {children}
    </FramedPage>
  );
}

/** Numbered steps for a channel that is not connected yet. */
export function Steps({ children }: { children: ReactNode }) {
  return (
    <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-[13px] text-muted-foreground marker:font-mono marker:text-[11px] marker:text-muted-foreground">
      {children}
    </ol>
  );
}

/** Lets this channel's owner receive routine results. */
export function Notifications({ channel }: { channel: ChannelId }) {
  const agentId = useAgentId();

  const targets = useQuery({
    ...getNotificationTargetsOptions({ path: { agentId } }),
    refetchInterval: 3000,
  });

  const primary = useMutation({
    ...setPrimaryNotificationTargetMutation(),
    onSuccess: () => targets.refetch(),
  });

  const own = targets.data?.targets.filter((target) => target.platform === channel) ?? [];

  return (
    <Section n={3} title="Notifications" caption="Routines send results to the primary channel.">
      <Panel>
        {own.length ? (
          <ul className="divide-y divide-border">
            {own.map((target) => (
              <li key={target.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <Mono className="min-w-0 flex-1 [overflow-wrap:anywhere]">{target.peer}</Mono>
                {targets.data?.primaryId === target.id ? (
                  <Badge variant="secondary" className="h-5 bg-primary/10 px-2 text-[11.5px] text-primary">
                    Primary
                  </Badge>
                ) : (
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={primary.isPending}
                    onClick={() => primary.mutate({ path: { agentId }, body: { targetId: target.id } })}
                  >
                    Make primary
                  </Button>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="px-4 py-3.5 text-[13px] text-muted-foreground">
            {targets.isPending ? "Loading…" : "After an owner pairs, routines can send results here."}
          </p>
        )}
      </Panel>
      {targets.isError || primary.isError ? (
        <div className="mt-3">
          <Failure title="Could not update notifications" error={targets.error ?? primary.error} />
        </div>
      ) : null}
    </Section>
  );
}

/** The commands and limits of a channel, collapsed. Extra items name what only this channel does. */
export function HowToUse({ channel, children }: { channel: ChannelId; children?: ReactNode }) {
  const name = channelInfo[channel].name;

  return (
    <Section n={4} title="How to use">
      <Panel className="px-4 py-3.5">
        <details>
          <summary className="cursor-pointer text-[13px] text-foreground">Commands and limits</summary>
          <ul className="mt-3 flex list-disc flex-col gap-1.5 pl-5 text-[13px] text-muted-foreground marker:text-muted-foreground">
            <li>
              <Mono>/clear</Mono> clears the main context when main is idle. It takes no text.{" "}
              <Mono>/new</Mono> also works.
            </li>
            <li>
              <Mono>/stop</Mono> stops the main answer and its background work.
            </li>
            <li>
              <Mono>/help</Mono> lists the commands.
            </li>
            <li>
              {name} and the web share the main timeline. dex replies where the input came from. The web shows
              all main input and tool calls.
            </li>
            {children}
          </ul>
        </details>
      </Panel>
      <div className="mt-3">
        <Note tone="warn">
          <strong className="font-medium">Channels are for the owner.</strong> Use only your own accounts. The
          bot can read and change files and run commands on this computer.
        </Note>
      </div>
    </Section>
  );
}

export function TurnOff({ channel, onTurnOff }: { channel: ChannelId; onTurnOff: () => void }) {
  return (
    <Section n={5} title="Turn off" caption="The bot stops answering here. The main timeline stays.">
      <Button size="sm" variant="secondary" className="text-red" onClick={onTurnOff}>
        Turn off {channelInfo[channel].name}
      </Button>
    </Section>
  );
}
