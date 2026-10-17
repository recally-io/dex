import { useAgentId } from "../lib/agent-scope";
import { Link } from "@tanstack/react-router";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { buttonVariants } from "../components/atoms/Button";
import { WithSettings } from "../components/with-settings";

export function SecretsPage() {
  const agentId = useAgentId();

  return (
    <FramedPage
      title="Secrets"
      description="Tool credentials come from .env files in $HOME/.agents, $DEX_HOME/.agents, and $AGENT_HOME/.agents. Narrower values win."
    >
      <WithSettings>
        {(settings) => (
          <Section n={1} title="What dex stores now" caption="Only the credentials that dex itself needs.">
            <Panel>
              <Facts
                rows={[
                  ...settings.providers.map((p): [string, string] => [
                    `${p.name} API key`,
                    p.keySet ? "Saved" : "None",
                  ]),
                  ["Channel credentials", "Manage in each agent’s Channels tab"],
                  ["Stored in", <Mono key="s">control.sqlite in the data directory</Mono>],
                  ["Shown in the web", "Never. You can only replace them"],
                ]}
              />
            </Panel>
            <div className="mt-3 flex flex-wrap gap-2">
              <Link to="/providers" className={buttonVariants({ size: "xs", variant: "secondary" })}>
                Provider keys
              </Link>
              <Link
                to="/a/$agentId/channels/$channelId"
                params={{ agentId, channelId: "telegram" }}
                className={buttonVariants({ size: "xs", variant: "secondary" })}
              >
                Telegram token
              </Link>
            </div>
          </Section>
        )}
      </WithSettings>
      <Section n={2} title="Tool environment">
        <div className="mb-3">
          <Link
            to="/a/$agentId/extensions"
            params={{ agentId }}
            className={buttonVariants({ size: "xs", variant: "secondary" })}
          >
            Current agent’s Extensions
          </Link>
        </div>
        <Note tone="warn">
          Use mode 0600 for .env files and keep them out of Git. The model can read the files or run env. Tool
          output becomes part of the transcript. dex stores credentials as plain text. Do not put passwords in
          chats, instructions or memory. Memory is a git repository, so a password stays in its history.
        </Note>
      </Section>
    </FramedPage>
  );
}
