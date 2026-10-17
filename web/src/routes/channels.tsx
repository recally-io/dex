import { scopedSettingsOptions, useAgentId } from "../lib/agent-scope";
import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { IconChevronRightSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronRightSmall";
import { IconPaperPlane } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPaperPlane";
import type { Settings } from "../api/generated/types.gen";
import {
  createTelegramPairingMutation,
  deleteTelegramMutation,
  getFeishuOptions,
  setTelegramMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshSettings } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { WechatChannel } from "../components/wechat-channel";
import { Confirm } from "../components/confirm";
import { Modal } from "../components/dialog";
import { Failure, Spinner } from "../components/feedback";
import { Field, Input, SavedSecret, Textarea } from "../components/form";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { FeishuChannel } from "./feishu";
import { Crumb } from "../components/crumb";
import { WithSettings } from "../components/with-settings";

function ChannelRow({
  channel,
  name,
  text,
  status,
}: {
  channel: "telegram" | "wechat" | "feishu";
  name: string;
  text: string;
  status: ReactNode;
}) {
  const agentId = useAgentId();

  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-[9px] bg-inset text-ink-2 shadow-hairline">
        <IconPaperPlane size={16} />
      </span>
      <span className="min-w-0 flex-1">
        <Link
          to="/a/$agentId/channels/$channelId"
          params={{ agentId, channelId: channel }}
          className="block w-fit text-[13.5px] font-medium text-ink hover:underline"
        >
          {name}
        </Link>
        <span className="block text-[12px] text-ink-3">{text}</span>
      </span>
      {status}
      <IconChevronRightSmall size={16} className="shrink-0 text-ink-3" />
    </div>
  );
}

// All channel input shares the owner's main context.
function Boundary() {
  return (
    <Note tone="warn">
      <strong className="font-medium">Channels are for the owner.</strong> Use only your own accounts. Channel
      input shares the main timeline's context. The agent can read and change files and run commands on this
      computer.
    </Note>
  );
}

export function ChannelsSection({ settings }: { settings: Settings }) {
  const agentId = useAgentId();
  const saved = useQuery({ ...scopedSettingsOptions(agentId), refetchInterval: 3000 });
  const feishu = useQuery({ ...getFeishuOptions(), refetchInterval: 3000 });
  const current = saved.data ?? settings;

  const status = (state: string) => (
    <StatusPill tone={state === "running" ? "green" : "neutral"} className="h-5 shrink-0 px-2 text-[11.5px]">
      {state}
    </StatusPill>
  );

  return (
    <Section
      n={1}
      title="All channels"
      caption="Channels share the main timeline. Open a channel to set it up."
    >
      <Panel>
        <div className="divide-y divide-line">
          <ChannelRow
            channel="telegram"
            name="Telegram"
            text="Private messages from allowed people."
            status={status(current.telegram.status.state)}
          />
          <ChannelRow
            channel="wechat"
            name="WeChat"
            text="Private messages from the paired owner."
            status={status(current.wechat.status.state)}
          />
          <ChannelRow
            channel="feishu"
            name="Feishu / Lark"
            text="Private messages from the paired owner."
            status={status(feishu.isError ? "unavailable" : (feishu.data?.status ?? "Loading…"))}
          />
        </div>
      </Panel>
    </Section>
  );
}

export function ChannelsPage() {
  const agentId = useAgentId();

  if (agentId !== "default") return <ChannelsNote />;

  return (
    <FramedPage title="Channels">
      <WithSettings>{(settings) => <ChannelsSection settings={settings} />}</WithSettings>
    </FramedPage>
  );
}

export function ChannelPage({ channel }: { channel: "telegram" | "wechat" | "feishu" }) {
  const agentId = useAgentId();

  if (agentId !== "default") return <ChannelsNote />;

  return channel === "wechat" ? (
    <FramedPage
      eyebrow={<Crumb to="/a/$agentId/channels">Channels</Crumb>}
      title="WeChat"
      description="Scan once. dex recognizes the owner from the login."
    >
      <WithSettings>{(settings) => <WechatChannel settings={settings} />}</WithSettings>
    </FramedPage>
  ) : channel === "feishu" ? (
    <FeishuChannel />
  ) : (
    <TelegramChannel />
  );
}

function TelegramChannel() {
  const agentId = useAgentId();
  const saved = useQuery({ ...scopedSettingsOptions(agentId), refetchInterval: 2000 });

  return (
    <FramedPage
      eyebrow={<Crumb to="/a/$agentId/channels">Channels</Crumb>}
      title="Telegram"
      badge={
        saved.data ? (
          <StatusPill tone={saved.data.telegram.status.state === "running" ? "green" : "neutral"}>
            {saved.data.telegram.status.state}
          </StatusPill>
        ) : null
      }
      description="Talk to dex from your phone. Only the users that you allow can use the bot."
    >
      <WithSettings>{(settings) => <Telegram settings={settings} />}</WithSettings>
    </FramedPage>
  );
}

