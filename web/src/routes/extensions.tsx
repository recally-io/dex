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
import { Facts, FramedPage, Mono, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";

export function ExtensionsPage() {
  const { notify } = useApp();
  const query = useQuery(getExtensionsOptions());

  const reload = useMutation({
    ...reloadExtensionsMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getExtensionsQueryKey(), data);
      notify("MCP servers reloaded.");
    },
    onError: (error) => notify(errorText(error)),
  });

  return (
    <FramedPage
      title="Extensions"
      description="Skills and MCP servers come from files in ~/.agents and in the agent directory. An agent entry replaces a global entry with the same name."
      actions={
        <Button size="sm" variant="secondary" disabled={reload.isPending} onClick={() => reload.mutate({})}>
          {reload.isPending ? <Spinner /> : null}
          Reload MCP servers
        </Button>
      }
    >
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
  return (
    <>
      <Section
        n={2}
        title="Skills"
        caption="The agent reads a skill when a task needs it. An edit applies to the next message."
      >
        <List
          items={e.skills.map((s) => (
            <SkillRow key={s.name} skill={s} />
          ))}
          empty={
            <>
              Add <Mono>{e.paths.globalSkills}/&lt;name&gt;/SKILL.md</Mono> or{" "}
              <Mono>{e.paths.agentSkills}/&lt;name&gt;/SKILL.md</Mono>.
            </>
          }
        />
      </Section>
      <Section
        n={3}
        title="MCP servers"
        caption="The agent finds their tools with tool_search. After an edit, reload."
      >
        {e.errors.length ? (
          <div className="mb-3">
            <Failure title="dex cannot read some files">
              <ul className="font-mono text-[11.5px]">
                {e.errors.map((error) => (
                  <li key={error}>{error}</li>
                ))}
              </ul>
            </Failure>
          </div>
        ) : null}
        <List
          items={e.servers.map((s) => (
            <ServerRow key={s.name} server={s} />
          ))}
          empty={
            <>
              Add servers to <Mono>{e.paths.globalMcp}</Mono> or <Mono>{e.paths.agentMcp}</Mono>, in the
              mcpServers format of Claude Code. In the agent file, <Mono>{'{ "disabled": true }'}</Mono> turns
              off a global server.
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
        {scope === "global" ? "global" : overrides ? "agent, replaces global" : "agent"}
      </span>
    </>
  );
}

function SkillRow({ skill: s }: { skill: Skill }) {
  return (
    <li className="px-4 py-3">
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

function ServerRow({ server: s }: { server: McpServer }) {
  const state = STATE[s.state];

  return (
    <li className="px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <Header name={s.name} scope={s.scope} overrides={s.overrides} />
        <span className="flex-1" />
        <StatusPill tone={state.tone} className="h-5 px-2 text-[11.5px]">
          {state.label}
        </StatusPill>
      </div>
      {s.tools.length ? (
        <p className="mt-1 font-mono text-[11.5px] text-ink-2">{s.tools.join(", ")}</p>
      ) : null}
      {s.error ? <p className="mt-1 text-[12.5px] text-red">{s.error}</p> : null}
    </li>
  );
}
