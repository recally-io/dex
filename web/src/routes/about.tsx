import { useQuery } from "@tanstack/react-query";
import { getHealthOptions } from "../api/generated/@tanstack/react-query.gen";
import { Failure, Loading } from "../components/feedback";
import { Card, Page, PageHeader, Rows, Section } from "../components/page";
import { ValuePill } from "../components/atoms/ValuePill";

export function AboutPage() {
  const health = useQuery(getHealthOptions());

  return (
    <Page>
      <PageHeader title="About dex" description="A personal agent that runs on this computer." />
      <Section title="This server">
        {health.isPending ? (
          <Loading rows={1} />
        ) : health.isError ? (
          <Failure
            title="Could not reach the server"
            error={health.error}
            retry={() => void health.refetch()}
          />
        ) : (
          <Card>
            <Rows
              rows={[
                [
                  "Server",
                  <ValuePill key="server" tone="green">
                    Running
                  </ValuePill>,
                ],
                [
                  "Version",
                  <span key="version" className="font-mono">
                    {health.data.version}
                  </span>,
                ],
                [
                  "Data folder",
                  <span key="data" className="break-all font-mono text-xs">
                    {health.data.dataDir}
                  </span>,
                ],
              ]}
            />
          </Card>
        )}
      </Section>
      <Section title="How it runs">
        <Card>
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] leading-6 text-ink-2">
            <li>The control process serves this page and the API, and keeps chats in control.sqlite.</li>
            <li>The worker process runs the agent and its tools. Control restarts it when it stops.</li>
            <li>The data folder holds the database, the agent memory and the workspace.</li>
          </ul>
        </Card>
      </Section>
      <Section title="Safety boundaries">
        <Card>
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] leading-6 text-ink-2">
            <li>The web page has no sign-in. Any program that can reach the port can use dex.</li>
            <li>The agent runs read, write, edit and bash on this computer without a sandbox.</li>
            <li>Allowed Telegram users reach the same agent, tools and memory.</li>
            <li>Keys and tokens are write-only in the API and plain text in the data folder.</li>
          </ul>
        </Card>
      </Section>
    </Page>
  );
}
