import { Panel, Section } from "./page";
import { buttonVariants } from "./ui/button";

// The native WebView handles activation of this link.
export const workspaceSettingsHref = "dex-workspace://settings";

export function getWorkspaceInfo() {
  if (!navigator.userAgent.split(" ").includes("DexWorkspace/1")) return null;

  const { host, origin } = window.location;

  if (origin === "dex-local://workspace" || origin === "https://local.dex.internal") {
    return { label: "Local Workspace", subtitle: "On this device", isLocal: true, origin };
  }

  return { label: host || origin, subtitle: "Remote server", isLocal: false, origin };
}

export function WorkspaceSettings() {
  const workspace = getWorkspaceInfo();

  if (!workspace) return null;

  const label = workspace.isLocal ? workspace.label : workspace.origin;

  return (
    <Section title="Workspace">
      <Panel className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
        <span className="min-w-0 text-sm [overflow-wrap:anywhere]">{`${label} · ${workspace.subtitle}`}</span>
        <a href={workspaceSettingsHref} className={buttonVariants({ variant: "secondary" })}>
          Switch workspace
        </a>
      </Panel>
    </Section>
  );
}
