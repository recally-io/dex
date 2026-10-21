import { useId } from "react";
import { instructionsLimit, useInstructions } from "../lib/instructions";
import { Textarea } from "./form";
import { Button } from "./ui/button";
import { Failure, Spinner } from "./feedback";
import { cn } from "../lib/utils";

/**
 * The instructions textarea with its save row. The settings page frames it in a numbered section;
 * the timeline frames it in a side panel. Both share one draft, so the text follows the user.
 */
export function InstructionsEditor({
  saved,
  rows,
  grow,
}: {
  saved: string;
  rows?: number;
  /** Fill the panel's height instead of showing a fixed number of rows. */
  grow?: boolean;
}) {
  const status = useId();
  const { text, dirty, setDraft, save } = useInstructions(saved);

  return (
    <>
      <Textarea
        aria-label="Bot instructions"
        aria-describedby={status}
        rows={rows}
        value={text}
        maxLength={instructionsLimit}
        placeholder="For example: Reply in the language I write in."
        onChange={(e) => setDraft(e.target.value)}
        className={cn(
          "rounded-[8px] font-sans text-[13.5px] leading-relaxed shadow-none focus:shadow-none",
          grow && "h-full min-h-0 resize-none",
        )}
      />
      <div className="flex flex-wrap items-center gap-2 border-t border-border px-3 py-2">
        <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
          {text.length.toLocaleString()} / {instructionsLimit.toLocaleString()}
        </span>
        <span id={status} aria-live="polite" className="min-w-0 text-[11.5px] text-muted-foreground">
          {dirty ? "Unsaved draft. Runs use the saved instructions until you save." : null}
        </span>
        <span className="flex-1" />
        <Button
          size="xs"
          variant="ghost"
          aria-label="Discard draft"
          disabled={!dirty || save.isPending}
          onClick={() => {
            save.reset();
            setDraft(null);
          }}
        >
          Discard
        </Button>
        <Button
          size="xs"
          variant="default"
          aria-label="Save instructions"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate(text)}
        >
          {save.isPending ? <Spinner className="border-background/40 border-t-canvas" /> : null}
          {save.isPending ? "Saving" : "Save"}
        </Button>
      </div>
      {save.isError ? (
        <div className="border-t border-border p-3">
          <Failure title="Instructions not saved. Your draft is kept." error={save.error} />
        </div>
      ) : null}
    </>
  );
}
