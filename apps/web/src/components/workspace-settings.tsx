import { Panel, Section } from "./page";
import { buttonVariants } from "./ui/button";

/** The app handles this link itself. A regular browser has no workspace to switch. */
export function WorkspaceSettings() {
  if (!navigator.userAgent.split(" ").includes("DexWorkspace/1")) return null;

  return (
    <Section title="Workspace">
      <Panel className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span className="min-w-0 text-sm [overflow-wrap:anywhere]">{window.location.origin}</span>
        <a href="dex-workspace://settings" className={buttonVariants({ variant: "secondary" })}>
          Switch workspace
        </a>
      </Panel>
    </Section>
  );
}
