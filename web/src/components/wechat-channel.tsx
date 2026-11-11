import type { Channels as Settings } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { WechatLogin } from "../api/generated/types.gen";
import {
  beginWechatLoginMutation,
  deleteWechatMutation,
  pollWechatLoginMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { refreshSettings } from "../lib/queries";
import { Button } from "./ui/button";
import { Failure, Spinner } from "./feedback";
import { Field, Input } from "./form";
import { Facts, Mono, Note, Panel, Section } from "./page";
import { Confirm } from "./confirm";
import { HowToUse, Notifications, Steps, TurnOff } from "./channel-kit";

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
  const agentId = useAgentId();
  const account = settings.wechat;
  const [login, setLogin] = useState<WechatLogin>();
  const [code, setCode] = useState("");
  const [off, setOff] = useState(false);
  const start = useMutation({ ...beginWechatLoginMutation({ path: { agentId } }), onSuccess: setLogin });

  const poll = useMutation({
    ...pollWechatLoginMutation(),
    onSuccess: async (next) => {
      setLogin(next);

      if (!next.active) await refreshSettings();
    },
  });

  const deletion = useMutation({
    ...deleteWechatMutation({ path: { agentId } }),
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
      pollLogin({ path: { agentId, loginId: login.id }, body: {} });
    }, 1000);

    return () => window.clearTimeout(timer);
  }, [agentId, login, pending, pollLogin]);

  const scan = (
    <Button
      size="sm"
      variant={account.enabled ? "secondary" : "default"}
      disabled={start.isPending || pending}
      onClick={() => {
        poll.reset();
        start.mutate({ path: { agentId } });
      }}
    >
      {start.isPending ? "Getting QR code…" : account.enabled ? "Scan again" : "Connect WeChat"}
    </Button>
  );

  return (
    <>
      <Section n={1} title="Connection" action={account.enabled ? scan : null}>
        {account.enabled ? (
          <Panel>
            <Facts rows={[["Bot", <Mono key="bot">{account.botId}</Mono>]]} />
          </Panel>
        ) : (
          <Panel className="px-4 py-3.5">
            <Steps>
              <li>Select Connect WeChat. dex shows a QR code.</li>
              <li>Scan the code with WeChat on your phone and confirm.</li>
            </Steps>
            <div className="mt-4">{scan}</div>
          </Panel>
        )}
        {account.status.state === "needs_login" ? (
          <div className="mt-3">
            <Note tone="warn">
              The saved login expired. Scan again to resume messages and pending replies.
            </Note>
          </div>
        ) : account.status.reason ? (
          <div className="mt-3">
            <Note>{account.status.reason}</Note>
          </div>
        ) : null}
        {login ? (
          <Panel className="mt-3 space-y-3 px-4 py-3.5">
            <p role="status" className="text-[13px] text-muted-foreground">
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

                  pollLogin({ path: { agentId, loginId: login.id }, body: { verifyCode: code } });
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
                  variant="default"
                  disabled={pending || !/^[0-9]{1,16}$/.test(code)}
                >
                  Confirm
                </Button>
              </form>
            ) : null}
            {pending ? <Spinner /> : null}
          </Panel>
        ) : null}
        {start.error || poll.error ? (
          <div className="mt-3">
            <Failure title="WeChat login did not finish" error={start.error ?? poll.error} />
          </div>
        ) : null}
      </Section>
      {account.enabled ? (
        <>
          <Section n={2} title="Who can use it" caption="Only the person who confirmed the login.">
            <Panel>
              <Facts rows={[["Owner", <Mono key="owner">{account.owner}</Mono>]]} />
            </Panel>
          </Section>
          <Notifications channel="wechat" />
        </>
      ) : null}
      <HowToUse channel="wechat" />
      {account.enabled ? <TurnOff channel="wechat" onTurnOff={() => setOff(true)} /> : null}
      {off ? (
        <Confirm
          title="Turn off WeChat?"
          description="dex stops the bot and forgets its credentials. Past chats stay read-only. Scan again to reconnect."
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
