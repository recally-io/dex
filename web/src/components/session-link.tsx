import { Link } from "@tanstack/react-router";
import { IconBubbles } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBubbles";

export function SessionsLink({ active }: { active: number }) {
  return (
    <Link
      to="/sessions"
      className="flex h-7 items-center gap-1.5 rounded-[7px] px-2 text-[12.5px] font-medium text-ink-2 hover:bg-hover hover:text-ink"
    >
      <IconBubbles size={16} /> Sessions
      {active > 0 ? (
        <span
          role="img"
          aria-label={`${active} active`}
          className="flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-orange-tint px-1.5 text-[11px] font-medium text-orange tabular-nums"
        >
          {active}
        </span>
      ) : null}
    </Link>
  );
}
