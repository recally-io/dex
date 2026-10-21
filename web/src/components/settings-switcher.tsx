import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useAgentId } from "../lib/agent-scope";
import { cn } from "../lib/utils";

/**
 * A segmented track, not a second tab bar: tabs and the sidebar name areas, this switches between
 * views inside one area, and it must not read as the same control.
 */
function Track({ label, children }: { label: string; children: ReactNode }) {
  return (
    <nav aria-label={label} className="inline-flex items-center gap-0.5 rounded-[8px] bg-accent p-0.5">
      {children}
    </nav>
  );
}

function segment(current: boolean) {
  return cn(
    "flex h-7 shrink-0 items-center gap-1.5 rounded-[6px] px-3 text-[12.5px] whitespace-nowrap transition-colors",
    current
      ? "bg-card font-medium text-foreground shadow-card"
      : "text-muted-foreground hover:text-foreground",
  );
}

const botPages = [
  { to: "/a/$agentId/agent", label: "General", segment: "agent" },
  { to: "/a/$agentId/instructions", label: "Instructions", segment: "instructions" },
  { to: "/a/$agentId/channels", label: "Channels", segment: "channels" },
] as const;

/** The three pages of a bot's settings. */
export function BotSettingsSwitcher({
  segment: active,
  channels = 0,
  draft = false,
}: {
  segment: string | undefined;
  channels?: number;
  draft?: boolean;
}) {
  const agentId = useAgentId();

  return (
    <Track label="Settings pages">
      {botPages.map((page) => {
        const current = active === page.segment;

        return (
          <Link
            key={page.to}
            to={page.to}
            params={{ agentId }}
            aria-current={current ? "page" : undefined}
            className={segment(current)}
          >
            {page.label}
            {page.segment === "channels" && channels ? (
              <span className="text-[11px] text-muted-foreground tabular-nums">{channels}</span>
            ) : null}
            {page.segment === "instructions" && draft ? (
              <span role="img" aria-label="Unsaved draft" className="size-1.5 rounded-full bg-orange" />
            ) : null}
          </Link>
        );
      })}
    </Track>
  );
}

/** The pages that belong to the whole installation rather than to one bot. */
export const globalPages = [
  { to: "/providers", label: "Providers" },
  { to: "/secrets", label: "Secrets" },
  { to: "/about", label: "About" },
] as const;

export type GlobalPage = (typeof globalPages)[number]["to"];

export function GlobalSettingsSwitcher({ page: active }: { page: GlobalPage }) {
  return (
    <Track label="Settings pages">
      {globalPages.map((page) => (
        <Link
          key={page.to}
          to={page.to}
          aria-current={active === page.to ? "page" : undefined}
          className={segment(active === page.to)}
        >
          {page.label}
        </Link>
      ))}
    </Track>
  );
}
