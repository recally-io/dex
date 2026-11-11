import type { Channels as Settings } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";

import {
  createTelegramPairingMutation,
  deleteTelegramMutation,
  getChannelsOptions,
  getNotificationTargetsOptions,
  setPrimaryNotificationTargetMutation,
  setTelegramMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshSettings } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { WechatChannel } from "../components/wechat-channel";
import { Confirm } from "../components/confirm";
import { Modal } from "../components/dialog";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Field, Input, SavedSecret, Select, Textarea } from "../components/form";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Button } from "../components/ui/button";
import {
  ChannelFrame,
  ChannelIcon,
  ChannelState,
  HowToUse,
  Notifications,
  Steps,
  TurnOff,
  channelInfo,
  type ChannelId,
} from "../components/channel-kit";
import { FeishuChannel } from "./feishu";

function ChannelRow({ channel, text, state }: { channel: ChannelId; text: string; state: string }) {
  const agentId = useAgentId();

  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-3">
      <ChannelIcon channel={channel} />
      <span className="min-w-0 flex-1">
        <Link
          to="/a/$agentId/channels/$channelId"
          params={{ agentId, channelId: channel }}
          className="block w-fit text-[13.5px] font-medium text-foreground hover:underline"
        >
          {channelInfo[channel].name}
        </Link>
        <span className="block text-[12px] text-muted-foreground">{text}</span>
      </span>
      <ChannelState channel={channel} state={state} />
      <ChevronRight size={16} className="shrink-0 text-muted-foreground" />
    </div>
  );
}

