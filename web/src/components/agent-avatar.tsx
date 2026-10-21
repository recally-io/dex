import type { CSSProperties } from "react";
import type { Agent } from "../api/generated/types.gen";
import { cn } from "../lib/utils";

/**
 * The mark of an agent: its first letter on a tint that stays the same for that agent. The hue
 * comes from the ID, so the list reads at a glance without a stored colour. The tint and the
 * letter take their lightness from the theme in styles.css.
 */
export function AgentAvatar({ agent, className }: { agent: Pick<Agent, "id" | "name">; className?: string }) {
  const seed = agent.id || agent.name;
  let hue = 0;

  for (const char of seed) hue = (hue * 31 + char.codePointAt(0)!) % 360;

  // SAFETY: React forwards unknown CSS properties verbatim; CSSProperties only types known ones.
  const style = { "--avatar-hue": hue } as CSSProperties;

  return (
    <span
      aria-hidden
      style={style}
      className={cn(
        "agent-avatar flex size-6 shrink-0 items-center justify-center rounded-[7px] text-[12px] font-semibold",
        className,
      )}
    >
      {agent.name.charAt(0).toLowerCase()}
    </span>
  );
}
