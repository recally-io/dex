import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronDown, ChevronRight, Plus } from "lucide-react";
import {
  getBotIntegrationsQueryKey,
  getSharedIntegrationsOptions,
  getSharedIntegrationsQueryKey,
  listBotsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import {
  createBotSession,
  getBotIntegrations,
  getSharedIntegrations,
  sendMessage,
} from "../api/generated/sdk.gen";
import type { McpServerInput, SharedIntegrations, SkillCatalogEntry } from "../api/generated/types.gen";
import { Modal } from "../components/dialog";
import { Failure, Loading } from "../components/feedback";
import { Field, Input, RadioSegments, Select } from "../components/form";
import { FramedPage, Note } from "../components/page";
import { Button } from "../components/ui/button";
import { Switch } from "../components/ui/switch";
import {
  checkMcpHeaders,
  mcpServerName,
  requiresVault,
  saveMcpDeclaration,
  type McpDraft,
  type McpHeaderDraft,
} from "../lib/mcp-declaration";
import { errorText, refreshSessions } from "../lib/queries";
import { buildSkillInstallPrompt, validSkillDirectory } from "../lib/skill-install";
import { newId } from "../lib/utils";
import { GoogleMcpClientFields, reusedClientSecret, type GoogleClientForm } from "./google-mcp-client";
import { McpCatalog, SkillCatalog } from "./integration-catalogs";
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
  const query = useQuery({ ...getSharedIntegrationsOptions(), refetchOnMount: "always" });
  const [install, setInstall] = useState<SkillCatalogEntry>();
  const [draft, setDraft] = useState<McpDraft>();

  function openMcp(prefill?: McpDraft) {
    setDraft(prefill);
    onAdd("mcp");
  }

  return (
    <FramedPage
      headerTitle="Integrations"
      title="Integrations"
      description="Find and add skills and MCP servers for all Bots or for one Bot."
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
              <Button
                size="sm"
                onClick={() => {
                  if (add === "skill") document.getElementById("skill-catalog-search")?.focus();
                  else onAdd("skill");
                }}
              >
                <Plus aria-hidden /> Add skill
              </Button>
            }
            mcpAction={
              <Button size="sm" onClick={() => openMcp()}>
                <Plus aria-hidden /> Add MCP server
              </Button>
            }
            skillCatalog={<SkillCatalog focus={add === "skill"} onInstall={setInstall} />}
            mcpCatalog={<McpCatalog onAdd={openMcp} />}
          />
        </>
      ) : query.isPending ? (
        <Loading />
      ) : null}
      {add === "mcp" ? (
        <AddMcpServerDialog key={target} initialTarget={target} draft={draft} onClose={onClose} />
      ) : null}
      {install ? (
        <InstallSkillDialog
          key={install.id}
          skill={install}
          initialTarget={target}
          onClose={() => setInstall(undefined)}
        />
      ) : null}
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

