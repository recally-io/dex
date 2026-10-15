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
import { Empty, Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { Button } from "../components/atoms/Button";
import { StatusPill } from "../components/atoms/StatusPill";

export function ExtensionsPage() {
  const { notify } = useApp();
  const query = useQuery(getExtensionsOptions());

  const reload = useMutation({
    ...reloadExtensionsMutation(),
    onSuccess: (data) => {
      queryClient.setQueryData(getExtensionsQueryKey(), data);
      notify("Extensions reloaded. New runs use them.");
    },
    onError: (error) => notify(errorText(error)),
  });

  return (
    <FramedPage
      title="Extensions"
      description="Skills and MCP servers extend the agent. dex reads them from files in two places. An agent entry replaces a global entry with the same name."
      actions={
        <Button size="sm" variant="secondary" disabled={reload.isPending} onClick={() => reload.mutate({})}>
          {reload.isPending ? <Spinner /> : null}
          Reload
        </Button>
      }
    >
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
  const usable = e.skills.filter((s) => !s.error).length;
  const connected = e.servers.filter((s) => s.state === "connected").length;
  const on = e.servers.filter((s) => s.state !== "disabled").length;

  return (
    <>
      {e.errors.length ? (
        <div className="border-b border-dashed border-line-strong px-5 py-6 sm:px-8">
          <Failure title="dex cannot read some files">
            <ul className="flex flex-col gap-1 font-mono text-[11.5px]">
              {e.errors.map((error) => (
                <li key={error}>{error}</li>
              ))}
            </ul>
            {e.errors.some((error) => error.includes("mcp.json")) ? (
              <p className="mt-2">No MCP server starts until every mcp.json file is valid.</p>
            ) : null}
          </Failure>
        </div>
      ) : null}
      <Section
        n={1}
        title="Skills"
        caption={
          e.skills.length
            ? `${usable} in the prompt`
            : "Instructions that the agent reads when a task needs them."
        }
      >
        {e.skills.length ? (
          <Panel>
            <ul className="divide-y divide-line">
              {e.skills.map((s) => (
                <SkillRow key={s.name} skill={s} />
              ))}
            </ul>
          </Panel>
        ) : (
          <Empty title="No skills yet">
            Put a skill in <Mono>{e.paths.globalSkills}/&lt;name&gt;/SKILL.md</Mono> for every agent, or in{" "}
            <Mono>{e.paths.agentSkills}</Mono> for this agent. Then reload.
          </Empty>
        )}
      </Section>
      <Section
        n={2}
        title="MCP servers"
        caption={
          e.servers.length ? `${connected} of ${on} connected` : "Tools from Model Context Protocol servers."
        }
      >
        {e.servers.length ? (
          <div className="flex flex-col gap-3">
            {e.servers.map((s) => (
              <ServerCard key={s.name} server={s} />
            ))}
            <Note>
              The agent finds MCP tools with <Mono>tool_search</Mono> and calls each as{" "}
              <Mono>mcp__&lt;server&gt;__&lt;tool&gt;</Mono>. dex does not repeat a call that a restart
              interrupted, because the call can have changed data.
            </Note>
          </div>
        ) : (
          <Empty title="No MCP servers yet">
            Add servers to <Mono>{e.paths.globalMcp}</Mono> or <Mono>{e.paths.agentMcp}</Mono>. Then reload.
          </Empty>
        )}
      </Section>
      <Section n={3} title="Built in" caption="Always on. You cannot turn them off.">
        <Panel>
          <Facts
            monoLabels
            rows={[
              ["memory", <Muted key="m">Puts the memory rules and MEMORY.md in the prompt</Muted>],
              [
                "read, write, edit, bash",
                <Muted key="t">The four agent tools. They run on this computer without a sandbox</Muted>,
              ],
            ]}
          />
        </Panel>
      </Section>
      <Files extensions={e} />
    </>
  );
}

function SkillRow({ skill: s }: { skill: Skill }) {
  return (
    <li className="px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[12.5px] font-medium text-ink">{s.name}</span>
        <ScopePill scope={s.scope} overrides={s.overrides} />
        {s.error ? (
          <StatusPill tone="red" className="h-5 px-2 text-[11px]">
            Not loaded
          </StatusPill>
        ) : null}
      </div>
      {s.error ? (
        <p className="mt-1 text-[12.5px] text-red [overflow-wrap:anywhere]">{s.error}</p>
      ) : (
        <p className="mt-1 text-[12.5px] leading-relaxed text-ink-2">{s.description}</p>
      )}
      <Mono className="mt-1 block truncate text-[11px] text-ink-3">{s.path}</Mono>
    </li>
  );
}

const STATE: Record<McpServer["state"], { tone: "green" | "red" | "neutral"; label: string }> = {
  connected: { tone: "green", label: "Connected" },
  failed: { tone: "red", label: "Failed" },
  disabled: { tone: "neutral", label: "Off" },
  idle: { tone: "neutral", label: "Starts with the agent" },
};

function ServerCard({ server: s }: { server: McpServer }) {
  const state = STATE[s.state];

  return (
    <Panel>
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <h3 className="font-mono text-[13px] font-medium text-ink">{s.name}</h3>
        <ScopePill scope={s.scope} overrides={s.overrides} />
        {s.transport ? <Mono className="text-[11.5px] text-ink-3">{s.transport}</Mono> : null}
        <span className="flex-1" />
        <StatusPill tone={state.tone} className="h-5 px-2 text-[11.5px]">
          {state.label}
          {s.state === "connected" ? ` · ${s.tools.length} ${s.tools.length === 1 ? "tool" : "tools"}` : ""}
        </StatusPill>
      </div>
      {s.tools.length ? (
        <div className="flex flex-wrap gap-1.5 border-t border-line px-4 py-3">
          {s.tools.map((tool) => (
            <code key={tool} className="rounded-md bg-inset px-1.5 py-0.5 font-mono text-[11.5px] text-ink-2">
              {tool}
            </code>
          ))}
        </div>
      ) : null}
      {s.error ? (
        <div className="border-t border-line p-3">
          <Failure title={s.state === "failed" ? "The server is not available" : "Some tools are not loaded"}>
            <p className="font-mono text-[11.5px] whitespace-pre-wrap">{s.error}</p>
          </Failure>
        </div>
      ) : null}
    </Panel>
  );
}

function ScopePill({ scope, overrides }: { scope: Skill["scope"]; overrides: boolean }) {
  if (scope === "global") {
    return (
      <StatusPill dot={false} className="h-5 px-2 text-[11px]">
        Global
      </StatusPill>
    );
  }

  return (
    <StatusPill tone="accent" dot={false} className="h-5 px-2 text-[11px]">
      {overrides ? "Agent · replaces global" : "Agent"}
    </StatusPill>
  );
}

const EXAMPLE = `{
  "mcpServers": {
    "github": { "type": "http", "url": "https://api.githubcopilot.com/mcp/",
                "headers": { "Authorization": "Bearer <token>" } },
    "files": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] },
    "old": { "disabled": true }
  }
}`;

function Files({ extensions: e }: { extensions: Extensions }) {
  return (
    <Section n={4} title="Files" caption="Edit the files, then reload. dex keeps no copy of them.">
      <Panel>
        <Facts
          rows={[
            ["Global skills", <Mono key="gs">{e.paths.globalSkills}</Mono>],
            ["Global MCP servers", <Mono key="gm">{e.paths.globalMcp}</Mono>],
            ["Agent skills", <Mono key="as">{e.paths.agentSkills}</Mono>],
            ["Agent MCP servers", <Mono key="am">{e.paths.agentMcp}</Mono>],
          ]}
        />
      </Panel>
      <p className="mt-4 mb-2 px-0.5 text-[12.5px] text-ink-2">
        A skill is a directory with a <Mono>SKILL.md</Mono> file that starts with a <Mono>description</Mono>{" "}
        in YAML front matter. mcp.json uses the format of Claude Code and Cursor. In the agent file,{" "}
        <Mono>{'{ "disabled": true }'}</Mono> turns off a global server.
      </p>
      <Panel>
        <pre className="overflow-x-auto px-4 py-3 font-mono text-[11.5px] leading-relaxed text-ink-2">
          {EXAMPLE}
        </pre>
      </Panel>
      <div className="mt-3">
        <Note tone="warn">
          An MCP server runs as your user, with no sandbox. dex stores its headers and environment only in
          mcp.json, as plain text. Reload applies to new runs, and dex refuses it while the agent has work.
        </Note>
      </div>
    </Section>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="font-normal text-ink-2">{children}</span>;
}
