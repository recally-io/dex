import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconFileText } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconFileText";
import { IconPlusMedium } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPlusMedium";
import { formatSize, type Attachments, type DraftFile } from "../lib/attachments";
import { Spinner } from "./feedback";
import { cn } from "../lib/utils";

// The file chip from upstream PromptBar.tsx, with a thumbnail, the upload state and the error.
function FileChip({ file, onRemove }: { file: DraftFile; onRemove: () => void }) {
  const uploading = !file.upload && !file.error;

  return (
    <li
      className={cn(
        "flex h-10 max-w-full min-w-0 animate-[pop-in_200ms_var(--ease-out-strong)_both] items-center gap-2 rounded-[10px] py-1 pr-1 pl-1 text-[11.5px] shadow-hairline",
        file.error ? "bg-red-tint" : "bg-field",
      )}
    >
      {file.preview ? (
        <img src={file.preview} alt="" className="size-8 shrink-0 rounded-[6px] object-cover" />
      ) : (
        <span className="flex size-8 shrink-0 items-center justify-center rounded-[6px] bg-inset text-ink-3">
          <IconFileText size={15} />
        </span>
      )}
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="max-w-40 truncate font-medium text-ink" title={file.name}>
          {file.name}
        </span>
        {file.error ? (
          <span role="alert" className="max-w-40 truncate text-red" title={file.error}>
            {file.error}
          </span>
        ) : (
          <span className="text-ink-3 tabular-nums">{uploading ? "Uploading…" : formatSize(file.size)}</span>
        )}
      </span>
      {uploading ? <Spinner /> : null}
      <button
        type="button"
        aria-label={`Remove ${file.name}`}
        onClick={onRemove}
        className="flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors duration-100 hover:bg-line/70 hover:text-ink"
      >
        <IconCrossSmall size={14} />
      </button>
    </li>
  );
}

