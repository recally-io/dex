import type { ComponentProps } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { cn } from "../lib/utils";

/** A square icon button. Its label is the accessible name and shows as a tooltip on hover. */
export function IconButton({
  label,
  className,
  children,
  ...props
}: ComponentProps<"button"> & { label: string }) {
  return (
    <Tooltip>
      <TooltipTrigger
        delay={300}
        render={
          <button
            type="button"
            aria-label={label}
            {...props}
            className={cn(
              "flex size-8 shrink-0 items-center justify-center rounded-control text-ink-2 transition-colors hover:bg-hover-2 hover:text-ink disabled:opacity-40 aria-pressed:bg-hover-2 aria-pressed:text-ink",
              className,
            )}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent
        side="bottom"
        sideOffset={6}
        className="rounded-full bg-ink px-2.5 py-1 text-[12px] font-medium text-canvas"
      >
        {label}
      </TooltipContent>
    </Tooltip>
  );
}
