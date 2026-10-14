import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Button } from "./ui/button";
import { Failure } from "./feedback";

export function Confirm({
  title,
  description,
  onCancel,
  onConfirm,
  pending,
  error,
}: {
  title: string;
  description: string;
  onCancel: () => void;
  onConfirm: () => void;
  pending: boolean;
  error?: unknown;
}) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onCancel();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {error ? <Failure title="Could not complete this action" error={error} retry={onConfirm} /> : null}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onCancel} disabled={pending}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={onConfirm} disabled={pending}>
            {pending ? "Please wait…" : "Confirm"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
