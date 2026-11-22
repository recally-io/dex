import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { ArrowUp, ChevronDown, CornerDownLeft, FileText, ListEnd, Plus, X } from "lucide-react";
import { formatSize, type Attachments, type DraftFile } from "../lib/attachments";
import { Spinner } from "./feedback";
import { Button } from "./ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Textarea } from "./ui/textarea";
import { findCommand, matchCommands, parseCommand, SlashMenu, type SlashCommand } from "./slash-commands";
import { cn } from "../lib/utils";

// The file chip from upstream PromptBar.tsx, with a thumbnail, the upload state and the error.
function FileChip({ file, onRemove }: { file: DraftFile; onRemove: () => void }) {
  const uploading = !file.upload && !file.error;

  return (
    <li
      className={cn(
        "flex h-10 max-w-full min-w-0 animate-[pop-in_200ms_var(--ease-out-strong)_both] items-center gap-2 rounded-card py-1 pr-1 pl-1 text-[11.5px] shadow-card",
        file.error ? "bg-red-tint" : "bg-card",
      )}
    >
      {file.preview ? (
        <img src={file.preview} alt="" className="size-8 shrink-0 rounded-control object-cover" />
      ) : (
        <span className="flex size-8 shrink-0 items-center justify-center rounded-control bg-muted text-muted-foreground">
          <FileText className="size-[15px]" />
        </span>
      )}
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="max-w-40 truncate font-medium text-foreground" title={file.name}>
          {file.name}
        </span>
        {file.error ? (
          <span role="alert" className="max-w-40 truncate text-red" title={file.error}>
            {file.error}
          </span>
        ) : (
          <span className="text-muted-foreground tabular-nums">
            {uploading ? "Uploading…" : formatSize(file.size)}
          </span>
        )}
      </span>
      {uploading ? <Spinner /> : null}
      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        aria-label={`Remove ${file.name}`}
        onClick={onRemove}
        className="shrink-0 rounded-control text-muted-foreground hover:bg-border/70 hover:text-foreground"
      >
        <X className="size-[14px]" />
      </Button>
    </li>
  );
}

/** What a message does while the bot runs: join the run after the current tool step, or wait. */
export type SendMode = "steer" | "followUp";

const menuPopup =
  "w-[260px] origin-(--transform-origin) animate-[pop-in_160ms_var(--ease-out-strong)_both] rounded-card bg-card p-1 shadow-raised outline-none";

const menuItem =
  "flex w-full items-start gap-2.5 rounded-control px-2 py-1.5 text-left text-foreground data-highlighted:bg-accent data-disabled:opacity-40";

