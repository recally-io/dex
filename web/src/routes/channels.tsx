import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation } from "@tanstack/react-query";
import { ChevronRight, Globe, MessageCircle, Send, ShieldAlert } from "lucide-react";
import type { Settings } from "../api/generated/types.gen";
import { deleteTelegramMutation, setTelegramMutation } from "../api/generated/@tanstack/react-query.gen";
import { errorText, refreshSettings } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { Confirm } from "../components/confirm";
import { Failure } from "../components/feedback";
import { Card, Field, Note, NotAvailable, Page, PageHeader, Rows, Section } from "../components/page";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Textarea } from "../components/ui/textarea";
import { Button as Pill } from "../components/atoms/Button";
import { ValuePill } from "../components/atoms/ValuePill";
import { WithSettings } from "./agent";

function Boundary() {
  return (
    <div className="mb-8 flex items-start gap-3 rounded-xl bg-orange-tint px-4 py-3 text-[13px] leading-6 text-ink">
      <ShieldAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-orange" />
      <p>
        <strong>A channel gives its allowed people the whole agent.</strong> They reach the same model,
        instructions and memory as you. The agent can run read, write, edit and bash on this computer for
        them. Allow only people you trust.
      </p>
    </div>
  );
}

function telegramStatus(settings: Settings) {
  return settings.telegram.enabled ? (
    <ValuePill tone="green">Configured</ValuePill>
  ) : (
    <ValuePill>Off</ValuePill>
  );
}

const unsupported = [
  {
    name: "Slack",
    icon: (
      <span aria-hidden className="text-base font-bold">
        #
      </span>
    ),
  },
  {
    name: "Feishu / Lark",
    icon: (
      <span aria-hidden className="text-sm font-bold">
        飞
      </span>
    ),
  },
  { name: "WeChat", icon: <MessageCircle aria-hidden className="size-5" /> },
];

function ChannelLink({
  channel,
  icon,
  name,
  status,
  summary,
}: {
  channel: "web" | "telegram";
  icon: ReactNode;
  name: string;
  status: ReactNode;
  summary: string;
}) {
  return (
    <li>
      <Link
        to="/channels/$channelId"
        params={{ channelId: channel }}
        className="flex h-full items-start gap-4 rounded-[14px] border border-line bg-surface p-4 shadow-card hover:bg-hover focus-visible:outline-2 focus-visible:outline-ink"
      >
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-inset text-ink-2">
          {icon}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold text-ink">{name}</span>
            {status}
          </span>
          <span className="mt-1 block text-[13px] leading-5 text-ink-2">{summary}</span>
        </span>
        <ChevronRight aria-hidden className="mt-2 size-4 shrink-0 text-ink-3" />
      </Link>
    </li>
  );
}

