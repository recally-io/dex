import { Link } from "@tanstack/react-router";
import { Facts, FramedPage, NotAvailable, Panel, Section } from "../components/page";

export function ExtensionsPage() {
  return (
    <FramedPage
      title="Extensions"
      description="Extensions add skills and tools to the agent. dex cannot add them from the web yet."
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
      <Section
        n={2}
        title="Skills"
        caption="Instructions that the agent loads when a task needs them."
        action={<NotAvailable />}
      >
        <Panel className="px-4 py-3.5 text-[12.5px] leading-relaxed text-ink-2">
          dex does not load skills now. The agent can still read instruction files that you put in the
          workspace or in memory.
        </Panel>
      </Section>
      <Section
        n={3}
        title="MCP servers"
        caption="Tools from Model Context Protocol servers."
        action={<NotAvailable />}
      >
        <Panel className="px-4 py-3.5 text-[12.5px] leading-relaxed text-ink-2">
          dex does not connect to MCP servers now. The agent has only its four tools.
        </Panel>
        <p className="mt-3 px-0.5 text-[12px] text-ink-3">
          To change how the agent works now, edit its{" "}
          <Link to="/agent" className="font-medium text-accent-ink underline">
            instructions
          </Link>
          .
        </p>
      </Section>
    </FramedPage>
  );
}
