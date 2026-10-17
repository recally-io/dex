import { useAgentId } from "../lib/agent-scope";
import type { ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Extensions, McpServer, Skill } from "../api/generated/types.gen";
import {
  getExtensionsOptions,
  getExtensionsQueryKey,
  reloadExtensionsMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { errorText, queryClient } from "../lib/queries";
import { useApp } from "../lib/app-state";
import { Failure, Loading, Spinner } from "../components/feedback";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";

export function ExtensionsPage() {
  const agentId = useAgentId();
  const { notify } = useApp();
  const query = useQuery(getExtensionsOptions({ path: { agentId } }));

  const reload = useMutation({
    ...reloadExtensionsMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getExtensionsQueryKey({ path: { agentId } }), data);
      notify("MCP servers reloaded. The worker restarted for every agent.");
    },
    onError: (error) => notify(errorText(error)),
  });

  return (
    <FramedPage
      title="Extensions"
      description="Read home, then dex, then agent. A narrower level replaces a skill or MCP server with the same name. Environment keys merge in this order."
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={reload.isPending}
          onClick={() => reload.mutate({ path: { agentId } })}
        >
          {reload.isPending ? <Spinner /> : null}
          Reload (restarts worker for all agents)
        </Button>
      }
    >
      <div className="px-5 pt-5 sm:px-8">
        <Note tone="warn">
          Environment values stay out of this page, the extensions API, and worker logs. The model can read
          .env files or run env. Tool output becomes part of the transcript. Use mode 0600 and keep .env files
          out of Git. Disabling a server does not remove environment keys.
        </Note>
      </div>
      <Section n={1} title="Built in" caption="Always on. You cannot turn them off.">
        <Panel>
          <Facts
            monoLabels
            rows={[
              [
                "memory",
                <span key="memory" className="font-normal text-ink-2">
                  Puts the memory rules and MEMORY.md in the prompt
                </span>,
              ],
              [
                "read, write, edit, bash",
                <span key="tools" className="font-normal text-ink-2">
                  The four agent tools. They run on this computer without a sandbox
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
    </FramedPage>
  );
}

function Catalog({ extensions: e }: { extensions: Extensions }) {
  const levels = [
    { name: "home", skills: e.paths.homeSkills, mcp: e.paths.homeMcp, env: e.paths.homeEnv },
    { name: "dex", skills: e.paths.dexSkills, mcp: e.paths.dexMcp, env: e.paths.dexEnv },
    { name: "agent", skills: e.paths.agentSkills, mcp: e.paths.agentMcp, env: e.paths.agentEnv },
  ];

  return (
    <>
      <Section n={2} title="File levels" caption="Order: home, dex, agent. Narrower wins.">
        <Panel>
          <ul className="divide-y divide-line">
            {levels.map((level) => (
              <li key={level.name} className="px-4 py-3 text-[12px] [overflow-wrap:anywhere]">
                <p className="mb-1 font-medium text-ink">{level.name}</p>
                <p>
                  <Mono>{level.skills}</Mono>
                </p>
                <p>
                  <Mono>{level.mcp}</Mono>
                </p>
                <p>
                  <Mono>{level.env}</Mono>
                </p>
              </li>
            ))}
          </ul>
        </Panel>
      </Section>
      {e.warnings.length || e.errors.length ? (
        <Section n={3} title="Configuration">
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
      <Section n={4} title="Skills on disk" caption="Edits apply to the next model request.">
        <List
          items={e.skills.map((skill) => (
            <SkillRow key={skill.name} skill={skill} />
          ))}
          empty={<>Add &lt;name&gt;/SKILL.md in a skills directory above.</>}
        />
      </Section>
      <Section n={5} title="MCP servers on disk" caption="Reload to apply these declarations.">
        <List
          items={e.declaredServers.map((server) => (
            <ServerRow key={server.name} server={server} declared />
          ))}
          empty={
            <>
              Add mcpServers to an mcp.json file above. A narrower {'{ "disabled": true }'} entry turns off a
              wider server.
            </>
          }
        />
      </Section>
      <Section
        n={6}
        title="MCP servers now"
        caption="Current connections. Tools are available through tool_search."
      >
        <List
          items={e.servers.map((server) => (
            <ServerRow key={server.name} server={server} />
          ))}
          empty="No servers are loaded."
        />
      </Section>
      <Section n={7} title="Environment keys on disk" caption="Names only. Reload to apply changes.">
        <List
          items={e.env.map((entry) => (
            <li
              key={`${entry.scope}:${entry.key}`}
              className="flex flex-wrap items-center gap-2 px-4 py-3 [overflow-wrap:anywhere]"
            >
              <Mono>{entry.key}</Mono>
              <span className="text-[11.5px] text-ink-3">{entry.scope}</span>
              {entry.shadowed ? <StatusPill className="h-5 px-2 text-[11.5px]">Shadowed</StatusPill> : null}
            </li>
          ))}
          empty="Add KEY=VALUE lines to an .env file above. The narrower value wins. A server's own env wins over .env."
        />
      </Section>
    </>
  );
}

function List({ items, empty }: { items: ReactNode[]; empty: ReactNode }) {
  return (
    <Panel>
      {items.length ? (
        <ul className="divide-y divide-line">{items}</ul>
      ) : (
        <p className="px-4 py-3.5 text-[12.5px] leading-relaxed text-ink-2">{empty}</p>
      )}
    </Panel>
  );
}

function Header({ name, scope, overrides }: { name: string; scope: Skill["scope"]; overrides: boolean }) {
  return (
    <>
      <span className="font-mono text-[12.5px] font-medium text-ink">{name}</span>
      <span className="text-[11.5px] text-ink-3">
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
      <p className={`mt-1 text-[12.5px] ${s.error ? "text-red" : "text-ink-2"}`}>
        {s.error ?? s.description}
      </p>
    </li>
  );
}

const STATE: Record<McpServer["state"], { tone: "green" | "red" | "neutral"; label: string }> = {
  connected: { tone: "green", label: "Connected" },
  failed: { tone: "red", label: "Failed" },
  disabled: { tone: "neutral", label: "Off" },
  idle: { tone: "neutral", label: "Starts with the agent" },
};

function ServerRow({ server: s, declared = false }: { server: McpServer; declared?: boolean }) {
  const state = STATE[s.state];

  return (
    <li className="px-4 py-3 [overflow-wrap:anywhere]">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
        <span className="flex-1" />
        <StatusPill tone={state.tone} className="h-5 px-2 text-[11.5px]">
          {declared ? (s.state === "disabled" ? "Off on disk" : "Declared") : state.label}
        </StatusPill>
      </div>
      {s.tools.length ? (
        <p className="mt-1 font-mono text-[11.5px] text-ink-2">{s.tools.join(", ")}</p>
      ) : null}
      {s.error ? <p className="mt-1 text-[12.5px] text-red">{s.error}</p> : null}
    </li>
  );
}
