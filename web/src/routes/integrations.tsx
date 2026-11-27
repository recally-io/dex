import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight, Plus } from "lucide-react";
import {
  getBotIntegrationsQueryKey,
  getSharedIntegrationsOptions,
  getSharedIntegrationsQueryKey,
  listBotsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import {
  createMcpServer,
  createSharedMcpServer,
  getBotIntegrations,
  getSharedIntegrations,
} from "../api/generated/sdk.gen";
import type { McpServerInput, SharedIntegrations } from "../api/generated/types.gen";
import { Modal } from "../components/dialog";
import { Failure, Loading } from "../components/feedback";
import { Field, Input, RadioSegments, Select } from "../components/form";
import { FramedPage, Note } from "../components/page";
import { Button } from "../components/ui/button";
import { useApp } from "../lib/app-state";
import {
  Configuration,
  IntegrationSections,
  McpServerStatus,
  useAuthorizeMcp,
  type IntegrationSection,
} from "./skills";

const vaultName = /^[A-Za-z_][A-Za-z0-9_]*$/;

type AddIntegration = "mcp" | "skill";

export type IntegrationsSearch = { section?: IntegrationSection; add?: AddIntegration; target?: string };

export function IntegrationsPage({
  section,
  add,
  target,
  onAdd,
  onClose,
}: IntegrationsSearch & {
  onAdd: (kind: AddIntegration) => void;
  onClose: () => void;
}) {
  const query = useQuery(getSharedIntegrationsOptions());

  return (
    <FramedPage
      headerTitle="Integrations"
      title="Integrations"
      description="Browse shared skills and MCP servers for this Dex instance."
    >
      {query.isError ? (
        <Failure
          title="Could not load shared integrations"
          error={query.error}
          retry={() => void query.refetch()}
        />
      ) : null}
      {query.data ? (
        <>
          <Configuration extensions={{ warnings: query.data.errors, errors: [] }} />
          <IntegrationSections
            {...query.data}
            section={add ? undefined : section}
            skillAction={
              <Button size="sm" onClick={() => onAdd("skill")}>
                <Plus aria-hidden /> Add skill
              </Button>
            }
            mcpAction={
              <Button size="sm" onClick={() => onAdd("mcp")}>
                <Plus aria-hidden /> Add MCP server
              </Button>
            }
          />
        </>
      ) : query.isPending ? (
        <Loading />
      ) : null}
      {add === "mcp" ? <AddMcpServerDialog key={target} initialTarget={target} onClose={onClose} /> : null}
      {add === "skill" ? <AddSkillDialog key={target} initialTarget={target} onClose={onClose} /> : null}
    </FramedPage>
  );
}

type BotTarget = { id: string; name: string };

type AddDialogProps = { initialTarget?: string; onClose: () => void };

function useTarget(initialTarget?: string) {
  const [selected, setSelected] = useState(initialTarget);
  const list = useQuery({ ...listBotsOptions(), refetchOnMount: "always" });

  const bots = (list.data?.bots ?? []).flatMap((bot) =>
    bot.id && !bot.archivedAt ? [{ id: bot.id, name: bot.name }] : [],
  );

  const bot = bots.find((item) => item.id === selected);
  const checking = !list.isFetchedAfterMount && list.isFetching;
  const valid = selected === undefined || (!!bot && !checking && !list.isError);

  return { selected, setSelected, bots, bot, checking, valid, list };
}

function TargetField({ target, disabled }: { target: ReturnType<typeof useTarget>; disabled?: boolean }) {
  const { selected, setSelected, bots, bot, checking, list } = target;
  const unavailable = selected !== undefined && !bot;

  return (
    <>
      <Field
        label="Target"
        hint="Bots inherit a shared entry unless they override or disable it."
        error={
          unavailable && !checking && list.isSuccess
            ? "This Bot is not available. Choose a target."
            : undefined
        }
      >
        {({ id, describedBy, invalid }) => (
          <Select
            id={id}
            aria-describedby={describedBy}
            aria-invalid={invalid || undefined}
            disabled={disabled}
            value={unavailable ? "?" : (bot?.id ?? "")}
            onChange={(event) => setSelected(event.target.value || undefined)}
          >
            {unavailable ? (
              <option value="?" disabled>
                {checking ? "Checking target…" : "Unavailable Bot"}
              </option>
            ) : null}
            <option value="">Shared (all Bots)</option>
            {bots.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </Select>
        )}
      </Field>
      {list.isError ? (
        <Failure title="Could not load Bots" error={list.error} retry={() => void list.refetch()} />
      ) : null}
    </>
  );
}

function AddSkillDialog({ initialTarget, onClose }: AddDialogProps) {
  const target = useTarget(initialTarget);
  const { notify } = useApp();
  const level = target.bot ? "agent" : "dex";
  const request = `Use skill_import to import /path/to/my-skill from your Runner at level ${level}.`;

  async function copyRequest() {
    try {
      await navigator.clipboard.writeText(request);
      notify("Request copied");
    } catch {
      notify("Could not copy. Select the request to copy it.");
    }
  }

  return (
    <Modal
      title="Add skill"
      description="Import a skill folder from a Bot's Runner in chat. This page does not upload files."
      onClose={onClose}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <TargetField target={target} />
      {target.valid ? (
        <>
          <p className="text-[12.5px] text-muted-foreground">
            {target.bot ? "Ask this Bot" : "Ask any Bot"} to run <code>skill_import</code>. Replace the
            example path with the folder that contains SKILL.md.
          </p>
          <p className="rounded-md bg-muted p-3 font-mono text-[12px]">{request}</p>
        </>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" disabled={!target.valid} onClick={() => void copyRequest()}>
          Copy request
        </Button>
        {target.valid && target.bot ? (
          <Link to="/b/$botId" params={{ botId: target.bot.id }} className="text-[12.5px] underline">
            {`Open ${target.bot.name} chat`}
          </Link>
        ) : null}
      </div>
    </Modal>
  );
}

type Auth = "none" | "oauth";

/** A saved declaration. savedAt tells a status read after the save from the list cached before it. */
type Added = { name: string; auth: Auth; savedAt: number; bot?: BotTarget };

const authOptions = [
  { value: "none", label: "No authentication" },
  { value: "oauth", label: "Browser OAuth" },
] as const;

function AddMcpServerDialog({ initialTarget, onClose }: AddDialogProps) {
  const target = useTarget(initialTarget);
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [auth, setAuth] = useState<Auth>("none");
  const [advanced, setAdvanced] = useState(false);
  const [clientId, setClientId] = useState("");
  const [secretName, setSecretName] = useState("");
  const [scope, setScope] = useState("");
  const [added, setAdded] = useState<Added>();
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
    mutationFn: ({ bot, body }: { bot?: BotTarget; body: McpServerInput }) =>
      bot
        ? createMcpServer({ path: { botId: bot.id }, body, throwOnError: true })
        : createSharedMcpServer({ body, throwOnError: true }),
    onSuccess: (_, { bot, body }) => {
      // The POST decides the outcome. The status read that follows can fail on its own.
      setAdded({ name: body.name, auth: body.oauth ? "oauth" : "none", savedAt: Date.now(), bot });

      if (bot) {
        void client.invalidateQueries({ queryKey: getBotIntegrationsQueryKey({ path: { botId: bot.id } }) });
      } else {
        void client.invalidateQueries({ queryKey: getSharedIntegrationsQueryKey() });
        // Mark cached Bot views stale without opening a Bot to refresh them here.
        void client.invalidateQueries({ queryKey: [{ _id: "getBotIntegrations" }], refetchType: "none" });
      }
    },
  });

  if (added) return <AddedMcpServer added={added} onClose={onClose} />;

  const canSave = target.valid && !save.isPending && !!name.trim() && !!url.trim() && !advancedError;

  const submit = () => {
    if (!canSave) return;

    const body: McpServerInput = { name: name.trim(), url: url.trim() };

    // No authentication sends no OAuth settings, even if Advanced options hold values.
    if (auth === "oauth") {
      const config: NonNullable<McpServerInput["oauth"]> = {};

      if (clientId.trim()) config.clientId = clientId.trim();

      if (secret) config.clientSecret = `vault:${secret}`;

      if (scope.trim()) config.scope = scope.trim();

      body.oauth = config;
    }

    save.mutate({ bot: target.bot, body });
  };

  return (
    <Modal
      title="Add MCP server"
      description="Add a remote server for all Bots or for one Bot."
      pending={save.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={save.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="add-mcp-server" disabled={!canSave}>
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
        <TargetField target={target} disabled={save.isPending} />
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
                  hint={
                    target.bot ? "Use a name from this Bot's Secrets." : "Use a name from shared Secrets."
                  }
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
 * The POST confirms the save. A fresh read supplies status for the target used at submit time.
 */
function AddedMcpServer({ added, onClose }: { added: Added; onClose: () => void }) {
  const { bot } = added;

  const integrations = useQuery({
    queryKey: bot ? getBotIntegrationsQueryKey({ path: { botId: bot.id } }) : getSharedIntegrationsQueryKey(),
    queryFn: async ({ signal }): Promise<SharedIntegrations> => {
      const { data } = bot
        ? await getBotIntegrations({ path: { botId: bot.id }, signal, throwOnError: true })
        : await getSharedIntegrations({ signal, throwOnError: true });

      return data;
    },
  });

  const scope = bot ? "agent" : "dex";
  const hasFreshStatus = integrations.dataUpdatedAt >= added.savedAt;

  const server = hasFreshStatus
    ? integrations.data?.servers.find((item) => item.name === added.name && item.scope === scope)
    : undefined;

  const authorize = useAuthorizeMcp(bot?.id);
  const canAuthorize = !!server?.oauth && server.state !== "disabled";

  return (
    <Modal
      title={`Added ${added.name}`}
      description={
        bot
          ? `Saved for ${bot.name}. Other Bots do not change.`
          : "Saved as shared. Bots inherit it unless they override or disable it."
      }
      pending={authorize.isPending}
      onClose={onClose}
      footer={
        <>
          <Button
            variant={canAuthorize ? "secondary" : "default"}
            disabled={authorize.isPending}
            onClick={onClose}
          >
            Done
          </Button>
          {canAuthorize ? (
            <Button
              disabled={authorize.isPending}
              onClick={() => authorize.mutate({ name: added.name, scope })}
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
      {authorize.isPending ? (
        <p role="status" className="text-[12.5px] text-muted-foreground">
          Starting browser authorization…
        </p>
      ) : integrations.isError ? (
        <Failure
          title="Could not load the server status"
          error={integrations.error}
          retry={() => void integrations.refetch()}
        >
          <p>The server is saved. Try again to read its status.</p>
          {bot ? (
            <Link
              to="/b/$botId/integrations"
              params={{ botId: bot.id }}
              className="mt-1 inline-block text-foreground underline"
            >
              {`Open ${bot.name} Integrations`}
            </Link>
          ) : null}
        </Failure>
      ) : server ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-[12.5px] font-medium text-foreground">{server.name}</span>
            <span className="flex-1" />
            <McpServerStatus server={server} botView={!!bot} />
          </div>
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">
            {canAuthorize
              ? "Next, authorize it. Dex opens the server's sign-in page and returns to Integrations."
              : bot
                ? "Dex connects when this Bot starts or reloads. The MCP servers list shows the connection."
                : "Bots that use this server connect when they start or reload."}
          </p>
          {authorize.isError ? (
            <Failure error={authorize.error} title="Could not start authorization" />
          ) : null}
        </>
      ) : !hasFreshStatus ? (
        // No list read since the save, or none at all. Wait for it before any idle or auth guidance.
        <p role="status" className="text-[12.5px] text-muted-foreground">
          Checking server status…
        </p>
      ) : bot ? (
        <Note>
          {`The server appears under MCP servers when ${bot.name} is idle.${added.auth === "oauth" ? " Authorize it there." : ""}`}
        </Note>
      ) : (
        <Failure title="The saved server is not in the list" retry={() => void integrations.refetch()} />
      )}
    </Modal>
  );
}
