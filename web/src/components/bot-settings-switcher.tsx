import { Link } from "@tanstack/react-router";
import { useAgentId } from "../lib/agent-scope";
import { cn } from "../lib/utils";

const pages = [
  { to: "/a/$agentId/agent", label: "General", segment: "agent" },
  { to: "/a/$agentId/instructions", label: "Instructions", segment: "instructions" },
  { to: "/a/$agentId/channels", label: "Channels", segment: "channels" },
] as const;

/**
 * The three pages of a bot's settings. A segmented track, not a second tab bar: the tabs name the
 * bot's areas, this switches between views inside one area, and it must not read as the same control.
 */
export function BotSettingsSwitcher({
  segment,
  channels = 0,
  draft = false,
}: {
  segment: string | undefined;
  channels?: number;
  draft?: boolean;
}) {
  const agentId = useAgentId();

  return (
    <nav
      aria-label="Settings pages"
      className="inline-flex items-center gap-0.5 rounded-[8px] bg-hover-2 p-0.5"
    >
      {pages.map((page) => {
        const current = segment === page.segment;

        return (
          <Link
            key={page.to}
            to={page.to}
            params={{ agentId }}
            aria-current={current ? "page" : undefined}
            className={cn(
              "flex h-7 shrink-0 items-center gap-1.5 rounded-[6px] px-3 text-[12.5px] whitespace-nowrap transition-colors",
              current ? "bg-surface font-medium text-ink shadow-hairline" : "text-ink-2 hover:text-ink",
            )}
          >
            {page.label}
            {page.segment === "channels" && channels ? (
              <span className="text-[11px] text-ink-3 tabular-nums">{channels}</span>
            ) : null}
            {page.segment === "instructions" && draft ? (
              <span role="img" aria-label="Unsaved draft" className="size-1.5 rounded-full bg-orange" />
            ) : null}
          </Link>
        );
      })}
    </nav>
  );
}
