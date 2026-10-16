import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, Outlet, useRouterState, type LinkProps } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Dialog } from "@base-ui/react/dialog";
import { IconBubbles } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBubbles";
import { IconChevronDownSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconChevronDownSmall";
import { IconCircleInfo } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCircleInfo";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconCloudApi } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCloudApi";
import { IconCrossSmall } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconCrossSmall";
import { IconKey2 } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconKey2";
import { IconPuzzle } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconPuzzle";
import { IconRobot } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconRobot";
import { getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";
import GlideMenu from "./primitives/GlideMenu";
import { ShellContext } from "./page";
import { ThemeToggle, useSystemTheme } from "./theme";
import { useAgentModel } from "../lib/agent";
import { useApp } from "../lib/app-state";
import { cn } from "../lib/utils";

export function Shell() {
  const [menu, setMenu] = useState(false);
  const location = useRouterState({ select: (s) => s.location });
  const path = location.pathname;
  const shown = useRef(path);
  // The drawer closes when the page changes.
  const [menuPath, setMenuPath] = useState(path);
  const shell = useMemo(() => ({ openDrawer: () => setMenu(true) }), []);
  useSystemTheme();

  if (menuPath !== path) {
    setMenuPath(path);
    setMenu(false);
  }

  // After navigation, focus moves to the page title, so keyboard and screen reader users land on
  // the new page. A new chat keeps the focus in its composer.
  useEffect(() => {
    // The first page load keeps the browser's focus.
    if (shown.current === path) return;
    shown.current = path;

    const frame = requestAnimationFrame(() => document.getElementById("page-title")?.focus());

    return () => cancelAnimationFrame(frame);
  }, [path]);

  return (
    <ShellContext.Provider value={shell}>
      <div className="flex h-dvh bg-canvas text-ink lg:gap-2.5 lg:p-2.5 lg:pl-0">
        <a
          href="#main"
          onClick={(e) => {
            e.preventDefault();
            document.getElementById("main")?.focus();
          }}
          className="sr-only z-50 rounded-control bg-ink px-3 py-2 text-canvas focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
        >
          Skip to content
        </a>
        <Sidebar path={path} className="hidden pt-0.5 lg:flex" />
        <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 outline-none lg:gap-2.5">
          <Outlet />
        </main>
      </div>
      <Dialog.Root open={menu} onOpenChange={setMenu}>
        <Dialog.Portal>
          <Dialog.Backdrop className="fixed inset-0 z-50 animate-[fade-in_160ms_ease-out_both] bg-[oklch(0.2_0.01_264/0.32)] lg:hidden" />
          <Dialog.Popup
            aria-label="Navigation"
            className="fixed inset-y-0 left-0 z-50 flex w-[272px] max-w-[85vw] animate-[drawer-in_240ms_var(--ease-out-strong)_both] flex-col bg-canvas py-2 shadow-overlay outline-none lg:hidden"
          >
            <Dialog.Close
              aria-label="Close navigation"
              className="absolute top-3 right-3 z-20 flex size-8 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2 hover:text-ink"
            >
              <IconCrossSmall size={18} />
            </Dialog.Close>
            <Sidebar path={path} className="w-full" />
          </Dialog.Popup>
        </Dialog.Portal>
      </Dialog.Root>
    </ShellContext.Provider>
  );
}

// Layout and motion from upstream primitives/SidebarNav.tsx.
function GlideGroup({ children }: { children: ReactNode }) {
  return (
    <GlideMenu
      rowSelector="[data-row]"
      highlightClassName="sidebar-glide-highlight rounded-[7px] bg-hover-2"
      className="group/glide flex flex-col gap-px"
    >
      {children}
    </GlideMenu>
  );
}

function Row({
  to,
  params,
  icon,
  label,
  current,
  trailing,
  small,
  title,
}: {
  to: LinkProps["to"];
  params?: LinkProps["params"];
  icon?: ReactNode;
  label: ReactNode;
  current: boolean;
  trailing?: ReactNode;
  small?: boolean;
  title?: string;
}) {
  return (
    <Link
      to={to}
      params={params}
      title={title}
      data-row
      aria-current={current ? "page" : undefined}
      className={cn(
        "relative z-10 mx-2 flex items-center rounded-control px-2 text-left transition-[background-color,color,transform] duration-150 active:scale-[0.98]",
        small ? "h-7" : "h-8",
        current && "bg-hover-2 group-hover/glide:bg-transparent",
      )}
    >
      {icon ? (
        <span
          className={cn(
            "flex size-5 shrink-0 items-center justify-center",
            current ? "text-ink" : small ? "text-ink-3" : "text-ink-2",
          )}
        >
          {icon}
        </span>
      ) : null}
      <span
        className={cn(
          "min-w-0 flex-1 truncate",
          icon && "ml-1.5",
          small ? "text-[13px]" : "text-[14px] font-medium",
          current ? "text-ink" : "text-ink-2",
        )}
      >
        {label}
      </span>
      {trailing ? <span className="ml-2 flex shrink-0 items-center text-ink-3">{trailing}</span> : null}
    </Link>
  );
}

function GroupLabel({
  label,
  open,
  onToggle,
  controls,
  children,
}: {
  label: string;
  open: boolean;
  onToggle: () => void;
  controls: string;
  children?: ReactNode;
}) {
  return (
    <div className="relative mx-2 mb-1 flex h-8 items-center">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={controls}
        onClick={onToggle}
        className="flex h-8 min-w-0 flex-1 items-center gap-1.5 rounded-control px-2 text-[12.5px] font-medium text-ink-3 transition-colors hover:text-ink-2"
      >
        <IconChevronDownSmall
          size={16}
          className={cn("shrink-0 transition-transform duration-200", !open && "-rotate-90")}
        />
        <span className="truncate">{label}</span>
      </button>
      {children}
    </div>
  );
}

function Dot({ tone, label }: { tone: "orange" | "red"; label: string }) {
  return (
    <span
      role="img"
      aria-label={label}
      className={cn("size-1.5 rounded-full", tone === "red" ? "bg-red" : "bg-orange")}
    />
  );
}

const configure = [
  { to: "/agent", label: "Agent", icon: <IconRobot size={16} /> },
  { to: "/providers", label: "Providers", icon: <IconCloudApi size={16} /> },
  { to: "/channels", label: "Channels", icon: <IconBubbles size={16} /> },
  { to: "/extensions", label: "Extensions", icon: <IconPuzzle size={16} /> },
  { to: "/secrets", label: "Secrets", icon: <IconKey2 size={16} /> },
  { to: "/about", label: "About", icon: <IconCircleInfo size={16} /> },
] as const;

function Sidebar({ path, className }: { path: string; className?: string }) {
  const settings = useQuery(getSettingsOptions());
  const model = useAgentModel(settings.data);
  const { draft } = useApp();
  const [configOpen, setConfigOpen] = useState(true);
  const [a] = path.split("/").filter(Boolean);

  const providerErrors = settings.data?.providers.filter((p) => p.error).length ?? 0;
  const dirty = draft !== null && draft !== settings.data?.agent.instructions;

  const agentTrailing = dirty ? (
    <span className="text-[11px] text-ink-3">Draft</span>
  ) : model && !model.ok ? (
    <Dot tone="orange" label="No available model" />
  ) : null;

  return (
    <aside aria-label="Navigation" className={cn("flex h-full w-[232px] shrink-0 flex-col", className)}>
      {/* The workspace header: dex and the model of its one agent. */}
      <div className="mx-2 mb-2.5 flex h-11 shrink-0 items-center gap-2 px-2">
        <span
          aria-hidden
          className="flex size-6 shrink-0 items-center justify-center rounded-[7px] bg-ink text-[12px] font-semibold text-surface"
        >
          d
        </span>
        <span className="min-w-0 flex-1 leading-tight">
          <span className="block text-[14px] font-semibold text-ink">
            {settings.data?.agent.name ?? "dex"}
          </span>
          {model ? (
            <Link
              to="/agent"
              className={cn(
                "block truncate text-[11.5px] hover:underline",
                model.ok ? "text-ink-3" : "text-orange",
              )}
            >
              {settings.data?.agent.modelId ? model.label : "No model chosen"}
            </Link>
          ) : null}
        </span>
      </div>

      <GlideGroup>
        <Row icon={<IconRobot size={18} />} label="Timeline" to="/" current={path === "/"} />
        <Row
          icon={<IconClock size={18} />}
          label="Automations"
          to="/automations"
          current={a === "automations"}
        />
      </GlideGroup>
      <div className="flex-1" />

      {/* Configuration: present, but quieter than the chats. */}
      <div className="mt-2 shrink-0 border-t border-dashed border-line-strong pt-2">
        <GroupLabel
          label="Configure"
          open={configOpen}
          controls="sidebar-configure"
          onToggle={() => setConfigOpen(!configOpen)}
        />
        <div id="sidebar-configure" hidden={!configOpen}>
          <GlideGroup>
            {configure.map((item) => (
              <Row
                key={item.to}
                small
                icon={item.icon}
                label={item.label}
                to={item.to}
                current={`/${a}` === item.to}
                trailing={
                  item.to === "/agent" ? (
                    agentTrailing
                  ) : item.to === "/providers" && providerErrors ? (
                    <Dot
                      tone="red"
                      label={`${providerErrors} ${providerErrors === 1 ? "provider needs" : "providers need"} attention`}
                    />
                  ) : null
                }
              />
            ))}
          </GlideGroup>
        </div>
        <div className="mx-2 mt-2 flex items-center border-t border-line px-1 pt-2.5 pb-0.5">
          <ThemeToggle />
        </div>
      </div>
    </aside>
  );
}
