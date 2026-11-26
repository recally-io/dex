import { createContext, useContext, useId, type ReactNode } from "react";
import { Menu, PanelLeft } from "lucide-react";
import { cn } from "../lib/utils";

export const ShellContext = createContext<{
  openDrawer: () => void;
  attention: boolean;
  /** The desktop sidebar. Attention shows that Inbox has items while the sidebar is hidden. */
  sidebar?: { open: boolean; toggle: () => void; attention: boolean };
}>({ openDrawer: () => {}, attention: false });

/** The title bar of every page. One height at every width. */
function PageHeader({ title }: { title: ReactNode }) {
  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border pr-2 pl-3 sm:pl-4">
      <MenuButton />
      <span className="max-w-[60%] min-w-0 shrink-0 truncate font-sans text-[14px] font-semibold tracking-[-0.01em] text-foreground outline-none">
        {title}
      </span>
      <span className="min-w-0 flex-1" />
    </header>
  );
}

/**
 * The first control of the header. Below lg it opens the drawer. From lg on, it shows the sidebar
 * again after the user hid it; the sidebar holds its own Hide button.
 */
export function MenuButton() {
  const { openDrawer, attention, sidebar } = useContext(ShellContext);
  const sidebarLabel = sidebar?.attention ? "Show sidebar, Board needs attention" : "Show sidebar";

  return (
    <>
      {sidebar && !sidebar.open ? (
        <button
          type="button"
          aria-label={sidebarLabel}
          title={sidebarLabel}
          onClick={sidebar.toggle}
          className="relative -ml-1 hidden size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground lg:flex"
        >
          <PanelLeft size={16} />
          {sidebar.attention ? (
            <span
              aria-hidden
              className="absolute top-1 right-1 size-2 rounded-full bg-orange ring-2 ring-background"
            />
          ) : null}
        </button>
      ) : null}
      <button
        type="button"
        aria-label={attention ? "Open navigation, Board needs attention" : "Open navigation"}
        onClick={openDrawer}
        className="relative -ml-1 flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground lg:hidden"
      >
        <Menu size={18} />
        {attention ? (
          <span
            aria-hidden
            className="absolute top-1.5 right-1.5 size-2 rounded-full bg-orange ring-2 ring-background"
          />
        ) : null}
      </button>
    </>
  );
}

export const InPaneContext = createContext(false);

/** The rounded main pane. Below lg it has a small bar with the drawer button. */
export function MainPane({
  children,
  headerTitle = "dex",
}: {
  children: ReactNode;
  headerTitle?: ReactNode;
}) {
  if (useContext(InPaneContext))
    return <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">{children}</div>;

  return (
    <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
      <PageHeader title={headerTitle} />
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">{children}</div>
    </section>
  );
}

type PageProps = {
  headerTitle?: string;
  eyebrow?: ReactNode;
  title: string;
  description?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
  /** A control that switches between the pages of this area, shown above the content. */
  toolbar?: ReactNode;
  /** A page with two columns needs the wide measure. Single-column pages stay narrow to read. */
  wide?: boolean;
  children?: ReactNode;
};

/**
 * A page outside the bot pane: one fluid column on the page ground, a title row, then sections
 * separated by a hairline. No frame around the column and no numbering: the heading names the
 * section, and a rule marks where the next one starts.
 */
export function FramedPage(props: PageProps) {
  if (useContext(InPaneContext)) return <PanePage {...props} />;

  const {
    eyebrow,
    title,
    description,
    badge,
    actions,
    toolbar,
    wide,
    children,
    headerTitle = "Settings",
  } = props;

  return (
    <MainPane headerTitle={headerTitle}>
      <div className={cn("w-full px-6 py-8", wide ? "max-w-[1120px]" : "max-w-[816px]")}>
        <header className="mb-8 flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
          <div className="min-w-0">
            {eyebrow ? <div className="mb-2 text-[12px] text-muted-foreground">{eyebrow}</div> : null}
            <div className="flex flex-wrap items-center gap-2">
              <h1
                id="page-title"
                tabIndex={-1}
                className="text-[24px] leading-tight font-semibold tracking-[-0.015em] text-foreground outline-none [overflow-wrap:anywhere]"
              >
                {title}
              </h1>
              {badge}
            </div>
            {description ? (
              <p className="mt-2 max-w-[640px] text-[14px] leading-relaxed text-muted-foreground">
                {description}
              </p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
        {toolbar ? <div className="mb-6">{toolbar}</div> : null}
        {children}
      </div>
    </MainPane>
  );
}

/**
 * A page inside the bot pane. The tab already names the group, so the title is smaller here; the
 * page still opens with its own heading and one line of description.
 */
function PanePage({ eyebrow, title, description, badge, actions, toolbar, wide, children }: PageProps) {
  return (
    <MainPane>
      <div className={cn("w-full px-6 pt-6 pb-10", wide ? "max-w-[1120px]" : "max-w-[816px]")}>
        <header className="mb-6 flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
          <div className="min-w-0">
            {eyebrow ? <div className="mb-2 text-[12px] text-muted-foreground">{eyebrow}</div> : null}
            <div className="flex flex-wrap items-center gap-2">
              <h1
                id="page-title"
                tabIndex={-1}
                className="text-[18px] leading-tight font-semibold tracking-[-0.01em] text-foreground outline-none [overflow-wrap:anywhere]"
              >
                {title}
              </h1>
              {badge}
            </div>
            {description ? (
              <p className="mt-1.5 max-w-[640px] text-[13.5px] leading-relaxed text-muted-foreground">
                {description}
              </p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
        {toolbar ? <div className="mb-5">{toolbar}</div> : null}
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
  title: string;
  caption?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}) {
  const id = useId();

  return (
    <section
      aria-labelledby={id}
      className="border-t border-border py-7 first-of-type:border-t-0 first-of-type:pt-0 last:border-t-0"
    >
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
          <h2 id={id} className="text-[14px] font-semibold text-foreground">
            {title}
          </h2>
          {caption ? <p className="text-[13px] text-muted-foreground">{caption}</p> : null}
        </div>
        {action ? <div className="-my-1 shrink-0">{action}</div> : null}
      </div>
      {children}
    </section>
  );
}

/** The white hairline card that holds the content of a section. */
export function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("overflow-hidden rounded-card bg-card shadow-card", className)}>{children}</div>;
}

/** Label-left and value-right rows, like upstream TextRow, divided by hairlines. Labels are unique. */
export function Facts({ rows, monoLabels = false }: { rows: [string, ReactNode][]; monoLabels?: boolean }) {
  return (
    <dl className="divide-y divide-border">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="flex min-h-11 flex-wrap items-center justify-between gap-x-4 gap-y-1 px-4 py-2.5"
        >
          <dt
            className={
              monoLabels ? "font-mono text-[12px] text-foreground" : "text-[13px] text-muted-foreground"
            }
          >
            {label}
          </dt>
          <dd className="min-w-0 text-right text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
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
      <p className="text-[13px] font-medium text-foreground">{title}</p>
      <div className="mx-auto mt-1 max-w-[380px] text-[12.5px] text-muted-foreground">{children}</div>
    </Panel>
  );
}

export function Note({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "warn" }) {
  return (
    <div
      className={cn(
        "rounded-card px-3.5 py-2.5 text-[12.5px] leading-relaxed",
        tone === "warn" ? "bg-orange-tint text-foreground" : "bg-muted text-muted-foreground shadow-card",
      )}
    >
      {children}
    </div>
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[12px] text-muted-foreground", className)}>{children}</span>;
}
