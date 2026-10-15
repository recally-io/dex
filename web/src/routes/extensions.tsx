import { Link } from "@tanstack/react-router";
import { Blocks, Plug } from "lucide-react";
import type { ReactNode } from "react";
import { Card, Note, NotAvailable, Page, PageHeader, Section } from "../components/page";
import { Button } from "../components/ui/button";
import { ValuePill } from "../components/atoms/ValuePill";

function Capability({
  id,
  icon,
  title,
  body,
  action,
}: {
  id: string;
  icon: ReactNode;
  title: string;
  body: string;
  action: string;
}) {
  return (
    <Card className="border-dashed shadow-none">
      <div className="flex items-start gap-4">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-inset text-ink-3">
          {icon}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-ink">{title}</h2>
            <NotAvailable />
          </div>
          <p id={id} className="mt-1 text-[13px] leading-5 text-ink-2">
            {body}
          </p>
        </div>
        <Button variant="outline" size="sm" disabled aria-describedby={id}>
          {action}
        </Button>
      </div>
    </Card>
  );
}

export function ExtensionsPage() {
  return (
    <Page>
      <PageHeader
        title="Extensions"
        description="Extensions would give the agent more tools. This version of dex has none; the agent uses only its built-in tools."
      />
      <Section title="Built-in tools">
        <Card>
          <p className="mb-3 text-[13px] text-ink-2">
            Always on. The agent runs them on this computer without a sandbox and does not ask before each
            call.
          </p>
          <div className="flex flex-wrap gap-1.5">
            {["read", "write", "edit", "bash"].map((tool) => (
              <ValuePill key={tool}>
                <span className="font-mono">{tool}</span>
              </ValuePill>
            ))}
          </div>
        </Card>
      </Section>
      <Section title="Not available yet">
        <div className="flex flex-col gap-3">
          <Capability
            id="skills-status"
            icon={<Blocks aria-hidden className="size-5" />}
            title="Skills"
            body="Skills are packaged instructions and scripts. dex cannot install or load skills in this version."
            action="Add skill"
          />
          <Capability
            id="mcp-status"
            icon={<Plug aria-hidden className="size-5" />}
            title="MCP servers"
            body="MCP servers give the agent tools from other programs. dex cannot connect to MCP servers in this version."
            action="Add server"
          />
        </div>
      </Section>
      <Note>
        To change how the agent works now, edit its{" "}
        <Link to="/agent" className="text-brand underline">
          instructions
        </Link>
        .
      </Note>
    </Page>
  );
}
