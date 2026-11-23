import { Vault } from "../components/vault";
import { ExecutionEnvironments } from "../components/execution-environments";
import { OnePasswordSettings } from "../components/one-password";
import { FramedPage } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function EnvironmentPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Environment"
      toolbar={<GlobalSettingsSwitcher page="/settings/environment" />}
      description="Configure shared secrets, execution environments, and 1Password. To set secrets for one bot, use its Tools page."
    >
      <Vault target={{ scope: "dex" }} />
      <ExecutionEnvironments />
      <OnePasswordSettings />
    </FramedPage>
  );
}
