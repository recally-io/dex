import { cn } from "../lib/utils";

/** A composer command such as /clear. The text after the name is its argument. */
export type SlashCommand = {
  name: string;
  aliases?: string[];
  /** The argument hint, for example "[handoff]". */
  args: string;
  help: string;
  /** Why the command cannot run now. The menu shows it greyed out with this reason. */
  unavailable?: string;
  /** Resolves true when the command ran, so the composer clears the draft. */
  run: (arg: string) => Promise<boolean>;
};

/** The command name and argument of a draft that starts with "/", or undefined for a message. */
export function parseCommand(text: string) {
  const match = /^\/(\S*)(?:\s+([\s\S]*))?$/.exec(text);

  if (!match) return undefined;

  return { name: match[1].toLowerCase(), arg: match[2]?.trim() ?? "", typingName: match[2] === undefined };
}

export function findCommand(commands: SlashCommand[], name: string) {
  return commands.find((command) => command.name === name || command.aliases?.includes(name));
}

/** The commands whose name or alias starts with the typed name. */
export function matchCommands(commands: SlashCommand[], typed: string) {
  return commands.filter(
    (command) => command.name.startsWith(typed) || command.aliases?.some((alias) => alias.startsWith(typed)),
  );
}

export function SlashMenu({
  id,
  options,
  active,
  onPick,
}: {
  id: string;
  options: SlashCommand[];
  active: number;
  onPick: (command: SlashCommand) => void;
}) {
  if (!options.length) return null;

  return (
    <div
      id={id}
      role="listbox"
      aria-label="Commands"
      className="absolute right-0 bottom-full left-0 z-10 mb-2 flex animate-[pop-in_160ms_var(--ease-out-strong)_both] flex-col rounded-[10px] bg-card p-1 shadow-raised"
    >
      {options.map((command, index) => (
        <div
          key={command.name}
          id={`${id}-${command.name}`}
          role="option"
          tabIndex={-1}
          aria-selected={index === active}
          aria-disabled={!!command.unavailable}
          // Keep the focus in the text box while the pointer picks a command.
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => onPick(command)}
          className={cn(
            "flex cursor-pointer flex-col rounded-[7px] px-2 py-1.5 text-left",
            index === active && "bg-accent",
            command.unavailable && "cursor-default opacity-50",
          )}
        >
          <span className="text-[12.5px] text-foreground">
            <span className="font-mono font-medium">/{command.name}</span>{" "}
            <span className="font-mono text-muted-foreground">{command.args}</span>
            {command.aliases?.length ? (
              <span className="text-muted-foreground">
                {" "}
                · also {command.aliases.map((a) => `/${a}`).join(", ")}
              </span>
            ) : null}
          </span>
          <span className="text-[11.5px] text-muted-foreground">{command.unavailable ?? command.help}</span>
        </div>
      ))}
    </div>
  );
}
