import { useEffect, useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Plus } from "lucide-react";
import {
  createMcpServerMutation,
  getBotIntegrationsOptions,
  getBotIntegrationsQueryKey,
  listBotsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import type { Bot, CreateMcpServerData } from "../api/generated/types.gen";
import { Modal } from "../components/dialog";
import { Failure, Loading } from "../components/feedback";
import { Field, Input, RadioSegments, Select } from "../components/form";
import { FramedPage, Note } from "../components/page";
import { SourceSettings } from "../components/source-settings";
import { Button } from "../components/ui/button";
import { BotContext, useBotId } from "../lib/bot-scope";
import { ChannelsSection, WithChannels } from "./channels";
import { IntegrationCatalog, McpServerStatus, useAuthorizeMcp } from "./skills";

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

type Auth = "none" | "oauth";

const authOptions = [
  { value: "none", label: "No authentication" },
  { value: "oauth", label: "Browser OAuth" },
] as const;

function AddMcpServerDialog({ bot, onClose }: { bot: Bot; onClose: () => void }) {
  // The target is fixed when the dialog opens. A save and its result always use this Bot.
  const botId = bot.id!;
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [auth, setAuth] = useState<Auth>("none");
  const [advanced, setAdvanced] = useState(false);
  const [clientId, setClientId] = useState("");
  const [secretName, setSecretName] = useState("");
  const [scope, setScope] = useState("");
  const [added, setAdded] = useState<{ name: string; auth: Auth }>();
  const advancedId = useId();

  const secret = secretName.trim();

  const secretError =
    secret && !vaultName.test(secret)
      ? "Use a vault name: letters, digits, and _."
      : secret && !clientId.trim()
        ? "Add a Client ID for this client secret."
        : undefined;

  const advancedError = auth === "oauth" ? secretError : undefined;

  const advancedSet = [
    clientId.trim() ? "Client ID" : "",
    secret ? "client secret" : "",
    scope.trim() ? "scope" : "",
  ].filter(Boolean);

  const save = useMutation({
    ...createMcpServerMutation(),
    onSuccess: async (_, request) => {
      await client.invalidateQueries({ queryKey: getBotIntegrationsQueryKey({ path: { botId } }) });
      setAdded({ name: request.body.name, auth: request.body.oauth ? "oauth" : "none" });
    },
  });

  if (added) return <AddedMcpServer bot={bot} added={added} onClose={onClose} />;

  const submit = () => {
    const body: CreateMcpServerData["body"] = { name: name.trim(), url: url.trim() };

    // No authentication sends no OAuth settings, even if Advanced options hold values.
    if (auth === "oauth") {
      const config: NonNullable<CreateMcpServerData["body"]["oauth"]> = {};

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
      description="Connects one Bot to a remote server. Other Bots do not change."
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
            disabled={save.isPending || !name.trim() || !url.trim() || !!advancedError}
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
        <p className="rounded-md bg-muted px-3 py-2 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
          {`Bot: ${bot.name}`}
        </p>
        <Field label="Name" hint="Letters, digits, - and _.">
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
        <Field label="Server URL" hint="HTTPS, or HTTP on this computer. No user name, password, or query.">
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
        <div className="flex flex-col gap-1.5">
          <span aria-hidden className="text-[12.5px] font-medium text-foreground">
            Authentication
          </span>
          <RadioSegments label="Authentication" options={authOptions} value={auth} onChange={setAuth} />
          <p className="text-[12px] text-muted-foreground">
            {auth === "none"
              ? "For a public server. Dex sends no credentials."
              : "You sign in to the server in this browser after you add it. Most servers need nothing else."}
          </p>
        </div>
        {auth === "oauth" ? (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Button
                type="button"
                size="xs"
                variant="ghost"
                aria-expanded={advanced}
                aria-controls={advancedId}
                onClick={() => setAdvanced((open) => !open)}
              >
                {advanced ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}
                Advanced options
              </Button>
              {!advanced && !advancedError && advancedSet.length ? (
                <span className="text-[11.5px] text-muted-foreground">{`Set: ${advancedSet.join(", ")}`}</span>
              ) : null}
            </div>
            {!advanced && advancedError ? (
              <p className="text-[12px] text-red">{`Check Advanced options. ${advancedError}`}</p>
            ) : null}
            {advanced ? (
              <div id={advancedId} className="flex flex-col gap-4 rounded-md border border-border p-3.5">
                <p className="text-[12px] text-muted-foreground">
                  Only for a provider that gave you a registered client.
                </p>
                <Field label="Client ID">
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
                  hint="The name of an entry in this Bot's Secrets, never the secret itself."
                  error={secretError}
                >
                  {({ id, describedBy, invalid }) => (
                    <Input
                      id={id}
                      aria-describedby={describedBy}
                      aria-invalid={invalid || undefined}
                      value={secretName}
                      onChange={(event) => setSecretName(event.target.value)}
                      autoComplete="off"
                      spellCheck={false}
                    />
                  )}
                </Field>
                <Field label="Scope" hint="Otherwise Dex uses the scope that the server asks for.">
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
              </div>
            ) : null}
          </div>
        ) : null}
        {save.isError ? <Failure title="Could not add the MCP server" error={save.error} /> : null}
      </form>
    </Modal>
  );
}

/**
 * The result of a save, read from the Bot's stored declarations. It names the next action and
 * does not claim a live connection. A busy Bot lists the declaration only once it is idle.
 */
function AddedMcpServer({
  bot,
  added,
  onClose,
}: {
  bot: Bot;
  added: { name: string; auth: Auth };
  onClose: () => void;
}) {
  const botId = bot.id!;
  const integrations = useQuery(getBotIntegrationsOptions({ path: { botId } }));

  const server = integrations.data?.servers.find(
    (item) => item.name === added.name && item.scope === "agent",
  );

  const authorize = useAuthorizeMcp();
  const canAuthorize = !!server?.oauth && server.state !== "disabled";

  return (
    <Modal
      title={`Added ${added.name}`}
      description={`Saved for ${bot.name}. Other Bots do not change.`}
      pending={authorize.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant={canAuthorize ? "secondary" : "default"} onClick={onClose}>
            Done
          </Button>
          {canAuthorize ? (
            <Button
              disabled={authorize.isPending}
              onClick={() =>
                authorize.mutate({ path: { botId, name: added.name }, body: { scope: "agent" } })
              }
            >
              {authorize.isPending
                ? "Starting…"
                : server?.oauth === "authorize"
                  ? "Authorize"
                  : "Reauthorize"}
            </Button>
          ) : null}
        </>
      }
    >
      {integrations.isPending ? (
        <Loading />
      ) : server ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-[12.5px] font-medium text-foreground">{server.name}</span>
            <span className="flex-1" />
            <McpServerStatus server={server} />
          </div>
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">
            {canAuthorize
              ? "Next, authorize it. Dex opens the server's sign-in page and returns to this Bot's Integrations."
              : "Dex connects when this Bot starts or reloads. The MCP servers list shows the connection."}
          </p>
          {authorize.isError ? (
            <Failure error={authorize.error} title="Could not start authorization" />
          ) : null}
        </>
      ) : (
        <Note>
          {`The server appears under MCP servers when ${bot.name} is idle.${added.auth === "oauth" ? " Authorize it there." : ""}`}
        </Note>
      )}
    </Modal>
  );
}
