import { useAgentId } from "../lib/agent-scope";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  beginFeishuMutation,
  deleteFeishuMutation,
  getFeishuOptions,
  pollFeishuMutation,
  setFeishuMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { Button } from "../components/ui/button";
import { Failure } from "../components/feedback";
import { Field, Input } from "../components/form";
import { Facts, Mono, Note, Panel, Section } from "../components/page";
import { HowToUse, Notifications, Steps, TurnOff } from "../components/channel-kit";
import { Confirm } from "../components/confirm";
import type { FeishuSetup } from "../api/generated/types.gen";

export function FeishuChannel() {
  const agentId = useAgentId();
  const client = useQueryClient();
  const saved = useQuery({ ...getFeishuOptions({ path: { agentId } }), refetchInterval: 3000 });
  const [selectedBrand, setBrand] = useState<string | null>(null);
  const brand = selectedBrand ?? saved.data?.brand ?? "feishu";
  const [appId, setAppId] = useState("");
  const [appSecret, setAppSecret] = useState("");
  const [setup, setSetup] = useState<FeishuSetup | null>(null);
  const [qrError, setQrError] = useState("");
  const [off, setOff] = useState(false);
  const [expires, setExpires] = useState(0);

  const refresh = () =>
    client.invalidateQueries({ queryKey: getFeishuOptions({ path: { agentId } }).queryKey });

  const begin = useMutation({
    ...beginFeishuMutation({ path: { agentId } }),
    onSuccess: (data) => {
      setQrError("");
      setSetup(data);
      setExpires(Date.now() + data.expiresIn * 1000);
    },
  });

  const poll = useMutation({
    ...pollFeishuMutation({ path: { agentId } }),
    onSuccess: (data) => {
      if (data.status === "created") {
        setSetup(null);
        void refresh();
      } else setSetup((old) => (old ? { ...old, interval: data.interval } : old));
    },
  });

  const save = useMutation({
    ...setFeishuMutation({ path: { agentId } }),
    onSuccess: (data) => {
      setAppSecret("");
      setSetup(data);
      void refresh();
    },
    onError: () => setAppSecret(""),
  });

  const remove = useMutation({
    ...deleteFeishuMutation({ path: { agentId } }),
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
        } else pollApp({ path: { agentId }, body: { session } });
      },
      Math.max(1, interval) * 1000,
    );

    return () => window.clearTimeout(timer);
  }, [agentId, session, interval, pending, pollFailed, pollApp, expires]);
  const busy = begin.isPending || save.isPending || remove.isPending;

  const connected = !!saved.data?.enabled;

  // Creating an app by QR code pairs its creator. An existing app needs its own setup steps and a
  // pairing code, so it stays folded.
  const setupForm = (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <label className="text-[13px] text-muted-foreground">
          Platform
          <select
            className="ml-2 rounded border border-border bg-sidebar px-3 py-2"
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
          variant={connected ? "secondary" : "default"}
          disabled={busy || !!session}
          onClick={() => {
            poll.reset();
            begin.mutate({ path: { agentId }, body: { brand } });
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
        <img className="rounded-lg" src={qr.data} alt="Scan to create the dex app" width={256} height={256} />
      ) : null}
      {session ? (
        <p className="text-[13px] text-muted-foreground">
          Scan the QR code and approve the app. Keep this page open.
        </p>
      ) : null}
      {qr.isError ? <Failure title="Cannot render QR code" error={qr.error} /> : null}
      {qrError ? <Note tone="warn">{qrError}</Note> : null}
      {begin.isError ? <Failure title="Cannot create QR code" error={begin.error} /> : null}
      {poll.isError ? (
        <div className="flex flex-col items-start gap-2">
          <Failure title="Cannot finish setup" error={poll.error} />
          <Button size="sm" onClick={() => poll.reset()}>
            Retry
          </Button>
        </div>
      ) : null}
      <details>
        <summary className="cursor-pointer text-[13px] text-muted-foreground">Use an existing app</summary>
        <p className="mt-2 text-[12.5px] leading-relaxed text-muted-foreground">
          Enable the bot and long connection. Add <Mono>im:message</Mono>,{" "}
          <Mono>im:message.p2p_msg:readonly</Mono>, <Mono>im:resource</Mono> and the{" "}
          <Mono>im.message.receive_v1</Mono> event.
        </p>
        <form
          className="mt-3 flex max-w-lg flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setSetup(null);
            save.mutate({ path: { agentId }, body: { appId, appSecret, brand } });
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
          <div>
            <Button type="submit" size="sm" variant="secondary" disabled={busy || !!session}>
              Save app
            </Button>
          </div>
        </form>
        {save.isError ? (
          <div className="mt-3">
            <Failure title="Cannot save app" error={save.error} />
          </div>
        ) : null}
      </details>
    </div>
  );

  return (
    <>
      <Section n={1} title="Connection">
        {saved.isError ? <Failure title="Cannot read connection" error={saved.error} /> : null}
        {connected ? (
          <>
            <Panel>
              <Facts
                rows={[
                  ["Platform", saved.data?.brand === "lark" ? "Lark" : "Feishu"],
                  ["App ID", <Mono key="app">{saved.data?.appId}</Mono>],
                ]}
              />
            </Panel>
            <Panel className="mt-3 px-4 py-3.5">
              <details>
                <summary className="cursor-pointer text-[13px] text-muted-foreground">
                  Replace the app
                </summary>
                <div className="mt-3">{setupForm}</div>
              </details>
            </Panel>
          </>
        ) : (
          <Panel className="px-4 py-3.5">
            <Steps>
              <li>Choose Feishu or Lark, then select Create QR code.</li>
              <li>Scan the code in the app and approve. dex saves the app and pairs your account.</li>
            </Steps>
            <div className="mt-4">{setupForm}</div>
          </Panel>
        )}
        {saved.data?.error ? (
          <div className="mt-3">
            <Note tone="warn">{saved.data.error}</Note>
          </div>
        ) : null}
      </Section>
      {connected || setup?.pairingCode ? (
        <Section n={2} title="Who can use it" caption="Only the paired owner, in a private chat.">
          <Panel>
            <Facts rows={[["Owner", saved.data?.paired ? "Paired" : "Not paired"]]} />
          </Panel>
          {setup?.pairingCode ? (
            <div className="mt-3">
              <Note>
                Send <Mono>/pair {setup.pairingCode}</Mono> to the bot in a private chat within 10 minutes.
                dex refuses other messages until you pair.
              </Note>
            </div>
          ) : null}
        </Section>
      ) : null}
      {connected ? <Notifications channel="feishu" /> : null}
      <HowToUse channel="feishu">
        <li>Send text, rich text, images or files up to 20 MB.</li>
        <li>Groups and threads are not supported.</li>
      </HowToUse>
      {connected ? <TurnOff channel="feishu" onTurnOff={() => setOff(true)} /> : null}
      {off ? (
        <Confirm
          title="Turn off Feishu / Lark?"
          description="dex stops the bot and forgets its credentials. Past chats stay."
          confirmLabel="Turn off"
          onCancel={() => setOff(false)}
          onConfirm={() => remove.mutate({ path: { agentId } })}
          pending={remove.isPending}
          error={remove.isError ? remove.error : undefined}
        />
      ) : null}
    </>
  );
}
