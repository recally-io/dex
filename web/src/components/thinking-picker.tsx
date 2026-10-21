import { useRef, useState, type ReactElement } from "react";
import { useMutation } from "@tanstack/react-query";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { Command, CommandList, CommandItem } from "./ui/command";
import type { ThinkingLevel } from "../api/generated/types.gen";
import { thinkingNames } from "../lib/agent";
import { Failure } from "./feedback";
import { Mark } from "./model-picker";

const itemClass =
  "flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left transition-colors duration-100 data-[disabled=true]:cursor-wait data-[selected=true]:bg-accent";

// An anchored, non-modal list of the levels that pi-ai supports for the model. It saves like
// ModelPicker. With clearable, the first item "Default" clears the chat's level (save(null)).
export function ThinkingPicker({
  trigger,
  value,
  levels,
  clearable,
  save,
}: {
  trigger: ReactElement;
  value: ThinkingLevel | null;
  levels: ThinkingLevel[];
  clearable?: boolean;
  save: (level: ThinkingLevel | null) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const mutation = useMutation({ mutationFn: save, onSuccess: () => setOpen(false) });
  const pending = mutation.isPending ? mutation.variables : undefined;
  const current = value ?? "default";

  const pick = (level: ThinkingLevel | null) => {
    if ((level ?? "default") === current) setOpen(false);
    else mutation.mutate(level);
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (mutation.isPending) return;

        if (next) mutation.reset();
        setOpen(next);
      }}
    >
      <PopoverTrigger render={trigger} />
      <PopoverContent
        align="start"
        sideOffset={6}
        aria-label="Thinking level"
        initialFocus={root}
        className="flex w-[240px] max-w-[calc(100vw-1rem)] origin-(--transform-origin) animate-[pop-in_180ms_var(--ease-out-strong)_both] flex-col overflow-hidden rounded-[12px] bg-card shadow-raised outline-none"
      >
        <Command ref={root} defaultValue={current} label="Thinking level" className="outline-none">
          <CommandList className="p-1">
            {clearable ? (
              <CommandItem
                value="default"
                data-checked={current === "default"}
                disabled={pending !== undefined}
                onSelect={() => pick(null)}
                className={itemClass}
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-foreground">
                  Default
                </span>
                <Mark checked={current === "default"} saving={pending === null} />
              </CommandItem>
            ) : null}
            {levels.map((level) => (
              <CommandItem
                key={level}
                value={level}
                data-checked={current === level}
                disabled={pending !== undefined}
                onSelect={() => pick(level)}
                className={itemClass}
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-foreground">
                  {thinkingNames[level]}
                </span>
                <Mark checked={current === level} saving={pending === level} />
              </CommandItem>
            ))}
          </CommandList>
          {mutation.isError ? (
            <div className="border-t border-border p-2">
              <Failure title="The thinking level did not change" error={mutation.error} />
            </div>
          ) : null}
        </Command>
      </PopoverContent>
    </Popover>
  );
}
