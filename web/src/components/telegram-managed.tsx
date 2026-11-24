import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";

import type { Telegram, TelegramManagedSetup } from "../api/generated/types.gen";
import {
  beginTelegramManagedSetupMutation,
  cancelTelegramManagedSetupMutation,
  getTelegramManagedSetupOptions,
  setTelegramManagerMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { refreshSettings } from "../lib/queries";
import { Modal } from "./dialog";
import { Failure } from "./feedback";
import { Field, Input } from "./form";
import { Mono, Note, Section } from "./page";
import { Button } from "./ui/button";

export function TelegramManagerSettings({ telegram }: { telegram: Telegram }) {
  const [open, setOpen] = useState(false);

  return (
    <Section
      title="Telegram bot manager"
      caption="Use a separate bot in BotFather’s Bot Management Mode. This manager is shared by all Dex bots."
      action={
        <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>
          Configure manager
        </Button>
      }
    >
      {telegram.managerConfigured && !telegram.managedCreationAvailable ? (
        <Note tone="warn">
          Dex cannot verify Bot Management Mode. Check the manager token and BotFather settings.
        </Note>
      ) : null}
      {open ? (
        <ManagerDialog configured={!!telegram.managerConfigured} onClose={() => setOpen(false)} />
      ) : null}
    </Section>
  );
}

function ManagerDialog({ configured, onClose }: { configured: boolean; onClose: () => void }) {
  const [token, setToken] = useState("");

  const save = useMutation({
    ...setTelegramManagerMutation(),
    onSuccess: async () => {
      await refreshSettings();
      onClose();
    },
  });

  return (
    <Modal
      title="Configure Telegram manager"
      description="Enable Bot Management Mode for a dedicated bot in the BotFather Mini App. Do not use this bot as a channel or with another poller or webhook."
      pending={save.isPending}
      onClose={onClose}
      footer={
        <>
          {configured ? (
            <Button
              variant="ghost"
              disabled={save.isPending}
              onClick={() => save.mutate({ body: { token: "" } })}
            >
              Remove manager
            </Button>
          ) : null}
          <Button variant="outline" disabled={save.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="telegram-manager" disabled={!token.trim() || save.isPending}>
            Save manager
          </Button>
        </>
      }
    >
      <form
        id="telegram-manager"
        onSubmit={(event) => {
          event.preventDefault();
          const value = token.trim();

          if (!value || save.isPending) return;

          setToken("");
          save.mutate({ body: { token: value } });
        }}
      >
        <Field label="Manager bot token">
          {({ id }) => (
            <Input
              id={id}
              type="password"
              autoComplete="off"
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
          )}
        </Field>
      </form>
      {save.isError ? <Failure title="Manager not saved" error={save.error} /> : null}
    </Modal>
  );
}

export function TelegramManagedCreation({
  available,
  replacing,
}: {
  available: boolean;
  replacing: boolean;
}) {
  const agentId = useAgentId();
  const [setup, setSetup] = useState<{ initial: TelegramManagedSetup; replacing: boolean }>();

  const begin = useMutation({
    ...beginTelegramManagedSetupMutation(),
    onSuccess: (initial) => setSetup({ initial, replacing }),
    onError: () => void refreshSettings(),
  });

  return (
    <>
      {available ? (
        <Button
          size="sm"
          variant="secondary"
          disabled={begin.isPending || !!setup}
          onClick={() => begin.mutate({ path: { agentId } })}
        >
          Create a new Bot
        </Button>
      ) : null}
      {begin.isError ? <Failure title="Could not start Telegram setup" error={begin.error} /> : null}
      {setup ? <SetupDialog {...setup} onClose={() => setSetup(undefined)} /> : null}
    </>
  );
}

function SetupDialog({
  initial,
  replacing,
  onClose,
}: {
  initial: TelegramManagedSetup;
  replacing: boolean;
  onClose: () => void;
}) {
  const agentId = useAgentId();
  const path = { agentId, setupId: initial.id };

  const setup = useQuery({
    ...getTelegramManagedSetupOptions({ path }),
    initialData: initial,
    refetchInterval: (query) => (isPending(query.state.data) ? 1000 : false),
  });

  const cancel = useMutation({ ...cancelTelegramManagedSetupMutation(), onSuccess: onClose });
  const current = setup.data;
  const pending = isPending(current);
  const connected = current.state === "connected";

  useEffect(() => {
    if (connected) void refreshSettings();
  }, [connected]);

  function close() {
    if (pending) cancel.mutate({ path });
    else onClose();
  }

  return (
    <Modal
      title="Create a Telegram bot"
      description={
        replacing
          ? "Your existing connection stays active until the new bot is connected."
          : "Confirm each step in Telegram. Dex connects the bot without showing its token."
      }
      pending={cancel.isPending}
      onClose={close}
      footer={
        <Button variant="outline" disabled={cancel.isPending} onClick={close}>
          {pending ? "Cancel setup" : "Done"}
        </Button>
      }
    >
      {pending && !current.url ? (
        <Note tone="warn">The manager is unavailable. Wait or cancel this setup.</Note>
      ) : null}
      {current.state === "verify_owner" && current.url ? (
        <>
          <p>
            Open the private link and tap Start from the Telegram account that will own the new bot. Do not
            share this link.
          </p>
          <a className="underline" href={current.url} target="_blank" rel="noreferrer">
            Verify your Telegram account
          </a>
        </>
      ) : null}
      {current.state === "create_bot" && current.url ? (
        <>
          <p>
            Use the same Telegram account to confirm creation. Keep the username{" "}
            <Mono>{current.username}</Mono> unchanged.
          </p>
          <a className="underline" href={current.url} target="_blank" rel="noreferrer">
            Open bot creation in Telegram
          </a>
        </>
      ) : null}
      {pending ? (
        <Note>Setup expires at {new Date(current.expiresAt * 1000).toLocaleTimeString()}.</Note>
      ) : (
        <Note tone={connected ? "neutral" : "warn"}>
          {connected
            ? "Your new Telegram bot is connected."
            : `Setup ${current.state}. This setup did not change the saved channel.`}
        </Note>
      )}
      {setup.isError ? <Failure title="Could not read setup status" error={setup.error} /> : null}
      {cancel.isError ? <Failure title="Could not cancel setup" error={cancel.error} /> : null}
    </Modal>
  );
}

function isPending(setup?: TelegramManagedSetup) {
  return setup?.state === "verify_owner" || setup?.state === "create_bot";
}
