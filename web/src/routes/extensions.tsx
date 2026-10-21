import { useAgentId } from "../lib/agent-scope";
import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
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
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";

export function ExtensionsPage() {
  const agentId = useAgentId();
  const { notify } = useApp();
  const query = useQuery(getExtensionsOptions({ path: { agentId } }));

  const reload = useMutation({
    ...reloadExtensionsMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getExtensionsQueryKey({ path: { agentId } }), data);
      notify("MCP servers reloaded for the selected bot.");
    },
    onError: (error) => notify(errorText(error)),
  });

  return (
    <FramedPage
      title="Extensions"
      description="Read home, then dex, then bot. A narrower level replaces a skill or MCP server with the same name. Environment keys merge in this order."
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={reload.isPending}
          onClick={() => reload.mutate({ path: { agentId } })}
        >
          {reload.isPending ? <Spinner /> : null}
          Reload selected bot
        </Button>
      }
    >
      <div className="px-5 pt-5 sm:px-8">
        <Note tone="warn">
          Environment values stay out of this page, the extensions API, and app logs. The model can read .env
          files or run env. Tool output becomes part of the transcript. Use mode 0600 and keep .env files out
          of Git. Disabling a server does not remove environment keys.
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
    </FramedPage>
  );
}

function Catalog({ extensions: e }: { extensions: Extensions }) {
  return (
    <>
      <Section
        n={2}
        title="File levels"
        caption={
          <>
            Each holds skills/, mcp.json and .env. Set .env values on the{" "}
            <Link to="/secrets" className="underline">
              Secrets
            </Link>{" "}
            page.
          </>
        }
      >
        <Panel>
          <ul className="divide-y divide-line">
            {e.levels.map((level) => (
              <li key={level.scope} className="px-4 py-3 text-[12px] [overflow-wrap:anywhere]">
                <p className="mb-1 font-medium text-ink">{level.scope}</p>
                <Mono>{level.dir}/</Mono>
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
      <Section n={4} title="Skills" caption="Edits apply to the next model request.">
        <List
          items={e.skills.map((skill) => (
            <SkillRow key={skill.name} skill={skill} />
          ))}
          empty={<>Add skills/&lt;name&gt;/SKILL.md in a level above.</>}
        />
      </Section>
      <Section
        n={5}
        title="MCP servers"
        caption="Reload to apply mcp.json changes. Tools are available through tool_search."
      >
        <List
          items={e.servers.map((server) => (
            <ServerRow key={server.name} server={server} />
          ))}
          empty={
            <>
              Add mcpServers to an mcp.json file in a level above. A narrower {'{ "disabled": true }'} entry
              turns off a wider server.
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
  idle: { tone: "neutral", label: "Starts with the bot" },
};

function ServerRow({ server: s }: { server: McpServer }) {
  const state = STATE[s.state];

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
      </div>
      {s.tools.length ? (
        <p className="mt-1 font-mono text-[11.5px] text-ink-2">{s.tools.join(", ")}</p>
      ) : null}
      {s.error ? <p className="mt-1 text-[12.5px] text-red">{s.error}</p> : null}
    </li>
  );
}
