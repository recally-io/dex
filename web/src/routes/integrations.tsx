import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import {
  createMcpServerMutation,
  getBotIntegrationsQueryKey,
  listBotsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import type { Bot, CreateMcpServerData } from "../api/generated/types.gen";
import { Modal } from "../components/dialog";
import { Failure, Loading } from "../components/feedback";
import { Field, Input, Select } from "../components/form";
import { FramedPage, Note } from "../components/page";
import { SourceSettings } from "../components/source-settings";
import { Button } from "../components/ui/button";
import { Switch } from "../components/ui/switch";
import { useApp } from "../lib/app-state";
import { BotContext, useBotId } from "../lib/bot-scope";
import { ChannelsSection, WithChannels } from "./channels";
import { IntegrationCatalog } from "./skills";

const vaultName = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * One starting point for connections. Channels and MCP servers belong to the selected Bot, which
 * the address names. Sources belong to the whole Dex instance, so the selection does not change them.
 */
export function IntegrationsPage({
  bot,
  onBotChange,
}: {
  bot?: string;
  onBotChange: (bot: string, replace: boolean) => void;
}) {
  const bots = useQuery(listBotsOptions());
  // Outside a Bot route, the shell provides the Bot that the owner opened last.
  const recent = useBotId();
  const list = bots.data?.bots ?? [];
  const selected = list.find((item) => item.id === bot);
  const fallback = list.some((item) => item.id === recent) ? recent : list[0]?.id;

  // Without a Bot in the address, name the recent Bot there, so the scope is always explicit.
  useEffect(() => {
    if (!bot && bots.data && fallback) onBotChange(fallback, true);
  }, [bot, bots.data, fallback, onBotChange]);

  return (
    <FramedPage
      headerTitle="Integrations"
      title="Integrations"
      description="Connect a Bot to private chats and MCP servers. Sources send events to the whole Dex instance."
      toolbar={
        bots.isError ? (
          <Failure title="Could not load the bots" error={bots.error} retry={() => void bots.refetch()} />
        ) : bots.data ? (
          <div className="flex max-w-[360px] flex-col gap-3">
            <Field
              label="Bot"
              hint="Channels and new MCP servers belong to this Bot. Inherited Dex servers are shared."
            >
              {({ id, describedBy }) => (
                <Select
                  id={id}
                  aria-describedby={describedBy}
                  value={selected?.id ?? ""}
                  onChange={(event) => {
                    if (event.target.value) onBotChange(event.target.value, false);
                  }}
                >
                  {selected ? null : <option value="">Choose a Bot</option>}
                  {list.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            {bot && !selected ? <Note tone="warn">This Bot is not available. Choose a Bot.</Note> : null}
          </div>
        ) : (
          <Loading />
        )
      }
    >
      {selected ? (
        <BotContext.Provider key={selected.id} value={selected.id!}>
          <WithChannels>{(settings) => <ChannelsSection settings={settings} />}</WithChannels>
          <IntegrationCatalog section="integrations" addAction={<AddMcpServer bot={selected} />} />
        </BotContext.Provider>
      ) : null}
      <SourceSettings caption="Sources send events to this whole Dex instance, for every Bot. Changes apply when Dex starts again." />
    </FramedPage>
  );
}

/** Adds a remote Streamable HTTP server to one Bot. It takes no headers or static tokens. */
function AddMcpServer({ bot }: { bot: Bot }) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}>
        <Plus aria-hidden /> Add MCP server
      </Button>
      {open ? <AddMcpServerDialog bot={bot} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function AddMcpServerDialog({ bot, onClose }: { bot: Bot; onClose: () => void }) {
  const botId = bot.id!;
  const client = useQueryClient();
  const { notify } = useApp();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [oauth, setOAuth] = useState(false);
  const [clientId, setClientId] = useState("");
  const [secretName, setSecretName] = useState("");
  const [scope, setScope] = useState("");
  const secretInvalid = secretName.trim() !== "" && !vaultName.test(secretName.trim());

  const save = useMutation({
    ...createMcpServerMutation(),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: getBotIntegrationsQueryKey({ path: { botId } }) });
      notify(`Added ${name.trim()} to ${bot.name}.${oauth ? " Authorize the server before use." : ""}`);
      onClose();
    },
  });

  const submit = () => {
    const body: CreateMcpServerData["body"] = { name: name.trim(), url: url.trim() };

    if (oauth) {
      const config: NonNullable<CreateMcpServerData["body"]["oauth"]> = {};
      const secret = secretName.trim();

      if (clientId.trim()) config.clientId = clientId.trim();

      if (secret) config.clientSecret = `vault:${secret}`;

      if (scope.trim()) config.scope = scope.trim();

      body.oauth = config;
    }

    save.mutate({ path: { botId }, body });
  };

  return (
    <Modal
      title="Add MCP server"
      description={`Adds a Bot-level declaration to ${bot.name}. Other Bots do not change. A name that this Bot or Dex already uses is refused.`}
      pending={save.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={save.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="add-mcp-server"
            disabled={save.isPending || !name.trim() || !url.trim() || secretInvalid}
          >
            {save.isPending ? "Adding…" : "Add server"}
          </Button>
        </>
      }
    >
      <form
        id="add-mcp-server"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Field label="Name" hint="Letters, digits, - and _. The bot gets tools as mcp__<name>__<tool>.">
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              value={name}
              onChange={(event) => setName(event.target.value)}
              pattern="[A-Za-z0-9_\-]{1,32}"
              maxLength={32}
              autoComplete="off"
              required
              autoFocus
            />
          )}
        </Field>
        <Field
          label="Server URL"
          hint="A Streamable HTTP endpoint over HTTPS, or HTTP on this computer. No user name, password, or query."
        >
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              type="url"
              inputMode="url"
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              autoComplete="off"
              required
            />
          )}
        </Field>
        <label className="flex items-center justify-between gap-3 text-[13px] text-foreground">
          <span>
            Browser authorization (OAuth)
            <span className="block text-[12px] text-muted-foreground">
              For a server that needs sign-in. Dex takes no headers or static tokens here.
            </span>
          </span>
          <Switch checked={oauth} onCheckedChange={setOAuth} aria-label="Use browser authorization (OAuth)" />
        </label>
        {oauth ? (
          <>
            <Field label="Client ID" hint="Optional. Use it when the provider gave you a registered client.">
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  value={clientId}
                  onChange={(event) => setClientId(event.target.value)}
                  autoComplete="off"
                />
              )}
            </Field>
            <Field
              label="Client secret vault name"
              hint="Optional, with a Client ID. The name of an entry in this Bot's Secrets. Never the secret itself."
              error={secretInvalid ? "Use a vault name: letters, digits, and _." : undefined}
            >
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  aria-invalid={secretInvalid || undefined}
                  value={secretName}
                  onChange={(event) => setSecretName(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              )}
            </Field>
            <Field label="Scope" hint="Optional. Dex otherwise uses the scope that the server asks for.">
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  value={scope}
                  onChange={(event) => setScope(event.target.value)}
                  autoComplete="off"
                />
              )}
            </Field>
          </>
        ) : null}
        {save.isError ? <Failure title="Could not add the MCP server" error={save.error} /> : null}
      </form>
    </Modal>
  );
}
