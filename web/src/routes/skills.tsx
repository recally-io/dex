import { useBotId } from "../lib/bot-scope";
import type { ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { BotIntegrations, McpServer, Skill } from "../api/generated/types.gen";
import {
  getBotIntegrationsOptions,
  reloadBotIntegrationsMutation,
  startMcpOAuthMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { Failure, Loading } from "../components/feedback";
import { FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Vault } from "../components/vault";

export function SkillsPage() {
  return (
    <FramedPage
      title="Skills"
      toolbar={<BotSettingsSwitcher segment="skills" />}
      description="Browse this bot's skills. A skill with the same name replaces a shared skill."
    >
      <IntegrationCatalog section="skills" />
    </FramedPage>
  );
}

export function BotSecretsPage() {
  const botId = useBotId();

  return (
    <FramedPage title="Secrets" toolbar={<BotSettingsSwitcher segment="secrets" />}>
      <div className="mb-6">
        <Note tone="warn">
          Put secrets in the vault. Do not paste secret values in sessions, instructions, or skill files. Use
          vault:NAME to share a reference with the bot.
        </Note>
      </div>
      <Vault key={botId} target={{ scope: "agent", botId }} />
    </FramedPage>
  );
}

export function IntegrationCatalog({
  section,
  addAction,
}: {
  section: "skills" | "integrations";
  /** A control that adds a declaration to this bot, next to Reload. */
  addAction?: ReactNode;
}) {
  const botId = useBotId();

  const query = useQuery({
    ...getBotIntegrationsOptions({ path: { botId } }),
    // An invalid configuration does not reload until its files change.
    refetchInterval: (q) => (q.state.data?.reloadPending && !q.state.data.errors.length ? 3000 : false),
  });

  const reload = useMutation({ ...reloadBotIntegrationsMutation(), onSuccess: () => query.refetch() });

  if (query.isError) return <Failure error={query.error} retry={() => void query.refetch()} />;

  if (!query.data) return <Loading />;

  const reloadAction = (
    <Button
      variant="secondary"
      size="sm"
      disabled={reload.isPending}
      onClick={() => reload.mutate({ path: { botId } })}
    >
      {reload.isPending ? "Reloading…" : "Reload"}
    </Button>
  );

  return (
    <>
      {query.data.reloadPending && !query.data.errors.length ? (
        <Note>Change waiting for this bot to be idle</Note>
      ) : null}
      <Configuration extensions={query.data} />
      {section === "skills" ? (
        <Skills extensions={query.data} reloadAction={reloadAction} />
      ) : (
        <McpServers
          servers={query.data.servers}
          canAdd={!!addAction}
          reloadAction={
            addAction ? (
              <div className="flex flex-wrap items-center gap-2">
                {addAction}
                {reloadAction}
              </div>
            ) : (
              reloadAction
            )
          }
        />
      )}
      {reload.isError ? (
        <Failure title="Could not reload tools and MCP servers" error={reload.error} />
      ) : null}
    </>
  );
}

function Skills({ extensions: e, reloadAction }: { extensions: BotIntegrations; reloadAction: ReactNode }) {
  return (
    <>
      <Section title="File levels" caption="Each holds skills/.">
        <Panel>
          <ul className="divide-y divide-border">
            {e.levels.map((level) => (
              <li key={level.scope} className="px-4 py-3 text-[12px] [overflow-wrap:anywhere]">
                <p className="mb-1 font-medium text-foreground">{level.scope}</p>
                <Mono>{level.dir}/</Mono>
              </li>
            ))}
          </ul>
        </Panel>
      </Section>
      <Section
        title="Available skills"
        caption="Skill edits apply to the next request. Reload refreshes the bot's tools and MCP connections when this bot is idle."
        action={reloadAction}
      >
        <List
          items={e.skills.map((skill) => (
            <SkillRow key={skill.name} skill={skill} />
          ))}
          empty={<>Add skills/&lt;name&gt;/SKILL.md in a level above. Nested directories work too.</>}
        />
      </Section>
    </>
  );
}

function Configuration({ extensions: e }: { extensions: BotIntegrations }) {
  if (!e.warnings.length && !e.errors.length) return null;

  return (
    <Section title="Configuration">
      {e.warnings.length ? (
        <Note tone="warn">
          <ul className="[overflow-wrap:anywhere]">
            {e.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </Note>
      ) : null}
      {e.errors.length ? (
        <div className="mt-3 [overflow-wrap:anywhere]">
          <Failure title="Dex cannot read some files">
            <ul>
              {e.errors.map((error) => (
                <li key={error}>{error}</li>
              ))}
            </ul>
          </Failure>
        </div>
      ) : null}
    </Section>
  );
}

function McpServers({
  servers,
  canAdd,
  reloadAction,
}: {
  servers: McpServer[];
  canAdd: boolean;
  reloadAction: ReactNode;
}) {
  return (
    <Section
      title="MCP servers"
      caption={`${canAdd ? "Add a remote server here, or manage" : "Manage"} declarations with mcp_server tools in an owner session. Reload refreshes this bot's MCP connections when this bot is idle.`}
      action={reloadAction}
    >
      <List
        items={servers.map((server) => (
          <ServerRow key={server.name} server={server} />
        ))}
        empty={
          canAdd ? (
            "No MCP servers yet. Add a remote server for this Bot."
          ) : (
            <>
              Ask the bot to add a server with mcp_server_set. A bot declaration with disabled: true turns off
              a dex server with the same name.
            </>
          )
        }
      />
    </Section>
  );
}

function List({ items, empty }: { items: ReactNode[]; empty: ReactNode }) {
  return (
    <Panel>
      {items.length ? (
        <ul className="divide-y divide-border">{items}</ul>
      ) : (
        <p className="px-4 py-3.5 text-[12.5px] leading-relaxed text-muted-foreground">{empty}</p>
      )}
    </Panel>
  );
}

function Header({ name, scope, overrides }: { name: string; scope: Skill["scope"]; overrides: boolean }) {
  return (
    <>
      <span className="font-mono text-[12.5px] font-medium text-foreground">{name}</span>
      <span className="text-[11.5px] text-muted-foreground">
        {scope}
        {overrides ? ", replaces a wider entry" : ""}
      </span>
    </>
  );
}

function SkillRow({ skill: s }: { skill: Skill }) {
  return (
    <li className="px-4 py-3 [overflow-wrap:anywhere]">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
      </div>
      <p className={`mt-1 text-[12.5px] ${s.error ? "text-red" : "text-muted-foreground"}`}>
        {s.error ?? s.description}
      </p>
    </li>
  );
}

const STATE: Record<McpServer["state"], { tone: "green" | "red" | "neutral"; label: string }> = {
  connected: { tone: "green", label: "Connected" },
  failed: { tone: "red", label: "Failed" },
  disabled: { tone: "neutral", label: "Off" },
  idle: { tone: "neutral", label: "Starts with the bot" },
};

/** Authorization is a grant. It does not mean that the connection or its tools are live. */
const AUTH: Record<NonNullable<McpServer["oauth"]>, { tone: "green" | "red" | "neutral"; label: string }> = {
  authorize: { tone: "neutral", label: "Not authorized" },
  reauthorize: { tone: "red", label: "Authorization required" },
  authorized: { tone: "green", label: "Authorized" },
};

function StateBadge({
  label,
  tone,
  name,
}: {
  label: string;
  tone: "green" | "red" | "neutral";
  name: string;
}) {
  return (
    <Badge
      title={name}
      variant={tone === "red" ? "destructive" : tone === "green" ? "secondary" : "outline"}
      className={`h-5 px-2 text-[11.5px] ${tone === "green" ? "bg-green-tint text-green" : ""}`}
    >
      <span className="sr-only">{name}: </span>
      <span>{label}</span>
    </Badge>
  );
}

function ServerRow({ server: s }: { server: McpServer }) {
  const state = STATE[s.state];
  const auth = s.oauth && s.state !== "disabled" ? AUTH[s.oauth] : undefined;
  const botId = useBotId();
  const shared = s.scope === "dex";
  const authorizeLabel = `${s.oauth === "authorize" ? "Authorize" : "Reauthorize"}${shared ? " for Dex" : ""}`;

  const authorize = useMutation({
    ...startMcpOAuthMutation(),
    gcTime: 0,
    onSuccess: (data) => window.location.assign(data.authorizationUrl),
  });

  return (
    <li className="px-4 py-3 [overflow-wrap:anywhere]">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
        <span className="flex-1" />
        {auth ? <StateBadge name="Authorization" label={auth.label} tone={auth.tone} /> : null}
        <StateBadge name="Connection" label={state.label} tone={state.tone} />
        {s.oauth && s.state !== "disabled" ? (
          <Button
            size="sm"
            variant="outline"
            disabled={authorize.isPending}
            onClick={() => authorize.mutate({ path: { botId, name: s.name }, body: { scope: s.scope } })}
          >
            {authorize.isPending ? "Starting…" : authorizeLabel}
          </Button>
        ) : null}
      </div>
      {shared && auth ? (
        <p className="mt-1 text-[12.5px] text-muted-foreground">
          All Bots that inherit this server use the same authorization.
        </p>
      ) : null}
      {authorize.isError ? <Failure error={authorize.error} title="Could not start authorization" /> : null}
      {s.oauth === "reauthorize" ? (
        <p className="mt-1 text-[12.5px] text-red">This connection needs a new grant.</p>
      ) : null}
      {s.tools.length ? (
        <p className="mt-1 font-mono text-[11.5px] text-muted-foreground">{s.tools.join(", ")}</p>
      ) : null}
      {s.error ? <p className="mt-1 text-[12.5px] text-red">{s.error}</p> : null}
    </li>
  );
}