function Telegram({ settings }: { settings: Settings }) {
  const { notify } = useApp();
  const telegram = settings.telegram;
  const [edit, setEdit] = useState(false);
  const [off, setOff] = useState(false);
  const pairing = useMutation(createTelegramPairingMutation());

  const deletion = useMutation({
    ...deleteTelegramMutation(),
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
        title="Bot"
        caption={telegram.enabled ? undefined : "Not connected."}
        action={
          <Button
            size="sm"
            variant={telegram.enabled ? "secondary" : "primary"}
            onClick={() => setEdit(true)}
          >
            {telegram.enabled ? "Edit" : "Connect bot"}
          </Button>
        }
      >
        {telegram.enabled ? (
          <Panel>
            <Facts
              rows={[
                ["Bot ID", <Mono key="bot">{telegram.botId ?? "Unknown"}</Mono>],
                [
                  "Bot token",
                  <span key="token" className="text-ink-2">
                    Saved · write-only
                  </span>,
                ],
                [
                  "Allowed user IDs",
                  <span key="users" className="font-mono text-[12px] text-ink-2 tabular-nums">
                    {telegram.allowedUsers.join(", ") || "None. Pair your account below."}
                  </span>,
                ],
              ]}
            />
          </Panel>
        ) : (
          <Panel className="px-4 py-3.5">
            <p className="mb-2 text-[13px] text-ink-2">Telegram is off. dex does not read any bot.</p>
            <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-[13px] text-ink-2 marker:font-mono marker:text-[11px] marker:text-ink-3">
              <li>
                In Telegram, open <Mono>@BotFather</Mono> and send <Mono>/newbot</Mono>.
              </li>
              <li>Copy the token that BotFather gives you.</li>
              <li>Select Connect bot and enter the token.</li>
              <li>Create a pairing link, then open the link in Telegram and select Start.</li>
            </ol>
          </Panel>
        )}
        <div className="mt-3">
          <Note>
            Runtime status: {telegram.status.state}.{" "}
            {telegram.status.reason ?? "Send the bot a message to check reply delivery."}
          </Note>
        </div>
        {telegram.enabled && telegram.allowedUsers.length === 0 ? (
          <div className="mt-3 flex flex-col items-start gap-3">
            <Note>Pair your account to start. The bot ignores messages until an owner is paired.</Note>
            <Button
              size="sm"
              variant="secondary"
              disabled={pairing.isPending}
              onClick={() => pairing.mutate({})}
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
          </div>
        ) : null}
      </Section>
      <Section n={2} title="How messages work" caption="Telegram shares the main timeline.">
        <Panel className="px-4 py-3.5">
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] text-ink-2 marker:text-ink-3">
            <li>
              Telegram and web input share the main context. /new clears that context when main is idle.
            </li>
            <li>
              dex replies to the entry that sent the input. The web shows all main input and tool calls.
            </li>
            <li>/stop stops the main answer and its background work.</li>
            <li>dex ignores messages from user IDs that are not allowed.</li>
            <li>The agent, its tools and its memory are the same as on the web.</li>
          </ul>
        </Panel>
        <div className="mt-3">
          <Boundary />
        </div>
      </Section>
      {telegram.enabled ? (
        <Section n={3} title="Turn off" caption="The bot stops answering. The main timeline stays.">
          <Button
            size="sm"
            variant="secondary"
            className="text-red"
            onClick={() => {
              deletion.reset();
              setOff(true);
            }}
          >
            Turn off Telegram
          </Button>
        </Section>
      ) : null}
      {edit ? (
        <TelegramDialog
          settings={settings}
          onClose={() => setEdit(false)}
          onNeedsPairing={() => pairing.mutate({})}
        />
      ) : null}
      {off ? (
        <Confirm
          title="Turn off Telegram?"
          description="dex stops reading the bot and forgets the saved token. The main timeline stays. To turn Telegram on again, enter the token again."
          confirmLabel="Turn off"
          onCancel={() => setOff(false)}
          onConfirm={() => deletion.mutate({})}
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
  const { notify } = useApp();
  const saved = settings.telegram.enabled;
  const [token, setToken] = useState("");
  const [replace, setReplace] = useState(!saved);
  const [users, setUsers] = useState(settings.telegram.allowedUsers.join("\n"));
  const [tried, setTried] = useState(false);
  const parsed = parseUsers(users);
  const tokenError = !saved && !token.trim() ? "Enter the bot token from @BotFather." : "";

  const save = useMutation({
    ...setTelegramMutation(),
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
    save.mutate({ body: { token: token.trim(), allowedUsers: parsed.users } });
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
          <Button size="sm" variant="primary" onClick={submit} disabled={save.isPending}>
            {save.isPending ? <Spinner className="border-canvas/40 border-t-canvas" /> : null}
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
          <summary className="cursor-pointer text-[13px] text-ink-2">Advanced: enter user IDs</summary>
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

function ChannelsNote() {
  return (
    <FramedPage title="Channels">
      <div className="p-5 sm:p-8">
        <Note>
          Channels connect to the default agent for now.{" "}
          <Link to="/a/$agentId/channels" params={{ agentId: "default" }} className="font-medium underline">
            Open default agent channels
          </Link>
        </Note>
      </div>
    </FramedPage>
  );
}
