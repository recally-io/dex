import { useBotId } from "../lib/bot-scope";
import type { ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import type { BotIntegrations, IntegrationScope, McpServer, Skill } from "../api/generated/types.gen";
import {
  getBotIntegrationsOptions,
  reloadBotIntegrationsMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { startMcpOAuth, startSharedMcpOAuth } from "../api/generated/sdk.gen";
import { Failure, Loading } from "../components/feedback";
import { FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button, buttonVariants } from "../components/ui/button";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Vault } from "../components/vault";

export type IntegrationSection = "skills" | "mcp";

export function BotIntegrationsPage({ section }: { section?: IntegrationSection }) {
  return (
    <FramedPage
      title="Integrations"
      toolbar={<BotSettingsSwitcher segment="integrations" />}
      description="Browse this Bot's effective skills and MCP servers. Bot entries can override shared entries."
    >
      <BotCatalog section={section} />
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

function BotCatalog({ section }: { section?: IntegrationSection }) {
  const botId = useBotId();

  const query = useQuery({
    ...getBotIntegrationsOptions({ path: { botId } }),
    // An invalid configuration does not reload until its files change.
    refetchInterval: (q) => (q.state.data?.reloadPending && !q.state.data.errors.length ? 3000 : false),
  });

  const reload = useMutation({ ...reloadBotIntegrationsMutation(), onSuccess: () => query.refetch() });

  if (query.isError) return <Failure error={query.error} retry={() => void query.refetch()} />;

  if (!query.data) return <Loading />;

  return (
    <>
      {query.data.reloadPending && !query.data.errors.length ? (
        <Note>Change waiting for this bot to be idle</Note>
      ) : null}
      <Configuration extensions={query.data} />
      <IntegrationSections
        {...query.data}
        section={section}
        botId={botId}
        skillAction={<AddLink section="skills" botId={botId} />}
        mcpAction={
          <div className="flex flex-wrap items-center gap-2">
            <AddLink section="mcp" botId={botId} />
            <Button
              variant="secondary"
              size="sm"
              disabled={reload.isPending}
              onClick={() => reload.mutate({ path: { botId } })}
            >
              {reload.isPending ? "Reloading…" : "Reload"}
            </Button>
          </div>
        }
      />
      {reload.isError ? (
        <Failure title="Could not reload tools and MCP servers" error={reload.error} />
      ) : null}
    </>
  );
}

function AddLink({ section, botId }: { section: IntegrationSection; botId: string }) {
  return (
    <Link
      to="/integrations"
      search={{ section, target: botId, add: section === "skills" ? "skill" : "mcp" }}
      className={buttonVariants({ size: "sm" })}
    >
      <Plus aria-hidden /> {section === "skills" ? "Add skill" : "Add MCP server"}
    </Link>
  );
}

function focusHeading(heading: HTMLHeadingElement | null) {
  if (!heading) return;

  const frame = requestAnimationFrame(() => {
    heading.focus({ preventScroll: true });
    heading.scrollIntoView({ block: "start" });
  });

  return () => cancelAnimationFrame(frame);
}

export function IntegrationSections({
  skills,
  servers,
  levels,
  section,
  botId,
  skillAction,
  mcpAction,
  skillCatalog,
  mcpCatalog,
}: Pick<BotIntegrations, "skills" | "servers"> & {
  levels?: BotIntegrations["levels"];
  section?: IntegrationSection;
  botId?: string;
  skillAction: ReactNode;
  mcpAction: ReactNode;
  skillCatalog?: ReactNode;
  mcpCatalog?: ReactNode;
}) {
  return (
    <>
      <Section
        title="Skills"
        headingRef={section === "skills" ? focusHeading : undefined}
        action={skillAction}
      >
        <List
          items={skills.map((skill) => (
            <SkillRow key={skill.name} skill={skill} botView={!!botId} />
          ))}
          empty={botId ? "No skills available to this Bot." : "No shared skills yet."}
        />
        {levels?.length ? (
          <details className="mt-3 text-[12.5px] text-muted-foreground">
            <summary className="cursor-pointer">Skill file levels</summary>
            <ul className="mt-2 space-y-1 [overflow-wrap:anywhere]">
              {levels.map((level) => (
                <li key={level.scope}>
                  {level.scope}: <Mono>{level.dir}/skills/</Mono>
                </li>
              ))}
            </ul>
          </details>
        ) : null}
        {skillCatalog}
      </Section>
      <Section
        title="MCP servers"
        headingRef={section === "mcp" ? focusHeading : undefined}
        caption={botId ? "Reload refreshes this Bot's tools and MCP connections when it is idle." : undefined}
        action={mcpAction}
      >
        <List
          items={servers.map((server) => (
            <ServerRow key={server.name} server={server} botId={botId} />
          ))}
          empty={botId ? "No MCP servers available to this Bot." : "No shared MCP servers yet."}
        />
        {mcpCatalog}
      </Section>
    </>
  );
}

export function Configuration({
  extensions: e,
}: {
  extensions: Pick<BotIntegrations, "warnings" | "errors">;
}) {
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
        {scope === "dex" ? "Shared" : scope}
        {overrides ? ", replaces a wider entry" : ""}
      </span>
    </>
  );
}

function ManageShared({ section }: { section: IntegrationSection }) {
  return (
    <Link to="/integrations" search={{ section }} className="text-[12px] text-muted-foreground underline">
      Manage in Integrations
    </Link>
  );
}

function SkillRow({ skill: s, botView }: { skill: Skill; botView: boolean }) {
  return (
    <li className="px-4 py-3 [overflow-wrap:anywhere]">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
        {botView && s.scope === "dex" ? <ManageShared section="skills" /> : null}
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

export function useAuthorizeMcp(botId?: string) {
  return useMutation({
    mutationFn: ({ name, scope }: { name: string; scope: IntegrationScope }) =>
      botId
        ? startMcpOAuth({ path: { botId, name }, body: { scope }, throwOnError: true })
        : startSharedMcpOAuth({ path: { name }, throwOnError: true }),
    gcTime: 0,
  });
}

export function McpServerStatus({ server: s, botView }: { server: McpServer; botView: boolean }) {
  const state = STATE[s.state];
  const auth = s.oauth && s.state !== "disabled" ? AUTH[s.oauth] : undefined;

  return (
    <>
      {auth ? <StateBadge name="Authorization" label={auth.label} tone={auth.tone} /> : null}
      {botView || s.state === "disabled" ? (
        <StateBadge name={botView ? "Connection" : "Declaration"} label={state.label} tone={state.tone} />
      ) : null}
    </>
  );
}

function ServerRow({ server: s, botId }: { server: McpServer; botId?: string }) {
  const shared = s.scope === "dex";
  // Bot Settings manages only the Bot's own entries. Shared entries are authorized on the global page.
  const canAuthorize = s.oauth && s.state !== "disabled" && !(botId && shared);
  const authorizeLabel = s.oauth === "authorize" ? "Authorize" : "Reauthorize";
  const authorize = useAuthorizeMcp(botId);

  return (
    <li className="px-4 py-3 [overflow-wrap:anywhere]">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
        {s.transport ? <Mono>{s.transport}</Mono> : null}
        {botId && shared ? <ManageShared section="mcp" /> : null}
        <span className="flex-1" />
        <McpServerStatus server={s} botView={!!botId} />
        {canAuthorize ? (
          <Button
            size="sm"
            variant="outline"
            disabled={authorize.isPending}
            onClick={() =>
              authorize.mutate(
                { name: s.name, scope: s.scope },
                { onSuccess: ({ data }) => window.location.assign(data.authorizationUrl) },
              )
            }
          >
            {authorize.isPending ? "Starting…" : authorizeLabel}
          </Button>
        ) : null}
      </div>
      {authorize.isError ? <Failure error={authorize.error} title="Could not start authorization" /> : null}
      {s.oauth === "reauthorize" ? (
        <p className="mt-1 text-[12.5px] text-red">This connection needs a new grant.</p>
      ) : null}
      {botId && s.tools.length ? (
        <p className="mt-1 font-mono text-[11.5px] text-muted-foreground">{s.tools.join(", ")}</p>
      ) : null}
      {s.error ? <p className="mt-1 text-[12.5px] text-red">{s.error}</p> : null}
    </li>
  );
}
