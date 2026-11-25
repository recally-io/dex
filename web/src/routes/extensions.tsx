import { useAgentId } from "../lib/agent-scope";
import type { ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Extensions, McpServer, Skill } from "../api/generated/types.gen";
import { getExtensionsOptions, startMcpOAuthMutation } from "../api/generated/@tanstack/react-query.gen";
import { Failure, Loading } from "../components/feedback";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { BotSettingsSwitcher } from "../components/settings-switcher";
import { Vault } from "../components/vault";

export function ExtensionsPage() {
  const agentId = useAgentId();

  const query = useQuery({
    ...getExtensionsOptions({ path: { agentId } }),
    // An invalid configuration does not reload until its files change, so it does not poll.
    refetchInterval: (q) => (q.state.data?.reloadPending && !q.state.data.errors.length ? 3000 : false),
  });

  return (
    <FramedPage
      title="Tools"
      toolbar={<BotSettingsSwitcher segment="extensions" />}
      description="Manage this bot’s skills, MCP servers, and secrets. A bot entry with the same name takes precedence over a shared entry."
      actions={
        query.data?.reloadPending && !query.data.errors.length ? (
          <Badge variant="secondary">Change waiting for idle bots</Badge>
        ) : null
      }
    >
      <div className="mb-6">
        <Note tone="warn">
          Put secrets in the vault. Do not paste secret values in chats, instructions, or skill files. Use
          vault:NAME to share a reference with the bot.
        </Note>
      </div>
      <Section title="Built in" caption="Always on. You cannot turn them off.">
        <Panel>
          <Facts
            monoLabels
            rows={[
              [
                "memory",
                <span key="memory" className="font-normal text-muted-foreground">
                  Puts the memory rules and MEMORY.md in the prompt
                </span>,
              ],
              [
                "read, write, edit, bash",
                <span key="tools" className="font-normal text-muted-foreground">
                  The four bot tools. They run on this computer without a sandbox
                </span>,
              ],
            ]}
          />
        </Panel>
      </Section>
      {query.data ? (
        <Catalog extensions={query.data} />
      ) : query.isError ? (
        <div className="px-5 py-8 sm:px-8">
          <Failure error={query.error} retry={() => void query.refetch()} />
        </div>
      ) : (
        <Loading />
      )}
      <Vault key={agentId} target={{ scope: "agent", agentId }} />
    </FramedPage>
  );
}

function Catalog({ extensions: e }: { extensions: Extensions }) {
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
      {e.warnings.length || e.errors.length ? (
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
              <Failure title="dex cannot read some files">
                <ul>
                  {e.errors.map((error) => (
                    <li key={error}>{error}</li>
                  ))}
                </ul>
              </Failure>
            </div>
          ) : null}
        </Section>
      ) : null}
      <Section
        title="Skills"
        caption="Edits apply to the next model request. The project level is .agents/skills in the bot workspace and is narrower than the bot."
      >
        <List
          items={e.skills.map((skill) => (
            <SkillRow key={skill.name} skill={skill} />
          ))}
          empty={<>Add skills/&lt;name&gt;/SKILL.md in a level above. Nested directories work too.</>}
        />
      </Section>
      <Section
        title="MCP servers"
        caption="Manage declarations with mcp_server tools in an owner session. Changes apply when the agent is idle."
      >
        <List
          items={e.servers.map((server) => (
            <ServerRow key={server.name} server={server} />
          ))}
          empty={
            <>
              Ask the agent to add a server with mcp_server_set. An agent declaration with disabled: true
              turns off a dex server with the same name.
            </>
          }
        />
      </Section>
    </>
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

function ServerRow({ server: s }: { server: McpServer }) {
  const state = s.oauth === "reauthorize" ? { tone: "red", label: "Authorization required" } : STATE[s.state];
  const agentId = useAgentId();

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
        <Badge
          variant={state.tone === "red" ? "destructive" : state.tone === "green" ? "secondary" : "outline"}
          className={`h-5 px-2 text-[11.5px] ${state.tone === "green" ? "bg-green-tint text-green" : ""}`}
        >
          {state.label}
        </Badge>
        {s.oauth && s.state !== "disabled" ? (
          <Button
            size="sm"
            variant="outline"
            disabled={authorize.isPending}
            onClick={() => authorize.mutate({ path: { agentId, name: s.name }, body: { scope: s.scope } })}
          >
            {authorize.isPending ? "Starting…" : s.oauth === "authorize" ? "Authorize" : "Reauthorize"}
          </Button>
        ) : null}
      </div>
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
