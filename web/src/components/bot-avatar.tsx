import type { CSSProperties } from "react";
import type { Bot } from "../api/generated/types.gen";
import { cn } from "../lib/utils";

/**
 * The mark of a bot: its first letter on a tint that stays the same for that bot. The hue
 * comes from the ID, so the list reads at a glance without a stored colour. The tint and the
 * letter take their lightness from the theme in styles.css.
 */
export function BotAvatar({ bot, className }: { bot: Pick<Bot, "id" | "name">; className?: string }) {
  const seed = bot.id || bot.name;
  let hue = 0;

  for (const char of seed) hue = (hue * 31 + char.codePointAt(0)!) % 360;

  // SAFETY: React forwards unknown CSS properties verbatim; CSSProperties only types known ones.
  const style = { "--avatar-hue": hue } as CSSProperties;

  return (
    <span
      aria-hidden
      style={style}
      className={cn(
        "bot-avatar flex size-6 shrink-0 items-center justify-center rounded-control text-[12px] font-semibold",
        className,
      )}
    >
      {bot.name.charAt(0).toUpperCase()}
    </span>
  );
}