function InstallSkillDialog({
  initialTarget,
  onClose,
  skill,
}: AddDialogProps & { skill: SkillCatalogEntry }) {
  const target = useTarget(initialTarget);
  const [level, setLevel] = useState<"dex" | "agent">(initialTarget ? "agent" : "dex");
  const [name, setName] = useState("");
  const navigate = useNavigate();

  const [attempt, setAttempt] = useState<{
    botId: string;
    sessionId: number;
    messageId: string;
    text: string;
  }>();

  const install = useMutation({
    mutationFn: async (botId: string) => {
      let request = attempt;

      if (!request) {
        const text = buildSkillInstallPrompt({
          sourceUrl: skill.sourceUrl,
          directoryPath: skill.directoryPath,
          level,
          name: name.trim(),
        });

        const { data } = await createBotSession({ path: { botId }, throwOnError: true });

        // Retry the same session and message if Dex accepted the input but the response was lost.
        request = { botId, sessionId: data.id, messageId: newId(), text };

        setAttempt(request);
      }

      const { sessionId, messageId, text } = request;

      await sendMessage({ path: { botId, sessionId }, body: { id: messageId, text }, throwOnError: true });
      await refreshSessions(botId);

      return { botId, sessionId: String(sessionId) };
    },
  });

  const locked = install.isPending || !!attempt;

  const nameError =
    name.trim() && !validSkillDirectory(name.trim())
      ? "Use one directory name with letters, digits, dots, - or _."
      : undefined;

  const unavailable = !!target.selected && !target.bot && !target.checking && target.list.isSuccess;
  const canStart = !!target.bot && target.valid && !nameError && !install.isPending;
  const failedSession = install.isError ? attempt : undefined;

  return (
    <Modal
      title={`Install ${skill.name}`}
      description="A Bot reviews the source in a new Session. Follow the review and the install result there."
      pending={install.isPending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={install.isPending} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="install-skill" disabled={!canStart}>
            {install.isPending ? "Starting…" : failedSession ? "Retry sending" : "Start install"}
          </Button>
        </>
      }
    >
      <form
        id="install-skill"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();

          if (!canStart || !target.bot) return;

          install.mutate(target.bot.id, {
            onSuccess: (params) => {
              void navigate({ to: "/b/$botId/s/$sessionId", params });
            },
          });
        }}
      >
        <Field
          label="Bot"
          hint="This Bot runs the review on its Runner."
          error={unavailable ? "This Bot is not available. Choose a Bot." : undefined}
        >
          {({ id, describedBy, invalid }) => (
            <Select
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid || undefined}
              value={target.bot?.id ?? ""}
              disabled={locked}
              onChange={(event) => target.setSelected(event.target.value || undefined)}
              required
            >
              <option value="" disabled>
                {target.checking ? "Loading Bots…" : "Choose a Bot"}
              </option>
              {target.bots.map((bot) => (
                <option key={bot.id} value={bot.id}>
                  {bot.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {target.list.isError ? (
          <Failure
            title="Could not load Bots"
            error={target.list.error}
            retry={() => void target.list.refetch()}
          />
        ) : null}
        <Field label="Install level">
          {({ id }) => (
            <Select
              id={id}
              value={level}
              disabled={locked}
              onChange={(event) => setLevel(event.target.value === "agent" ? "agent" : "dex")}
            >
              <option value="dex">Shared (all Bots)</option>
              <option value="agent">This Bot only</option>
            </Select>
          )}
        </Field>
        <Field
          label="Directory name"
          hint="Optional. Keep the original directory name if blank."
          error={nameError}
        >
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid || undefined}
              autoComplete="off"
              value={name}
              disabled={locked}
              onChange={(event) => setName(event.target.value)}
            />
          )}
        </Field>
        {install.isError ? (
          <Failure title="Could not start the install" error={install.error}>
            {failedSession ? (
              <Link
                to="/b/$botId/s/$sessionId"
                params={{ botId: failedSession.botId, sessionId: String(failedSession.sessionId) }}
                className="underline"
              >
                Open the install Session
              </Link>
            ) : null}
          </Failure>
        ) : null}
      </form>
    </Modal>
  );
}

type Auth = Exclude<McpDraft["auth"], "oauth-client">;

/** A saved declaration. savedAt tells a status read after the save from the list cached before it. */
type Added = { name: string; auth: Auth; savedAt: number; bot?: BotTarget };

const authOptions = [
  { value: "none", label: "No authentication" },
  { value: "header", label: "HTTP headers" },
  { value: "oauth", label: "Browser OAuth" },
] as const;

type HeaderRow = McpHeaderDraft & { id: string };

