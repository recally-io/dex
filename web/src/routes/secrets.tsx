import { useAgentId } from "../lib/agent-scope";
import { Link } from "@tanstack/react-router";
import { Facts, FramedPage, Mono, Note, NotAvailable, Panel, Section } from "../components/page";
import { buttonVariants } from "../components/atoms/Button";
import { WithSettings } from "../components/with-settings";

export function SecretsPage() {
  const agentId = useAgentId();

  return (
    <FramedPage
      title="Secrets"
      badge={<NotAvailable />}
      description="A place for passwords and tokens that the agent can use in its tools. dex does not have it yet."
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
                  ["Telegram bot token", settings.telegram.enabled ? "Saved" : "None"],
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
      <Section n={2} title="Until then">
        <Note tone="warn">
          dex stores these values as plain text. Anyone who can read the data directory can read them. Do not
          put passwords in chats, instructions or memory. Memory is a git repository, so a password stays in
          its history.
        </Note>
      </Section>
    </FramedPage>
  );
}
