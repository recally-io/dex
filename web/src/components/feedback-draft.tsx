import { useId, useState } from "react";
import { Modal } from "./dialog";
import { Button, buttonVariants } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";

export function FeedbackDraft({ onClose }: { onClose: () => void }) {
  const id = useId();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [preview, setPreview] = useState(false);
  const [status, setStatus] = useState("");
  const draftTitle = title.trim();
  const draftDescription = description.trim();
  const valid = !!draftTitle && !!draftDescription;

  async function copy(text: string, field: string) {
    try {
      await navigator.clipboard.writeText(text);
      setStatus(`${field} copied.`);
    } catch {
      setStatus(`Select the ${field.toLowerCase()} to copy it manually.`);
    }
  }

  return (
    <Modal
      title="Feedback"
      description="recally-io/dex is public. Remove secrets and private details before posting. Dex does not post on your behalf."
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          {preview ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() => {
                  setPreview(false);
                  setStatus("");
                }}
              >
                Edit draft
              </Button>
              <a
                href="https://github.com/recally-io/dex/issues/new"
                target="_blank"
                rel="noreferrer"
                className={buttonVariants()}
              >
                Open GitHub new issue
              </a>
            </>
          ) : (
            <Button type="submit" form={`${id}-feedback`} disabled={!valid}>
              Preview draft
            </Button>
          )}
        </>
      }
    >
      <form
        id={`${id}-feedback`}
        className="grid gap-4"
        onSubmit={(event) => {
          event.preventDefault();

          if (valid) setPreview(true);
        }}
      >
        <div className="grid gap-2">
          <Label htmlFor={`${id}-title`}>Title</Label>
          <Input
            id={`${id}-title`}
            value={preview ? draftTitle : title}
            onChange={(event) => setTitle(event.target.value)}
            readOnly={preview}
            required
            maxLength={120}
          />
          {preview ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="justify-self-start"
              onClick={() => void copy(draftTitle, "Title")}
            >
              Copy title
            </Button>
          ) : null}
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${id}-description`}>Description</Label>
          <Textarea
            id={`${id}-description`}
            value={preview ? draftDescription : description}
            onChange={(event) => setDescription(event.target.value)}
            readOnly={preview}
            required
            maxLength={1500}
            rows={6}
          />
          {preview ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="justify-self-start"
              onClick={() => void copy(draftDescription, "Description")}
            >
              Copy description
            </Button>
          ) : null}
        </div>
      </form>
      {preview ? (
        <p className="text-xs text-muted-foreground">
          Copy the draft into GitHub and submit it with your own account.
        </p>
      ) : null}
      {status ? (
        <p role="status" className="text-xs text-muted-foreground">
          {status}
        </p>
      ) : null}
    </Modal>
  );
}