function AddMcpServerDialog({ initialTarget, onClose, draft }: AddDialogProps & { draft?: McpDraft }) {
  const target = useTarget(initialTarget);
  const client = useQueryClient();
  const google = draft?.auth === "oauth-client" ? draft : undefined;
  const [name, setName] = useState(draft?.name ?? "");
  const [url, setUrl] = useState(draft?.url ?? "");
  const [auth, setAuth] = useState<Auth>(draft?.auth === "oauth-client" ? "oauth" : (draft?.auth ?? "none"));

  const [googleClient, setGoogleClient] = useState<GoogleClientForm>({
    id: "",
    scope: google?.scope ?? "",
    secret: { kind: "paste", value: "" },
  });

  const [headers, setHeaders] = useState<HeaderRow[]>(
    () => draft?.headers?.map((row) => ({ ...row, id: newId() })) ?? [],
  );

  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
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

  const advancedError = auth === "oauth" && !google ? secretError : undefined;
  const reused = reusedClientSecret(googleClient.secret, target.bot?.id);
  const pasted = googleClient.secret.kind === "paste" ? googleClient.secret.value.trim() : "";

  const googleReady =
    !google || (!!googleClient.id.trim() && !!googleClient.scope.trim() && !!(pasted || reused));

  const advancedSet = [
    clientId.trim() ? "Client ID" : "",
    secret ? "client secret" : "",
    scope.trim() ? "scope" : "",
  ].filter(Boolean);

  if (added) return <AddedMcpServer added={added} onClose={onClose} />;

  const headerError = auth === "header" ? checkMcpHeaders(headers) : undefined;

  const canSave =
    target.valid &&
    !saving &&
    mcpServerName.test(name.trim()) &&
    !!url.trim() &&
    googleReady &&
    !advancedError &&
    !headerError;

  async function submit() {
    if (!canSave) return;

    const body: McpServerInput = { name: name.trim(), url: url.trim() };

    if (google) {
      body.oauth = {
        clientId: googleClient.id.trim(),
        scope: googleClient.scope.trim().replace(/\s+/g, " "),
      };

      if (reused) body.oauth.clientSecret = `vault:${reused}`;
    } else if (auth === "oauth") {
      const config: NonNullable<McpServerInput["oauth"]> = {};

      if (clientId.trim()) config.clientId = clientId.trim();

      if (secret) config.clientSecret = `vault:${secret}`;

      if (scope.trim()) config.scope = scope.trim();

      body.oauth = config;
    }

    const { bot } = target;

    setSaving(true);
    setSaveError(undefined);

    try {
      await saveMcpDeclaration(bot?.id, body, auth === "header" ? headers : [], google ? pasted : undefined);
      // The POST decides the outcome. The status read that follows can fail on its own.
      setHeaders([]);

      if (google) setGoogleClient({ ...googleClient, secret: { kind: "paste", value: "" } });

      setAdded({ name: body.name, auth, savedAt: Date.now(), bot });

      if (bot) {
        void client.invalidateQueries({ queryKey: getBotIntegrationsQueryKey({ path: { botId: bot.id } }) });
      } else {
        void client.invalidateQueries({ queryKey: getSharedIntegrationsQueryKey() });
        void client.invalidateQueries({ queryKey: [{ _id: "getBotIntegrations" }], refetchType: "none" });
      }
    } catch (error) {
      setSaveError(errorText(error));
    } finally {
      if (auth === "header" || (google && pasted))
        void client.invalidateQueries({ queryKey: [{ _id: "listVault" }] });
      setSaving(false);
    }
  }

  return (
    <Modal
      title="Add MCP server"
      description="Add a Streamable HTTP server for all Bots or for one Bot."
      pending={saving}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="add-mcp-server" disabled={!canSave}>
            {saving ? "Adding…" : "Add server"}
          </Button>
        </>
      }
    >
      <form
        id="add-mcp-server"
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <TargetField target={target} disabled={saving} />
        <Field label="Name" hint="Letters, digits, - and _.">
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              value={name}
              onChange={(event) => setName(event.target.value)}
              pattern="[A-Za-z0-9_\-]{1,32}"
              maxLength={32}
              disabled={saving}
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
              readOnly={!!google}
              disabled={saving}
              onChange={(event) => setUrl(event.target.value)}
              autoComplete="off"
              required
            />
          )}
        </Field>
        {google ? (
          <GoogleMcpClientFields
            preset={google}
            botId={target.bot?.id}
            targetValid={target.valid}
            pending={saving}
            value={googleClient}
            onChange={setGoogleClient}
          />
        ) : (
          <div className="flex flex-col gap-1.5">
            <span aria-hidden className="text-[12.5px] font-medium text-foreground">
              Authentication
            </span>
            <RadioSegments
              label="Authentication"
              options={authOptions}
              value={auth}
              onChange={(value) => {
                if (!saving) setAuth(value);
              }}
              className="[&_button]:px-2 [&_button]:text-xs"
            />
            <p className="text-[12px] text-muted-foreground">
              {auth === "none"
                ? "For a public server. Dex sends no credentials."
                : auth === "oauth"
                  ? "You sign in to the server in this browser after you add it. Most servers need nothing else."
                  : "Dex sends these headers to this server. Check the URL before you enter secrets."}
            </p>
          </div>
        )}
        {auth === "header" ? (
          <div className="space-y-2">
            <McpHeaderFields rows={headers} onChange={setHeaders} disabled={saving} />
            {headerError ? <p className="text-xs text-muted-foreground">{headerError}</p> : null}
          </div>
        ) : null}
        {auth === "oauth" && !google ? (
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
        {saveError ? <Failure title="Could not add the MCP server" error={saveError} /> : null}
      </form>
    </Modal>
  );
}

