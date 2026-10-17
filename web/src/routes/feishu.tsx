import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  beginFeishuMutation,
  deleteFeishuMutation,
  getFeishuOptions,
  pollFeishuMutation,
  setFeishuMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { Crumb } from "../components/crumb";
import { Button } from "../components/atoms/Button";
import { Failure } from "../components/feedback";
import { Field, Input } from "../components/form";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Confirm } from "../components/confirm";
import type { FeishuSetup } from "../api/generated/types.gen";

export function FeishuChannel() {
  const client = useQueryClient();
  const saved = useQuery({ ...getFeishuOptions(), refetchInterval: 3000 });
  const [selectedBrand, setBrand] = useState<string | null>(null);
  const brand = selectedBrand ?? saved.data?.brand ?? "feishu";
  const [appId, setAppId] = useState("");
  const [appSecret, setAppSecret] = useState("");
  const [setup, setSetup] = useState<FeishuSetup | null>(null);
  const [qrError, setQrError] = useState("");
  const [off, setOff] = useState(false);
  const [expires, setExpires] = useState(0);
  const refresh = () => client.invalidateQueries({ queryKey: getFeishuOptions().queryKey });

  const begin = useMutation({
    ...beginFeishuMutation(),
    onSuccess: (data) => {
      setQrError("");
      setSetup(data);
      setExpires(Date.now() + data.expiresIn * 1000);
    },
  });

  const poll = useMutation({
    ...pollFeishuMutation(),
    onSuccess: (data) => {
      if (data.status === "created") {
        setSetup(null);
        void refresh();
      } else setSetup((old) => (old ? { ...old, interval: data.interval } : old));
    },
  });

  const save = useMutation({
    ...setFeishuMutation(),
    onSuccess: (data) => {
      setAppSecret("");
      setSetup(data);
      void refresh();
    },
    onError: () => setAppSecret(""),
  });

  const remove = useMutation({
    ...deleteFeishuMutation(),
    onSuccess: () => {
      setOff(false);
      setSetup(null);
      void refresh();
    },
  });

  const qrUrl = setup?.qrUrl ?? "";

  const qr = useQuery({
    queryKey: ["feishu-qr", qrUrl],
    enabled: !!qrUrl,
    queryFn: async () => {
      const qrcode = await import("qrcode");

      return qrcode.toDataURL(qrUrl, { width: 256, margin: 2 });
    },
  });

  const session = setup?.session ?? "";
  const interval = setup?.interval ?? 5;
  const pending = poll.isPending;
  const pollFailed = poll.isError;
  const pollApp = poll.mutate;
  useEffect(() => {
    if (!session || pending || pollFailed) return;

    const timer = window.setTimeout(
      () => {
        if (Date.now() >= expires) {
          setSetup(null);
          setQrError("The QR code expired. Create a new QR code.");
        } else pollApp({ body: { session } });
      },
      Math.max(1, interval) * 1000,
    );

    return () => window.clearTimeout(timer);
  }, [session, interval, pending, pollFailed, pollApp, expires]);
  const busy = begin.isPending || save.isPending || remove.isPending;

  return (
    <FramedPage
      eyebrow={<Crumb to="/a/$agentId/channels">Channels</Crumb>}
      title="Feishu / Lark"
      description="Talk to dex in a private chat. Only the paired owner can use the bot."
    >
      <Section n={1} title="Connection">
        <Panel>
          <Facts
            rows={[
              ["Status", saved.data?.status ?? "Loading…"],
              ["App ID", <Mono key="app">{saved.data?.appId || "Not connected"}</Mono>],
              ["Owner", saved.data?.paired ? "Paired" : "Not paired"],
            ]}
          />
        </Panel>
        {saved.data?.error ? <Note tone="warn">{saved.data.error}</Note> : null}
        {saved.isError ? <Failure title="Cannot read connection" error={saved.error} /> : null}
      </Section>
      <Section
        n={2}
        title="Create an app"
        caption="Scan with Feishu or Lark. dex saves the app and pairs the account that creates it."
      >
        <div className="flex flex-wrap items-center gap-3">
          <label className="text-[13px] text-ink-2">
            Platform
            <select
              className="ml-2 rounded border border-line bg-canvas px-3 py-2"
              value={brand}
              onChange={(e) => setBrand(e.target.value)}
              disabled={busy || !!session}
            >
              <option value="feishu">Feishu</option>
              <option value="lark">Lark</option>
            </select>
          </label>
          <Button
            size="sm"
            disabled={busy || !!session}
            onClick={() => {
              poll.reset();
              begin.mutate({ body: { brand } });
            }}
          >
            Create QR code
          </Button>
          {session ? (
            <Button size="sm" variant="secondary" disabled={pending} onClick={() => setSetup(null)}>
              Cancel
            </Button>
          ) : null}
        </div>
        {qr.data ? (
          <img
            className="mt-4 rounded-lg"
            src={qr.data}
            alt="Scan to create the dex app"
            width={256}
            height={256}
          />
        ) : null}
        {session ? (
          <p className="mt-2 text-[13px] text-ink-2">
            Scan the QR code and approve the app. Keep this page open.
          </p>
        ) : null}
        {qr.isError ? <Failure title="Cannot render QR code" error={qr.error} /> : null}
        {qrError ? <Note tone="warn">{qrError}</Note> : null}
        {begin.isError ? <Failure title="Cannot create QR code" error={begin.error} /> : null}
        {poll.isError ? (
          <>
            <Failure title="Cannot finish setup" error={poll.error} />
            <Button size="sm" onClick={() => poll.reset()}>
              Retry
            </Button>
          </>
        ) : null}
      </Section>
      <Section
        n={3}
        title="Use an existing app"
        caption="Enable the bot and long connection. Add im:message, im:message.p2p_msg:readonly, im:resource and the im.message.receive_v1 event."
      >
        <form
          className="flex max-w-lg flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setSetup(null);
            save.mutate({ body: { appId, appSecret, brand } });
          }}
        >
          <Field label="App ID">
            {({ id }) => (
              <Input
                id={id}
                required
                value={appId}
                placeholder={saved.data?.appId}
                onChange={(e) => setAppId(e.target.value)}
              />
            )}
          </Field>
          <Field label="App Secret" hint="Write-only. Leave empty to keep the secret for the same app.">
            {({ id }) => (
              <Input
                id={id}
                type="password"
                autoComplete="new-password"
                value={appSecret}
                onChange={(e) => setAppSecret(e.target.value)}
              />
            )}
          </Field>
          <Button type="submit" size="sm" disabled={busy || !!session}>
            Save app
          </Button>
        </form>
        {setup?.pairingCode ? (
          <Note>
            Send <Mono>/pair {setup.pairingCode}</Mono> to the bot in a private chat within 10 minutes. dex
            refuses other messages until you pair.
          </Note>
        ) : null}
        {save.isError ? <Failure title="Cannot save app" error={save.error} /> : null}
      </Section>
      <Section n={4} title="Use the bot" caption="Groups and threads are not supported.">
        <Note>
          Send text, rich text, images or files up to 20 MB. Feishu shares the main timeline. Use /new to
          clear main's context when it is idle. Use /stop to stop the main answer and its background work. Use
          /help for commands.
        </Note>
      </Section>
      {saved.data?.enabled ? (
        <Section n={5} title="Turn off">
          <Button size="sm" variant="secondary" onClick={() => setOff(true)}>
            Turn off Feishu / Lark
          </Button>
        </Section>
      ) : null}
      {off ? (
        <Confirm
          title="Turn off Feishu / Lark?"
          description="dex stops the bot and forgets its credentials. Past chats stay."
          confirmLabel="Turn off"
          onCancel={() => setOff(false)}
          onConfirm={() => remove.mutate({})}
          pending={remove.isPending}
          error={remove.isError ? remove.error : undefined}
        />
      ) : null}
    </FramedPage>
  );
}