function MenuText({ title, help }: { title: string; help: string }) {
  return (
    <span className="flex min-w-0 flex-col">
      <span className="text-[12.5px] font-medium">{title}</span>
      <span className="text-[11.5px] text-muted-foreground">{help}</span>
    </span>
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
  commands,
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
  /** The mode is steer only while the bot runs. */
  onSend: (text: string, mode: SendMode) => Promise<void>;
  onStop: () => void;
  text: string;
  onTextChange: (text: string) => void;
  placeholder: string;
  /** Slash commands. A draft that starts with "/" runs one of them and is never sent. */
  commands?: SlashCommand[];
  autoFocus?: boolean;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const composing = useRef(false);
  const descriptionId = useId();
  const menuId = useId();
  const [dropping, setDropping] = useState(false);
  // The send button keeps the chosen mode until the run ends.
  const [mode, setMode] = useState<SendMode>("steer");
  const [wasRunning, setWasRunning] = useState(running);

  if (running !== wasRunning) {
    setWasRunning(running);

    if (!running) setMode("steer");
  }

  // The hint stays until the draft changes.
  const [hint, setHint] = useState<{ text: string; for: string }>();
  const [active, setActive] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const command = commands ? parseCommand(text) : undefined;
  const options = command?.typingName && !dismissed ? matchCommands(commands ?? [], command.name) : [];
  const highlighted = options[Math.min(active, options.length - 1)];
  const shownHint = hint?.for === text ? hint.text : undefined;

  const canAttach = !!files && !disabledReason;

  const blocked = files?.uploading
    ? "Wait until the files upload"
    : files?.failed
      ? "Remove the files that failed"
      : "";

  const canSend = (!!text.trim() || !!files?.ready.length) && !blocked && !disabledReason && !pending;

  const hasDraft = !!text.trim() || !!files?.files.length;
  const sendMode: SendMode = running ? mode : "followUp";
  const sendLabel = !running ? "Send message" : mode === "steer" ? "Steer this run" : "Queue message";

  useEffect(() => {
    if (autoFocus) area.current?.focus();
  }, [autoFocus]);

  function complete(picked: SlashCommand) {
    onTextChange(`/${picked.name} `);
    setActive(0);
    area.current?.focus();
  }

  async function runCommand(name: string, arg: string) {
    const found = findCommand(commands ?? [], name);

    if (!found) {
      setHint({
        for: text,
        text: `Unknown command /${name}. Type / to see the commands. To send text that starts with /, put a space before it.`,
      });

      return;
    }

    if (found.unavailable) {
      setHint({ for: text, text: `/${found.name}: ${found.unavailable}` });

      return;
    }

    if (await found.run(arg)) onTextChange("");
  }

  async function send() {
    if (command) {
      // A partly typed name runs the highlighted command, as the menu shows it.
      const exact = findCommand(commands ?? [], command.name);

      if (!exact && highlighted && command.typingName) return complete(highlighted);

      if (!pending) await runCommand(exact?.name ?? command.name, command.arg);

      return;
    }

    if (!canSend) return;
    const value = text;
    await onSend(value.trim(), sendMode);
  }

  const sendIcon = !running ? (
    <ArrowUp className="size-4" />
  ) : mode === "steer" ? (
    <CornerDownLeft className="size-4" />
  ) : (
    <ListEnd className="size-4" />
  );

  return (
    <div className="relative w-full">
      <SlashMenu
        id={menuId}
        options={options}
        active={Math.min(active, options.length - 1)}
        onPick={complete}
      />
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
          "relative isolate flex flex-col gap-2 overflow-hidden rounded-window border border-border bg-card px-3 pt-2 pb-2 transition-[border-color] duration-150 focus-within:border-ring",
          dropping && "border-primary focus-within:border-primary",
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
        {shownHint ? (
          <p role="status" className="px-1 text-[12px] text-muted-foreground">
            {shownHint}
          </p>
        ) : null}
        {files?.notice ? (
          <p role="alert" className="px-1 text-[12px] text-red">
            {files.notice}
          </p>
        ) : null}
        <Textarea
          ref={(element) => {
            area.current = element;

            if (element) {
              element.style.height = "auto";
              element.style.height = `${Math.min(element.scrollHeight, 200)}px`;
            }
          }}
          rows={1}
          className="min-h-[44px] w-full min-w-0 resize-none rounded-none border-0 bg-transparent px-1 py-1.5 text-[14px] leading-5 text-foreground shadow-none outline-none [overflow-wrap:anywhere] dark:bg-transparent placeholder:text-muted-foreground focus-visible:border-0 focus-visible:ring-0 disabled:cursor-not-allowed"
          maxLength={32000}
          placeholder={running ? "Add to this run…" : placeholder}
          aria-label={running ? "Add to this run…" : placeholder}
          aria-describedby={descriptionId}
          aria-controls={options.length ? menuId : undefined}
          aria-activedescendant={highlighted ? `${menuId}-${highlighted.name}` : undefined}
          value={text}
          disabled={!!disabledReason}
          onChange={(e) => {
            setDismissed(false);
            setActive(0);
            onTextChange(e.target.value);
          }}
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
            if (options.length && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
              e.preventDefault();
              const step = e.key === "ArrowDown" ? 1 : options.length - 1;
              setActive((Math.min(active, options.length - 1) + step) % options.length);

              return;
            }

            if (options.length && e.key === "Escape") {
              e.preventDefault();
              setDismissed(true);

              return;
            }

            if (highlighted && e.key === "Tab" && !e.shiftKey) {
              e.preventDefault();
              complete(highlighted);

              return;
            }

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
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="Attach files"
                title="Attach files"
                disabled={!canAttach}
                onClick={() => picker.current?.click()}
                className="shrink-0 rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
              >
                <Plus className="size-4" />
              </Button>
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
          {controls ? <div className="min-w-0 flex-1">{controls}</div> : <span className="min-w-2 flex-1" />}
          {stoppable && !hasDraft ? (
            <Button
              type="button"
              variant="default"
              size="icon-sm"
              aria-label="Stop run"
              title="Stop this run and its background work"
              // A missing model blocks new messages, not stopping the current run.
              disabled={pending}
              onClick={onStop}
              className="shrink-0 rounded-md bg-foreground text-card hover:bg-foreground"
            >
              <span aria-hidden className="size-2.5 rounded-[2px] bg-current" />
            </Button>
          ) : stoppable && !command ? (
            // While anything can be stopped, a draft gets a split button: the main part sends,
            // the menu picks the mode and keeps Stop in reach.
            <div className="flex h-8 shrink-0 items-stretch overflow-hidden rounded-md bg-foreground text-card">
              <button
                type="submit"
                aria-label={sendLabel}
                title={blocked || `${sendLabel} (Enter)`}
                disabled={!canSend}
                className="flex w-9 items-center justify-center disabled:opacity-40"
              >
                {sendIcon}
              </button>
              <DropdownMenu>
                <DropdownMenuTrigger
                  aria-label="Choose when to send"
                  className="flex w-6 items-center justify-center border-l border-card/20 text-card/70 hover:text-card"
                >
                  <ChevronDown className="size-3.5" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" side="top" sideOffset={6} className={menuPopup}>
                  {running ? (
                    <>
                      <DropdownMenuRadioGroup
                        value={mode}
                        onValueChange={(value) => setMode(value === "followUp" ? "followUp" : "steer")}
                      >
                        <DropdownMenuRadioItem value="steer" className={cn(menuItem, "pr-8")}>
                          <CornerDownLeft className="mt-0.5 size-4 shrink-0" />
                          <MenuText title="Steer" help="Joins this run after the current tool step" />
                        </DropdownMenuRadioItem>
                        <DropdownMenuRadioItem value="followUp" className={cn(menuItem, "pr-8")}>
                          <ListEnd className="mt-0.5 size-4 shrink-0" />
                          <MenuText title="Queue" help="Runs after this reply ends" />
                        </DropdownMenuRadioItem>
                      </DropdownMenuRadioGroup>
                      <DropdownMenuSeparator className="mx-1.5 my-1 h-px bg-border" />
                    </>
                  ) : null}
                  <DropdownMenuItem disabled={pending} onClick={onStop} className={menuItem}>
                    <span aria-hidden className="mt-1 ml-[3px] size-2.5 shrink-0 rounded-[2px] bg-current" />
                    <MenuText title="Stop" help="Stop this run. Your draft stays." />
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          ) : (
            <Button
              type="submit"
              variant="default"
              size="icon-sm"
              aria-label={command ? "Run command" : "Send message"}
              title={command ? "Run command (Enter)" : blocked || "Send message"}
              disabled={command ? pending : !canSend}
              className={cn(
                "shrink-0 rounded-md transition-[background-color,color,transform] duration-200",
                canSend || command
                  ? "bg-foreground text-card hover:bg-foreground"
                  : "bg-ring text-muted-foreground hover:bg-ring",
              )}
            >
              <ArrowUp className="size-4" />
            </Button>
          )}
        </div>
      </form>
      {/* The sandbox warning is on the blank page. Screen readers hear it with every message. */}
      <p
        id={descriptionId}
        className={cn(
          "mt-2 px-2 text-center text-[11.5px] text-muted-foreground",
          !disabledReason && !fix && "sr-only",
        )}
      >
        {disabledReason || "Tools run on this computer without a sandbox. Review commands and results."}
        {fix ? <> {fix}</> : null}
      </p>
    </div>
  );
}
