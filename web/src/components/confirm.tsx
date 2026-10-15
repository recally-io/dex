import type { ReactNode } from "react";
import { Button } from "./atoms/Button";
import { Modal } from "./dialog";
import { Failure } from "./feedback";

export function Confirm({
  title,
  description,
  children,
  confirmLabel = "Confirm",
  onCancel,
  onConfirm,
  pending,
  error,
}: {
  title: string;
  description: string;
  children?: ReactNode;
  confirmLabel?: string;
  onCancel: () => void;
  onConfirm: () => void;
  pending: boolean;
  error?: unknown;
}) {
  return (
    <Modal
      title={title}
      description={description}
      pending={pending}
      onClose={onCancel}
      footer={
        <>
          <Button size="sm" variant="secondary" onClick={onCancel} disabled={pending}>
            Cancel
          </Button>
          <Button
            size="sm"
            variant="primary"
            className="bg-red text-white"
            onClick={onConfirm}
            disabled={pending}
          >
            {pending ? "Please wait…" : confirmLabel}
          </Button>
        </>
      }
    >
      {children || error ? (
        <>
          {children}
          {error ? <Failure title="Could not complete this action" error={error} /> : null}
        </>
      ) : null}
    </Modal>
  );
}