export function ChannelsSection({ settings }: { settings: Settings }) {
  const agentId = useAgentId();
  const saved = useQuery({ ...getChannelsOptions({ path: { agentId } }), refetchInterval: 3000 });
  const current = saved.data ?? settings;

  const targets = useQuery({
    ...getNotificationTargetsOptions({ path: { agentId } }),
    refetchInterval: 3000,
  });

  const primary = useMutation({
    ...setPrimaryNotificationTargetMutation(),
    onSuccess: () => targets.refetch(),
  });

  return (
    <Section n={1} title="Connections" caption="Each channel talks to the same bot.">
      <Panel>
        <div className="divide-y divide-border">
          <ChannelRow
            channel="telegram"
            text="Private messages from allowed people."
            state={current.telegram.status.state}
          />
          <ChannelRow
            channel="wechat"
            text="Private messages from the paired owner."
            state={current.wechat.status.state}
          />
          <ChannelRow
            channel="feishu"
            text="Private messages from the paired owner."
            state={current.feishu.status}
          />
        </div>
        <div className="border-t border-border p-4">
          <Field
            label="Primary notification channel"
            hint="Routines can follow this default or choose another authorized recipient."
          >
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                disabled={!targets.data || primary.isPending}
                value={targets.data?.primaryId ?? ""}
                onChange={(event) =>
                  primary.mutate({ path: { agentId }, body: { targetId: event.target.value } })
                }
              >
                <option value="">None</option>
                {targets.data?.targets.map((target) => (
                  <option key={target.id} value={target.id}>
                    {target.platform}: {target.peer}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {targets.isError || primary.isError ? (
            <p className="text-red">Could not update notification targets.</p>
          ) : null}
        </div>
      </Panel>
    </Section>
  );
}

export function ChannelsPage() {
  return (
    <FramedPage
      title="Channels"
      description="Channels share the main timeline. Open one to set it up."
      toolbar={<BotSettingsSwitcher segment="channels" />}
    >
      <WithChannels>{(settings) => <ChannelsSection settings={settings} />}</WithChannels>
    </FramedPage>
  );
}

const descriptions: Record<ChannelId, string> = {
  telegram: "Talk to dex from your phone through your own Telegram bot.",
  wechat: "Talk to dex in a private WeChat chat. Scan once to connect.",
  feishu: "Talk to dex in a private Feishu or Lark chat.",
};

export function ChannelPage({ channel }: { channel: ChannelId }) {
  const agentId = useAgentId();
  const saved = useQuery({ ...getChannelsOptions({ path: { agentId } }), refetchInterval: 3000 });
  const data = saved.data;
  const state = data && (channel === "feishu" ? data.feishu.status : data[channel].status.state);

  return (
    <ChannelFrame channel={channel} state={state} description={descriptions[channel]}>
      {data ? (
        channel === "telegram" ? (
          <Telegram settings={data} />
        ) : channel === "wechat" ? (
          <WechatChannel settings={data} />
        ) : (
          <FeishuChannel />
        )
      ) : saved.isError ? (
        <Failure title="Could not load channels" error={saved.error} retry={() => void saved.refetch()} />
      ) : (
        <Loading />
      )}
    </ChannelFrame>
  );
}

function Telegram({ settings }: { settings: Settings }) {
  const agentId = useAgentId();
  const { notify } = useApp();
  const telegram = settings.telegram;
  const [edit, setEdit] = useState(false);
  const [off, setOff] = useState(false);
  const pairing = useMutation(createTelegramPairingMutation({ path: { agentId } }));

  const deletion = useMutation({
    ...deleteTelegramMutation({ path: { agentId } }),
    onSuccess: async () => {
      await refreshSettings();
      setOff(false);
      notify("Telegram is off. dex forgot the bot token.");
    },
  });

  return (
    <>
      <Section
        n={1}
        title="Connection"
        action={
          telegram.enabled ? (
            <Button size="sm" variant="secondary" onClick={() => setEdit(true)}>
              Edit
            </Button>
          ) : null
        }
      >
        {telegram.enabled ? (
          <Panel>
            <Facts
              rows={[
                ["Bot ID", <Mono key="bot">{telegram.botId ?? "Unknown"}</Mono>],
                [
                  "Bot token",
                  <span key="token" className="text-muted-foreground">
                    Saved · write-only
                  </span>,
                ],
              ]}
            />
          </Panel>
        ) : (
          <Panel className="px-4 py-3.5">
            <Steps>
              <li>
                In Telegram, open <Mono>@BotFather</Mono> and send <Mono>/newbot</Mono>.
              </li>
              <li>Copy the token that BotFather gives you.</li>
              <li>Select Connect bot and enter the token.</li>
            </Steps>
            <Button className="mt-4" size="sm" onClick={() => setEdit(true)}>
              Connect bot
            </Button>
          </Panel>
        )}
        {telegram.status.reason ? (
          <div className="mt-3">
            <Note tone={telegram.status.state === "error" ? "warn" : "neutral"}>
              {telegram.status.reason}
            </Note>
          </div>
        ) : null}
      </Section>
      {telegram.enabled ? (
        <>
          <Section n={2} title="Who can use it" caption="dex ignores messages from other Telegram users.">
            {telegram.allowedUsers.length ? (
              <Panel>
                <Facts
                  rows={[
                    [
                      "Allowed user IDs",
                      <span key="users" className="font-mono text-[12px] text-muted-foreground tabular-nums">
                        {telegram.allowedUsers.join(", ")}
                      </span>,
                    ],
                  ]}
                />
              </Panel>
            ) : (
              <Panel className="flex flex-col items-start gap-3 px-4 py-3.5">
                <p className="text-[13px] text-muted-foreground">
                  Pair your account to start. The bot ignores messages until an owner is paired.
                </p>
                <Button
                  size="sm"
                  disabled={pairing.isPending}
                  onClick={() => pairing.mutate({ path: { agentId } })}
                >
                  {pairing.isPending ? "Creating link…" : "Create owner pairing link"}
                </Button>
                {pairing.data ? (
                  <Note>
                    <a className="underline" href={pairing.data.url} target="_blank" rel="noreferrer">
                      Open Telegram and select Start
                    </a>
                    . This link works once and expires at{" "}
                    {new Date(pairing.data.expiresAt * 1000).toLocaleTimeString()}. A new link cancels the
                    previous link.
                  </Note>
                ) : null}
                {pairing.isError ? <Failure title="Pairing link failed" error={pairing.error} /> : null}
              </Panel>
            )}
          </Section>
          <Notifications channel="telegram" />
        </>
      ) : null}
      <HowToUse channel="telegram">
        <li>Send text, images or files in a private chat with the bot.</li>
      </HowToUse>
      {telegram.enabled ? (
        <TurnOff
          channel="telegram"
          onTurnOff={() => {
            deletion.reset();
            setOff(true);
          }}
        />
      ) : null}
      {edit ? (
        <TelegramDialog
          settings={settings}
          onClose={() => setEdit(false)}
          onNeedsPairing={() => pairing.mutate({ path: { agentId } })}
        />
      ) : null}
      {off ? (
        <Confirm
          title="Turn off Telegram?"
          description="dex stops reading the bot and forgets the saved token. The main timeline stays. To turn Telegram on again, enter the token again."
          confirmLabel="Turn off"
          onCancel={() => setOff(false)}
          onConfirm={() => deletion.mutate({ path: { agentId } })}
          pending={deletion.isPending}
          error={deletion.error}
        />
      ) : null}
    </>
  );
}

function parseUsers(text: string) {
  const fields = text.split(/[\s,]+/).filter(Boolean);

  if (!fields.length) return { users: [], error: "" };

  if (fields.some((field) => !/^[1-9]\d{0,15}$/.test(field) || !Number.isSafeInteger(Number(field))))
    return { users: [], error: "A Telegram user ID is a positive number." };

  if (new Set(fields).size !== fields.length) return { users: [], error: "Remove the duplicate IDs." };

  return { users: fields.map(Number), error: "" };
}

function TelegramDialog({
  settings,
  onClose,
  onNeedsPairing,
}: {
  settings: Settings;
  onClose: () => void;
  onNeedsPairing: () => void;
}) {
  const agentId = useAgentId();
  const { notify } = useApp();
  const saved = settings.telegram.enabled;
  const [token, setToken] = useState("");
  const [replace, setReplace] = useState(!saved);
  const [users, setUsers] = useState(settings.telegram.allowedUsers.join("\n"));
  const [tried, setTried] = useState(false);
  const parsed = parseUsers(users);
  const tokenError = !saved && !token.trim() ? "Enter the bot token from @BotFather." : "";

  const save = useMutation({
    ...setTelegramMutation({ path: { agentId } }),
    onSuccess: async (_, variables) => {
      await refreshSettings();
      onClose();

      if (variables.body.allowedUsers.length === 0) onNeedsPairing();

      notify("Telegram saved. dex restarted the bot.");
    },
    // The typed token is dropped; enter it again to retry.
    onError: () => setToken(""),
  });

  function submit() {
    setTried(true);

    if (tokenError || parsed.error || save.isPending) return;
    save.mutate({ path: { agentId }, body: { token: token.trim(), allowedUsers: parsed.users } });
  }

  return (
    <Modal
      title={saved ? "Edit Telegram" : "Connect Telegram"}
      description="Create a bot with @BotFather. Save the token, then use a pairing link to allow your account."
      pending={save.isPending}
      onClose={onClose}
      footer={
        <>
          <Button size="sm" variant="secondary" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button size="sm" onClick={submit} disabled={save.isPending}>
            {save.isPending ? <Spinner className="border-background/40 border-t-canvas" /> : null}
            {save.isPending ? "Saving…" : "Save Telegram"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {saved && !replace ? (
          <SavedSecret
            label="Bot token"
            text="Saved token ••••"
            hint="dex never shows a saved token."
            action="Replace token"
            onReplace={() => setReplace(true)}
          />
        ) : (
          <Field
            label="Bot token"
            hint={
              saved
                ? "Write-only. An empty field keeps the saved token."
                : "Write-only. It looks like 123456:ABC-DEF."
            }
            error={tried ? tokenError : undefined}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                type="password"
                autoComplete="new-password"
                spellCheck={false}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            )}
          </Field>
        )}
        <details>
          <summary className="cursor-pointer text-[13px] text-muted-foreground">
            Advanced: enter user IDs
          </summary>
          <Field
            label="Allowed user IDs"
            hint="Optional. Use a pairing link if you do not know your ID. One ID per line, or separated with commas."
            error={tried ? parsed.error : undefined}
          >
            {({ id, describedBy, invalid }) => (
              <Textarea
                id={id}
                inputMode="numeric"
                rows={3}
                className="font-mono tabular-nums"
                aria-describedby={describedBy}
                aria-invalid={invalid}
                value={users}
                onChange={(e) => setUsers(e.target.value)}
              />
            )}
          </Field>
        </details>
        <button type="submit" hidden />
      </form>
      {save.isError ? (
        <Failure
          title="Telegram not saved"
          error={
            save.variables?.body.token
              ? `${errorText(save.error)} Enter the bot token again to retry.`
              : save.error
          }
        />
      ) : null}
    </Modal>
  );
}

function WithChannels({ children }: { children: (settings: Settings) => ReactNode }) {
  const agentId = useAgentId();
  const settings = useQuery(getChannelsOptions({ path: { agentId } }));

  if (settings.isPending) return <Loading />;

  if (settings.isError)
    return (
      <Failure title="Could not load channels" error={settings.error} retry={() => void settings.refetch()} />
    );

  return children(settings.data);
}
