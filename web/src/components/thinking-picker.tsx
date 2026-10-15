import { useRef, useState, type ReactElement } from "react";
import { useMutation } from "@tanstack/react-query";
import { Popover } from "@base-ui/react/popover";
import { Command } from "cmdk";
import type { ThinkingLevel } from "../api/generated/types.gen";
import { thinkingNames } from "../lib/agent";
import { Failure } from "./feedback";
import { Mark } from "./model-picker";

const itemClass =
  "flex h-8 w-full min-w-0 items-center gap-2 rounded-[7px] px-2 text-left transition-colors duration-100 data-[disabled=true]:cursor-wait data-[selected=true]:bg-hover";

// An anchored, non-modal list of the levels that pi-ai supports for the model. It saves like
// ModelPicker. With defaultLevel, the first item clears the chat's level (save(null)).
export function ThinkingPicker({
  trigger,
  value,
  levels,
  defaultLevel,
  save,
}: {
  trigger: ReactElement;
  value: ThinkingLevel | null;
  levels: ThinkingLevel[];
  defaultLevel?: ThinkingLevel;
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
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        if (mutation.isPending) return;

        if (next) mutation.reset();
        setOpen(next);
      }}
    >
      <Popover.Trigger render={trigger} />
      <Popover.Portal>
        <Popover.Positioner align="start" sideOffset={6} collisionPadding={8} className="z-40">
          <Popover.Popup
            aria-label="Thinking level"
            initialFocus={root}
            className="flex w-[240px] max-w-[calc(100vw-1rem)] origin-(--transform-origin) animate-[pop-in_180ms_var(--ease-out-strong)_both] flex-col overflow-hidden rounded-[12px] bg-surface shadow-raised outline-none"
          >
            <Command ref={root} defaultValue={current} label="Thinking level" className="outline-none">
              <Command.List className="p-1">
                {defaultLevel ? (
                  <Command.Item
                    value="default"
                    disabled={pending !== undefined}
                    onSelect={() => pick(null)}
                    className={itemClass}
                  >
                    <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">
                      Use default ({thinkingNames[defaultLevel]})
                    </span>
                    <Mark checked={current === "default"} saving={pending === null} />
                  </Command.Item>
                ) : null}
                {levels.map((level) => (
                  <Command.Item
                    key={level}
                    value={level}
                    disabled={pending !== undefined}
                    onSelect={() => pick(level)}
                    className={itemClass}
                  >
                    <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">
                      {thinkingNames[level]}
                    </span>
                    <Mark checked={current === level} saving={pending === level} />
                  </Command.Item>
                ))}
              </Command.List>
              {mutation.isError ? (
                <div className="border-t border-line p-2">
                  <Failure title="The thinking level did not change" error={mutation.error} />
                </div>
              ) : null}
            </Command>
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}
