import { Vault } from "../components/vault";
import { Runners } from "../components/runners";
import { OnePasswordSettings } from "../components/one-password";
import { FramedPage } from "../components/page";
import { SourceSettings } from "../components/source-settings";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function RunnersPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Runners"
      toolbar={<GlobalSettingsSwitcher page="/settings/runners" />}
      description="Choose where bots run tools: locally, on a machine, or in a sandbox."
    >
      <Runners />
    </FramedPage>
  );
}

export function SharedSecretsPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Secrets"
      toolbar={<GlobalSettingsSwitcher page="/settings/secrets" />}
      description="Manage shared Vault secrets and 1Password. Set values for one bot in its own Secrets page."
    >
      <Vault target={{ scope: "dex" }} />
      <OnePasswordSettings />
    </FramedPage>
  );
}

export function GlobalIntegrationsPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Integrations"
      toolbar={<GlobalSettingsSwitcher page="/settings/integrations" />}
      description="Manage the external sources that send events to Dex. Configure channels and MCP servers in each bot's settings."
    >
      <SourceSettings />
    </FramedPage>
  );
}