export function ChannelsPage() {
  return (
    <Page>
      <PageHeader
        title="Channels"
        description="Channels are the ways messages reach the agent and the way answers go back. Each chat shows where it came from."
      />
      <WithSettings>
        {(settings) => (
          <>
            <Boundary />
            <ul className="grid gap-3 md:grid-cols-2">
              <ChannelLink
                channel="web"
                icon={<Globe aria-hidden className="size-5" />}
                name="Web"
                status={<ValuePill tone="green">Built in</ValuePill>}
                summary="This page, on this computer. You can read and send in every web and automation chat."
              />
              <ChannelLink
                channel="telegram"
                icon={<Send aria-hidden className="size-5" />}
                name="Telegram"
                status={telegramStatus(settings)}
                summary={
                  settings.telegram.enabled
                    ? `Bot ${settings.telegram.botId ?? ""} · ${settings.telegram.allowedUsers.length} allowed user${settings.telegram.allowedUsers.length === 1 ? "" : "s"}. Chats are read-only on the web.`
                    : "Talk to dex from a Telegram bot. Needs a bot token and allowed user IDs."
                }
              />
              {unsupported.map(({ name, icon }) => (
                <li
                  key={name}
                  className="flex items-start gap-4 rounded-[14px] border border-dashed border-line p-4"
                >
                  <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-inset text-ink-3">
                    {icon}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-semibold text-ink-2">{name}</span>
                      <NotAvailable />
                    </span>
                    <span className="mt-1 block text-[13px] leading-5 text-ink-3">
                      dex cannot connect to {name} in this version.
                    </span>
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}
      </WithSettings>
    </Page>
  );
}

export function ChannelPage({ channel }: { channel: "web" | "telegram" }) {
  return (
    <Page>
      <WithSettings>
        {(settings) =>
          channel === "web" ? (
            <>
              <PageHeader
                eyebrow={
                  <Link to="/channels" className="hover:underline">
                    Channels
                  </Link>
                }
                title="Web"
                badge={<ValuePill tone="green">Built in</ValuePill>}
              />
              <Section title="Access">
                <Card>
                  <Rows
                    rows={[
                      [
                        "Address",
                        <span key="address" className="break-all font-mono">
                          {window.location.origin}
                        </span>,
                      ],
                      ["Sign-in", "None. Any program on this computer that can reach the port can use dex."],
                      ["Can send in", "Web chats and automation chats"],
                      ["Read-only", "Telegram chats"],
                    ]}
                  />
                </Card>
              </Section>
              <Note>The web channel is built in. You cannot turn it off.</Note>
            </>
          ) : (
            <Telegram settings={settings} />
          )
        }
      </WithSettings>
    </Page>
  );
}

function Telegram({ settings }: { settings: Settings }) {
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
      <PageHeader
        eyebrow={
          <Link to="/channels" className="hover:underline">
            Channels
          </Link>
        }
        title="Telegram"
        badge={telegramStatus(settings)}
      />
      <Boundary />
      <Section
        title="Saved configuration"
        actions={
          telegram.enabled ? (
            <>
              <Button variant="outline" size="sm" onClick={() => setEdit(true)}>
                Edit
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  deletion.reset();
                  setOff(true);
                }}
              >
                Turn off
              </Button>
            </>
          ) : (
            <Pill variant="primary" size="sm" onClick={() => setEdit(true)}>
              Set up Telegram
            </Pill>
          )
        }
      >
        <Card>
          {telegram.enabled ? (
            <Rows
              rows={[
                [
                  "Bot ID",
                  <span key="bot" className="font-mono">
                    {telegram.botId ?? "Unknown"}
                  </span>,
                ],
                ["Bot token", "Saved · write-only"],
                [
                  "Allowed user IDs",
                  <span key="users" className="break-all font-mono">
                    {telegram.allowedUsers.join(", ")}
                  </span>,
                ],
              ]}
            />
          ) : (
            <p className="text-[13px] text-ink-2">Telegram is off. dex does not read any bot.</p>
          )}
        </Card>
      </Section>
      <Section title="Status">
        <Note>
          Configured means that dex saved the bot and started it. dex does not report whether the bot reaches
          Telegram. To check it, send the bot a message from an allowed account.
        </Note>
      </Section>
      <Section title="How chats work">
        <Card>
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] text-ink-2">
            <li>Each Telegram chat becomes one dex chat, marked with the Telegram origin.</li>
            <li>dex replies in Telegram. On the web, these chats are read-only.</li>
            <li>dex ignores messages from user IDs that are not allowed.</li>
            <li>The agent, its tools and its memory are the same as on the web.</li>
          </ul>
        </Card>
      </Section>
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
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !save.isPending) onClose();
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="pr-8">{saved ? "Edit Telegram" : "Set up Telegram"}</DialogTitle>
          <DialogDescription>
            Create a bot with @BotFather. Message @userinfobot to find your user ID.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          {saved && !replace ? (
            <div className="flex flex-col gap-1.5">
              <span className="text-[13px] font-medium text-ink">Bot token</span>
              <div className="flex items-center justify-between gap-3 rounded-lg bg-inset p-3">
                <span className="text-sm text-ink-2">Saved token ••••</span>
                <Button type="button" variant="outline" size="sm" onClick={() => setReplace(true)}>
                  Replace token
                </Button>
              </div>
              <p className="text-xs leading-5 text-ink-3">dex never shows a saved token.</p>
            </div>
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
                className="font-mono"
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
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save Telegram"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