// Look from upstream IceCreamHarness.tsx: a floating card with a model button and round actions.
export function Composer({
  controls,
  fix,
  files,
  running,
  stoppable,
  disabledReason,
  pending,
  onSend,
  onStop,
  text,
  onTextChange,
  placeholder,
  autoFocus = false,
}: {
  /** Actions for the current context. */
  controls?: ReactNode;
  /** The action after the disabled reason, for example a model picker. */
  fix?: ReactNode;
  /** The attachments for this input. */
  files?: Attachments;
  running: boolean;
  stoppable: boolean;
  disabledReason?: string;
  pending: boolean;
  onSend: (text: string) => Promise<void>;
  onStop: () => void;
  text: string;
  onTextChange: (text: string) => void;
  placeholder: string;
  autoFocus?: boolean;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const composing = useRef(false);
  const descriptionId = useId();
  const [dropping, setDropping] = useState(false);
  const canAttach = !!files && !disabledReason;

  const blocked = files?.uploading
    ? "Wait until the files upload"
    : files?.failed
      ? "Remove the files that failed"
      : "";

  const canSend = (!!text.trim() || !!files?.ready.length) && !blocked && !disabledReason && !pending;

  useEffect(() => {
    if (autoFocus) area.current?.focus();
  }, [autoFocus]);

  async function send() {
    if (!canSend) return;
    const value = text;
    await onSend(value.trim());
  }

  return (
    <div className="w-full">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void send().catch(() => {});
        }}
        onDragOver={(e) => {
          if (!canAttach || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDropping(true);
        }}
        onDragLeave={(e) => {
          if (!(e.relatedTarget instanceof Node && e.currentTarget.contains(e.relatedTarget)))
            setDropping(false);
        }}
        onDrop={(e) => {
          setDropping(false);

          if (!canAttach || !e.dataTransfer.files.length) return;
          e.preventDefault();
          files.add([...e.dataTransfer.files]);
        }}
        className={cn(
          "relative isolate flex flex-col gap-2 overflow-hidden rounded-[12px] border border-line bg-surface px-3 pt-2 pb-2 transition-[border-color] duration-150 focus-within:border-line-strong",
          dropping && "border-dex-accent focus-within:border-dex-accent",
        )}
      >
        {files?.files.length ? (
          // Three rows at most; more files scroll, so the composer stays usable on a phone.
          <ul
            aria-label="Attached files"
            className="-m-1 flex max-h-[142px] flex-wrap gap-1.5 overflow-y-auto overscroll-contain p-1"
          >
            {files.files.map((file) => (
              <FileChip key={file.key} file={file} onRemove={() => files.remove(file.key)} />
            ))}
          </ul>
        ) : null}
        {files?.notice ? (
          <p role="alert" className="px-1 text-[12px] text-red">
            {files.notice}
          </p>
        ) : null}
        <textarea
          ref={(element) => {
            area.current = element;

            if (element) {
              element.style.height = "auto";
              element.style.height = `${Math.min(element.scrollHeight, 200)}px`;
            }
          }}
          rows={1}
          className="min-h-[44px] w-full min-w-0 resize-none bg-transparent px-1 py-1.5 text-[14px] leading-5 text-ink outline-none [overflow-wrap:anywhere] placeholder:text-ink-3 disabled:cursor-not-allowed"
          maxLength={32000}
          placeholder={running ? "Queue a follow-up…" : placeholder}
          aria-label={running ? "Queue a follow-up…" : placeholder}
          aria-describedby={descriptionId}
          value={text}
          disabled={!!disabledReason}
          onChange={(e) => onTextChange(e.target.value)}
          onPaste={(e) => {
            if (!canAttach) return;

            const pasted = [...e.clipboardData.items].flatMap((item) => {
              if (item.kind !== "file") return [];

              const file = item.getAsFile();

              return file ? [file] : [];
            });

            if (!pasted.length) return;
            e.preventDefault();
            files.add(pasted);
          }}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={() => {
            composing.current = false;
          }}
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing &&
              !composing.current &&
              e.nativeEvent.keyCode !== 229
            ) {
              e.preventDefault();
              void send().catch(() => {});
            }
          }}
        />
        <div className="flex min-w-0 items-center gap-1">
          {files ? (
            <>
              <button
                type="button"
                aria-label="Attach files"
                title="Attach files"
                disabled={!canAttach}
                onClick={() => picker.current?.click()}
                className="flex size-[30px] shrink-0 items-center justify-center rounded-control text-ink-3 transition-[background-color,color,transform] duration-150 enabled:hover:bg-hover enabled:hover:text-ink enabled:active:scale-[0.94] disabled:opacity-40"
              >
                <IconPlusMedium size={16} />
              </button>
              <input
                ref={picker}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  files.add([...(e.target.files ?? [])]);
                  // The same file can be picked again after a remove.
                  e.target.value = "";
                }}
              />
            </>
          ) : null}
          {controls}
          <span className="min-w-2 flex-1" />
          {stoppable ? (
            <button
              type="button"
              aria-label="Stop run"
              title="Stop this run and its background work"
              // A missing model blocks new messages, not stopping the current run.
              disabled={pending}
              onClick={onStop}
              className="flex size-[30px] shrink-0 items-center justify-center rounded-control bg-ink text-surface transition-[transform,opacity] duration-150 enabled:active:scale-[0.94] disabled:opacity-40"
            >
              <span aria-hidden className="size-2.5 rounded-[2px] bg-current" />
            </button>
          ) : null}
          {!stoppable || text.trim() || files?.files.length ? (
            <button
              type="submit"
              aria-label={running ? "Queue message" : "Send message"}
              title={blocked || (running ? "Queue message" : "Send message")}
              disabled={!canSend}
              className={cn(
                "flex size-[30px] shrink-0 items-center justify-center rounded-control transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.94]",
                canSend ? "bg-ink text-surface" : "bg-line-strong text-ink-2",
              )}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.4"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
              >
                <path d="M12 19V5M5 12l7-7 7 7" />
              </svg>
            </button>
          ) : null}
        </div>
      </form>
      <p id={descriptionId} className="mt-2 px-2 text-center text-[11.5px] text-ink-3">
        {disabledReason || "Tools run on this computer without a sandbox. Review commands and results."}
        {fix ? <> {fix}</> : null}
      </p>
    </div>
  );
}
