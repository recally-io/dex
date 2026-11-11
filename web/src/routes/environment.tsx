import { EnvKeys } from "../components/env-keys";
import { FramedPage, Note, Section } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";

export function EnvironmentPage() {
  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="Environment"
      toolbar={<GlobalSettingsSwitcher page="/environment" />}
      description="Keys for the tools of all bots, such as API tokens for MCP servers. Set keys for one bot in its Tools tab."
    >
      <EnvKeys target={{ scope: "dex" }} n={1} />
      <Section n={3} title="Secret storage">
        <Note tone="warn">
          Use mode 0600 for .env files and keep them out of Git. The model can read the files or run env. Tool
          output becomes part of the transcript. dex stores credentials as plain text. Do not put passwords in
          chats, instructions or memory. Memory is a git repository, so a password stays in its history.
        </Note>
      </Section>
    </FramedPage>
  );
}
