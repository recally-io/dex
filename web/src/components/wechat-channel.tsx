import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Settings, WechatLogin } from "../api/generated/types.gen";
import {
  beginWechatLoginMutation,
  deleteWechatMutation,
  getSettingsOptions,
  pollWechatLoginMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { Button } from "./atoms/Button";
import { StatusPill } from "./atoms/StatusPill";
import { Failure, Spinner } from "./feedback";
import { Field, Input } from "./form";
import { Facts, Mono, Note, Panel, Section } from "./page";
import { Confirm } from "./confirm";

const loginText: Record<WechatLogin["status"], string> = {
  wait: "Scan this QR code with WeChat on your phone.",
  scaned: "QR code scanned. Confirm the connection on your phone.",
  need_verifycode: "Enter the digits shown on your phone.",
  verify_code_blocked: "Too many incorrect codes. Scan the new QR code to try again.",
  scaned_but_redirect: "WeChat is checking the connection in another region.",
  expired: "The QR code expired. Scan the new code if one is shown.",
  binded_redirect: "This bot is already linked. WeChat returned no new credentials.",
  confirmed: "WeChat connected. Only the person who confirmed can talk to dex.",
};

export function WechatChannel({ settings }: { settings: Settings }) {
  const current = useQuery({ ...getSettingsOptions(), refetchInterval: 3000 }).data ?? settings;
  const account = current.wechat;
  const [login, setLogin] = useState<WechatLogin>();
  const [code, setCode] = useState("");
  const [off, setOff] = useState(false);
  const start = useMutation({ ...beginWechatLoginMutation(), onSuccess: setLogin });

  const poll = useMutation({
    ...pollWechatLoginMutation(),
    onSuccess: async (next) => {
      setLogin(next);

      if (!next.active) await refreshSettings();
    },
  });

  const deletion = useMutation({
    ...deleteWechatMutation(),
    onSuccess: async () => {
      setOff(false);
      setLogin(undefined);
      await refreshSettings();
    },
  });

  const pollLogin = poll.mutate;
  const pending = poll.isPending;

  useEffect(() => {
    if (!login?.active || login.status === "need_verifycode" || pending) return;

    const timer = window.setTimeout(() => {
      pollLogin({ path: { loginId: login.id }, body: {} });
    }, 1000);

    return () => window.clearTimeout(timer);
  }, [login, pending, pollLogin]);

  return (
    <Section n={2} title="WeChat" caption="Scan once. dex recognizes the owner from the login.">
      <Panel className="space-y-4 px-4 py-3.5">
        <div className="flex items-center justify-between gap-3">
          <p className="text-[13px] text-ink-2">Private messages from the owner only.</p>
          <StatusPill tone={account.status.state === "running" ? "green" : "neutral"}>
            {account.status.state === "needs_login" ? "Scan again" : account.status.state}
          </StatusPill>
        </div>
        {account.enabled ? (
          <Facts
            rows={[
              ["Owner", <Mono key="owner">{account.owner}</Mono>],
              ["Bot", <Mono key="bot">{account.botId}</Mono>],
            ]}
          />
        ) : null}
        {account.status.reason ? <Note>{account.status.reason}</Note> : null}
        {account.status.state === "needs_login" ? (
          <Note tone="warn">The saved login expired. Scan again to resume messages and pending replies.</Note>
        ) : null}
        {login ? (
          <div className="space-y-3">
            <p role="status" className="text-[13px] text-ink-2">
              {loginText[login.status]}
            </p>
            {login.active ? <WechatQR content={login.qrContent} /> : null}
            {login.reason ? <Note>{login.reason}</Note> : null}
            {login.active && login.status === "need_verifycode" ? (
              <form
                className="flex max-w-xs items-end gap-2"
                onSubmit={(event) => {
                  event.preventDefault();

                  if (!/^[0-9]{1,16}$/.test(code) || pending) return;

                  pollLogin({ path: { loginId: login.id }, body: { verifyCode: code } });
                  setCode("");
                }}
              >
                <Field label="Phone code" hint="Use the digits shown in WeChat.">
                  {({ id }) => (
                    <Input
                      id={id}
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      value={code}
                      onChange={(event) => setCode(event.target.value)}
                    />
                  )}
                </Field>
                <Button
                  type="submit"
                  size="sm"
                  variant="primary"
                  disabled={pending || !/^[0-9]{1,16}$/.test(code)}
                >
                  Confirm
                </Button>
              </form>
            ) : null}
            {pending ? <Spinner /> : null}
          </div>
        ) : null}
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="primary"
            disabled={start.isPending || pending}
            onClick={() => {
              poll.reset();
              start.mutate({});
            }}
          >
            {start.isPending ? "Getting QR code…" : account.enabled ? "Scan again" : "Connect WeChat"}
          </Button>
          {account.enabled ? (
            <Button size="sm" variant="secondary" onClick={() => setOff(true)}>
              Turn off WeChat
            </Button>
          ) : null}
        </div>
        {start.error || poll.error ? (
          <Failure title="WeChat login did not finish" error={start.error ?? poll.error} />
        ) : null}
        <p className="text-[12px] text-ink-3">
          Use /new for a new chat, /stop to stop an answer, and /help for commands. Past chats stay read-only
          on the web.
        </p>
      </Panel>
      {off ? (
        <Confirm
          title="Turn off WeChat?"
          description="dex stops the bot and forgets its credentials. Past chats stay read-only. Scan again to reconnect."
          confirmLabel="Turn off"
          onCancel={() => setOff(false)}
          onConfirm={() => deletion.mutate({})}
          pending={deletion.isPending}
          error={deletion.error}
        />
      ) : null}
    </Section>
  );
}

function WechatQR({ content }: { content: string }) {
  const qr = useQuery({
    queryKey: ["wechat-qr", content],
    queryFn: async () => {
      const { toDataURL } = await import("qrcode");

      return toDataURL(content, { width: 240, margin: 2 });
    },
    staleTime: Infinity,
  });

  return qr.data ? (
    <img
      src={qr.data}
      width={240}
      height={240}
      alt="Scan with WeChat to connect dex"
      className="rounded-[9px] bg-white"
    />
  ) : qr.error ? (
    <Failure title="Could not draw the QR code" error={qr.error} />
  ) : (
    <Spinner />
  );
}
