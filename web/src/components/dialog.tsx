import type { ReactNode } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

/** A modal dialog for forms and confirmations. It does not close while a save is pending. */
export function Modal({
  title,
  description,
  pending = false,
  onClose,
  footer,
  children,
}: {
  title: string;
  description?: ReactNode;
  pending?: boolean;
  onClose: () => void;
  footer: ReactNode;
  children?: ReactNode;
}) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <DialogContent
        // Cancel closes the dialog, so it needs no corner button.
        showCloseButton={false}
        className="flex max-h-[92dvh] w-full max-w-[520px] sm:max-w-[520px] flex-col overflow-hidden rounded-t-[18px] bg-card p-0 shadow-raised sm:rounded-window"
      >
        <DialogHeader className="border-b border-border px-5 pt-5 pb-4">
          <DialogTitle className="text-[15px] font-semibold text-foreground [overflow-wrap:anywhere]">
            {title}
          </DialogTitle>
          {description ? (
            <DialogDescription className="mt-1 text-[12.5px] leading-relaxed text-muted-foreground">
              {description}
            </DialogDescription>
          ) : null}
        </DialogHeader>
        {children ? (
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">{children}</div>
        ) : null}
        <DialogFooter className="flex flex-wrap items-center justify-end gap-2 border-t border-border bg-muted px-5 py-3">
          {footer}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
