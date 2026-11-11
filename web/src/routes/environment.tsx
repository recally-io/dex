import { EnvKeys } from "../components/env-keys";
import { OnePasswordSettings } from "../components/one-password";
import { FramedPage, Note, Section } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function EnvironmentPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Environment"
      toolbar={<GlobalSettingsSwitcher page="/environment" />}
      description="Tools of all bots start from your login shell environment, then these keys. Set keys for one bot in its Tools tab. Changes apply automatically when all bots are idle."
    >
      <EnvKeys target={{ scope: "dex" }} n={1} />
      <OnePasswordSettings n={3} />
      <Section n={4} title="Secret storage">
        <Note tone="warn">
          Use mode 0600 for .env files and keep them out of Git. The model can read the files or run env. Tool
          output becomes part of the transcript. dex stores credentials as plain text. With 1Password, bots
          read a secret only when a command needs it. Do not put passwords in chats, instructions or memory.
          Memory is a git repository, so a password stays in its history.
        </Note>
      </Section>
    </FramedPage>
  );
}
