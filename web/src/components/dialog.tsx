import type { ReactNode } from "react";
import { Dialog } from "@base-ui/react/dialog";

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
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 animate-[fade-in_160ms_ease-out_both] bg-[oklch(0.2_0.01_264/0.28)]" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex items-end justify-center sm:items-center sm:p-6">
          <Dialog.Popup className="flex max-h-[92dvh] w-full max-w-[520px] animate-[pop-in_200ms_var(--ease-out-strong)_both] flex-col overflow-hidden rounded-t-[18px] bg-surface shadow-overlay outline-none sm:rounded-window">
            <div className="border-b border-line px-5 pt-5 pb-4">
              <Dialog.Title className="text-[15px] font-semibold text-ink [overflow-wrap:anywhere]">
                {title}
              </Dialog.Title>
              {description ? (
                <Dialog.Description className="mt-1 text-[12.5px] leading-relaxed text-ink-2">
                  {description}
                </Dialog.Description>
              ) : null}
            </div>
            {children ? (
              <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">{children}</div>
            ) : null}
            <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line bg-inset px-5 py-3">
              {footer}
            </div>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
