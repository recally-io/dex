import { useQuery } from "@tanstack/react-query";
import { getHealthOptions } from "../api/generated/@tanstack/react-query.gen";
import { Failure, Loading } from "../components/feedback";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { StatusPill } from "../components/atoms/StatusPill";

export function AboutPage() {
  const health = useQuery(getHealthOptions());

  return (
    <FramedPage title="About dex" description="A personal bot that runs on this computer.">
      <Section n={1} title="This server">
        {health.isPending ? (
          <Loading />
        ) : health.isError ? (
          <Failure
            title="Could not reach the server"
            error={health.error}
            retry={() => void health.refetch()}
          />
        ) : (
          <Panel>
            <Facts
              rows={[
                [
                  "Server",
                  <StatusPill key="server" tone="green" className="h-5 px-2 text-[11.5px]">
                    Running
                  </StatusPill>,
                ],
                ["Version", <Mono key="version">{health.data.version}</Mono>],
              ]}
            />
          </Panel>
        )}
      </Section>
      <Section n={2} title="Files" caption="Everything is in the data directory.">
        {health.data ? (
          <Panel>
            <Facts
              rows={[
                ["Data directory", <Mono key="data">{health.data.dataDir}</Mono>],
                ["Chats and settings", <Mono key="control">control.sqlite</Mono>],
                ["Transcripts and runs", <Mono key="state">agents/default/state.sqlite</Mono>],
                ["Workspace", <Mono key="workspace">agents/default/workspace</Mono>],
                ["Memory", <Mono key="memory">memory</Mono>],
              ]}
            />
          </Panel>
        ) : (
          <p className="px-0.5 text-[12.5px] text-ink-3">The paths show when the server answers.</p>
        )}
        <p className="mt-3 px-0.5 text-[12px] text-ink-3">
          Set <Mono>DEX_DATA_DIR</Mono> to use a different directory.
        </p>
      </Section>
      <Section n={3} title="How it runs">
        <Panel className="px-4 py-3.5">
          <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] leading-6 text-ink-2 marker:text-ink-3">
            <li>The control process serves this page and the API, and keeps chats in control.sqlite.</li>
            <li>The worker process runs the bot and its tools. Control restarts it when it stops.</li>
            <li>The data directory holds the database, the bot memory and the workspace.</li>
          </ul>
        </Panel>
      </Section>
      <Section n={4} title="Safety">
        <div className="flex flex-col gap-3">
          <Note tone="warn">
            Tools run without a sandbox, and the server has no sign-in. Keep dex on 127.0.0.1.
          </Note>
          <Panel className="px-4 py-3.5">
            <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] leading-6 text-ink-2 marker:text-ink-3">
              <li>The web page has no sign-in. Any program that can reach the port can use dex.</li>
              <li>The bot runs read, write, edit and bash on this computer without a sandbox.</li>
              <li>Allowed Telegram users reach the same bot, tools and memory.</li>
              <li>Keys and tokens are write-only in the API and plain text in the data directory.</li>
            </ul>
          </Panel>
        </div>
      </Section>
      <Section n={5} title="This design">
        <p className="px-0.5 text-[12.5px] leading-relaxed text-ink-2">
          The look comes from Beautiful UI by slev12397 (MIT): its tokens, atoms and the layout of
          beautifului.dev.
        </p>
      </Section>
    </FramedPage>
  );
}
