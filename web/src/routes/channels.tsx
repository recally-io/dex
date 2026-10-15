import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { IconBrowserTabs } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBrowserTabs";
import { IconPaperPlane } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPaperPlane";
import type { Settings } from "../api/generated/types.gen";
import {
  deleteTelegramMutation,
  getFeishuOptions,
  getSettingsOptions,
  listChatsOptions,
  setTelegramMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshSettings } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { Confirm } from "../components/confirm";
import { Modal } from "../components/dialog";
import { Failure, Spinner } from "../components/feedback";
import { Field, Input, SavedSecret, Textarea } from "../components/form";
import { Facts, FramedPage, Mono, Note, NotAvailable, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";
import { FeishuChannel } from "./feishu";
import { WithSettings } from "./agent";

const unsupported = [
  { name: "Slack", text: "dex cannot connect to Slack in this version." },
  { name: "WeChat", text: "dex cannot connect to WeChat in this version." },
];

function OnOff({ on }: { on: boolean }) {
  return (
    <StatusPill tone={on ? "green" : "neutral"} className="h-5 px-2 text-[11.5px]">
      {on ? "On" : "Off"}
    </StatusPill>
  );
}

function ChannelRow({
  channel,
  icon,
  name,
  text,
  status,
}: {
  channel?: "web" | "telegram" | "feishu";
  icon: ReactNode;
  name: string;
  text: string;
  status: ReactNode;
}) {
  const body = (
    <>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-[9px] bg-inset text-ink-2 shadow-hairline">
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[13.5px] font-medium text-ink group-hover:underline">{name}</span>
        <span className="block text-[12px] text-ink-3">{text}</span>
      </span>
      <span className="shrink-0">{status}</span>
    </>
  );

  return channel ? (
    <Link
      to="/channels/$channelId"
      params={{ channelId: channel }}
      className="group flex items-center gap-3 px-4 py-3 transition-colors hover:bg-hover"
    >
      {body}
    </Link>
  ) : (
    <div className="flex items-center gap-3 px-4 py-3">{body}</div>
  );
}

// A channel gives its allowed people the whole agent.
function Boundary() {
  return (
    <Note tone="warn">
      <strong className="font-medium">A channel gives its allowed people the whole agent.</strong> They reach
      the same model, instructions and memory as you. The agent can run read, write, edit and bash on this
      computer for them. Allow only people you trust.
    </Note>
  );
}

export function ChannelsPage() {
  const feishu = useQuery({ ...getFeishuOptions(), refetchInterval: 3000 });

  return (
    <FramedPage
      title="Channels"
      description="A channel is a place where you talk to the agent. Every channel uses the same agent, memory and workspace. Each chat shows where it came from."
    >
      <WithSettings>
        {(settings) => {
          const tg = settings.telegram;

          return (
            <>
              <Section n={1} title="Available" caption="Web is always on.">
                <Panel>
                  <div className="divide-y divide-line">
                    <ChannelRow
                      channel="web"
                      icon={<IconBrowserTabs size={16} />}
                      name="Web"
                      text="This page. Read every chat, and send in web and automation chats."
                      status={<OnOff on />}
                    />
                    <ChannelRow
                      channel="telegram"
                      icon={<IconPaperPlane size={16} />}
                      name="Telegram"
                      text={
                        tg.enabled
                          ? `Bot ${tg.botId ?? "unknown"} · ${tg.allowedUsers.length} allowed ${tg.allowedUsers.length === 1 ? "user" : "users"}. Read-only on the web`
                          : "Talk to dex from a Telegram bot. Needs a bot token and allowed user IDs."
                      }
                      status={
                        <StatusPill tone={tg.status.state === "running" ? "green" : "neutral"}>
                          {tg.status.state}
                        </StatusPill>
                      }
                    />
                    <ChannelRow
                      channel="feishu"
                      icon={<IconPaperPlane size={16} />}
                      name="Feishu / Lark"
                      text="Private chat with the paired owner. Create an app with a QR code."
                      status={
                        <StatusPill tone={feishu.data?.status === "running" ? "green" : "neutral"}>
                          {feishu.isError ? "unavailable" : (feishu.data?.status ?? "Loading…")}
                        </StatusPill>
                      }
                    />
                  </div>
                </Panel>
              </Section>
              <Section n={2} title="Not yet available" caption="dex does not support these channels now.">
                <Panel>
                  <div className="divide-y divide-line opacity-80">
                    {unsupported.map((c) => (
                      <ChannelRow
                        key={c.name}
                        icon={<span className="text-[12px] font-semibold">{c.name[0]}</span>}
                        name={c.name}
                        text={c.text}
                        status={<NotAvailable />}
                      />
                    ))}
                  </div>
                </Panel>
              </Section>
              <Section n={3} title="Rules" caption="The same for every channel.">
                <Panel>
                  <Facts
                    rows={[
                      ["Agent", "One model and one set of instructions"],
                      ["Memory", "Shared. A fact from Telegram is known on the web"],
                      ["Replies", "In the channel where the chat started"],
                    ]}
                  />
                </Panel>
                <div className="mt-3">
                  <Boundary />
                </div>
              </Section>
            </>
          );
        }}
      </WithSettings>
    </FramedPage>
  );
}

function Crumb() {
  return (
    <Link to="/channels" className="hover:text-ink hover:underline">
      Channels
    </Link>
  );
}

export function ChannelPage({ channel }: { channel: "web" | "telegram" | "feishu" }) {
  return channel === "web" ? <WebChannel /> : channel === "feishu" ? <FeishuChannel /> : <TelegramChannel />;
}

function WebChannel() {
  return (
    <FramedPage
      eyebrow={<Crumb />}
      title="Web"
      badge={<OnOff on />}
      description="The web is built in and always on. It is the only place to change the agent, providers and automations."
    >
      <Section n={1} title="What you can do">
        <Panel>
          <Facts
            rows={[
              ["Address", <Mono key="address">{window.location.origin}</Mono>],
              ["Web chats", "Read and send"],
              ["Automation chats", "Read and reply"],
              ["Telegram chats", "Read only. Reply in Telegram"],
              ["Settings", "Change everything"],
            ]}
          />
        </Panel>
      </Section>
      <Section n={2} title="Access">
        <Note tone="warn">
          The server has no sign-in, and tools run without a sandbox. Any program on this computer that can
          reach the port can run commands with dex. Keep dex on 127.0.0.1.
        </Note>
      </Section>
    </FramedPage>
  );
}

function TelegramChannel() {
  const saved = useQuery(getSettingsOptions());
  const chats = useQuery(listChatsOptions());
  const telegramChats = chats.data?.chats.filter((c) => c.channel === "telegram").length;

  return (
    <FramedPage
      eyebrow={<Crumb />}
      title="Telegram"
      badge={saved.data ? <OnOff on={saved.data.telegram.enabled} /> : null}
      description="Talk to dex from your phone. Only the users that you allow can use the bot."
    >
      <WithSettings>
        {(settings) => <Telegram settings={settings} telegramChats={telegramChats} />}
      </WithSettings>
    </FramedPage>
  );
}

function Telegram({ settings, telegramChats }: { settings: Settings; telegramChats?: number }) {
  const { notify } = useApp();
  const telegram = settings.telegram;
  const [edit, setEdit] = useState(false);
  const [off, setOff] = useState(false);

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
                    {telegram.allowedUsers.join(", ")}
                  </span>,
                ],
                [
                  "Chats from Telegram",
                  <span key="chats" className="font-mono text-[12px] tabular-nums">
                    {telegramChats ?? "…"}
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
              <li>
                Get your user ID, for example from <Mono>@userinfobot</Mono>.
              </li>
              <li>Select Connect bot and enter the token and your ID.</li>
            </ol>
          </Panel>
        )}
        <div className="mt-3">
          <Note>
            Runtime status: {telegram.status.state}.{" "}
            {telegram.status.reason ?? "Send the bot a message to check reply delivery."}
          </Note>
        </div>
      </Section>
      <Section n={2} title="How chats work" caption="Telegram chats are read-only here.">
        <Panel className="px-4 py-3.5">
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] text-ink-2 marker:text-ink-3">
            <li>Each Telegram chat becomes one dex chat, marked with the Telegram origin.</li>
            <li>dex replies in Telegram. On the web, you can read these chats with their tool calls.</li>
            <li>dex ignores messages from user IDs that are not allowed.</li>
            <li>The agent, its tools and its memory are the same as on the web.</li>
          </ul>
        </Panel>
        <div className="mt-3">
          <Boundary />
        </div>
      </Section>
      {telegram.enabled ? (
        <Section n={3} title="Turn off" caption="The bot stops answering. Past chats stay.">
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
      {edit ? <TelegramDialog settings={settings} onClose={() => setEdit(false)} /> : null}
      {off ? (
        <Confirm
          title="Turn off Telegram?"
          description="dex stops reading the bot and forgets the saved token. Telegram chats stay, read-only. To turn Telegram on again, enter the token again."
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

  if (!fields.length) return { users: [], error: "Add at least one user ID. dex ignores everyone else." };

  if (fields.some((field) => !/^[1-9]\d{0,15}$/.test(field) || !Number.isSafeInteger(Number(field))))
    return { users: [], error: "A Telegram user ID is a positive number." };

  if (new Set(fields).size !== fields.length) return { users: [], error: "Remove the duplicate IDs." };

  return { users: fields.map(Number), error: "" };
}

function TelegramDialog({ settings, onClose }: { settings: Settings; onClose: () => void }) {
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
    onSuccess: async () => {
      await refreshSettings();
      onClose();
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
      description="Create a bot with @BotFather. Message @userinfobot to find your user ID. dex saves the bot and restarts it."
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
        <Field
          label="Allowed user IDs"
          hint="One per line, or separated with commas. dex ignores messages from all other users."
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
