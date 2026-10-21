import type { ComponentProps } from "react";
import { Tooltip } from "@base-ui/react/tooltip";
import { cn } from "../lib/utils";

/** A square icon button. Its label is the accessible name and shows as a tooltip on hover. */
export function IconButton({
  label,
  className,
  children,
  ...props
}: ComponentProps<"button"> & { label: string }) {
  return (
    <Tooltip.Root>
      <Tooltip.Trigger
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
      </Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Positioner side="bottom" sideOffset={6} collisionPadding={8} className="z-50">
          <Tooltip.Popup className="animate-[fade-in_120ms_ease-out_both] rounded-full bg-ink px-2.5 py-1 text-[12px] font-medium whitespace-nowrap text-canvas">
            {label}
          </Tooltip.Popup>
        </Tooltip.Positioner>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}
