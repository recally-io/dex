import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Dialog } from "@base-ui/react/dialog";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import type { Settings } from "../api/generated/types.gen";
import {
  getFeishuOptions,
  getSettingsOptions,
  listAutomationsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { saveAgent, thinkingNames, type AgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { EnabledSwitch } from "../routes/automations";
import { AutomationDialog } from "./automation-dialog";
import { ModelPicker } from "./model-picker";
import { ThinkingPicker } from "./thinking-picker";
import { Failure } from "./feedback";
import { Input } from "./form";
import { Button } from "./atoms/Button";
import { cn } from "../lib/utils";

const openKey = "dex-agent-dashboard";

/** The open state of the inline dashboard, kept per device. A phone opens it as a sheet each time. */
export function useDashboardOpen() {
  const [open, setOpen] = useState(() => {
    try {
      return localStorage.getItem(openKey) === "1";
    } catch {
      return false;
    }
  });

  function toggle() {
    const next = !open;

    setOpen(next);

    try {
      localStorage.setItem(openKey, next ? "1" : "0");
    } catch {
      // Private windows can refuse storage; the dashboard still opens for this visit.
    }
  }

  return [open, toggle] as const;
}

type Props = {
  settings: Settings;
  model: AgentModel;
  dirty: boolean;
  /** Opens the instructions beside the transcript, or undefined when the pane is too narrow. */
  onEditInstructions?: () => void;
};

/**
 * A row of tiles between the header and the transcript: the bot's model, name, channels,
 * automations and instructions. It holds only one-step changes; a change that needs a draft or a
 * confirmation opens its own panel or page. The tiles mount only while the dashboard is open, so
 * their polls stop when it closes.
 */
export function AgentDashboard(props: Props) {
  return (
    <div id="agent-dashboard" className="shrink-0 border-b border-line">
      <div className="grid h-[132px] grid-cols-5 gap-2 px-3 py-2.5 sm:px-4">
        <Tiles {...props} />
      </div>
    </div>
  );
}

/** The same tiles in a bottom sheet, for a pane too narrow to hold them above the transcript. */
export function AgentDashboardSheet({ onClose, ...props }: Props & { onClose: () => void }) {
  return (
    <Dialog.Root open onOpenChange={(open) => !open && onClose()}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 animate-[fade-in_160ms_ease-out_both] bg-[oklch(0.2_0.01_264/0.28)]" />
        <Dialog.Popup
          id="agent-dashboard"
          aria-label={`${props.settings.agent.name} dashboard`}
          className="fixed inset-x-0 bottom-0 z-50 max-h-[80dvh] animate-[pop-in_200ms_var(--ease-out-strong)_both] overflow-y-auto rounded-t-[18px] bg-canvas px-3 pt-2 pb-4 shadow-overlay outline-none"
        >
          <div className="flex h-10 items-center px-1">
            <Dialog.Title className="text-[14px] font-semibold text-ink">
              {props.settings.agent.name}
            </Dialog.Title>
            <span className="flex-1" />
            <Dialog.Close
              aria-label="Close dashboard"
              className="flex size-8 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2 hover:text-ink"
            >
              <IconCrossSmall size={18} />
            </Dialog.Close>
          </div>
          <div className="grid grid-cols-2 gap-2 [&>*]:min-h-[112px]">
            <Tiles {...props} />
          </div>
          <Link
            to="/agent"
            className="mt-3 inline-block px-1 text-[12.5px] font-medium text-accent-ink underline"
          >
            All settings
          </Link>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Tiles(props: Props) {
  return (
    <>
      <ModelTile {...props} />
      <NameTile name={props.settings.agent.name} />
      <ChannelsTile settings={props.settings} />
      <AutomationsTile noModel={!props.settings.agent.modelId} />
      <InstructionsTile {...props} />
    </>
  );
}

function Tile({ title, children, actions }: { title: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section
      aria-label={title}
      className="flex min-w-0 flex-col gap-1 overflow-hidden rounded-card bg-surface px-2.5 py-2 shadow-hairline"
    >
      <h2 className="text-[11px] font-medium text-ink-3">{title}</h2>
      <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-0.5">{children}</div>
      {actions ? <div className="flex min-w-0 items-center gap-2">{actions}</div> : null}
    </section>
  );
}

const value = "truncate text-[13px] font-medium text-ink";
const hint = "truncate text-[11.5px] text-ink-3";
const more = "shrink-0 text-[11.5px] font-medium whitespace-nowrap text-accent-ink hover:underline";

function ModelTile({ settings, model }: Props) {
  const { notify } = useApp();
  const reasoning = model.model?.reasoning;

  return (
    <Tile
      title="Model"
      actions={
        <>
          <ModelPicker
            trigger={
              <Button size="xs" variant="secondary">
                {settings.agent.modelId ? "Change" : "Choose"}
              </Button>
            }
          />
          {reasoning ? (
            <ThinkingPicker
              value={settings.agent.thinkingLevel}
              levels={model.model?.thinkingLevels ?? []}
              save={async (level) => {
                if (!level) return;
                await saveAgent((agent) => ({ ...agent, thinkingLevel: level }));
                notify(`Thinking is now ${thinkingNames[level]}. It applies from the next message.`);
              }}
              trigger={
                <Button size="xs" variant="quiet">
                  Thinking
                </Button>
              }
            />
          ) : null}
        </>
      }
    >
      <p className={cn(value, !model.ok && "text-orange")}>
        {settings.agent.modelId ? model.label : "No model chosen"}
      </p>
      <p className={hint}>
        {reasoning ? `Thinking ${thinkingNames[settings.agent.thinkingLevel]}` : "No thinking"}
      </p>
    </Tile>
  );
}

function NameTile({ name }: { name: string }) {
  const [draft, setDraft] = useState<string | null>(null);
  const text = draft ?? name;

  const save = useMutation({
    mutationFn: (nextName: string) => saveAgent((agent) => ({ ...agent, name: nextName })),
    onSuccess: () => setDraft(null),
  });

  function submit() {
    if (text.trim() === name) setDraft(null);
    else if (!save.isPending) save.mutate(text);
  }

  return (
    <Tile title="Name">
      <Input
        aria-label="Bot name"
        aria-invalid={save.isError}
        value={text}
        disabled={save.isPending}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={submit}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !event.nativeEvent.isComposing) event.currentTarget.blur();
          else if (event.key === "Escape") setDraft(null);
        }}
        className="h-8 text-[13px]"
      />
      {save.isError ? (
        <p role="alert" className="truncate text-[11.5px] text-red" title={String(save.error)}>
          Not saved
        </p>
      ) : (
        <p className={hint}>Enter saves</p>
      )}
    </Tile>
  );
}

const channelTone = (state: string) =>
  state === "running"
    ? "bg-green"
    : state === "error" || state === "needs_login"
      ? "bg-red"
      : "bg-line-strong";

function ChannelsTile({ settings }: { settings: Settings }) {
  const saved = useQuery({ ...getSettingsOptions(), refetchInterval: 3000 });
  const feishu = useQuery({ ...getFeishuOptions(), refetchInterval: 3000 });
  const current = saved.data ?? settings;

  // Turning a channel off forgets its credentials, so the tile links to the channel instead of
  // holding a switch.
  const rows = [
    { id: "telegram", name: "Telegram", state: current.telegram.status.state },
    { id: "wechat", name: "WeChat", state: current.wechat.status.state },
    { id: "feishu", name: "Feishu", state: feishu.isError ? "error" : (feishu.data?.status ?? "off") },
  ] as const;

  return (
    <Tile title="Channels">
      {rows.map((row) => (
        <Link
          key={row.id}
          to="/channels/$channelId"
          params={{ channelId: row.id }}
          className="flex min-w-0 items-center gap-1.5 text-[12px] text-ink-2 hover:text-ink hover:underline"
        >
          <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", channelTone(row.state))} />
          <span className="truncate">{row.name}</span>
          <span className="ml-auto shrink-0 text-[11px] text-ink-3">{row.state.replace("_", " ")}</span>
        </Link>
      ))}
    </Tile>
  );
}

function AutomationsTile({ noModel }: { noModel: boolean }) {
  const automations = useQuery({ ...listAutomationsOptions(), refetchInterval: 10_000 });
  const [create, setCreate] = useState(false);
  const list = automations.data?.automations ?? [];
  // Two rows fit the tile; the settings page lists the rest.
  const shown = list.slice(0, 2);

  return (
    <Tile
      title="Automations"
      actions={
        <>
          <Button size="xs" variant="secondary" disabled={noModel} onClick={() => setCreate(true)}>
            New
          </Button>
          <span className="flex-1" />
          <Link to="/agent" className={more}>
            {list.length > shown.length ? `All ${list.length}` : "All"}
          </Link>
        </>
      }
    >
      {automations.isError ? (
        <Failure title="Could not load" error={automations.error} />
      ) : !automations.isPending && !list.length ? (
        <p className={hint}>None yet</p>
      ) : (
        shown.map((automation) => (
          <div key={automation.id} className="flex min-w-0 items-center gap-2">
            <Link
              to="/automations/$automationId"
              params={{ automationId: automation.id }}
              className="min-w-0 flex-1 truncate text-[12px] text-ink-2 hover:text-ink hover:underline"
            >
              {automation.name}
            </Link>
            <span className="origin-right scale-[0.8]">
              <EnabledSwitch automation={automation} />
            </span>
          </div>
        ))
      )}
      {create ? <AutomationDialog onClose={() => setCreate(false)} /> : null}
    </Tile>
  );
}

function InstructionsTile({ settings, dirty, onEditInstructions }: Props) {
  const { draft } = useApp();
  const length = (draft ?? settings.agent.instructions).length;

  return (
    <Tile
      title="Instructions"
      actions={
        onEditInstructions ? (
          <Button size="xs" variant="secondary" onClick={onEditInstructions}>
            Edit
          </Button>
        ) : (
          <Link to="/agent" className={more}>
            Edit
          </Link>
        )
      }
    >
      <p className={value}>{length ? `${length.toLocaleString()} characters` : "Empty"}</p>
      <p className={cn(hint, dirty && "text-orange")}>{dirty ? "Unsaved draft" : "Saved"}</p>
    </Tile>
  );
}