function McpHeaderFields({
  rows,
  onChange,
  disabled,
}: {
  rows: HeaderRow[];
  onChange: (rows: HeaderRow[]) => void;
  disabled: boolean;
}) {
  return (
    <div className="space-y-3">
      {rows.map((row, index) => {
        const label = `Header ${index + 1}`;
        const secret = row.secret || requiresVault(row.name);

        const update = (next: Partial<McpHeaderDraft>) =>
          onChange(rows.map((header) => (header.id === row.id ? { ...header, ...next } : header)));

        return (
          <div key={row.id} className="space-y-3 rounded-md border border-border p-3">
            <Field label={`${label} name`}>
              {({ id }) => (
                <Input
                  id={id}
                  value={row.name}
                  onChange={(event) => update({ name: event.target.value })}
                  readOnly={row.required}
                  disabled={disabled}
                  autoComplete="off"
                  required
                />
              )}
            </Field>
            <Field
              label={`${label} value`}
              hint={
                row.prefix ? `${row.description ?? "Token"}. Format: ${row.prefix}{token}` : row.description
              }
            >
              {({ id, describedBy }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  type={secret ? "password" : "text"}
                  autoComplete={secret ? "new-password" : "off"}
                  value={row.value}
                  onChange={(event) => update({ value: event.target.value })}
                  disabled={disabled}
                  required
                />
              )}
            </Field>
            <div className="flex items-center gap-3">
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <Switch
                  checked={secret}
                  disabled={disabled || requiresVault(row.name) || (row.required && row.secret)}
                  onCheckedChange={(checked) => update({ secret: checked })}
                  aria-label={`${label} secret`}
                  size="sm"
                />
                Store in vault
              </label>
              {row.required ? null : (
                <Button
                  type="button"
                  size="xs"
                  variant="ghost"
                  disabled={disabled}
                  aria-label={`Remove ${label.toLowerCase()}`}
                  onClick={() => onChange(rows.filter((header) => header.id !== row.id))}
                >
                  Remove
                </Button>
              )}
            </div>
          </div>
        );
      })}
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={disabled}
        onClick={() => onChange([...rows, { id: newId(), name: "", value: "", secret: false }])}
      >
        <Plus aria-hidden /> Add header
      </Button>
    </div>
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
              onClick={() =>
                authorize.mutate(
                  { name: added.name, scope },
                  { onSuccess: ({ data }) => window.location.assign(data.authorizationUrl) },
                )
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
