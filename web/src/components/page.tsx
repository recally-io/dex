import { createContext, useContext, useId, type ReactNode } from "react";
import { IconBurger } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBurger";
import { StatusPill } from "./atoms/StatusPill";
import { cn } from "../lib/utils";

export const ShellContext = createContext({ openDrawer: () => {} });

/** The drawer button for screens below lg. */
export function MenuButton() {
  const { openDrawer } = useContext(ShellContext);

  return (
    <button
      type="button"
      aria-label="Open navigation"
      onClick={openDrawer}
      className="-ml-1 flex size-9 shrink-0 items-center justify-center rounded-control text-ink-2 transition-colors hover:bg-hover hover:text-ink lg:hidden"
    >
      <IconBurger size={18} />
    </button>
  );
}

/** True inside the bot pane, which already draws the frame and the drawer button. */
export const InPaneContext = createContext(false);

/** The rounded main pane. Below lg it has a small bar with the drawer button. */
export function MainPane({ children, bar = true }: { children: ReactNode; bar?: boolean }) {
  if (useContext(InPaneContext))
    return <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">{children}</div>;

  return (
    <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-page">
      {bar ? (
        <div className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3 lg:hidden">
          <MenuButton />
          <span className="text-[14px] font-semibold text-ink">dex</span>
        </div>
      ) : null}
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">{children}</div>
    </section>
  );
}

type PageProps = {
  eyebrow?: ReactNode;
  title: string;
  description?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
};

/**
 * A page outside the bot pane: one fluid column on the page ground, a title row, then sections
 * separated by a hairline. No frame around the column and no numbering: the heading names the
 * section, and a rule marks where the next one starts.
 */
export function FramedPage(props: PageProps) {
  if (useContext(InPaneContext)) return <PanePage {...props} />;

  const { eyebrow, title, description, badge, actions, children } = props;

  return (
    <MainPane>
      <div className="mx-auto w-full max-w-[1120px] px-6 py-8">
        <header className="mb-8 flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
          <div className="min-w-0">
            {eyebrow ? <div className="mb-2 text-[12px] text-ink-3">{eyebrow}</div> : null}
            <div className="flex flex-wrap items-center gap-2">
              <h1
                id="page-title"
                tabIndex={-1}
                className="text-[24px] leading-tight font-semibold tracking-[-0.015em] text-ink outline-none [overflow-wrap:anywhere]"
              >
                {title}
              </h1>
              {badge}
            </div>
            {description ? (
              <p className="mt-2 max-w-[640px] text-[14px] leading-relaxed text-ink-2">{description}</p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
        {children}
      </div>
    </MainPane>
  );
}

/**
 * A page inside the bot pane. The tab already names the group, so the title is smaller here; the
 * page still opens with its own heading and one line of description.
 */
function PanePage({ eyebrow, title, description, badge, actions, children }: PageProps) {
  return (
    <MainPane>
      <div className="mx-auto w-full max-w-[1120px] px-6 pt-6 pb-10">
        <header className="mb-6 flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
          <div className="min-w-0">
            {eyebrow ? <div className="mb-2 text-[12px] text-ink-3">{eyebrow}</div> : null}
            <div className="flex flex-wrap items-center gap-2">
              <h1
                id="page-title"
                tabIndex={-1}
                className="text-[18px] leading-tight font-semibold tracking-[-0.01em] text-ink outline-none [overflow-wrap:anywhere]"
              >
                {title}
              </h1>
              {badge}
            </div>
            {description ? (
              <p className="mt-1.5 max-w-[640px] text-[13.5px] leading-relaxed text-ink-2">{description}</p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
        {children}
      </div>
    </MainPane>
  );
}

/** One section: a heading row, then its content, divided from the next by a hairline. */
export function Section({
  title,
  caption,
  action,
  children,
}: {
  /** Positional label kept for the call sites; the heading carries the order now. */
  n?: number;
  title: string;
  caption?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();
  const inPane = useContext(InPaneContext);

  return (
    <section
      aria-labelledby={id}
      className={cn(
        "border-t border-line-soft py-7 first:border-t-0 first:pt-0",
        inPane ? "last:border-t-0" : "last:border-t-0",
      )}
    >
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
          <h2 id={id} className="text-[14px] font-semibold text-ink">
            {title}
          </h2>
          {caption ? <p className="text-[13px] text-ink-3">{caption}</p> : null}
        </div>
        {action ? <div className="-my-1 shrink-0">{action}</div> : null}
      </div>
      {children}
    </section>
  );
}

/** The white hairline card that holds the content of a section. */
export function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={cn("overflow-hidden rounded-window bg-surface shadow-card", className)}>{children}</div>
  );
}

/** Label-left and value-right rows, like upstream TextRow, divided by hairlines. Labels are unique. */
export function Facts({ rows, monoLabels = false }: { rows: [string, ReactNode][]; monoLabels?: boolean }) {
  return (
    <dl className="divide-y divide-line">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="flex min-h-11 flex-wrap items-center justify-between gap-x-4 gap-y-1 px-4 py-2.5"
        >
          <dt className={monoLabels ? "font-mono text-[12px] text-ink" : "text-[13px] text-ink-2"}>
            {label}
          </dt>
          <dd className="min-w-0 text-right text-[13px] font-medium text-ink [overflow-wrap:anywhere]">
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** A panel with a centered title and text, for a list that has no entries. */
export function Empty({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel className="px-5 py-8 text-center">
      <p className="text-[13px] font-medium text-ink">{title}</p>
      <div className="mx-auto mt-1 max-w-[380px] text-[12.5px] text-ink-3">{children}</div>
    </Panel>
  );
}

export function Note({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "warn" }) {
  return (
    <div
      className={cn(
        "rounded-card px-3.5 py-2.5 text-[12.5px] leading-relaxed",
        tone === "warn" ? "bg-orange-tint text-ink" : "bg-inset text-ink-2 shadow-hairline",
      )}
    >
      {children}
    </div>
  );
}

// Marks a capability that this version of dex does not have.
export function NotAvailable() {
  return <StatusPill className="h-5 shrink-0 px-2 text-[11.5px]">Not yet available</StatusPill>;
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[12px] text-ink-2", className)}>{children}</span>;
}
